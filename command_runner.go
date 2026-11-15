//go:build windows

// External command execution, output summaries, and process cancellation.
package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (a *app) runCommand(ctx context.Context, args []string) error {
	return a.runExternalCommand(ctx, a.ytDlpPath, args, "yt-dlp")
}

func (a *app) runLuxCommand(ctx context.Context, args []string) error {
	return a.runExternalCommand(ctx, a.luxPath, args, "Lux")
}

func (a *app) runExternalCommand(ctx context.Context, exePath string, args []string, engine string) error {
	cmd := exec.CommandContext(ctx, exePath, args...)
	cmd.Dir = a.binDir
	cmd.Env = append(os.Environ(), "PATH="+a.binDir+";"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP}

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return &commandRunError{Cause: err, Summary: "無法啟動 " + engine + "：" + err.Error()}
	}
	done := make(chan struct{})
	a.cmdMu.Lock()
	a.currentCmd = cmd
	a.currentDone = done
	a.cmdMu.Unlock()

	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		waitCh <- err
	}()

	isYtDlp := engine == "yt-dlp"
	a.lastYtArgs, a.lastYtFiles, a.lastYtSubs = nil, nil, nil
	if isYtDlp {
		a.lastYtArgs = append([]string(nil), args...)
		a.lastYtGeneric = false
		a.lastYtErrors = ytRunErrors{}
	}
	reader := bufio.NewReaderSize(pr, 64*1024)
	var readErr error
	var outputTail string
	for {
		line, lineErr := reader.ReadString('\n')
		if engine == "Lux" && isGoStackLine(line) {
			// Lux follows each error with Go stack traces; the error line
			// itself says what went wrong.
			line = ""
		}
		if len(line) > 0 {
			appendOutputTail(&outputTail, line, 96*1024)
			if isYtDlp && strings.Contains(line, "[") {
				for _, f := range downloadedFilesFromOutput(line) {
					a.lastYtFiles = append(a.lastYtFiles, f)
				}
				for _, f := range ytSubtitleFilesFromOutput(line) {
					a.lastYtSubs = append(a.lastYtSubs, f)
				}
				if strings.HasPrefix(strings.TrimSpace(line), "[generic]") {
					a.lastYtGeneric = true
				}
			}
			if isYtDlp {
				a.lastYtErrors.observe(line)
			}
			displayLine := strings.ReplaceAll(strings.ReplaceAll(line, "\r\n", "\n"), "\n", "\r\n")
			a.postLog(displayLine)
		}
		if lineErr != nil {
			if !errors.Is(lineErr, io.EOF) {
				readErr = lineErr
			}
			break
		}
		if a.stopRequested.Load() {
			break
		}
	}
	_ = pr.Close()
	err := <-waitCh
	a.lastCmdOutput = outputTail
	a.cmdMu.Lock()
	a.currentCmd = nil
	a.currentDone = nil
	close(done)
	a.cmdMu.Unlock()

	if a.stopRequested.Load() {
		return errStopped
	}
	if err != nil {
		runErr := &commandRunError{Cause: err, Summary: summarizeCommandOutputForEngine(outputTail, err, engine)}
		if isYtDlp && readErr == nil && a.lastYtErrors.onlyUnavailable() && (len(a.lastYtFiles) > 0 || len(a.lastYtSubs) > 0) {
			// The saved videos are kept for the media check (acceptPlaylistSkips).
			for _, f := range a.lastYtFiles {
				a.noteOutput(f)
			}
			return runErr
		}
		a.lastYtArgs, a.lastYtFiles, a.lastYtSubs = nil, nil, nil
		return runErr
	}
	if readErr != nil {
		return &commandRunError{Cause: readErr, Summary: "讀取 " + engine + " 執行訊息失敗：" + readErr.Error()}
	}
	if isYtDlp {
		for _, f := range a.lastYtFiles {
			a.noteOutput(f)
		}
	}
	return nil
}

func appendOutputTail(dst *string, value string, limit int) {
	if value == "" || limit <= 0 {
		return
	}
	*dst += value
	if len(*dst) > limit {
		*dst = (*dst)[len(*dst)-limit:]
		if i := strings.IndexByte(*dst, '\n'); i >= 0 && i+1 < len(*dst) {
			*dst = (*dst)[i+1:]
		}
	}
}

func summarizeCommandOutputForEngine(output string, cause error, engine string) string {
	result := summarizeCommandOutput(output, cause)
	if engine != "" && engine != "yt-dlp" {
		result = strings.ReplaceAll(result, "yt-dlp 結束時回報", engine+" 結束時回報")
		result = strings.ReplaceAll(result, "yt-dlp 執行失敗", engine+" 執行失敗")
	}
	return result
}

// isGoStackLine reports a line of a Go stack trace ("pkg/path.Func" followed
// by an indented "/path/file.go:123").
func isGoStackLine(line string) bool {
	l := strings.TrimSpace(line)
	if l == "" {
		return false
	}
	if goStackFileRE.MatchString(l) {
		return true
	}
	return goStackFuncRE.MatchString(l)
}

var (
	goStackFileRE = regexp.MustCompile(`^/\S+\.(go|s):\d+( \+0x[0-9a-f]+)?$`)
	goStackFuncRE = regexp.MustCompile(`^(main|runtime|[\w.-]+\.[a-z]{2,}/[\w./-]+)\.[\w().*]+(\(.*\))?$`)
)

func summarizeCommandOutput(output string, cause error) string {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	var lines []string
	for _, l := range strings.Split(normalized, "\n") {
		// "... error:" with the reason on the next line (Lux) is one message.
		if t := strings.TrimSpace(l); t != "" && len(lines) > 0 && strings.HasSuffix(strings.TrimSpace(lines[len(lines)-1]), "error:") {
			lines[len(lines)-1] = strings.TrimSpace(lines[len(lines)-1]) + " " + t
			continue
		}
		lines = append(lines, l)
	}
	var important []string
	var fallback []string
	for _, raw := range lines {
		line := strings.TrimSpace(stripANSI(raw))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "[download]") || strings.Contains(lower, "error") {
			fallback = append(fallback, line)
			if len(fallback) > 14 {
				fallback = fallback[len(fallback)-14:]
			}
		}
		if strings.Contains(lower, "error:") ||
			strings.Contains(lower, "unsupported url") ||
			strings.Contains(lower, "unable to") ||
			strings.Contains(lower, "http error") ||
			strings.Contains(lower, "sign in") ||
			strings.Contains(lower, "login required") ||
			strings.Contains(lower, "password") ||
			strings.Contains(lower, "no video formats") ||
			strings.Contains(lower, "precondition failed") ||
			strings.Contains(lower, "error opening") || // FFmpeg, after its version banner
			strings.Contains(lower, "invalid data found") ||
			strings.Contains(lower, "server returned") ||
			strings.Contains(lower, "forbidden") ||
			strings.Contains(lower, "too many requests") {
			important = append(important, line)
		}
	}
	chosen := important
	if len(chosen) == 0 {
		chosen = fallback
	}
	if len(chosen) > 8 {
		chosen = chosen[len(chosen)-8:]
	}
	if len(chosen) == 0 {
		if cause != nil {
			return "yt-dlp 結束時回報：" + cause.Error()
		}
		return "yt-dlp 執行失敗，但沒有提供可辨識的錯誤訊息。"
	}
	result := strings.Join(chosen, "\n")
	lowerAll := strings.ToLower(result)
	var tip string
	switch {
	case strings.Contains(lowerAll, "sign in") || strings.Contains(lowerAll, "login required") || strings.Contains(lowerAll, "cookies"):
		tip = "\n\n建議：先選『網站登入狀態 → 自動偵測』或 Firefox；也可指定 Netscape 格式 cookies.txt。若 Edge／Chrome Cookie 無法讀取，請先完全關閉瀏覽器或改用 Firefox。"
	case strings.Contains(lowerAll, "vimeo") && (strings.Contains(lowerAll, "404") || strings.Contains(lowerAll, "403") || strings.Contains(lowerAll, "password")):
		tip = "\n\n建議：Vimeo 私有、嵌入或密碼影片可能需要瀏覽器登入狀態、--referer 原始網頁網址或 --video-password。"
	case strings.Contains(lowerAll, "bilibili") && (strings.Contains(lowerAll, "412") || strings.Contains(lowerAll, "403") || strings.Contains(lowerAll, "no video formats")):
		tip = "\n\n建議：選擇已登入 Bilibili 的瀏覽器；空間頁請使用含 UID 的完整網址，例如 https://space.bilibili.com/123456/video。"
	case strings.Contains(lowerAll, "unsupported url"):
		tip = "\n\nyt-dlp 本身尚未支援此網址。AIXAI 會自動嘗試網站專用解析與 Lux 備援引擎；若仍失敗，可能是網站已改版、需要登入，或媒體採 DRM/加密保護。"
	}
	return result + tip
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func (a *app) stopCurrent() {
	if !a.busy.Load() {
		return
	}
	a.stopRequested.Store(true)
	a.postStatus("狀態：正在停止目前任務…")
	a.postLog("\r\n使用者要求停止。\r\n")
	a.cancelCurrentTask()
	a.killCurrentProcessTree(4 * time.Second)
}

func (a *app) killCurrentProcessTree(timeout time.Duration) {
	a.cmdMu.Lock()
	cmd := a.currentCmd
	done := a.currentDone
	a.cmdMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	killer := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	_ = killer.Run()
	_ = cmd.Process.Kill()

	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
}
