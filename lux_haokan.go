//go:build windows

// Lux fallback, media conversion, and Haokan stream extraction and selection.
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
	"strconv"
	"strings"
	"time"
)

type haokanStream struct {
	Key    string
	URL    string
	Height int
	SizeMB float64
}

func (a *app) runLuxFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	args, err := a.buildLuxArgs(rawURL, mode, formatID, output, cfg)
	if err != nil {
		return err
	}
	started := time.Now().Add(-2 * time.Second)
	if err := a.runLuxCommand(ctx, args); err != nil {
		return err
	}
	if mode != 3 {
		f, err := findNewestMediaFile(output, started)
		if err != nil {
			return errors.New("Lux 回報完成，但儲存位置中沒有新的檔案")
		}
		a.noteOutput(f)
	}
	if mode == 1 {
		mediaPath, err := findNewestMediaFile(output, started)
		if err != nil {
			return fmt.Errorf("Lux 已完成音訊下載，但找不到可轉換的輸出檔：%w", err)
		}
		if strings.EqualFold(filepath.Ext(mediaPath), ".mp3") {
			return nil
		}
		return a.convertMediaToMP3(ctx, mediaPath)
	}
	return nil
}

func (a *app) buildLuxArgs(rawURL string, mode int, formatID, output string, cfg settings) ([]string, error) {
	args := []string{"-o", output, "-retry", "10"}
	if cfg.FileName != "" && mode != 2 {
		args = append(args, "-O", cfg.FileName)
	}
	if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
		args = append(args, "-c", cookieFile)
	}
	switch mode {
	case 0:
		args = append(args, rawURL)
	case 1:
		args = append(args, "--audio-only", rawURL)
	case 2:
		args = append(args, "-p", rawURL)
	case 3:
		args = append(args, "-i", rawURL)
	case 4:
		if strings.TrimSpace(formatID) == "" {
			return nil, errors.New("格式 ID 不可空白")
		}
		args = append(args, "-f", strings.TrimSpace(formatID), rawURL)
	default:
		return nil, errors.New("此處理模式目前不適用 Lux 備援引擎")
	}
	return args, nil
}

func findNewestMediaFile(dir string, since time.Time) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".download") || strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".ytdl") || strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp") || strings.HasSuffix(lower, ".srt") || strings.HasSuffix(lower, ".vtt") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(since) {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = filepath.Join(dir, name)
			bestTime = info.ModTime()
		}
	}
	if best == "" {
		return "", errors.New("沒有找到剛完成的媒體檔")
	}
	return best, nil
}

func (a *app) convertMediaToMP3(ctx context.Context, input string) error {
	base := strings.TrimSuffix(input, filepath.Ext(input))
	out := base + ".mp3"
	a.postLog("→ 備援引擎已取得媒體，正在用 FFmpeg 轉成 MP3…\r\n")
	args := []string{"-y", "-i", input, "-vn", "-q:a", "0", out}
	if err := a.runExternalCommand(ctx, a.ffmpegPath, args, "FFmpeg"); err != nil {
		return fmt.Errorf("轉成 MP3 失敗：%w", err)
	}
	if validFile(out, 1024) {
		_ = os.Remove(input)
		a.postLog("✓ 已轉成 MP3：" + out + "\r\n")
		a.noteOutput(out)
		return nil
	}
	return errors.New("FFmpeg 執行完成但沒有產生 MP3")
}

func (a *app) runHaokanFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	title, streams, err := resolveHaokanPage(ctx, rawURL)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return errors.New("頁面中找不到公開的 clarityUrl 媒體資訊")
	}
	if mode == 3 {
		a.postLog("可用格式：\r\n")
		for _, st := range streams {
			size := ""
			if st.SizeMB > 0 {
				size = fmt.Sprintf("，約 %.1f MB", st.SizeMB)
			}
			a.postLog(fmt.Sprintf("  %s｜%dp%s\r\n", st.Key, st.Height, size))
		}
		return nil
	}
	selected, err := chooseHaokanStream(streams, mode, formatID)
	if err != nil {
		return err
	}
	directMode := mode
	if mode == 4 || mode == 5 || mode == 2 {
		directMode = 0
	}
	args, err := a.buildArgs(selected.URL, directMode, "", output, cfg)
	if err != nil {
		return err
	}
	u, _ := url.Parse(rawURL)
	origin := "https://haokan.baidu.com"
	if u != nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	extra := []string{"--referer", "https://haokan.baidu.com/", "--user-agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1", "--add-header", "Origin:" + origin}
	if cfg.FileName != "" {
		extra = append(extra, "-o", customTemplate(cfg.FileName))
	} else if clean := safeWindowsBaseName(title); clean != "" {
		extra = append(extra, "-o", clean+".%(ext)s")
	}
	args = insertBeforeLast(args, extra...)
	a.postLog(fmt.Sprintf("✓ 內建解析到 %s（%dp），交由 yt-dlp/FFmpeg 下載。\r\n", selected.Key, selected.Height))
	return a.runCommand(ctx, args)
}

func resolveHaokanPage(ctx context.Context, rawURL string) (string, []haokanStream, error) {
	vid, candidates := haokanPageCandidates(rawURL)
	if len(candidates) == 0 {
		candidates = []string{rawURL}
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		for _, candidate := range candidates {
			body, err := fetchHaokanPage(ctx, candidate)
			if err != nil {
				lastErr = err
				continue
			}

			jsonText := extractAssignedJSONObject(body, "window.__PRELOADED_STATE__")
			if jsonText == "" {
				jsonText = extractAssignedJSONObject(body, "__PRELOADED_STATE__")
			}
			if jsonText == "" {
				lastErr = errors.New("頁面中找不到 PRELOADED_STATE 公開資料")
				continue
			}

			dec := json.NewDecoder(strings.NewReader(jsonText))
			dec.UseNumber()
			var root any
			if err := dec.Decode(&root); err != nil {
				lastErr = fmt.Errorf("頁面資料解析失敗：%w", err)
				continue
			}

			cur := findNestedObject(root, "curVideoMeta")
			if cur == nil {
				lastErr = errors.New("找不到 curVideoMeta")
				continue
			}
			if vid != "" {
				pageID := stringFromAny(cur["id"])
				if pageID != "" && pageID != vid {
					lastErr = fmt.Errorf("頁面影片 ID 不符（預期 %s，取得 %s）", vid, pageID)
					continue
				}
			}

			streams := collectHaokanStreams(cur)
			if len(streams) == 0 {
				lastErr = errors.New("頁面中找不到 clarityUrl 或 videoInfoExt 公開媒體資訊")
				continue
			}

			title := firstNonEmptyString(cur, "title", "seo_title")
			if title == "" {
				title = extractHTMLTitle(body)
			}
			return title, streams, nil
		}
	}

	if lastErr == nil {
		lastErr = errors.New("無法取得公開影片資訊")
	}
	return "", nil, lastErr
}

func haokanPageCandidates(rawURL string) (string, []string) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", []string{rawURL}
	}

	q := u.Query()
	vid := strings.TrimSpace(q.Get("vid"))
	if vid == "" {
		contextText := strings.TrimSpace(q.Get("context"))
		if contextText != "" {
			var contextData map[string]any
			dec := json.NewDecoder(strings.NewReader(contextText))
			dec.UseNumber()
			if dec.Decode(&contextData) == nil {
				nid := stringFromAny(contextData["nid"])
				if strings.HasPrefix(nid, "sv_") {
					candidate := strings.TrimPrefix(nid, "sv_")
					if allDigits(candidate) {
						vid = candidate
					}
				}
			}
			if vid == "" {
				re := regexp.MustCompile(`(?i)sv_(\d{6,32})`)
				if m := re.FindStringSubmatch(contextText); len(m) == 2 {
					vid = m[1]
				}
			}
		}
	}
	if vid != "" && !allDigits(vid) {
		vid = ""
	}

	candidates := make([]string, 0, 2)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		candidates = append(candidates, v)
	}
	add(rawURL)
	if vid != "" {
		add("https://haokan.baidu.com/v?vid=" + url.QueryEscape(vid))
	}
	return vid, candidates
}

func fetchHaokanPage(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1")
	req.Header.Set("Referer", "https://haokan.baidu.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.6")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("網頁 HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func collectHaokanStreams(meta map[string]any) []haokanStream {
	streams := make([]haokanStream, 0, 8)
	seen := map[string]bool{}
	push := func(entry map[string]any, fallbackKey string) {
		if entry == nil {
			return
		}
		mediaURL := stringFromAny(entry["url"])
		if mediaURL == "" {
			for k, v := range entry {
				if strings.HasSuffix(strings.ToLower(k), "urlhttp") {
					if candidate := stringFromAny(v); strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
						mediaURL = candidate
						break
					}
				}
			}
		}
		mediaURL = html.UnescapeString(strings.ReplaceAll(strings.TrimSpace(mediaURL), `\/`, `/`))
		if (!strings.HasPrefix(mediaURL, "http://") && !strings.HasPrefix(mediaURL, "https://")) || seen[mediaURL] {
			return
		}
		seen[mediaURL] = true

		key := stringFromAny(entry["key"])
		if key == "" {
			key = fallbackKey
		}
		height := haokanEntryHeight(entry, key)
		streams = append(streams, haokanStream{
			Key: key, URL: mediaURL, Height: height, SizeMB: floatFromAny(entry["videoSize"]),
		})
	}

	if list, ok := meta["clarityUrl"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				push(m, "")
			}
		}
	}
	if ext, ok := meta["videoInfoExt"].(map[string]any); ok {
		for key, item := range ext {
			if m, ok := item.(map[string]any); ok {
				push(m, key)
			}
		}
	}
	return streams
}

func haokanEntryHeight(entry map[string]any, key string) int {
	for _, field := range []string{"key", "title"} {
		if value := stringFromAny(entry[field]); value != "" {
			if h := heightFromLabel(value); h > 0 {
				return h
			}
		}
	}
	if h := heightFromLabel(key); h > 0 {
		return h
	}

	hw := stringFromAny(entry["vodVideoHW"])
	if hw != "" {
		parts := strings.Split(hw, "$$")
		values := make([]int, 0, len(parts))
		for _, part := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil && n > 0 {
				values = append(values, n)
			}
		}
		if len(values) == 1 {
			return values[0]
		}
		if len(values) >= 2 {
			// Typical dimensions are width x height. Prefer the smaller dimension
			// because it is normally the vertical resolution (e.g. 1920 x 1080).
			best := values[0]
			for _, n := range values[1:] {
				if n < best {
					best = n
				}
			}
			return best
		}
	}
	return haokanHeight(key)
}

func heightFromLabel(label string) int {
	lower := strings.ToLower(strings.TrimSpace(label))
	if lower == "sd" {
		return 360
	}
	if lower == "hd" {
		return 480
	}
	if lower == "sc" {
		return 720
	}
	re := regexp.MustCompile(`(?i)(\d{3,4})p?`)
	if m := re.FindStringSubmatch(lower); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func firstNonEmptyString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromAny(m[key]); value != "" {
			return value
		}
	}
	return ""
}

func extractAssignedJSONObject(text, marker string) string {
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	tail := text[idx+len(marker):]
	start := strings.IndexByte(tail, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(tail); i++ {
		c := tail[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return tail[start : i+1]
			}
		}
	}
	return ""
}

func findNestedObject(node any, key string) map[string]any {
	switch v := node.(type) {
	case map[string]any:
		if found, ok := v[key].(map[string]any); ok {
			return found
		}
		for _, child := range v {
			if found := findNestedObject(child, key); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := findNestedObject(child, key); found != nil {
				return found
			}
		}
	}
	return nil
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return ""
	}
}

func floatFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func haokanHeight(key string) int {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch lower {
	case "sd":
		return 360
	case "hd":
		return 480
	case "sc":
		return 720
	case "1080p":
		return 1080
	}
	re := regexp.MustCompile(`(\d{3,4})`)
	if m := re.FindStringSubmatch(lower); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func chooseHaokanStream(streams []haokanStream, mode int, formatID string) (haokanStream, error) {
	if len(streams) == 0 {
		return haokanStream{}, errors.New("沒有可用格式")
	}
	if mode == 4 {
		want := strings.ToLower(strings.TrimSpace(formatID))
		for _, st := range streams {
			if strings.ToLower(st.Key) == want || strconv.Itoa(st.Height) == want || strconv.Itoa(st.Height)+"p" == want {
				return st, nil
			}
		}
		return haokanStream{}, fmt.Errorf("找不到指定格式 %s；可先使用『只查看可用格式』", formatID)
	}
	best := haokanStream{}
	for _, st := range streams {
		if mode == 5 && (st.Height == 0 || st.Height > 720) {
			continue
		}
		if best.URL == "" || st.Height > best.Height || (st.Height == best.Height && st.SizeMB > best.SizeMB) {
			best = st
		}
	}
	if best.URL == "" {
		return haokanStream{}, errors.New("找不到符合條件的畫質")
	}
	return best, nil
}
