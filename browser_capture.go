//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// browserCaptureEvent describes one media request seen by the DevTools
// listener of the dedicated capture browser. Request headers are kept in memory only and are never written to the log.
type browserCaptureEvent struct {
	Kind            string            `json:"kind"`
	URL             string            `json:"url"`
	PageURL         string            `json:"pageUrl"`
	Title           string            `json:"title"`
	RequestType     string            `json:"requestType"`
	Initiator       string            `json:"initiator"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
}

type browserMediaCandidate struct {
	URL            string
	PageURL        string
	Title          string
	Kind           string
	RequestType    string
	RequestHeaders map[string]string
	ContentType    string
	ContentLength  int64
	Score          int
	SeenAt         time.Time
	// PageJSON holds the page's JSON responses seen during this capture (memory
	// only); PlayedURL is the stream that actually played when URL was
	// replaced by a larger rendition from that data.
	PageJSON  [][]byte
	PlayedURL string
	// EpisodeMatch: the stream's path carries the episode number of the page.
	EpisodeMatch bool
}

// pageEpisodeNumber is the episode a page address asks for: a trailing number
// (/title/7), episode-7, or ?ep=7. 0 when the address has none.
func pageEpisodeNumber(raw string) int {
	if ep := episodeFromQuery(raw); ep > 0 {
		return ep
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return 0
	}
	last := strings.ToLower(parts[len(parts)-1])
	if m := captureEpisodeRE.FindStringSubmatch(last); len(m) > 1 {
		last = m[1]
	}
	if allDigits(last) && len(last) <= 4 {
		n, _ := strconv.Atoi(last)
		return n
	}
	return 0
}

// streamHasNumberSegment reports whether a stream path has a plain number as
// a segment of its own (…/9/stream.m3u8), i.e. it may name an episode.
func streamHasNumberSegment(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, s := range strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' }) {
		if allDigits(s) && len(s) <= 4 {
			return true
		}
	}
	return false
}

// streamHasEpisode reports whether a stream path has the episode number as a
// segment of its own (…/7/stream.m3u8). Players often preload the next
// episodes, so on an episode page the matching stream is the one to keep.
func streamHasEpisode(raw string, ep int) bool {
	u, err := url.Parse(raw)
	if err != nil || ep <= 0 {
		return false
	}
	want := strconv.Itoa(ep)
	for _, s := range strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' }) {
		if strings.TrimLeft(s, "0") == want {
			return true
		}
	}
	return false
}

const (
	maxPageJSONBytes = 2 << 20
	maxPageJSONCount = 60
)

// upgradeToBestVariant swaps the played stream for the largest rendition the
// page itself lists next to it (respecting the 720p mode). Audio-only and
// format-listing modes keep the played stream.
func (a *app) upgradeToBestVariant(c browserMediaCandidate, mode int) browserMediaCandidate {
	if mode != 0 && mode != 2 && mode != 5 {
		return c
	}
	limit := 0
	if mode == 5 {
		limit = 720
	}
	best, played, ok := betterVariantFromPageJSON(c.PageJSON, c.URL, limit)
	if !ok {
		return c
	}
	a.postLog(fmt.Sprintf("✓ 頁面播放器提供更高畫質：改下載 %d×%d（預設播放的是 %d×%d）。\r\n", best.Width, best.Height, played.Width, played.Height))
	c.PlayedURL = c.URL
	c.URL = best.URL
	return c
}

var captureEpisodeRE = regexp.MustCompile(`(?i)(?:episode[-_/])(\d+)(?:/)?$`)

func captureHeader(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func captureContentLength(headers map[string]string) int64 {
	raw := captureHeader(headers, "content-length")
	if raw == "" {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

// isAdMediaURL recognises video-ad requests by the address alone (ad servers,
// VAST tags, pre-roll clips). Ads served from ordinary hosts are caught from
// the page's VAST responses and ad <video> elements instead (cdp_session.go).
func isAdMediaURL(raw string) bool {
	lower := strings.ToLower(raw)
	for _, needle := range []string{"doubleclick", "googleads", "googlesyndication", "imasdk", "/ads/", "vast", "preroll", "pre-roll", "adserver"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

var vastMediaFileRE = regexp.MustCompile(`(?is)<MediaFile\b[^>]*>\s*(?:<!\[CDATA\[)?\s*(.*?)\s*(?:\]\]>)?\s*</MediaFile>`)

// vastMediaFiles returns the ad video addresses listed in a VAST response
// (the standard format for video ads). Anything else returns nil.
func vastMediaFiles(body string) []string {
	if !strings.Contains(body, "<VAST") && !strings.Contains(body, "<vast") {
		return nil
	}
	var out []string
	for _, m := range vastMediaFileRE.FindAllStringSubmatch(body, 64) {
		if u := strings.TrimSpace(html.UnescapeString(m[1])); strings.HasPrefix(u, "http") {
			out = append(out, u)
		}
	}
	return out
}

// adURLKey compares ad addresses without their query string, since tracking
// parameters often differ between the VAST entry and the actual request.
func adURLKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	return strings.ToLower(u.Host) + u.Path
}

func mediaCandidateScore(ev browserCaptureEvent) (browserMediaCandidate, bool) {
	raw := strings.TrimSpace(ev.URL)
	if raw == "" {
		return browserMediaCandidate{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return browserMediaCandidate{}, false
	}
	lower := strings.ToLower(raw)
	pathLower := strings.ToLower(u.Path)
	ct := strings.ToLower(captureHeader(ev.ResponseHeaders, "content-type"))
	// Never mistake a single media segment for the complete stream.
	segmentExts := []string{".ts", ".m4s", ".cmfv", ".cmfa", ".aac", ".vtt", ".srt"}
	for _, ext := range segmentExts {
		if strings.HasSuffix(pathLower, ext) {
			return browserMediaCandidate{}, false
		}
	}
	if isAdMediaURL(raw) {
		return browserMediaCandidate{}, false
	}
	// Some sign-in pages play a decorative static clip from a static-asset host;
	// it must never be mistaken for the requested video.
	if host := strings.ToLower(u.Hostname()); host == "ttwstatic.com" || strings.HasSuffix(host, ".ttwstatic.com") {
		return browserMediaCandidate{}, false
	}

	score := 0
	kind := "media"
	switch {
	case strings.Contains(lower, ".m3u8") || strings.Contains(ct, "mpegurl") || strings.Contains(ct, "application/vnd.apple.mpegurl"):
		score = 150
		kind = "HLS"
	case strings.Contains(lower, ".mpd") || strings.Contains(ct, "dash+xml"):
		score = 145
		kind = "DASH"
	case strings.Contains(ct, "video/mp4") || strings.Contains(lower, ".mp4"):
		score = 125
		kind = "MP4"
	case strings.HasPrefix(ct, "video/") || strings.EqualFold(ev.RequestType, "media"):
		score = 100
		kind = "Video"
	case strings.Contains(lower, ".webm") || strings.Contains(ct, "video/webm"):
		score = 105
		kind = "WebM"
	default:
		return browserMediaCandidate{}, false
	}

	length := captureContentLength(ev.ResponseHeaders)
	if length >= 1<<20 {
		score += 8
	}
	if length >= 5<<20 {
		score += 6
	}
	if strings.EqualFold(ev.RequestType, "media") {
		score += 8
	}
	return browserMediaCandidate{
		URL:            raw,
		PageURL:        strings.TrimSpace(ev.PageURL),
		Title:          strings.TrimSpace(ev.Title),
		Kind:           kind,
		RequestType:    ev.RequestType,
		RequestHeaders: ev.RequestHeaders,
		ContentType:    ct,
		ContentLength:  length,
		Score:          score,
		SeenAt:         time.Now(),
	}, true
}

func betterCaptureCandidate(newC, oldC browserMediaCandidate) bool {
	if oldC.URL == "" {
		return true
	}
	if newC.EpisodeMatch != oldC.EpisodeMatch {
		return newC.EpisodeMatch
	}
	if newC.Score != oldC.Score {
		return newC.Score > oldC.Score
	}
	if newC.ContentLength != oldC.ContentLength {
		return newC.ContentLength > oldC.ContentLength
	}
	return newC.SeenAt.After(oldC.SeenAt)
}

func killStandaloneProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	// Ask the dedicated browser to close cleanly first so its capture profile
	// (including a one-time login) is persisted. Force-kill only as a fallback.
	softCtx, softCancel := context.WithTimeout(context.Background(), 2*time.Second)
	soft := exec.CommandContext(softCtx, "taskkill.exe", "/PID", pid, "/T")
	soft.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	_ = soft.Run()
	softCancel()
	time.Sleep(250 * time.Millisecond)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return
	}
	hardCtx, hardCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer hardCancel()
	hard := exec.CommandContext(hardCtx, "taskkill.exe", "/PID", pid, "/T", "/F")
	hard.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	_ = hard.Run()
	_ = cmd.Process.Kill()
}

func (a *app) captureMediaWithBrowser(ctx context.Context, rawURL, cookieFile string) (browserMediaCandidate, error) {
	// Primary path: Chrome DevTools Protocol. Network.enable is active before
	// navigation, so immediate HLS/MP4 requests cannot race past the listener.
	candidate, cdpErr := a.captureMediaWithCDP(ctx, rawURL, cookieFile)
	if cdpErr == nil {
		return candidate, nil
	}
	// There is deliberately no second capture path: the DevTools listener is
	// the only supported way to observe the capture browser.
	return browserMediaCandidate{}, cdpErr
}

func sanitizeCapturedHeader(v string) string {
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, "\n", "")
	return strings.TrimSpace(v)
}

func replaceOutputTemplate(args []string, tmpl string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-o" || out[i] == "--output" {
			out[i+1] = tmpl
			return out
		}
	}
	return out
}

func capturedOutputBase(rawURL, title string) string {
	if dramaID, ep, ok := parseTikTokShortDramaURL(rawURL); ok {
		if clean := safeWindowsBaseName(title); clean != "" {
			return safeWindowsBaseName(fmt.Sprintf("%s_E%03d", clean, ep))
		}
		return safeWindowsBaseName(fmt.Sprintf("Video_%s_E%03d", dramaID, ep))
	}
	if u, err := url.Parse(rawURL); err == nil {
		parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
		if len(parts) >= 2 {
			last := parts[len(parts)-1]
			if m := captureEpisodeRE.FindStringSubmatch(strings.ToLower(last)); len(m) > 1 {
				ep, _ := strconv.Atoi(m[1])
				series := parts[len(parts)-2]
				return safeWindowsBaseName(fmt.Sprintf("%s_E%03d", series, ep))
			}
			// e.g. /play/123/3: the number a sequence download replaces. Episode
			// pages often share one title, so without the number every episode
			// gets the same name and yt-dlp skips the rest as already downloaded.
			if allDigits(last) && len(last) <= 4 {
				ep, _ := strconv.Atoi(last)
				name := safeWindowsBaseName(title)
				if name == "" {
					name = safeWindowsBaseName(parts[len(parts)-2])
				}
				return safeWindowsBaseName(fmt.Sprintf("%s_E%03d", name, ep))
			}
		}
	}
	// One address for every episode (?ep=N): the page title is the same for
	// all of them, so the episode number must be part of the name.
	if ep := episodeFromQuery(rawURL); ep > 0 {
		name := safeWindowsBaseName(title)
		if name == "" {
			name = seriesNameFromPath(rawURL)
		}
		if name == "" {
			name = "Video"
		}
		return safeWindowsBaseName(fmt.Sprintf("%s_E%03d", name, ep))
	}
	if strings.TrimSpace(title) != "" {
		return safeWindowsBaseName(title)
	}
	return "Captured_Video_" + time.Now().Format("20060102_150405")
}

func (a *app) downloadBrowserCapturedMedia(ctx context.Context, rawURL string, candidate browserMediaCandidate, mode int, formatID, output string, cfg settings) error {
	if mode == 3 {
		a.postLog(fmt.Sprintf("✓ 瀏覽器嗅探到 %s 媒體來源（主機：%s）。此模式無法可靠列出原網站的 yt-dlp 格式 ID。\r\n", candidate.Kind, hostOnly(candidate.URL)))
		return nil
	}
	if mode == 4 {
		return errors.New("瀏覽器嗅探得到的是實際媒體串流，不適用原網站的 yt-dlp 格式 ID；請改用最佳畫質或最高 720p")
	}
	localCfg := cfg
	// Browser capture already carries the exact request context. Avoid a second
	// attempt to read a possibly locked Chrome/Edge cookie database.
	localCfg.CookieBrowser = "none"
	localCfg.CookieFile = ""
	args, err := a.buildArgs(candidate.URL, mode, formatID, output, localCfg)
	if err != nil {
		return err
	}
	base := capturedOutputBase(rawURL, candidate.Title)
	args = replaceOutputTemplate(args, base+".%(ext)s")

	headers := candidate.RequestHeaders
	// Preserve the request context observed by the browser. Never log these
	// values; cookies and authorization tokens may be sensitive.
	if referer := sanitizeCapturedHeader(captureHeader(headers, "referer")); referer != "" {
		args = insertBeforeLast(args, "--referer", referer)
	} else {
		args = insertBeforeLast(args, "--referer", rawURL)
	}
	if ua := sanitizeCapturedHeader(captureHeader(headers, "user-agent")); ua != "" {
		args = insertBeforeLast(args, "--user-agent", ua)
	}
	for _, name := range []string{"cookie", "authorization", "origin", "accept", "accept-language"} {
		if value := sanitizeCapturedHeader(captureHeader(headers, name)); value != "" {
			canonical := map[string]string{"cookie": "Cookie", "authorization": "Authorization", "origin": "Origin", "accept": "Accept", "accept-language": "Accept-Language"}[name]
			args = insertBeforeLast(args, "--add-header", canonical+":"+value)
		}
	}
	a.postLog(fmt.Sprintf("✓ 瀏覽器偵測到 %s 媒體串流（%s），正在交由 yt-dlp 下載。\r\n", candidate.Kind, hostOnly(candidate.URL)))
	a.postStatus("狀態：已捕捉實際媒體串流，正在下載…")
	if err := a.runCommand(ctx, args); err == nil {
		for _, f := range a.lastYtFiles {
			if validFile(f, 1) {
				a.lastCaptureFile = f
			}
		}
		return nil
	} else if errors.Is(err, errStopped) {
		return err
	} else if candidate.PlayedURL != "" && ctx.Err() == nil {
		// The larger rendition could not be downloaded: fall back to the stream
		// the page actually played, exactly as before.
		a.postLog("⚠ 較高畫質下載失敗，改用頁面預設播放的畫質。\r\n")
		candidate.URL = candidate.PlayedURL
		candidate.PlayedURL = ""
		return a.downloadBrowserCapturedMedia(ctx, rawURL, candidate, mode, formatID, output, cfg)
	} else {
		a.postLog("⚠ 已捕捉到真實媒體網址，但 yt-dlp 下載仍失敗；改用 FFmpeg 並重放瀏覽器原始 Request Headers。\r\n")
		if ffErr := a.downloadCapturedWithFFmpeg(ctx, rawURL, candidate, mode, output); ffErr == nil {
			return nil
		} else {
			return fmt.Errorf("已捕捉媒體，但 yt-dlp 與 FFmpeg 都下載失敗：%v；FFmpeg：%w", err, ffErr)
		}
	}
}

func (a *app) downloadCapturedWithFFmpeg(ctx context.Context, rawURL string, candidate browserMediaCandidate, mode int, output string) error {
	if mode == 3 || mode == 4 {
		return errors.New("此模式不適用 FFmpeg 媒體捕捉備援")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	base := capturedOutputBase(rawURL, candidate.Title)
	ext := ".mp4"
	if mode == 1 {
		ext = ".mp3"
	}
	dest := filepath.Join(output, base+ext)
	headers := candidate.RequestHeaders
	var lines []string
	for _, item := range []struct{ key, name string }{
		{"referer", "Referer"}, {"origin", "Origin"}, {"cookie", "Cookie"},
		{"authorization", "Authorization"}, {"accept", "Accept"}, {"accept-language", "Accept-Language"},
	} {
		if v := sanitizeCapturedHeader(captureHeader(headers, item.key)); v != "" {
			lines = append(lines, item.name+": "+v)
		}
	}
	if captureHeader(headers, "referer") == "" {
		lines = append(lines, "Referer: "+sanitizeCapturedHeader(rawURL))
	}
	ffArgs := []string{"-y"}
	if ua := sanitizeCapturedHeader(captureHeader(headers, "user-agent")); ua != "" {
		ffArgs = append(ffArgs, "-user_agent", ua)
	}
	if len(lines) > 0 {
		ffArgs = append(ffArgs, "-headers", strings.Join(lines, "\r\n")+"\r\n")
	}
	ffArgs = append(ffArgs, "-i", candidate.URL)
	if mode == 1 {
		ffArgs = append(ffArgs, "-vn", "-q:a", "0", dest)
	} else {
		ffArgs = append(ffArgs, "-map", "0:v?", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart", dest)
	}
	if err := a.runExternalCommand(ctx, a.ffmpegPath, ffArgs, "FFmpeg Capture"); err != nil {
		return err
	}
	if !validFile(dest, 1024) {
		return errors.New("FFmpeg 執行完成但沒有產生有效媒體檔")
	}
	a.postLog("✓ FFmpeg 已使用瀏覽器實際播放請求完成下載：" + dest + "\r\n")
	a.lastCaptureFile = dest
	return nil
}

func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "未知來源"
	}
	return u.Hostname()
}

func (a *app) runBrowserCaptureFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	candidate, err := a.captureMediaWithBrowser(ctx, rawURL, cfg.CookieFile)
	if err != nil {
		return err
	}
	candidate = a.upgradeToBestVariant(candidate, mode)
	return a.downloadBrowserCapturedMedia(ctx, rawURL, candidate, mode, formatID, output, cfg)
}
