package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Diagnostics: every item keeps a short trail of the ways that were tried
// ("yt-dlp ✗ → 網頁解析 ✗ → 瀏覽器擷取 ✓ → 檔案檢查 ✓"). A finished item
// writes one summary line to the log; a failed one writes a full block with
// the candidates that were seen. 回報包 (report pack) collects those blocks
// and the end of the task's log, with secrets and local paths removed, into
// one text file the user can send as it is.

const (
	diagBlockStart = "=== 診斷摘要"
	diagBlockEnd   = "=== 摘要結束 ==="
	diagLinePrefix = "診斷："
	reportPrefix   = "AIXAI_回報包_"
)

type diagStep struct {
	Layer string
	Mark  string // ✓ ✗ ■ or … while running
	Error string
	Notes []string
}

type itemDiag struct {
	Index, Total int
	URL          string
	Steps        []diagStep
}

// diagStartItem begins the trail of one item of runURLs.
func (a *app) diagStartItem(index, total int, rawURL string) {
	a.diag = &itemDiag{Index: index, Total: total, URL: rawURL}
}

// diagBegin opens a step; notes added until diagEnd belong to it.
func (a *app) diagBegin(layer string) {
	if a.diag == nil {
		return
	}
	a.diag.Steps = append(a.diag.Steps, diagStep{Layer: layer, Mark: "…"})
}

// diagEnd closes the last open step with the step's result.
func (a *app) diagEnd(err error) {
	if a.diag == nil {
		return
	}
	for i := len(a.diag.Steps) - 1; i >= 0; i-- {
		st := &a.diag.Steps[i]
		if st.Mark != "…" {
			continue
		}
		switch {
		case err == nil:
			st.Mark = "✓"
		case errors.Is(err, errStopped) || a.stopRequested.Load():
			st.Mark = "■"
			st.Error = "已停止"
		default:
			st.Mark = "✗"
			st.Error = diagErrorText(err)
		}
		return
	}
}

// diagFail marks the last step failed after the fact (its file did not pass
// the check that runs after the step itself reported success).
func (a *app) diagFail(err error) {
	if a.diag == nil || len(a.diag.Steps) == 0 || err == nil {
		return
	}
	st := &a.diag.Steps[len(a.diag.Steps)-1]
	st.Mark, st.Error = "✗", diagErrorText(err)
}

// diagRecord adds a step that is already finished.
func (a *app) diagRecord(layer string, err error) {
	a.diagBegin(layer)
	a.diagEnd(err)
}

// diagNote attaches a detail to the open step (or the last one).
func (a *app) diagNote(text string) {
	if a.diag == nil || strings.TrimSpace(text) == "" {
		return
	}
	if len(a.diag.Steps) == 0 {
		a.diag.Steps = append(a.diag.Steps, diagStep{Layer: "準備", Mark: "·"})
	}
	idx := len(a.diag.Steps) - 1
	for i := idx; i >= 0; i-- {
		if a.diag.Steps[i].Mark == "…" {
			idx = i
			break
		}
	}
	st := &a.diag.Steps[idx]
	if len(st.Notes) < 40 {
		st.Notes = append(st.Notes, text)
	}
}

// diagErrorText is the first meaningful line of an error, kept short.
func diagErrorText(err error) string {
	var cmdErr *commandRunError
	text := err.Error()
	if errors.As(err, &cmdErr) && strings.TrimSpace(cmdErr.Summary) != "" {
		text = cmdErr.Summary
	}
	line := ""
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if r := []rune(line); len(r) > 160 {
		line = string(r[:160]) + "…"
	}
	return line
}

// diagFinishItem writes the item's trail to the log: one line when it went
// well, the full block when it failed or a check raised a warning.
func (a *app) diagFinishItem(err error) {
	d := a.diag
	a.diag = nil
	if d == nil || len(d.Steps) == 0 {
		return
	}
	warned := false
	for _, st := range d.Steps {
		for _, n := range st.Notes {
			if strings.Contains(n, "⚠") || strings.Contains(n, "✗") {
				warned = true
			}
		}
	}
	if err == nil && !warned {
		a.postLog(diagLinePrefix + d.compact() + "\r\n")
		return
	}
	a.postLog(d.block(err))
}

func (d *itemDiag) compact() string {
	var parts []string
	for _, st := range d.Steps {
		part := st.Layer + " " + st.Mark
		for _, n := range st.Notes {
			if strings.HasPrefix(n, "檔案檢查 ") {
				part += "（" + n + "）"
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " → ")
}

func (d *itemDiag) block(err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s（第 %d/%d 項）===\r\n", diagBlockStart, d.Index+1, d.Total)
	fmt.Fprintf(&b, "網址：%s\r\n", d.URL)
	for i, st := range d.Steps {
		line := fmt.Sprintf("%d. %s %s", i+1, st.Layer, st.Mark)
		if st.Error != "" {
			line += "：" + st.Error
		}
		b.WriteString(line + "\r\n")
		for _, n := range st.Notes {
			b.WriteString("   · " + n + "\r\n")
		}
	}
	if err != nil {
		b.WriteString("結果：失敗 — " + diagErrorText(err) + "\r\n")
	} else {
		b.WriteString("結果：完成（有警告）\r\n")
	}
	b.WriteString(diagBlockEnd + "\r\n")
	return b.String()
}

// diagMediaURL shortens a media address for the trail: host and path are
// kept (they show which server and which episode), query values are not
// (they are often signed tokens).
func diagMediaURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "（無法解析的網址）"
	}
	path := u.EscapedPath()
	if r := []rune(path); len(r) > 90 {
		path = string(r[:40]) + "…" + string(r[len(r)-45:])
	}
	out := u.Host + path
	if u.RawQuery != "" {
		var keys []string
		for k := range u.Query() {
			keys = append(keys, k)
		}
		if len(keys) > 6 {
			keys = append(keys[:6], "…")
		}
		out += "?" + strings.Join(keys, ",")
	}
	return out
}

// describeCaptureCandidate is one line of the capture's candidate list.
func describeCaptureCandidate(c browserMediaCandidate) string {
	var tags []string
	tags = append(tags, c.Kind, fmt.Sprintf("分數 %d", c.Score))
	if c.EpisodeMatch {
		tags = append(tags, "集數相符")
	}
	if c.ContentLength > 0 {
		tags = append(tags, fmt.Sprintf("%.1f MB", float64(c.ContentLength)/(1<<20)))
	}
	return "[" + strings.Join(tags, "，") + "] " + diagMediaURL(c.URL)
}

// ---- report pack ----

// reportFromLog extracts the useful part of a task log: the diagnostic
// blocks and summary lines, then the last lines of the log itself.
func reportFromLog(logText string, tailLines int) (diag []string, tail []string) {
	lines := strings.Split(strings.ReplaceAll(logText, "\r\n", "\n"), "\n")
	inBlock := false
	for _, l := range lines {
		content := stripLogStamp(strings.TrimSpace(l))
		switch {
		case strings.HasPrefix(content, diagBlockStart):
			inBlock = true
		case !inBlock && strings.HasPrefix(content, diagLinePrefix):
			diag = append(diag, content)
			continue
		}
		if !inBlock {
			continue
		}
		if strings.HasPrefix(content, "· ") {
			content = "   " + content // the log file drops leading spaces
		}
		diag = append(diag, content)
		if strings.HasPrefix(content, diagBlockEnd) {
			inBlock = false
			diag = append(diag, "")
		}
	}
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if s == "" || progressLineRE.MatchString(stripLogStamp(s)) {
			continue
		}
		tail = append(tail, s)
	}
	if len(tail) > tailLines {
		tail = tail[len(tail)-tailLines:]
	}
	return diag, tail
}

// stripLogStamp removes the "[2006-01-02 15:04:05] " prefix of log file lines.
func stripLogStamp(s string) string {
	if len(s) > 22 && s[0] == '[' && s[20] == ']' {
		return strings.TrimSpace(s[21:])
	}
	return s
}

// buildReportPack writes the report pack of task id next to its log file
// and returns the file path and its text.
func (a *app) buildReportPack(id int) (string, string, error) {
	logPath := a.jobLogFile(id)
	if logPath == "" || !validFile(logPath, 1) {
		return "", "", errors.New("這個任務還沒有紀錄檔，無法產生回報包。")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "", "", fmt.Errorf("讀取紀錄檔失敗：%v", err)
	}
	a.jobsMu.Lock()
	var j job
	if found := a.findJobLocked(id); found != nil {
		j = *found
	}
	a.jobsMu.Unlock()

	diag, tail := reportFromLog(strings.TrimPrefix(string(data), "\uFEFF"), 150)
	var b strings.Builder
	b.WriteString("AIXAI 問題回報包\r\n")
	b.WriteString("（已自動遮蔽 Cookie、登入資訊與電腦上的使用者資料夾路徑；可直接傳給開發者。）\r\n\r\n")
	fmt.Fprintf(&b, "版本：%s v%s\r\n系統：%s\r\n產生時間：%s\r\n", appTitle, appVersion, windowsVersionString(), time.Now().Format("2006-01-02 15:04:05"))
	if j.ID != 0 {
		modeNames := []string{"最佳畫質", "MP3", "播放清單", "查看格式", "指定格式", "720p"}
		mode := "?"
		if j.Mode >= 0 && j.Mode < len(modeNames) {
			mode = modeNames[j.Mode]
		}
		login := j.Cfg.CookieBrowser
		if strings.TrimSpace(j.Cfg.CookieFile) != "" {
			login = "cookie 檔"
		}
		fmt.Fprintf(&b, "任務：#%d，%d 個網址，狀態：%s\r\n", j.ID, len(j.URLs), j.State)
		fmt.Fprintf(&b, "處理方式：%s｜帳號安全模式：%v｜登入：%s｜連續集數：%v\r\n", mode, !j.Cfg.UnsafeMode, login, j.Cfg.Sequence)
		if strings.TrimSpace(j.Cfg.ExtraArgs) != "" {
			b.WriteString("進階參數：" + j.Cfg.ExtraArgs + "\r\n")
		}
		if strings.TrimSpace(j.Message) != "" {
			b.WriteString("結果訊息：" + firstLine(j.Message) + "\r\n")
		}
	}
	b.WriteString("\r\n--- 診斷摘要 ---\r\n")
	if len(diag) == 0 {
		b.WriteString("（這次執行沒有診斷摘要；可能是 v4.1 以前的紀錄，或任務在第一項開始前就停止）\r\n")
	} else {
		b.WriteString(strings.Join(diag, "\r\n") + "\r\n")
	}
	b.WriteString("\r\n--- 紀錄最後部分 ---\r\n")
	b.WriteString(strings.Join(tail, "\r\n") + "\r\n")
	text := privacyScrub(b.String())

	dir := a.dataSubdir(reportsSubdir)
	if dir == "" {
		dir = filepath.Dir(logPath)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s%d_%s.txt", reportPrefix, id, time.Now().Format("20060102_150405")))
	if err := os.WriteFile(path, []byte("\uFEFF"+text), 0644); err != nil {
		return "", text, fmt.Errorf("寫入回報包失敗：%v", err)
	}
	return path, text, nil
}

// exportReportPack is the 回報包 button: save the file, copy the text, and
// show the file in Explorer so it can be sent as an attachment.
func (a *app) exportReportPack(id int) string {
	path, text, err := a.buildReportPack(id)
	if err != nil && text == "" {
		return err.Error()
	}
	clipErr := setClipboardText(a.hwnd, text)
	if err != nil {
		if clipErr != nil {
			return err.Error()
		}
		return err.Error() + "（內容已複製到剪貼簿）"
	}
	openExplorerSelecting(path)
	if clipErr != nil {
		return "已產生回報包：" + filepath.Base(path) + "（已在檔案總管中選取，可直接傳送這個檔案）"
	}
	return "已產生回報包並複製到剪貼簿：" + filepath.Base(path) + "（已在檔案總管中選取，可直接傳送或貼上內容）"
}
