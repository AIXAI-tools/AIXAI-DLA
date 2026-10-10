//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
		if e.Episode != ep {
			continue
		}
		if e.Opaque {
			opaque = append(opaque, e.Stream)
		} else {
			streams = append(streams, e.Stream)
		}
	}
	return append(streams, opaque...), true, maxEp
}

// errNoSuchEpisode: the page lists the series' episodes and this one is not
// among them, so no other method (browser capture included) can find it.
type errNoSuchEpisode struct{ ep, max int }

func (e *errNoSuchEpisode) Error() string {
	return fmt.Sprintf("網頁的集數清單中沒有第 %d 集（清單最多到第 %d 集）", e.ep, e.max)
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
		if listed {
			return true, &errNoSuchEpisode{ep: ep, max: maxEp}
		}
		return false, nil
	}
	stream := streams[0]
	// Remember the listed streams: if they cannot be fetched directly, the
	// capture browser uses them to tell this episode apart from preloaded
	// neighbours.
	a.pageEpisodeHint = pageEpisodeHint{Page: rawURL, Streams: streams}
	a.postLog(fmt.Sprintf("→ 頁面播放資料列出各集串流，直接取得第 %d 集（%s）。\r\n", ep, hostOnly(stream)))
	streamMode := mode
	if mode == 2 {
		streamMode = 0
	}
	args, err := a.buildArgs(stream, streamMode, formatID, output, cfg)
	if err != nil {
		return true, err
	}
	args = replaceOutputTemplate(args, capturedOutputBase(rawURL, pageEpisodeTitle(rawURL, body))+".%(ext)s")
	extras := []string{"--no-playlist", "--referer", finalURL, "--impersonate", "chrome"}
	if base, err := url.Parse(finalURL); err == nil && base.Scheme != "" && base.Host != "" {
		extras = append(extras, "--add-header", "Origin:"+base.Scheme+"://"+base.Host)
	}
	args = insertBeforeLast(args, extras...)
	return true, a.runCommand(ctx, args)
}
