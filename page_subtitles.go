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
}

var (
	pageSubtitlesKeyRE = regexp.MustCompile(`(?:"subtitles"|'subtitles'|\bsubtitles)\s*:\s*\{`)
	pageTrackRE        = regexp.MustCompile(`(?is)<track\b[^>]*>`)
	pageTrackAttrRE    = regexp.MustCompile(`(?is)\b(kind|src|srclang|label|default)\b(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?`)
	subtitleExts       = map[string]bool{".vtt": true, ".srt": true, ".ass": true, ".ssa": true}
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
	src := resolveCandidateURL(baseURL, str("src", "url", "file"))
	if src == "" {
		return pageSubtitle{}, false
	}
	s := pageSubtitle{URL: src, Label: str("label", "name", "language_name"), Lang: str("lang", "srclang", "language", "language_code")}
	for _, k := range []string{"isDefault", "default"} {
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
	for _, tag := range pageTrackRE.FindAllString(body, -1) {
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
	return text, nil
}

// savePageSubtitles saves the page's subtitles for episode ep next to each
// downloaded video. WebVTT is converted to SRT, which more players open; if
// the conversion fails the .vtt is kept. A subtitle that cannot be fetched
// only logs a warning: the video itself is done.
func (a *app) savePageSubtitles(ctx context.Context, body, pageURL string, ep int, videos []string, cfg settings) {
	a.saveSubtitles(ctx, pageEpisodeSubtitles(body, pageURL, ep), pageURL, videos, cfg)
}

// saveSubtitles fetches subs and saves them next to each video file in videos
// (audio files are skipped).
func (a *app) saveSubtitles(ctx context.Context, subs []pageSubtitle, pageURL string, videos []string, cfg settings) {
	if len(subs) == 0 {
		return
	}
	var targets []string
	for _, v := range videos {
		ext := strings.ToLower(filepath.Ext(v))
		if mediaExts[ext] && !audioExts[ext] && validFile(v, 1) {
			targets = append(targets, v)
		}
	}
	if len(targets) == 0 {
		return
	}
	names := subtitleNames("", subs)
	saved := 0
	for i, s := range subs {
		if a.stopRequested.Load() {
			return
		}
		text, err := fetchSubtitle(ctx, s.URL, pageURL, cfg)
		if err != nil {
			a.postLog(fmt.Sprintf("⚠ 字幕下載失敗（%s）：%s\r\n", hostOnly(s.URL), firstLine(err.Error())))
			a.diagNote("外掛字幕 ✗ " + firstLine(err.Error()))
			continue
		}
		ext := subtitleExt(s.URL, text)
		for _, v := range targets {
			base := strings.TrimSuffix(v, filepath.Ext(v)) + names[i]
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
			a.postLog("✓ 已下載字幕：" + filepath.Base(path) + "\r\n")
			saved++
		}
	}
	if saved > 0 {
		a.diagNote(fmt.Sprintf("外掛字幕 ✓ %d 個", saved))
	}
}

// Pages that build their player with scripts (the browser-capture path) do
// not show their subtitles in the HTML: the player asks the site for the
// episode's subtitle list (JSON) and then loads the file it shows. The capture
// browser sees both.

// maxCaptureSubtitleFiles: more subtitle files than this in one capture are
// the segments of a streamed subtitle track, not separate files.
const maxCaptureSubtitleFiles = 6

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
