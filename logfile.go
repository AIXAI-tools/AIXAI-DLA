//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	// Each press of 開始下載 gets its own file in this subfolder of the download
	// folder, so a report only carries the run it is about.
	runLogDirName  = "AIXAI_下載紀錄"
	runLogPrefix   = "AIXAI_下載紀錄_"
	appLogFileName = "AIXAI_程式紀錄.txt" // messages outside a run, in the app data folder

	maxLogFileBytes    = 20 << 20 // rotate the on-disk log at ~20 MiB
	maxSessionLogBytes = 8 << 20  // in-memory copy kept for the 複製 Log button

	CF_UNICODETEXT = 13
	GMEM_MOVEABLE  = 0x0002
)

var (
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")

	// yt-dlp download progress lines are emitted many times per second; only
	// the final 100% line is worth keeping in the on-disk log.
	progressLineRE = regexp.MustCompile(`^\[download\]\s+\d{1,2}(\.\d+)?%`)
	// Defence in depth: never persist credentials even if a verbose engine
	// (e.g. yt-dlp -v) echoes captured request headers or cookie options.
	secretHeaderRE = regexp.MustCompile(`(?i)\b(cookie|authorization|x-tt-token|proxy-authorization)(\s*[:=]\s*)([^\r\n'"\]]+)`)
	secretFlagRE   = regexp.MustCompile(`(?i)(--(?:password|video-password|ap-password|twofactor)\s*['",]?\s*['"]?)([^\s'",\]]+)`)
)

func redactSecrets(s string) string {
	s = secretHeaderRE.ReplaceAllString(s, "$1$2[已遮蔽]")
	return secretFlagRE.ReplaceAllString(s, "$1[已遮蔽]")
}

// recordLog keeps a chronological session copy and appends to the log file.
// It is called for every message, whether it came from a worker (postLog) or
// directly from the UI thread (appendLog).
func (a *app) recordLog(s string) {
	if s == "" {
		return
	}
	a.recordSession(s)
	a.writeLogFile(s)
}

// recordSession keeps s in the in-memory copy used by 複製 Log.
func (a *app) recordSession(s string) {
	a.sessionMu.Lock()
	a.sessionLog = append(a.sessionLog, s)
	a.sessionBytes += len(s)
	for a.sessionBytes > maxSessionLogBytes && len(a.sessionLog) > 1 {
		a.sessionBytes -= len(a.sessionLog[0])
		a.sessionLog = a.sessionLog[1:]
	}
	a.sessionMu.Unlock()
}

func (a *app) sessionLogText() string {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	return strings.Join(a.sessionLog, "")
}

// logFileDir is the run-log folder inside the current download folder; it
// falls back to the app data folder when that folder cannot be created.
func (a *app) logFileDir() string {
	dir, _ := a.logDir.Load().(string)
	if dir = strings.TrimSpace(dir); dir != "" {
		dir = filepath.Join(dir, runLogDirName)
		if os.MkdirAll(dir, 0755) == nil {
			return dir
		}
	}
	return a.appDir
}

// beginRunLog starts a new log file for one press of 開始下載 and clears the
// in-memory copy, so 複製 Log and the file both hold only this run.
func (a *app) beginRunLog(started time.Time) {
	dir := a.logFileDir()
	base := runLogPrefix + started.Format("20060102_150405")
	path := filepath.Join(dir, base+".txt")
	for n := 2; n < 100; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s_%d.txt", base, n))
	}
	a.sessionMu.Lock()
	a.sessionLog, a.sessionBytes = nil, 0
	a.sessionMu.Unlock()
	a.logFileMu.Lock()
	a.runLogPath, a.lastRunLog = path, path
	a.logFileMu.Unlock()
}

// endRunLog is called after the run's final line; later messages go to the
// app log until the next run starts.
func (a *app) endRunLog() {
	a.logFileMu.Lock()
	a.runLogPath = ""
	a.logFileMu.Unlock()
}

// latestRunLog is the file of the running or most recent run ("" if none yet).
func (a *app) latestRunLog() string {
	a.logFileMu.Lock()
	defer a.logFileMu.Unlock()
	return a.lastRunLog
}

// setLogDir is called from the UI thread whenever the download folder is
// (re)applied; workers only ever read the value atomically.
func (a *app) setLogDir(dir string) { a.logDir.Store(strings.TrimSpace(dir)) }

func (a *app) writeLogFile(s string) {
	var b strings.Builder
	stamp := time.Now().Format("2006-01-02 15:04:05")
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if progressLineRE.MatchString(trimmed) && !strings.Contains(trimmed, "100%") {
			continue
		}
		b.WriteString("[" + stamp + "] " + redactSecrets(trimmed) + "\r\n")
	}
	if b.Len() == 0 {
		return
	}
	a.logFileMu.Lock()
	defer a.logFileMu.Unlock()
	path := a.runLogPath
	if path == "" {
		if a.appDir == "" {
			return // no data folder (unit tests): never write into the working directory
		}
		path = filepath.Join(a.appDir, appLogFileName)
		if st, err := os.Stat(path); err == nil && st.Size() > maxLogFileBytes {
			// Keep the old log under a dated name instead of discarding it.
			archived := filepath.Join(a.appDir, "AIXAI_程式紀錄_"+time.Now().Format("20060102_150405")+".txt")
			_ = os.Rename(path, archived)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		_, _ = f.WriteString("\uFEFF") // UTF-8 BOM so Notepad shows Chinese correctly
	}
	_, _ = f.WriteString(b.String())
}

// logTaskHeader writes everything needed to reproduce a run: time, version,
// URLs and the effective settings. Cookie contents are never written.
func (a *app) logTaskHeader(urls []string, mode int, formatID string, cfg settings) {
	modeName := []string{"下載最佳畫質影片", "轉成 MP3 音樂檔", "下載整張播放清單", "查看可用格式（-F）", "依格式 ID 下載（-f）", "最高 720p MP4"}
	name := fmt.Sprintf("模式 %d", mode)
	if mode >= 0 && mode < len(modeName) {
		name = modeName[mode]
	}
	var b strings.Builder
	b.WriteString("────────── 任務資訊 ──────────\r\n")
	b.WriteString("程式版本：" + appTitle + " v" + appVersion + "\r\n")
	b.WriteString("處理方式：" + name + "\r\n")
	if strings.TrimSpace(formatID) != "" {
		b.WriteString("格式 ID：" + formatID + "\r\n")
	}
	b.WriteString("儲存位置：" + cfg.OutputFolder + "\r\n")
	b.WriteString("進階參數：" + strings.TrimSpace(cfg.ExtraArgs) + "\r\n")
	b.WriteString("網站登入狀態：" + cfg.CookieBrowser + "\r\n")
	capture := cfg.CaptureBrowser
	if capture == "" || capture == "default" {
		capture = "系統預設"
		if _, name := systemDefaultBrowser(); name != "" {
			capture += "（" + name + "）"
		}
	}
	b.WriteString("擷取用瀏覽器：" + capture + "\r\n")
	if strings.TrimSpace(cfg.CookieFile) != "" {
		b.WriteString("Cookie 檔：已指定（內容不記錄）\r\n")
	}
	if cfg.Sequence {
		b.WriteString(fmt.Sprintf("連續序號：%d ～ %d\r\n", cfg.SequenceStart, cfg.SequenceEnd))
	}
	b.WriteString(fmt.Sprintf("網址（共 %d 個）：\r\n", len(urls)))
	for i, u := range urls {
		b.WriteString(fmt.Sprintf("  %d. %s\r\n", i+1, u))
	}
	b.WriteString("──────────────────────────────\r\n")
	a.recordLog(b.String())
}

func setClipboardText(hwnd uintptr, text string) error {
	utf16, err := syscall.UTF16FromString(strings.ReplaceAll(text, "\x00", ""))
	if err != nil {
		return err
	}
	size := uintptr(len(utf16) * 2)
	if r, _, _ := procOpenClipboard.Call(hwnd); r == 0 {
		return errors.New("剪貼簿正被其他程式使用，請稍後再試")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	mem, _, _ := procGlobalAlloc.Call(GMEM_MOVEABLE, size)
	if mem == 0 {
		return errors.New("無法配置剪貼簿記憶體")
	}
	ptr, _, _ := procGlobalLock.Call(mem)
	if ptr == 0 {
		procGlobalFree.Call(mem)
		return errors.New("無法鎖定剪貼簿記憶體")
	}
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&utf16[0])), size)
	procGlobalUnlock.Call(mem)
	if r, _, _ := procSetClipboardData.Call(CF_UNICODETEXT, mem); r == 0 {
		procGlobalFree.Call(mem)
		return errors.New("寫入剪貼簿失敗")
	}
	return nil // the clipboard now owns mem
}

// copyLogToClipboard copies the whole session log (oldest first, as debugging
// expects) with secrets redacted, and reports where the log file lives.
func (a *app) copyLogToClipboard() {
	text := redactSecrets(a.sessionLogText())
	if strings.TrimSpace(text) == "" {
		messageBox(a.hwnd, "複製 Log", "目前還沒有任何紀錄。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if err := setClipboardText(a.hwnd, text); err != nil {
		messageBox(a.hwnd, "複製 Log", err.Error(), MB_OK|MB_ICONWARNING)
		return
	}
	a.postStatus(fmt.Sprintf("狀態：已複製 %d 行 Log 到剪貼簿（本次紀錄檔：%s）", strings.Count(text, "\n"), a.latestRunLog()))
}
