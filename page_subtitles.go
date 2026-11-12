//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Some pages keep their subtitles as separate files beside the video: either
// in the player data (subtitles: {"1":[{"label":…,"src":"….vtt"}], …}, one
// list per episode) or as <track> elements. Those files never reach yt-dlp,
// which is only handed the video stream, so they are fetched here and saved
// next to the video under the same name, where players pick them up.

type pageSubtitle struct {
	URL     string
	Label   string
	Lang    string
	Default bool
	// HLS: URL is a subtitle playlist of WebVTT segments (audio_language.go),
	// fetched with Headers.
	HLS     bool
	Headers map[string]string
}

var (
	pageSubtitlesKeyRE = regexp.MustCompile(`(?:"subtitles"|'subtitles'|\bsubtitles)\s*:\s*\{`)
	pageTrackRE        = regexp.MustCompile(`(?is)<track\b[^>]*>`)
	// pageScriptRE: script and style blocks, where "<track src=blob>" can be
	// just words in a comment, not an element.
	pageScriptRE    = regexp.MustCompile(`(?is)<(?:script|style)\b[^>]*>.*?</(?:script|style)\s*>`)
	pageTrackAttrRE = regexp.MustCompile(`(?is)\b(kind|src|srclang|label|default)\b(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?`)
	subtitleExts    = map[string]bool{".vtt": true, ".srt": true, ".ass": true, ".ssa": true}
)

// balancedObject returns the JSON object that starts at body[start] ('{'),
// skipping braces inside strings.
func balancedObject(body string, start int) string {
	depth, inStr, esc := 0, false, false
	var quote byte
	for i := start; i < len(body); i++ {
		c := body[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == quote:
				inStr = false
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr, quote = true, c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : i+1]
			}
		}
	}
	return ""
}

func subtitleFromJSON(m map[string]any, baseURL string) (pageSubtitle, bool) {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	// Players list their preview thumbnails and chapters as tracks too.
	if kind := strings.ToLower(str("kind")); kind != "" && kind != "subtitles" && kind != "captions" {
		return pageSubtitle{}, false
	}
	src := resolveCandidateURL(baseURL, str("src", "url", "file", "subtitle_url"))
	if src == "" {
		return pageSubtitle{}, false
	}
	s := pageSubtitle{URL: src, Label: str("label", "name", "language_name"), Lang: str("lang", "srclang", "language", "language_code")}
	for _, k := range []string{"isDefault", "default", "is_default"} {
		if b, ok := m[k].(bool); ok && b {
			s.Default = true
		}
	}
	return s, true
}

// pageEpisodeSubtitles returns the subtitle files the page lists for episode
// ep: the per-episode subtitles object first, then the page's <track>
// elements (which belong to the video the page plays).
func pageEpisodeSubtitles(body, baseURL string, ep int) []pageSubtitle {
	return pageEpisodeSubtitlesFor(body, baseURL, "", ep)
}

func pageEpisodeSubtitlesFor(body, baseURL, streamURL string, ep int) []pageSubtitle {
	var out []pageSubtitle
	seen := map[string]bool{}
	add := func(s pageSubtitle) {
		if !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	for _, loc := range pageSubtitlesKeyRE.FindAllStringIndex(body, -1) {
		obj := balancedObject(body, loc[1]-1)
		var byEp map[string]json.RawMessage
		if obj == "" || json.Unmarshal([]byte(obj), &byEp) != nil {
			continue
		}
		raw, ok := byEp[strconv.Itoa(ep)]
		if !ok || ep <= 0 {
			continue
		}
		var list []map[string]any
		if json.Unmarshal(raw, &list) != nil {
			var one map[string]any
			if json.Unmarshal(raw, &one) != nil {
				continue
			}
			list = []map[string]any{one}
		}
		for _, m := range list {
			if s, ok := subtitleFromJSON(m, baseURL); ok {
				add(s)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if subs, matched := pageEntrySubtitles(body, baseURL, streamURL, ep); matched {
		return subs
	}
	for _, tag := range pageTrackRE.FindAllString(pageScriptRE.ReplaceAllString(body, ""), -1) {
		attrs := map[string]string{}
		for _, m := range pageTrackAttrRE.FindAllStringSubmatch(tag[len("<track"):], -1) {
			attrs[strings.ToLower(m[1])] = html.UnescapeString(m[2] + m[3] + m[4])
		}
		if kind := strings.ToLower(attrs["kind"]); kind != "" && kind != "subtitles" && kind != "captions" {
			continue
		}
		src := resolveCandidateURL(baseURL, attrs["src"])
		if src == "" {
			continue
		}
		_, isDefault := attrs["default"]
		add(pageSubtitle{URL: src, Label: attrs["label"], Lang: attrs["srclang"], Default: isDefault})
	}
	return out
}

// 字幕對應規則：影片是哪一支，字幕就要配哪一支。
//   - 下載 play_url 那支 → 用 multi_subtitles／subtitles（依 selected → 網址 lang → 預設 → 第一個）。
//   - 下載 direct_play_url 那支 → multi_subtitles／subtitles 裡 subtitle_url 的 src 與
//     direct_subtitle_url 相同的那一項優先，沒有就用 direct_subtitle_url（語言未知則不加標籤）。
//   - 兩支是同一個檔案且頁面有字幕清單 → 視為 play_url 那支。
func pageEntrySubtitles(body, baseURL, streamURL string, ep int) ([]pageSubtitle, bool) {
	if ep <= 0 {
		return nil, false
	}
	want := ""
	if u, err := url.Parse(baseURL); err == nil {
		want = u.Query().Get("lang")
	}
	var result []pageSubtitle
	matched := false
	episodeJSONObjects(body, ep, func(m map[string]any) bool {
		var subs []pageSubtitle
		for _, key := range []string{"multi_subtitles", "subtitles"} {
			list, _ := m[key].([]any)
			for _, item := range list {
				if sm, ok := item.(map[string]any); ok {
					if s, ok := subtitleFromJSON(sm, baseURL); ok {
						subs = append(subs, s)
					}
				}
			}
			if len(subs) > 0 {
				break
			}
		}
		directSub := resolveCandidateURL(baseURL, episodeObjectString(m, "direct_subtitle_url", "directSubtitleUrl"))
		if len(subs) == 0 && directSub == "" {
			return false
		}
		matched = true
		playURL, directPlayURL, preferPlay := episodeObjectStreams(m)
		if directPlayURL == "" {
			directPlayURL = resolveCandidateURL(baseURL, episodeObjectString(m, "direct_play_url", "directPlayUrl"))
		}
		useDirect := false
		if streamURL != "" {
			useDirect = directPlayURL != "" && sameMediaFile(streamURL, directPlayURL)
		} else {
			useDirect = directPlayURL != "" && !preferPlay
		}
		// play_url 與 direct_play_url 是同一支檔案時，就是播放器在播的那支：
		// direct_subtitle_url 只是網站的預設語言，要照頁面選定的語言。
		samePlay := playURL != "" && directPlayURL != "" && sameMediaFile(playURL, directPlayURL)
		if useDirect && !(samePlay && len(subs) > 0) {
			if directSub != "" {
				for _, s := range subs {
					if sameMediaFile(s.URL, directSub) {
						s.Default = true
						result = []pageSubtitle{s}
						return true
					}
				}
				result = []pageSubtitle{{URL: directSub, Default: true}}
				return true
			}
			if playURL != "" && !sameMediaFile(playURL, directPlayURL) {
				result = nil
				return true
			}
		}
		if len(subs) == 0 {
			result = nil
			return true
		}
		lang, _ := m["selected_subtitle_language"].(string)
		if strings.TrimSpace(lang) == "" {
			lang = want
		}
		pick := -1
		for i, s := range subs {
			if lang != "" && strings.EqualFold(s.Lang, strings.TrimSpace(lang)) {
				pick = i
				break
			}
		}
		for i := 0; pick < 0 && i < len(subs); i++ {
			if subs[i].Default {
				pick = i
			}
		}
		if pick < 0 {
			pick = 0
		}
		s := subs[pick]
		s.Default = true
		result = []pageSubtitle{s}
		return true
	})
	return result, matched
}

// entryEpisodeNumber is the episode number an episode object names
// (explicit episode keys before a plain "number").
func entryEpisodeNumber(m map[string]any) int {
	for _, k := range []string{"route_episode_number", "episode_number", "episodeNumber", "episode_no", "episodeNo", "episode", "ep", "number"} {
		switch v := m[k].(type) {
		case float64:
			if v > 0 && v == float64(int(v)) {
				return int(v)
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// subtitleNames gives each subtitle the suffix of its file name base: the
// default (or first) one takes the video's own name, the others add their
// language.
func subtitleNames(videoBase string, subs []pageSubtitle) []string {
	primary := 0
	for i, s := range subs {
		if s.Default {
			primary = i
			break
		}
	}
	names := make([]string, len(subs))
	// Windows file names ignore case: "en" and "EN" are the same file.
	used := map[string]bool{strings.ToLower(videoBase): true}
	for i, s := range subs {
		name := videoBase
		if i != primary {
			tag := safeWindowsBaseName(s.Lang)
			if tag == "" {
				tag = safeWindowsBaseName(s.Label)
			}
			if tag == "" || used[strings.ToLower(videoBase+"."+tag)] {
				tag = strconv.Itoa(i + 1)
			}
			// The number may be taken too (a language tagged "4"): count on.
			for n := i + 2; used[strings.ToLower(videoBase+"."+tag)]; n++ {
				tag = strconv.Itoa(n)
			}
			name = videoBase + "." + tag
			used[strings.ToLower(name)] = true
		}
		names[i] = name
	}
	return names
}

func subtitleExt(raw, text string) string {
	if u, err := url.Parse(raw); err == nil {
		if ext := strings.ToLower(filepath.Ext(u.Path)); subtitleExts[ext] {
			return ext
		}
	}
	if strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") {
		return ".vtt"
	}
	if strings.Contains(text, "[Script Info]") {
		return ".ass"
	}
	return ".srt"
}

func fetchSubtitle(ctx context.Context, raw, pageURL string, cfg settings) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	if pageURL != "" {
		req.Header.Set("Referer", pageURL)
		if base, err := url.Parse(pageURL); err == nil && base.Scheme != "" && base.Host != "" {
			req.Header.Set("Origin", base.Scheme+"://"+base.Host)
		}
	}
	if u, err := url.Parse(raw); err == nil {
		if cookie := cookieHeaderFromFile(cfg.CookieFile, u.Hostname()); cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return "", err
	}
	text := strings.TrimPrefix(string(b), "\ufeff")
	if !strings.Contains(text, "-->") && !strings.Contains(text, "[Script Info]") {
		return "", errors.New("內容不是字幕檔")
	}
	if isThumbnailTrack(text) {
		return "", errThumbnailTrack
	}
	return text, nil
}

var errThumbnailTrack = errors.New("內容是進度列的預覽縮圖清單，不是字幕")

var thumbnailCueRE = regexp.MustCompile(`(?i)#xywh=|\.(?:jpe?g|png|webp|gif|bmp)(?:[?#]|$)`)

// isThumbnailTrack reports whether a WebVTT/SRT file is a player's preview
// thumbnail track (every cue is an image, often a sprite region "#xywh=…")
// rather than text. Such files use the same format and type as subtitles.
func isThumbnailTrack(text string) bool {
	cues := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "-->") || strings.HasPrefix(line, "WEBVTT") {
			continue
		}
		if _, err := strconv.Atoi(line); err == nil {
			continue
		}
		if !thumbnailCueRE.MatchString(line) {
			return false
		}
		cues++
	}
	return cues > 0
}

// savePageSubtitles saves the page's subtitles for episode ep next to each
// downloaded video. WebVTT is converted to SRT, which more players open; if
// the conversion fails the .vtt is kept. A subtitle that cannot be fetched
// only logs a warning: the video itself is done.
func (a *app) savePageSubtitles(ctx context.Context, body, pageURL string, ep int, videos []string, cfg settings) {
	a.savePageSubtitlesFor(ctx, body, pageURL, "", ep, videos, cfg)
}

func (a *app) savePageSubtitlesFor(ctx context.Context, body, pageURL, streamURL string, ep int, videos []string, cfg settings) {
	a.saveSubtitles(ctx, pageEpisodeSubtitlesFor(body, pageURL, streamURL, ep), pageURL, videos, cfg)
}

// saveSubtitles fetches subs and saves them next to each video file in videos
// (audio files are skipped).
func (a *app) saveSubtitles(ctx context.Context, subs []pageSubtitle, pageURL string, videos []string, cfg settings) {
	a.saveSubtitlesAt(ctx, subs, pageURL, videoBaseNames(videos), cfg)
}

func videoBaseNames(videos []string) []string {
	var bases []string
	for _, v := range videos {
		ext := strings.ToLower(filepath.Ext(v))
		if mediaExts[ext] && !audioExts[ext] && validFile(v, 1) {
			bases = append(bases, strings.TrimSuffix(v, filepath.Ext(v)))
		}
	}
	return bases
}

func (a *app) saveSubtitlesAt(ctx context.Context, subs []pageSubtitle, pageURL string, bases []string, cfg settings) []savedSubtitle {
	subs = fetchableSubtitles(subs)
	if len(subs) == 0 {
		subs = a.hlsSubs
	}
	if len(subs) == 0 || len(bases) == 0 {
		return nil
	}
	names := subtitleNames("", subs)
	var out []savedSubtitle
	saved := 0
	for i, s := range subs {
		if a.stopRequested.Load() {
			return out
		}
		var text string
		var err error
		if s.HLS {
			text, err = fetchHLSSubtitle(ctx, s.URL, s.Headers)
		} else {
			text, err = fetchSubtitle(ctx, s.URL, pageURL, cfg)
		}
		if errors.Is(err, errThumbnailTrack) {
			a.postLog("ℹ 略過進度列的預覽縮圖清單（格式與字幕相同，但不是字幕）。\r\n")
			a.diagNote("略過預覽縮圖清單（不是字幕）")
			continue
		}
		if err != nil {
			a.postLog(fmt.Sprintf("⚠ 字幕下載失敗（%s）：%s\r\n", hostOnly(s.URL), firstLine(err.Error())))
			a.diagNote("外掛字幕 ✗ " + firstLine(err.Error()))
			continue
		}
		ext := subtitleExt(s.URL, text)
		for _, b := range bases {
			base := b + names[i]
			path := base + ext
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				a.postLog("⚠ 字幕存檔失敗：" + firstLine(err.Error()) + "\r\n")
				continue
			}
			if ext == ".vtt" {
				srt, tmp := base+".srt", base+".tmp.srt"
				runSmallContext(ctx, a.ffmpegPath, "-y", "-loglevel", "error", "-i", path, tmp)
				if validFile(tmp, 1) && os.Rename(tmp, srt) == nil {
					_ = os.Remove(path)
					path = srt
				} else {
					_ = os.Remove(tmp)
				}
			}
			label := ""
			if s.Label != "" {
				label = "（" + s.Label + "）"
			}
			a.postLog("✓ 已下載字幕：" + filepath.Base(path) + label + "\r\n")
			saved++
			itemLabel := s.Label
			if itemLabel == "" {
				itemLabel = s.Lang
			}
			out = append(out, savedSubtitle{Path: path, Label: itemLabel, Lang: s.Lang})
		}
	}
	if saved > 0 {
		a.diagNote(fmt.Sprintf("外掛字幕 ✓ %d 個", saved))
	}
	return out
}

// fetchableSubtitles drops subtitles that only exist inside the page (blob:
// and data: addresses the player made from a file it already loaded; that
// file is listed separately).
func fetchableSubtitles(subs []pageSubtitle) []pageSubtitle {
	var out []pageSubtitle
	for _, s := range subs {
		if u, err := url.Parse(s.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			out = append(out, s)
		}
	}
	return out
}

// Pages that build their player with scripts (the browser-capture path) do
// not show their subtitles in the HTML: the player asks the site for the
// episode's subtitle list (JSON) and then loads the file it shows. The capture
// browser sees both.

// maxCaptureSubtitleFiles: more subtitle files than this in one capture are
// the segments of a streamed subtitle track, not separate files.
const maxCaptureSubtitleFiles = 6

// subtitleLoadWait: how long a capture waits for the player to load a
// subtitle file the page lists, after the video stream was chosen.
const subtitleLoadWait = 5 * time.Second

// isSubtitleResponse reports whether a response is a subtitle file, by the
// URL's extension or its WebVTT type.
func isSubtitleResponse(raw, mimeType string) bool {
	if u, err := url.Parse(raw); err == nil && subtitleExts[strings.ToLower(filepath.Ext(u.Path))] {
		return true
	}
	return strings.Contains(strings.ToLower(mimeType), "text/vtt")
}

// jsonSubtitleList returns the subtitle files a JSON response lists: objects
// whose src/url/file names a subtitle file.
func jsonSubtitleList(body []byte, baseURL string) []pageSubtitle {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	var out []pageSubtitle
	seen := map[string]bool{}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 8 || len(out) > 32 {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			if s, ok := subtitleFromJSON(t, baseURL); ok && isSubtitleResponse(s.URL, "") {
				if !seen[s.URL] {
					seen[s.URL] = true
					out = append(out, s)
				}
				return
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k], depth+1)
			}
		case []any:
			for _, c := range t {
				walk(c, depth+1)
			}
		}
	}
	walk(root, 0)
	return out
}

// captureSubtitles picks the subtitles of the captured episode: the JSON list
// that holds a file the player loaded (every language of this episode), else
// the loaded files themselves, else the page's only subtitle list. The file
// the player showed becomes the main one (named like the video).
func captureSubtitles(pageJSON [][]byte, loaded []string, baseURL string) []pageSubtitle {
	if len(loaded) > maxCaptureSubtitleFiles {
		loaded = nil
	}
	loadedKey := map[string]bool{}
	for _, u := range loaded {
		loadedKey[adURLKey(u)] = true
	}
	var lists [][]pageSubtitle
	for _, body := range pageJSON {
		// A longer list is more likely every episode's subtitles than this one's.
		if l := jsonSubtitleList(body, baseURL); len(l) > 0 && len(l) <= 12 {
			lists = append(lists, l)
		}
	}
	for _, l := range lists {
		for _, s := range l {
			if !loadedKey[adURLKey(s.URL)] {
				continue
			}
			out := append([]pageSubtitle(nil), l...)
			for i := range out {
				out[i].Default = loadedKey[adURLKey(out[i].URL)]
			}
			return out
		}
	}
	if len(loaded) > 0 {
		var out []pageSubtitle
		for i, u := range loaded {
			out = append(out, pageSubtitle{URL: u, Default: i == 0})
		}
		return out
	}
	if len(lists) == 1 {
		return lists[0]
	}
	return nil
}
