//go:build windows

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

// The whole front end is one self-contained HTML file (CSS + JS inline) so the
// application stays a single portable .exe.
//
//go:embed ui/index.html
var uiHTML string

const (
	designLogicalW = 1240
	designLogicalH = 860
	minLogicalUIW  = 600
	minLogicalUIH  = 620

	gwlpWndProc                  = ^uintptr(3) // GWLP_WNDPROC (-4)
	wmClose                      = 0x0010
	dwmwaUseImmersiveDarkMode    = 20
	dwmwaCaptionColor            = 35
	dwmwaSystemBackdropType      = 38
	webviewRuntimeDownloadURL    = "https://developer.microsoft.com/microsoft-edge/webview2/"
	feedbackMaxIssueBodyURLBytes = 6000
)

var (
	procSetWindowLongPtrW     = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW       = user32.NewProc("CallWindowProcW")
	procGetDpiForWindow       = user32.NewProc("GetDpiForWindow")
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

type webUI struct {
	w        webview2.WebView
	hwnd     uintptr
	origProc uintptr
}

// emit sends an event to the page from any goroutine: aixai.on(name, payload).
func (a *app) emit(name string, payload interface{}) {
	if a.ui == nil || a.closing.Load() {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	js := "window.aixai&&aixai.on(" + strconv.Quote(name) + "," + string(data) + ")"
	a.ui.w.Dispatch(func() { a.ui.w.Eval(js) })
}

// ---------------------------------------------------------------------------
// Log / status / completion plumbing (replaces the Win32 message handlers).
// Workers call postLog/postStatus/postDone; draining happens on the UI thread.
// ---------------------------------------------------------------------------

func (a *app) signalLogDrain() {
	if a.ui == nil || a.closing.Load() {
		return
	}
	if a.logWakePending.CompareAndSwap(false, true) {
		a.ui.w.Dispatch(a.drainLogs)
	}
}

func (a *app) signalStatusDrain() {
	if a.ui == nil || a.closing.Load() {
		return
	}
	if a.statusWakePending.CompareAndSwap(false, true) {
		a.ui.w.Dispatch(a.drainStatus)
	}
}

// drainLogs forwards queued log text to the page in chronological order, in
// bounded batches so heavy engine output cannot stall the UI thread.
func (a *app) drainLogs() {
	const maxChars = 128 * 1024
	a.logMu.Lock()
	start := a.logHead
	end := start
	chars := 0
	for end < len(a.logPending) {
		n := len(a.logPending[end])
		if end > start && chars+n > maxChars {
			break
		}
		chars += n
		end++
	}
	batch := append([]string(nil), a.logPending[start:end]...)
	for i := start; i < end; i++ {
		a.logPendingBytes -= len(a.logPending[i])
		a.logPending[i] = ""
	}
	dropped := a.logDropped
	a.logDropped = 0
	a.logHead = end
	if a.logHead == len(a.logPending) {
		a.logPending = nil
		a.logHead = 0
		a.logPendingBytes = 0
	}
	a.logMu.Unlock()

	text := strings.Join(batch, "")
	if dropped > 0 {
		text += fmt.Sprintf("⚠ 為控制記憶體，已略過 %d 筆較舊的顯示紀錄（完整內容仍在紀錄檔）。\r\n", dropped)
	}
	if text != "" && a.ui != nil {
		data, _ := json.Marshal(text)
		a.ui.w.Eval("window.aixai&&aixai.on(\"log\"," + string(data) + ")")
	}
	a.logWakePending.Store(false)
	a.logMu.Lock()
	hasMore := a.logHead < len(a.logPending)
	a.logMu.Unlock()
	if hasMore {
		a.signalLogDrain()
	}
}

func (a *app) drainStatus() {
	var latestStatus, latestEnv string
	for i := 0; i < 128; i++ {
		select {
		case s := <-a.statusQueue:
			if strings.HasPrefix(s, "__ENV__") {
				latestEnv = strings.TrimPrefix(s, "__ENV__")
			} else {
				latestStatus = strings.TrimPrefix(s, "狀態：")
			}
		default:
			i = 128
		}
	}
	if a.ui != nil {
		if latestEnv != "" {
			data, _ := json.Marshal(latestEnv)
			a.ui.w.Eval("window.aixai&&aixai.on(\"env\"," + string(data) + ")")
		}
		if latestStatus != "" {
			data, _ := json.Marshal(latestStatus)
			a.ui.w.Eval("window.aixai&&aixai.on(\"status\"," + string(data) + ")")
		}
	}
	a.statusWakePending.Store(false)
	if len(a.statusQueue) > 0 {
		a.signalStatusDrain()
	}
}

// appendLog is used by UI-thread code; it goes through the same queue.
func (a *app) appendLog(s string) { a.postLog(s) }

func (a *app) emitTask(index int, state, message string) {
	if a.job == nil {
		return
	}
	index += a.itemOffset
	a.setJobItem(index, state, message)
	a.emit("task", map[string]interface{}{"job": a.job.ID, "index": index, "state": state, "message": message})
}

func (a *app) saveSettings() {
	data, _ := json.MarshalIndent(a.cfg, "", "  ")
	_ = os.MkdirAll(a.appDir, 0755)
	_ = os.WriteFile(a.configPath, data, 0644)
}

// ---------------------------------------------------------------------------
// Window
// ---------------------------------------------------------------------------

func (a *app) runWebUI() error {
	if err := procGetDpiForSystem.Find(); err == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi >= 96 && dpi <= 480 {
			a.dpi = int(dpi)
		}
	}
	a.scale = float64(a.dpi) / 96.0
	var work rect
	procSystemParametersInfoW.Call(SPI_GETWORKAREA, 0, uintptr(unsafe.Pointer(&work)), 0)
	width := int(float64(designLogicalW) * a.scale)
	height := int(float64(designLogicalH) * a.scale)
	if maxW := int(float64(work.Right-work.Left) * 0.92); width > maxW {
		width = maxW
	}
	if maxH := int(float64(work.Bottom-work.Top) * 0.92); height > maxH {
		height = maxH
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("AIXAI_UI_DEBUG") == "1",
		DataPath:  filepath.Join(a.appDir, "webview2-ui"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  appTitle + "  v" + appVersion,
			Width:  uint(width),
			Height: uint(height),
			IconId: APP_ICON_RESOURCE_ID,
			Center: true,
		},
	})
	if w == nil {
		if messageBox(0, "需要 WebView2 執行環境", "這個版本的介面需要 Microsoft Edge WebView2 Runtime（Windows 11 已內建）。\n\n要開啟下載頁面嗎？", MB_YESNO|MB_ICONWARNING) == IDYES {
			openInBrowser(webviewRuntimeDownloadURL)
		}
		return errors.New("無法建立 WebView2 介面")
	}
	a.ui = &webUI{w: w, hwnd: uintptr(w.Window())}
	a.hwnd = a.ui.hwnd
	w.SetSize(int(float64(minLogicalUIW)*a.scale), int(float64(minLogicalUIH)*a.scale), webview2.HintMin)
	a.subclassWindow()
	a.bindUI()
	a.cleanupAfterUpdate()
	w.SetHtml(uiHTML)
	w.Run()
	a.beginShutdown()
	return nil
}

// subclassWindow intercepts WM_CLOSE to confirm before abandoning a running task.
func (a *app) subclassWindow() {
	cb := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if uint32(msg) == wmClose && a.anyJobRunning() {
			if messageBox(hwnd, "任務仍在執行", "還有任務正在下載。關閉後這些任務會暫停，下次開啟程式可以按「繼續」接著下載。\n\n確定要關閉嗎？", MB_YESNO|MB_ICONQUESTION) != IDYES {
				return 0
			}
		}
		r, _, _ := procCallWindowProcW.Call(a.ui.origProc, hwnd, msg, wp, lp)
		return r
	})
	a.ui.origProc, _, _ = procSetWindowLongPtrW.Call(a.ui.hwnd, gwlpWndProc, cb)
}

// applyTitleBarTheme matches the native title bar to the page theme (Windows 11:
// dark mode + caption colour; harmless no-ops on older systems).
func (a *app) applyTitleBarTheme(dark bool) {
	if a.ui == nil || procDwmSetWindowAttribute.Find() != nil {
		return
	}
	var on int32
	caption := uint32(0x00EFF4F6) // BGR of #F6F4EF (light "paper")
	if dark {
		on = 1
		caption = 0x0017120E // BGR of #0E1217 (dark "ink")
	}
	procDwmSetWindowAttribute.Call(a.ui.hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), 4)
	procDwmSetWindowAttribute.Call(a.ui.hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&caption)), 4)
}

func openInBrowser(target string) {
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr(target))), 0, 0, SW_SHOWNORMAL)
}

// ---------------------------------------------------------------------------
// JavaScript bindings
// ---------------------------------------------------------------------------

type uiOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

type uiInit struct {
	Version  string     `json:"version"`
	Title    string     `json:"title"`
	Settings settings   `json:"settings"`
	Modes    []uiOption `json:"modes"`
	Logins   []uiOption `json:"logins"`
	Browsers []uiOption `json:"browsers"`
	Presets  []uiOption `json:"presets"`
	Repo     string     `json:"repo"`
	Email    string     `json:"email"`
	LogFile  string     `json:"logFile"`
	Jobs     []jobView  `json:"jobs"`
	MaxLimit int        `json:"maxLimit"`
}

type startRequest struct {
	URLs           string `json:"urls"`
	Mode           int    `json:"mode"`
	FormatID       string `json:"formatId"`
	Output         string `json:"output"`
	Sequence       bool   `json:"sequence"`
	SequenceStart  int    `json:"sequenceStart"`
	SequenceEnd    int    `json:"sequenceEnd"`
	Safe           bool   `json:"safe"`
	NoPlaylist     bool   `json:"noPlaylist"`
	PlaylistFolder bool   `json:"playlistFolder"`
	ExtraArgs      string `json:"extraArgs"`
	CookieBrowser  string `json:"cookieBrowser"`
	CookieFile     string `json:"cookieFile"`
	CaptureBrowser string `json:"captureBrowser"`
	AdvancedOpen   bool   `json:"advancedOpen"`
	MaxJobs        int    `json:"maxJobs"`
}

type startResponse struct {
	Error string   `json:"error,omitempty"`
	Field string   `json:"field,omitempty"`
	URLs  []string `json:"urls,omitempty"`
	JobID int      `json:"jobId,omitempty"`
}

func (a *app) bindUI() {
	w := a.ui.w
	_ = w.Bind("goInit", func() uiInit {
		modes := []uiOption{
			{"0", "最佳畫質影片", "自動選擇最高畫質並合併成 MP4"},
			{"5", "最高 720p 影片", "節省空間"},
			{"1", "轉成 MP3 音訊", ""},
			{"2", "整張播放清單", ""},
			{"3", "只查看可用格式（-F）", "不下載，列出所有畫質"},
			{"4", "指定格式 ID（-f）", "需要填寫格式 ID"},
		}
		logins := []uiOption{
			{"auto", "自動（只在網站要求時提示）", ""},
			{"none", "不使用登入", ""},
			{"firefox", "使用 Firefox 的登入", ""},
			{"edge", "使用 Edge 的登入", ""},
			{"chrome", "使用 Chrome 的登入", ""},
			{"brave", "使用 Brave 的登入", ""},
			{"vivaldi", "使用 Vivaldi 的登入", ""},
			{"cookiefile", "使用 Cookie 檔（cookies.txt）…", ""},
		}
		var presets []uiOption
		for _, p := range parameterPresets[1:] {
			label := p.Label
			hint := ""
			if i := strings.Index(label, "｜"); i >= 0 {
				hint = label[i+len("｜"):]
				label = label[:i]
			}
			presets = append(presets, uiOption{Value: p.Args, Label: label, Hint: hint})
		}
		a.initializeLocalState()
		a.loadJobsOnce.Do(a.loadJobs)
		return uiInit{Jobs: a.jobViews(), MaxLimit: maxJobsLimit, Version: appVersion, Title: appTitle, Settings: a.cfg, Modes: modes, Logins: logins, Browsers: captureBrowserOptions(), Presets: presets,
			Repo: updateRepoOwner + "/" + updateRepoName, Email: feedbackEmail, LogFile: filepath.Join(a.cfg.OutputFolder, runLogDirName)}
	})
	_ = w.Bind("goStart", a.startFromUI)
	_ = w.Bind("goJobAction", func(id int, action string) string { return a.jobAction(id, action) })
	_ = w.Bind("goClearFinished", func() { a.clearFinishedJobs() })
	_ = w.Bind("goSaveSettings", func(req startRequest) {
		a.applyRequestToSettings(req)
		a.saveSettings()
	})
	_ = w.Bind("goPickFolder", func(current string) string { return a.pickFolder(current) })
	_ = w.Bind("goPickCookieFile", func() string { return a.pickCookieFile() })
	_ = w.Bind("goPickResumeFile", func(current string) string { return a.pickResumeFile(current) })
	_ = w.Bind("goOpenFolder", func(path string) string { return a.openFolder(path) })
	_ = w.Bind("goOpenLogFile", func(id int) string {
		path := a.jobLogFile(id)
		if path == "" || !validFile(path, 1) {
			return "目前還沒有紀錄檔（開始下載後就會建立）。"
		}
		openInBrowser(path)
		return ""
	})
	_ = w.Bind("goCopyLog", func() string {
		text := redactSecrets(a.sessionLogText())
		if strings.TrimSpace(text) == "" {
			return "目前還沒有任何紀錄。"
		}
		if err := setClipboardText(a.hwnd, text); err != nil {
			return err.Error()
		}
		return ""
	})
	_ = w.Bind("goSetTheme", func(dark bool) { a.applyTitleBarTheme(dark) })
	_ = w.Bind("goAcceptDisclaimer", func(version string) {
		a.cfg.DisclaimerAccepted = version
		a.saveSettings()
		a.postLog("✓ 已同意免責聲明與使用條款（版本 " + version + "）。\r\n")
	})
	_ = w.Bind("goSetStartupUpdateCheck", func(on bool) {
		a.cfg.NoStartupUpdateCheck = !on
		a.saveSettings()
	})
	_ = w.Bind("goQuit", func() { procPostMessageW.Call(a.ui.hwnd, wmClose, 0, 0) })
	_ = w.Bind("goShowLoginWindow", func() string { return a.showLoginWindow() })
	_ = w.Bind("goOpenURL", func(target string) {
		if strings.HasPrefix(target, "https://") {
			openInBrowser(target)
		}
	})
	// Updates and feedback live in updater.go / feedback.go.
	_ = w.Bind("goCheckUpdates", func() { go a.checkUpdatesAsync() })
	_ = w.Bind("goInstallRelease", func(tag string) { go a.installReleaseAsync(tag) })
	_ = w.Bind("goCaptureScreenshot", func() (feedbackImage, error) { return a.captureWindowScreenshot() })
	_ = w.Bind("goPickImages", func() []feedbackImage { return a.pickFeedbackImages() })
	_ = w.Bind("goSubmitFeedback", func(fb feedbackRequest) (feedbackResult, error) { return a.submitFeedback(fb) })
}

func (a *app) applyRequestToSettings(req startRequest) {
	if m := req.Mode; m >= 0 && m <= 5 {
		a.cfg.Mode = m
	}
	a.cfg.OutputFolder = strings.TrimSpace(req.Output)
	a.cfg.NoPlaylist = req.NoPlaylist
	a.cfg.PlaylistFolder = req.PlaylistFolder
	a.cfg.ExtraArgs = req.ExtraArgs
	if req.CookieBrowser == "cookiefile" {
		a.cfg.CookieBrowser = "auto"
		a.cfg.CookieFile = strings.TrimSpace(req.CookieFile)
	} else {
		if req.CookieBrowser != "" {
			a.cfg.CookieBrowser = req.CookieBrowser
		}
		a.cfg.CookieFile = ""
	}
	if b := strings.TrimSpace(req.CaptureBrowser); b != "" {
		a.cfg.CaptureBrowser = b
		a.capturePref.Store(b)
	}
	a.cfg.Sequence = req.Sequence
	if req.SequenceStart > 0 {
		a.cfg.SequenceStart = req.SequenceStart
	}
	if req.SequenceEnd > 0 {
		a.cfg.SequenceEnd = req.SequenceEnd
	}
	a.cfg.UnsafeMode = !req.Safe
	a.cfg.AdvancedOpen = req.AdvancedOpen
	if req.MaxJobs > 0 {
		a.cfg.MaxJobs = clampMaxJobs(req.MaxJobs)
		if int(a.maxJobs.Swap(int32(a.cfg.MaxJobs))) != a.cfg.MaxJobs {
			go a.schedule() // a higher limit may start waiting tasks
		}
	}
	a.setLogDir(a.cfg.OutputFolder)
}

// startFromUI validates the request (returning field-level messages for the
// page to show inline) and starts the download task.
func (a *app) startFromUI(req startRequest) startResponse {
	var urls []string
	var notes []string
	for _, line := range strings.Split(strings.ReplaceAll(req.URLs, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		normalized, err := normalizeInputURL(line)
		if err != nil {
			return startResponse{Error: err.Error(), Field: "urls"}
		}
		if normalized != line {
			notes = append(notes, "✓ 已將網址自動整理為："+normalized+"\r\n")
		}
		urls = append(urls, normalized)
	}
	if len(urls) == 0 {
		return startResponse{Error: "請先貼上影片或播放清單網址。", Field: "urls"}
	}
	if req.Sequence {
		if req.SequenceStart <= 0 || req.SequenceEnd <= 0 || req.SequenceEnd < req.SequenceStart {
			return startResponse{Error: "請輸入有效的集數範圍，例如 1 ～ 50。", Field: "sequence"}
		}
		if req.SequenceEnd-req.SequenceStart+1 > 500 {
			return startResponse{Error: "單次最多 500 集，請縮小範圍。", Field: "sequence"}
		}
		var expanded []string
		for _, baseURL := range urls {
			series, err := expandSequenceURL(baseURL, req.SequenceStart, req.SequenceEnd)
			if err != nil {
				return startResponse{Error: err.Error(), Field: "sequence"}
			}
			if episodeFromQuery(baseURL) == 0 && len(series) > 0 && episodeFromQuery(series[0]) > 0 {
				notes = append(notes, "ℹ 網址中沒有集數，改以「?ep=集數」逐集開啟，並用瀏覽器實際播放擷取（較慢，每集約多 10～30 秒）。若網站不支援這個參數，偵測到重複影片會自動停止。\r\n")
			}
			expanded = append(expanded, series...)
		}
		urls = expanded
		notes = append(notes, fmt.Sprintf("✓ 已建立連續序號下載：%d ～ %d，共 %d 個網址。\r\n", req.SequenceStart, req.SequenceEnd, len(urls)))
	}
	formatID := strings.TrimSpace(req.FormatID)
	if req.Mode == 4 && formatID == "" {
		return startResponse{Error: "請輸入格式 ID，例如 136 或 136+bestaudio/best。", Field: "formatId"}
	}
	output := strings.TrimSpace(req.Output)
	if output == "" {
		return startResponse{Error: "請選擇儲存位置。", Field: "output"}
	}
	if req.Mode != 3 {
		if err := os.MkdirAll(output, 0755); err != nil {
			return startResponse{Error: "無法建立儲存資料夾：" + err.Error(), Field: "output"}
		}
	}
	if req.CookieBrowser == "cookiefile" && strings.TrimSpace(req.CookieFile) == "" {
		return startResponse{Error: "請選擇 Cookie 檔，或改用其他登入方式。", Field: "cookieFile"}
	}

	a.applyRequestToSettings(req)
	a.saveSettings()
	if a.resumePath != "" {
		if _, err := os.Stat(a.resumePath); err != nil {
			notes = append(notes, "⚠ 先前選擇的接續檔已不存在，改為一般下載。\r\n")
			a.resumePath = ""
		} else {
			notes = append(notes, "✓ 本次將嘗試接續："+a.resumePath+"\r\n")
		}
	}

	j := a.addJob(urls, req.Mode, formatID, output, a.cfg, notes)
	a.resumePath = ""
	return startResponse{URLs: urls, JobID: j.ID}
}
