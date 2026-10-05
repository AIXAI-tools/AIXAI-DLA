//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// browserCaptureEvent is sent by the temporary AIXAI capture extension that is
// loaded only into the dedicated Edge/Chromium profile started by this app.
// Request headers are kept in memory only and are never written to the log.
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
}

var captureEpisodeRE = regexp.MustCompile(`(?i)(?:episode[-_/])(\d+)(?:/)?$`)

func randomCaptureToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

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
	if strings.Contains(lower, "doubleclick") || strings.Contains(lower, "googleads") || strings.Contains(lower, "/ads/") {
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
	if newC.Score != oldC.Score {
		return newC.Score > oldC.Score
	}
	if newC.ContentLength != oldC.ContentLength {
		return newC.ContentLength > oldC.ContentLength
	}
	return newC.SeenAt.After(oldC.SeenAt)
}

func findCaptureBrowser() (string, string, error) {
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	local := os.Getenv("LOCALAPPDATA")
	checks := []struct {
		name string
		path string
	}{
		{"Microsoft Edge", filepath.Join(pf86, "Microsoft", "Edge", "Application", "msedge.exe")},
		{"Microsoft Edge", filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe")},
		{"Microsoft Edge", filepath.Join(local, "Microsoft", "Edge", "Application", "msedge.exe")},
		{"Brave", filepath.Join(pf, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")},
		{"Brave", filepath.Join(pf86, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")},
		{"Brave", filepath.Join(local, "BraveSoftware", "Brave-Browser", "Application", "brave.exe")},
		{"Vivaldi", filepath.Join(local, "Vivaldi", "Application", "vivaldi.exe")},
		{"Google Chrome", filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe")},
		{"Google Chrome", filepath.Join(pf86, "Google", "Chrome", "Application", "chrome.exe")},
		{"Google Chrome", filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe")},
	}
	for _, item := range checks {
		if item.path == "" {
			continue
		}
		if st, err := os.Stat(item.path); err == nil && !st.IsDir() {
			return item.name, item.path, nil
		}
	}
	return "", "", errors.New("找不到 Microsoft Edge／Brave／Vivaldi／Chrome，無法啟動瀏覽器媒體嗅探")
}

func writeCaptureExtension(dir, endpoint string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	manifest := `{
  "manifest_version": 3,
  "name": "AIXAI Media Capture Helper",
  "version": "1.0.0",
  "description": "Local-only media request detector for AIXAI YT-DLP",
  "permissions": ["webRequest", "tabs"],
  "host_permissions": ["http://*/*", "https://*/*"],
  "background": {"service_worker": "background.js"},
  "content_scripts": [{"matches": ["http://*/*", "https://*/*"], "js": ["content.js"], "run_at": "document_start", "all_frames": true}]
}`

	background := fmt.Sprintf(`const ENDPOINT = %q;
const requestState = new Map();
function hobj(list) {
  const out = {};
  for (const h of (list || [])) {
    if (!h || !h.name) continue;
    out[String(h.name).toLowerCase()] = String(h.value || "");
  }
  return out;
}
function looksMedia(url, type, responseHeaders) {
  const u = String(url || "").toLowerCase();
  const ct = String((responseHeaders || {})["content-type"] || "").toLowerCase();
  if (/\.(?:ts|m4s|cmfv|cmfa|aac|vtt|srt)(?:$|[?#])/i.test(u)) return false;
  return /\.(?:m3u8|mpd|mp4|m4v|webm)(?:$|[?#])/i.test(u)
    || ct.includes("mpegurl") || ct.includes("dash+xml") || ct.startsWith("video/")
    || type === "media";
}
async function post(payload) {
  try {
    await fetch(ENDPOINT, {method: "POST", headers: {"content-type":"application/json"}, body: JSON.stringify(payload)});
  } catch (_) {}
}
post({kind:"ready"});
function emit(details, responseHeaders) {
  const state = requestState.get(details.requestId) || {};
  const finish = (tab) => post({
    kind: "network",
    url: details.url,
    pageUrl: (tab && tab.url) || state.pageUrl || details.initiator || "",
    title: (tab && tab.title) || "",
    requestType: details.type || state.type || "",
    initiator: details.initiator || state.initiator || "",
    requestHeaders: state.requestHeaders || {},
    responseHeaders: responseHeaders || {}
  });
  if (details.tabId >= 0) {
    chrome.tabs.get(details.tabId, tab => {
      if (chrome.runtime.lastError) finish(null); else finish(tab);
    });
  } else finish(null);
}
chrome.webRequest.onBeforeSendHeaders.addListener(details => {
  const rh = hobj(details.requestHeaders);
  requestState.set(details.requestId, {requestHeaders: rh, type: details.type || "", initiator: details.initiator || ""});
  if (looksMedia(details.url, details.type, {})) emit(details, {});
}, {urls:["<all_urls>"], types:["media","xmlhttprequest","object","other"]}, ["requestHeaders", "extraHeaders"]);
chrome.webRequest.onResponseStarted.addListener(details => {
  const headers = hobj(details.responseHeaders);
  if (looksMedia(details.url, details.type, headers)) emit(details, headers);
}, {urls:["<all_urls>"], types:["media","xmlhttprequest","object","other"]}, ["responseHeaders", "extraHeaders"]);
chrome.webRequest.onCompleted.addListener(details => requestState.delete(details.requestId), {urls:["<all_urls>"]});
chrome.webRequest.onErrorOccurred.addListener(details => requestState.delete(details.requestId), {urls:["<all_urls>"]});
chrome.runtime.onMessage.addListener((msg, sender) => {
  if (!msg) return;
  if (msg.kind === "hello") { post({kind:"ready"}); return; }
  if (msg.kind !== "dom") return;
  const tab = sender && sender.tab;
  post({kind:"dom", url:msg.url || "", pageUrl:msg.pageUrl || (tab && tab.url) || "", title:msg.title || (tab && tab.title) || "", requestType:"dom", initiator:"", requestHeaders:{}, responseHeaders:{}});
});
`, endpoint)

	content := `(() => {
  try { chrome.runtime.sendMessage({kind:"hello"}); } catch (_) {}
  const seen = new Set();
  const mediaRe = /\.(?:m3u8|mpd|mp4|m4v|webm)(?:$|[?#])/i;
  function report(raw) {
    try {
      const u = new URL(raw, location.href).href;
      if (!/^https?:/i.test(u) || seen.has(u)) return;
      seen.add(u);
      chrome.runtime.sendMessage({kind:"dom", url:u, pageUrl:location.href, title:document.title || ""});
    } catch (_) {}
  }
  function scan() {
    document.querySelectorAll("video,source").forEach(el => {
      if (el.src) report(el.src);
      const s = el.getAttribute && el.getAttribute("src");
      if (s) report(s);
    });
    try {
      performance.getEntriesByType("resource").forEach(e => { if (mediaRe.test(e.name || "")) report(e.name); });
    } catch (_) {}
  }
  function tryPlay() {
    document.querySelectorAll("video").forEach(v => {
      try { v.muted = true; v.autoplay = true; const p = v.play(); if (p && p.catch) p.catch(() => {}); } catch (_) {}
    });
  }
  scan(); tryPlay();
  new MutationObserver(() => { scan(); tryPlay(); }).observe(document.documentElement || document, {subtree:true, childList:true, attributes:true, attributeFilter:["src"]});
  setInterval(() => { scan(); tryPlay(); }, 1500);
})();`

	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "background.js"), []byte(background), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "content.js"), []byte(content), 0644); err != nil {
		return err
	}
	return nil
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

func (a *app) captureMediaWithExtension(ctx context.Context, rawURL string) (browserMediaCandidate, error) {
	browserName, browserPath, err := findCaptureBrowser()
	if err != nil {
		return browserMediaCandidate{}, err
	}
	token, err := randomCaptureToken()
	if err != nil {
		return browserMediaCandidate{}, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return browserMediaCandidate{}, fmt.Errorf("建立本機媒體嗅探通道失敗：%w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/capture/%s", port, token)
	extDir := filepath.Join(a.appDir, "capture-extension")
	if err := writeCaptureExtension(extDir, endpoint); err != nil {
		return browserMediaCandidate{}, fmt.Errorf("建立瀏覽器嗅探模組失敗：%w", err)
	}

	events := make(chan browserMediaCandidate, 256)
	var helperReady atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/capture/"+token, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		var ev browserCaptureEvent
		if err := dec.Decode(&ev); err != nil {
			http.Error(w, "json", http.StatusBadRequest)
			return
		}
		if ev.Kind == "ready" {
			helperReady.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if candidate, ok := mediaCandidateScore(ev); ok {
			select {
			case events <- candidate:
			default:
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	readyPath := "/ready/" + token
	startPath := "/start/" + token
	mux.HandleFunc(readyPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if helperReady.Load() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	targetJSON, _ := json.Marshal(rawURL)
	mux.HandleFunc(startPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>AIXAI 媒體監聽準備中</title><body style="font-family:sans-serif;padding:24px"><h2>AIXAI 正在啟動媒體監聽…</h2><p>監聽器就緒後會自動前往影片頁面，請勿關閉此視窗。</p><script>const target=%s;const ready=%q;async function go(){try{const r=await fetch(ready,{cache:'no-store'});if(r.status===204){location.replace(target);return}}catch(e){}setTimeout(go,250)}go();setTimeout(()=>location.replace(target),8000);</script>`, string(targetJSON), readyPath)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	serveDone := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(serveDone)
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		cancel()
		select {
		case <-serveDone:
		case <-time.After(2 * time.Second):
		}
	}()

	profileDir := filepath.Join(a.appDir, "capture-browser-profile")
	_ = os.MkdirAll(profileDir, 0755)
	prepareCaptureProfile(profileDir)
	args := []string{
		"--user-data-dir=" + profileDir,
		"--disable-extensions-except=" + extDir,
		"--load-extension=" + extDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--autoplay-policy=no-user-gesture-required",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--new-window",
		"--disable-session-crashed-bubble",
		"--hide-crash-restore-bubble",
	}
	if strings.Contains(strings.ToLower(browserName), "edge") {
		args = append(args, "--disable-features=msEdgeFirstRunExperience", "--edge-skip-compat-layer-relaunch")
	}
	// Chrome 137+ disabled --load-extension in the branded build. Keep this
	// compatibility flag for versions where Chromium still exposes the switch;
	// Edge/Brave/Vivaldi remain the preferred capture browsers.
	if strings.Contains(strings.ToLower(browserName), "chrome") {
		args = append(args, "--disable-features=DisableLoadExtensionCommandLineSwitch")
	}
	startURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, startPath)
	args = append(args, startURL)
	cmd := exec.Command(browserPath, args...)
	cmd.Env = browserEnv()
	if err := cmd.Start(); err != nil {
		return browserMediaCandidate{}, fmt.Errorf("啟動 %s 媒體嗅探視窗失敗：%w", browserName, err)
	}
	defer killStandaloneProcessTree(cmd)

	a.postLog("→ 啟動 " + browserName + " 擴充模組媒體嗅探（第二備援）；會先確認監聽器就緒，再自動導向影片頁。\r\n")
	a.postLog("ℹ 嗅探使用 AIXAI 專用瀏覽器設定檔；不會讀取或寫出 Cookie 到紀錄。若網站需要登入，可在嗅探視窗登入一次，之後會沿用該專用設定檔。\r\n")
	a.postStatus("狀態：正在以瀏覽器嗅探實際影音串流…")

	deadline := time.NewTimer(35 * time.Second)
	defer deadline.Stop()
	var best browserMediaCandidate
	var settle *time.Timer
	var settleC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return browserMediaCandidate{}, ctx.Err()
		case c := <-events:
			if betterCaptureCandidate(c, best) {
				best = c
				if best.Score >= 125 {
					if settle == nil {
						settle = time.NewTimer(2500 * time.Millisecond)
						settleC = settle.C
					} else {
						if !settle.Stop() {
							select {
							case <-settle.C:
							default:
							}
						}
						settle.Reset(2500 * time.Millisecond)
					}
				}
			}
		case <-settleC:
			if best.URL != "" {
				return best, nil
			}
		case <-deadline.C:
			if best.URL != "" {
				return best, nil
			}
			return browserMediaCandidate{}, errors.New("瀏覽器已開啟頁面，但 35 秒內沒有偵測到 HLS／DASH／MP4 媒體請求；可能需要手動播放影片、登入網站，或媒體受 DRM 保護")
		}
	}
}

func (a *app) captureMediaWithBrowser(ctx context.Context, rawURL, cookieFile string) (browserMediaCandidate, error) {
	// Primary path: Chrome DevTools Protocol. Network.enable is active before
	// navigation, so immediate HLS/MP4 requests cannot race past the listener.
	candidate, cdpErr := a.captureMediaWithCDP(ctx, rawURL, cookieFile)
	if cdpErr == nil {
		return candidate, nil
	}
	if a.stopRequested.Load() {
		return browserMediaCandidate{}, cdpErr
	}
	// The extension fallback uses a separate profile without the user's sign-in,
	// so it cannot help with pages that require signing in.
	if _, _, isTikTokDrama := parseTikTokShortDramaURL(rawURL); isTikTokDrama {
		return browserMediaCandidate{}, cdpErr
	}
	a.postLog("⚠ DevTools 媒體監聽未成功，改用瀏覽器擴充模組相容路徑：" + firstLine(cdpErr.Error()) + "\r\n")
	candidate, extErr := a.captureMediaWithExtension(ctx, rawURL)
	if extErr == nil {
		return candidate, nil
	}
	return browserMediaCandidate{}, fmt.Errorf("DevTools 監聽：%v；擴充模組監聽：%v", cdpErr, extErr)
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
		}
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
		return nil
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
	return a.downloadBrowserCapturedMedia(ctx, rawURL, candidate, mode, formatID, output, cfg)
}
