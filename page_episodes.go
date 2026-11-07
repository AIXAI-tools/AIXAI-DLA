//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Some episode pages carry the whole series in their player data: one object
// per episode with its number and its stream address. When the page address
// names an episode, that episode's entry is the exact stream to download, with
// no need to open the capture browser.

var (
	pageStreamRE  = regexp.MustCompile(`"(?:direct_play_url|directPlayUrl|play_url|playUrl|video_url|videoUrl|stream_url|streamUrl|hls_url|hlsUrl|url|src|file)"\s*:\s*"(https?:[^"]+?\.(?:m3u8|mp4|mpd)[^"]*)"`)
	pageEpisodeRE = regexp.MustCompile(`"(route_episode_number|episode_number|episodeNumber|episode_no|episodeNo|episode|ep|number)"\s*:\s*"?(\d{1,4})"?[,}]`)
	// pageOpaqueStreamRE: stream keys whose address has no file extension
	// (e.g. a signed player proxy). Used only when the episode has no address
	// with a media extension.
	pageOpaqueStreamRE = regexp.MustCompile(`"(?:direct_play_url|directPlayUrl|play_url|playUrl|stream_url|streamUrl|hls_url|hlsUrl)"\s*:\s*"(https?:[^"]+)"`)
	// pageEmptyStreamRE: an episode the list names but gives no stream for
	// (the site holds it back, e.g. until it is unlocked on the site).
	pageEmptyStreamRE  = regexp.MustCompile(`"(?:play_url|playUrl)"\s*:\s*(?:""|null)`)
	episodeObjectKeyRE = regexp.MustCompile(`"(?:direct_play_url|directPlayUrl|play_url|playUrl|multi_subtitles|subtitles|direct_subtitle_url|directSubtitleUrl)"\s*:`)
	pageTitleRE        = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	jsonEscapeRE       = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
)

// unescapeJSONText decodes \uXXXX escapes left in a URL copied out of JSON
// text (e.g. & for "&"); without this the query string is broken and the
// stream server answers 403.
func unescapeJSONText(s string) string {
	return jsonEscapeRE.ReplaceAllStringFunc(s, func(m string) string {
		n, err := strconv.ParseUint(m[2:], 16, 32)
		if err != nil {
			return m
		}
		return string(rune(n))
	})
}

// pageEpisodeHint holds the streams the page data lists for the episode on Page.
type pageEpisodeHint struct {
	Page    string
	Streams []string
	// Others: the streams the page data lists for other episodes; a capture
	// that only sees one of these has caught a preloaded neighbour.
	Others []pageEpisodeEntry
}

// otherEpisode returns the episode whose listed stream is stream, or 0.
func (h pageEpisodeHint) otherEpisode(stream string) int {
	for _, e := range h.Others {
		if sameStreamPath(stream, e.Stream) {
			return e.Episode
		}
	}
	return 0
}

// matches reports whether stream is one the page data lists for this episode.
func (h pageEpisodeHint) matches(stream string) bool {
	for _, s := range h.Streams {
		if sameStreamPath(stream, s) {
			return true
		}
	}
	return false
}

// freshEpisodeStream is a stream the capture browser saw the page load for
// another episode of the series (players preload the next episodes). Its
// signature is current, while the page data the site serves from its cache
// often carries expired ones for the same file.
type freshEpisodeStream struct {
	URL     string
	Episode int
	Seen    time.Time
}

// freshStreamTTL: signed stream addresses seen so far stay valid for about an
// hour; keep a margin.
const freshStreamTTL = 45 * time.Minute

// rememberFreshStream records a preloaded stream of episode ep and reports
// whether it was not known yet.
func (a *app) rememberFreshStream(stream string, ep int) bool {
	a.freshMu.Lock()
	defer a.freshMu.Unlock()
	for i, f := range a.freshStreams {
		if sameStreamPath(f.URL, stream) {
			a.freshStreams[i] = freshEpisodeStream{URL: stream, Episode: ep, Seen: time.Now()}
			return false
		}
	}
	a.freshStreams = append(a.freshStreams, freshEpisodeStream{URL: stream, Episode: ep, Seen: time.Now()})
	return true
}

// freshStreamFor returns a recently seen address of the same file as one of
// streams (in their order), or "".
func (a *app) freshStreamFor(streams []string) string {
	a.freshMu.Lock()
	defer a.freshMu.Unlock()
	for _, s := range streams {
		for _, f := range a.freshStreams {
			if time.Since(f.Seen) < freshStreamTTL && sameStreamPath(f.URL, s) {
				return f.URL
			}
		}
	}
	return ""
}

// sameStreamPath reports whether two stream addresses name the same file
// (same host and path; the signed query string may differ per request).
// Signed proxy addresses (a path segment carrying a base64 JSON token) are
// re-signed on every page load, so they are compared by the source file the
// token names instead.
func sameStreamPath(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil || ua.Path == "" {
		return false
	}
	if strings.EqualFold(ua.Host, ub.Host) && ua.Path == ub.Path {
		return true
	}
	sa := signedStreamSource(ua.Path)
	return sa != "" && sa == signedStreamSource(ub.Path)
}

// signedStreamSource returns the "src" named by a base64 JSON token in the
// address path ("eyJ…" segment, optionally followed by ".signature").
func signedStreamSource(path string) string {
	for _, seg := range strings.Split(path, "/") {
		if !strings.HasPrefix(seg, "eyJ") {
			continue
		}
		part := strings.TrimRight(strings.SplitN(seg, ".", 2)[0], "=")
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(part)
		}
		if err != nil {
			continue
		}
		var tok map[string]interface{}
		if json.Unmarshal(raw, &tok) != nil {
			continue
		}
		if src, _ := tok["src"].(string); src != "" {
			return src
		}
	}
	return ""
}

// streamSourceURL unwraps a signed proxy address (/e/m/<token> or /e/s/<token>)
// to the underlying "src" URL in its token, or returns raw unchanged.
func streamSourceURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if src := signedStreamSource(u.Path); src != "" {
		if resolved := resolveCandidateURL(raw, src); resolved != "" {
			return resolved
		}
		return src
	}
	return raw
}

// sameMediaFile reports whether two media or subtitle addresses refer to the
// same underlying file (unwrapping any signed proxy token on either side first,
// then comparing host and path).
func sameMediaFile(a, b string) bool {
	ua, err1 := url.Parse(streamSourceURL(a))
	ub, err2 := url.Parse(streamSourceURL(b))
	if err1 != nil || err2 != nil || ua.Path == "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) && ua.Path == ub.Path
}

func episodeObjectString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func episodeObjectBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if b, ok := m[k].(bool); ok && b {
			return true
		}
	}
	return false
}

func cleanHTTPURL(s string) string {
	s = strings.TrimSpace(html.UnescapeString(unescapeJSONText(strings.ReplaceAll(s, `\/`, `/`))))
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return s
	}
	return ""
}

// episodeJSONObjects walks each episode JSON object in body whose episode
// number equals ep, stopping early when fn returns true.
func episodeJSONObjects(body string, ep int, fn func(map[string]any) bool) {
	if ep <= 0 {
		return
	}
	seenStart := map[int]bool{}
	for _, loc := range episodeObjectKeyRE.FindAllStringIndex(body, 4000) {
		start := enclosingObjectStart(body, loc[0])
		if start < 0 || seenStart[start] {
			continue
		}
		seenStart[start] = true
		obj := balancedObject(body, start)
		var m map[string]any
		if obj == "" || json.Unmarshal([]byte(obj), &m) != nil || entryEpisodeNumber(m) != ep {
			continue
		}
		if fn(m) {
			return
		}
	}
}

// episodeObjectStreams extracts play_url and direct_play_url from one episode
// JSON object and reports whether play_url should be preferred (is_playable is
// true, play_url is non-empty, and play_url does not point to the same media
// file as direct_play_url).
func episodeObjectStreams(m map[string]any) (playURL, directPlayURL string, preferPlay bool) {
	playURL = cleanHTTPURL(episodeObjectString(m, "play_url", "playUrl"))
	directPlayURL = cleanHTTPURL(episodeObjectString(m, "direct_play_url", "directPlayUrl"))
	isPlayable := episodeObjectBool(m, "is_playable", "isPlayable")
	preferPlay = isPlayable && playURL != "" && (directPlayURL == "" || !sameMediaFile(playURL, directPlayURL))
	return playURL, directPlayURL, preferPlay
}

func lookupEpisodeObjectStreams(body string, ep int) (playURL, directPlayURL string, preferPlay bool) {
	episodeJSONObjects(body, ep, func(m map[string]any) bool {
		playURL, directPlayURL, preferPlay = episodeObjectStreams(m)
		return playURL != "" || directPlayURL != ""
	})
	return playURL, directPlayURL, preferPlay
}

type pageEpisodeEntry struct {
	Episode int
	Stream  string
	// Opaque: the address has no media file extension (less certain to be
	// directly downloadable than one that has).
	Opaque bool
}

// enclosingObjectStart returns the index of the "{" that opens the JSON
// object containing pos (nested objects before pos are skipped), or -1.
func enclosingObjectStart(body string, pos int) int {
	depth := 0
	for i := pos - 1; i >= 0 && pos-i <= 64*1024; i-- {
		switch body[i] {
		case '}':
			depth++
		case '{':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// pageEpisodeEntries lists the (episode, stream) pairs found in the page data.
// Each stream is paired with an episode number from its own object: the text
// between the "{" that opens the object holding the stream and the stream.
func pageEpisodeEntries(body string) []pageEpisodeEntry {
	body = strings.ReplaceAll(body, `\/`, `/`)
	var out []pageEpisodeEntry
	type match struct {
		loc    []int
		opaque bool
	}
	var matches []match
	taken := map[int]bool{}
	for _, m := range pageStreamRE.FindAllStringSubmatchIndex(body, 2000) {
		matches = append(matches, match{m, false})
		taken[m[2]] = true
	}
	for _, m := range pageOpaqueStreamRE.FindAllStringSubmatchIndex(body, 2000) {
		if !taken[m[2]] {
			matches = append(matches, match{m, true})
		}
	}
	// Episodes listed without a stream become entries with an empty Stream.
	for _, m := range pageEmptyStreamRE.FindAllStringIndex(body, 2000) {
		matches = append(matches, match{[]int{m[0], m[1], m[1], m[1]}, false})
	}
	for _, mm := range matches {
		m := mm.loc
		start := enclosingObjectStart(body, m[0])
		if start < 0 {
			continue
		}
		obj := body[start:m[0]]
		ep, rank := 0, 99
		for _, e := range pageEpisodeRE.FindAllStringSubmatch(obj, -1) {
			// Prefer explicit episode keys over a plain "number".
			r := map[string]int{"route_episode_number": 0, "episode_number": 1, "episodeNumber": 1, "episode_no": 1, "episodeNo": 1, "episode": 2, "ep": 2, "number": 3}[e[1]]
			if n, err := strconv.Atoi(e[2]); err == nil && n > 0 && r < rank {
				ep, rank = n, r
			}
		}
		if ep > 0 {
			out = append(out, pageEpisodeEntry{Episode: ep, Stream: html.UnescapeString(unescapeJSONText(body[m[2]:m[3]])), Opaque: mm.opaque})
		}
	}
	return out
}

// episodeStreamFromPage returns the stream listed for episode ep. It only
// trusts a real episode list (at least three different episode numbers), so
// a lone number elsewhere in the data is never mistaken for an episode.
func episodeStreamFromPage(body string, ep int) (string, bool) {
	stream, _, _ := lookupPageEpisode(body, ep)
	return stream, stream != ""
}

// lookupPageEpisode also reports whether the page has an episode list at all
// and the highest episode in it.
func lookupPageEpisode(body string, ep int) (stream string, listed bool, maxEp int) {
	streams, listed, maxEp := pageEpisodeStreams(body, ep)
	if len(streams) > 0 {
		stream = streams[0]
	}
	return stream, listed, maxEp
}

// pageEpisodeStreams lists every stream the page data gives for episode ep,
// addresses with a media file extension first.
func pageEpisodeStreams(body string, ep int) (streams []string, listed bool, maxEp int) {
	entries := pageEpisodeEntries(body)
	distinct := map[int]bool{}
	for _, e := range entries {
		distinct[e.Episode] = true
		if e.Episode > maxEp {
			maxEp = e.Episode
		}
	}
	if ep <= 0 || len(distinct) < 3 {
		return nil, false, 0
	}
	var opaque []string
	for _, e := range entries {
		if e.Episode != ep || e.Stream == "" {
			continue
		}
		if e.Opaque {
			opaque = append(opaque, e.Stream)
		} else {
			streams = append(streams, e.Stream)
		}
	}
	all := append(streams, opaque...)
	if playURL, directPlayURL, preferPlay := lookupEpisodeObjectStreams(body, ep); preferPlay && len(all) > 0 {
		var ordered []string
		addUnique := func(u string) {
			if u == "" {
				return
			}
			for _, existing := range ordered {
				if existing == u {
					return
				}
			}
			ordered = append(ordered, u)
		}
		addUnique(playURL)
		addUnique(directPlayURL)
		for _, s := range all {
			addUnique(s)
		}
		return ordered, true, maxEp
	}
	return all, true, maxEp
}

// errNoSuchEpisode: the page lists the series' episodes and this one is not
// among them, so no other method (browser capture included) can find it.
type errNoSuchEpisode struct{ ep, max int }

func (e *errNoSuchEpisode) Error() string {
	return fmt.Sprintf("網頁的集數清單中沒有第 %d 集（清單最多到第 %d 集）", e.ep, e.max)
}

// errEpisodeWithheld: the page lists this episode but gives no stream for it
// (the site holds it back until it is unlocked there). Only the page itself,
// played in the capture browser, can load it.
type errEpisodeWithheld struct{ ep int }

func (e *errEpisodeWithheld) Error() string {
	return fmt.Sprintf("網頁資料列出第 %d 集，但沒有提供這集的串流（網站可能要先在播放畫面解鎖）", e.ep)
}

// errEpisodeDeferred: a withheld episode put off to the end of the batch
// (app.deferWithheld), so every episode the page gives directly is saved
// first and the ones that need the user are handled together.
type errEpisodeDeferred struct{ ep int }

func (e *errEpisodeDeferred) Error() string {
	return fmt.Sprintf("第 %d 集需要在網站上解鎖，排到最後處理", e.ep)
}

// pageEpisodeListed reports whether the page data names episode ep at all
// (with or without a stream), and returns the streams listed for the others.
func pageEpisodeListed(body string, ep int) (bool, []pageEpisodeEntry) {
	found := false
	var others []pageEpisodeEntry
	for _, e := range pageEpisodeEntries(body) {
		if e.Episode == ep {
			found = true
		} else if e.Stream != "" {
			others = append(others, e)
		}
	}
	return found, others
}

func pageTitle(body string) string {
	m := pageTitleRE.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

// pageEpisodeTitle is the name base for a page-data download. A readable
// series name in the address (…/series-name/3) wins, so files are named and
// sorted like browser-capture downloads (series-name_E003); the page title
// (which often repeats the episode number and site text) is only used when
// that address segment is a bare number.
func pageEpisodeTitle(rawURL, body string) string {
	if u, err := url.Parse(rawURL); err == nil {
		parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
		if len(parts) >= 2 && !allDigits(parts[len(parts)-2]) && safeWindowsBaseName(parts[len(parts)-2]) != "" {
			return ""
		}
	}
	return pageTitle(body)
}

// downloadPageEpisode downloads the stream the page data lists for the page's
// episode, named after the page title (which includes the episode number).
func (a *app) downloadPageEpisode(ctx context.Context, rawURL, finalURL, body string, mode int, formatID, output string, cfg settings) (bool, error) {
	ep := pageEpisodeNumber(rawURL)
	streams, listed, maxEp := pageEpisodeStreams(body, ep)
	if len(streams) == 0 {
		if !listed {
			return false, nil
		}
		if present, others := pageEpisodeListed(body, ep); present {
			if a.deferWithheld {
				return true, &errEpisodeDeferred{ep: ep}
			}
			// The capture browser plays the page; the other episodes' streams
			// tell it which preloaded ones are not this episode.
			a.pageEpisodeHint = pageEpisodeHint{Page: rawURL, Others: others}
			a.postLog(fmt.Sprintf("ℹ 網站沒有直接提供第 %d 集，需要在播放畫面解鎖：接著會打開瀏覽器視窗，請在視窗中操作（本工具不會自動點擊廣告；解鎖通常會維持一段時間，期間後面的集數可直接播放）。若知道影片的原始來源網址，改用原始網址下載可省去這一步。\r\n", ep))
			return true, &errEpisodeWithheld{ep: ep}
		}
		return true, &errNoSuchEpisode{ep: ep, max: maxEp}
	}
	// Remember the listed streams: if they cannot be fetched directly, the
	// capture browser uses them to tell this episode apart from preloaded
	// neighbours, and never takes another episode's stream for this one.
	_, others := pageEpisodeListed(body, ep)
	a.pageEpisodeHint = pageEpisodeHint{Page: rawURL, Streams: streams, Others: others}

	streamMode := mode
	if mode == 2 {
		streamMode = 0
	}
	runOne := func(target string) ([]string, error) {
		a.resetOutputs()
		done := len(a.itemFiles)
		if a.pageStreamDownload != nil {
			return a.pageStreamDownload(ctx, target)
		}
		args, err := a.buildArgs(target, streamMode, formatID, output, cfg)
		if err != nil {
			return nil, err
		}
		args = replaceOutputTemplate(args, a.outputBase(rawURL, pageEpisodeTitle(rawURL, body))+".%(ext)s")
		extras := []string{"--no-playlist", "--referer", finalURL, "--impersonate", "chrome"}
		headers := map[string]string{"referer": finalURL, "user-agent": chromeUserAgent}
		if base, err := url.Parse(finalURL); err == nil && base.Scheme != "" && base.Host != "" {
			extras = append(extras, "--add-header", "Origin:"+base.Scheme+"://"+base.Host)
			headers["origin"] = base.Scheme + "://" + base.Host
		}
		args = a.applyHLSTracks(ctx, args, streamMode, cfg, rawURL, target, headers)
		args = insertBeforeLast(args, extras...)
		err = a.runCommand(ctx, args)
		if err == nil && !a.stopRequested.Load() {
			err = a.finishStep(ctx, mode, cfg, true)
		}
		if err != nil {
			return nil, err
		}
		return a.itemFiles[done:], nil
	}

	playURL, directPlayURL, preferPlay := lookupEpisodeObjectStreams(body, ep)
	if preferPlay && directPlayURL != "" {
		primary := playURL
		if fresh := a.freshStreamFor([]string{playURL}); fresh != "" {
			primary = fresh
			a.postLog(fmt.Sprintf("→ 使用先前播放其他集時瀏覽器預先載入的第 %d 集串流（%s；網頁資料中的連結可能已過期）。\r\n", ep, hostOnly(primary)))
		} else {
			a.postLog(fmt.Sprintf("→ 本集可直接播放，改下載網頁播放器使用的串流（%s）。\r\n", hostOnly(primary)))
		}
		videos, err := runOne(primary)
		if err == nil {
			if !a.stopRequested.Load() {
				a.savePageSubtitlesFor(ctx, body, finalURL, primary, ep, videos, cfg)
			}
			return true, nil
		}
		if a.stopRequested.Load() || errors.Is(err, errStopped) || ctx.Err() != nil {
			return true, err
		}
		fallback := directPlayURL
		if fresh := a.freshStreamFor([]string{directPlayURL}); fresh != "" {
			fallback = fresh
		}
		a.postLog(fmt.Sprintf("ℹ 頁面播放器的串流無法下載（%s），改用頁面備用串流（%s）。\r\n", firstLine(err.Error()), hostOnly(fallback)))
		a.diagNote("播放器串流失敗，改用頁面備用串流：" + diagErrorText(err))
		videos, fbErr := runOne(fallback)
		if fbErr == nil && !a.stopRequested.Load() {
			a.savePageSubtitlesFor(ctx, body, finalURL, fallback, ep, videos, cfg)
		}
		return true, fbErr
	}

	stream := streams[0]
	if fresh := a.freshStreamFor(streams); fresh != "" {
		stream = fresh
		a.postLog(fmt.Sprintf("→ 使用先前播放其他集時瀏覽器預先載入的第 %d 集串流（%s；網頁資料中的連結可能已過期）。\r\n", ep, hostOnly(stream)))
	} else {
		a.postLog(fmt.Sprintf("→ 頁面播放資料列出各集串流，直接取得第 %d 集（%s）。\r\n", ep, hostOnly(stream)))
	}
	videos, err := runOne(stream)
	if err == nil && !a.stopRequested.Load() {
		a.savePageSubtitlesFor(ctx, body, finalURL, stream, ep, videos, cfg)
	}
	return true, err
}
