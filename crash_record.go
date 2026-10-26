//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 程式沒有正常關閉時留下紀錄：每次執行在 sessions\ 放一個標記（每分鐘更新最後
// 執行時間），Go 當機訊息另存一份到同名的 crash 檔（debug.SetCrashOutput）。
// 正常關閉時清掉；下次啟動若發現別的已結束程序留下的標記，就比對開機時間，
// 分辨「電腦關機／重新開機」與「程式自己結束」，寫進程式紀錄與當機紀錄。
const (
	sessionsSubdir      = "sessions"
	crashLogFileName    = "AIXAI_當機紀錄.txt" // in the data folder; attached to reports
	sessionBeat         = time.Minute
	maxCrashTextBytes   = 256 << 10
	maxCrashLogBytes    = 4 << 20
	crashAttachMaxAge   = 7 * 24 * time.Hour
	processQueryLimited = 0x1000
)

var procGetTickCount64 = kernel32.NewProc("GetTickCount64")

type sessionMark struct {
	PID      int       `json:"pid"`
	Version  string    `json:"version"`
	Started  time.Time `json:"started"`
	LastSeen time.Time `json:"lastSeen"`
}

type sessionRecorder struct {
	dir   string
	mark  sessionMark
	crash *os.File
	stop  chan struct{}
}

func (s *sessionRecorder) markPath() string {
	return filepath.Join(s.dir, "run_"+strconv.Itoa(s.mark.PID)+".json")
}

func crashPathFor(dir string, pid int) string {
	return filepath.Join(dir, "crash_"+strconv.Itoa(pid)+".txt")
}

// startSessionRecord reports sessions that ended without a clean shutdown,
// then marks this one as running. Call once the data folder is known.
func (a *app) startSessionRecord() {
	if a.appDir == "" {
		return // unit tests
	}
	dir := filepath.Join(a.appDir, sessionsSubdir)
	if os.MkdirAll(dir, 0755) != nil {
		return
	}
	a.reportEndedSessions(dir, time.Now())

	now := time.Now()
	s := &sessionRecorder{dir: dir, stop: make(chan struct{}),
		mark: sessionMark{PID: os.Getpid(), Version: appVersion, Started: now, LastSeen: now}}
	if f, err := os.OpenFile(crashPathFor(dir, s.mark.PID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
		if debug.SetCrashOutput(f, debug.CrashOptions{}) == nil {
			s.crash = f
		} else {
			f.Close()
		}
	}
	s.beat()
	a.session = s
	go func() {
		t := time.NewTicker(sessionBeat)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.beat()
			}
		}
	}()
}

func (s *sessionRecorder) beat() {
	s.mark.LastSeen = time.Now()
	if data, err := json.Marshal(s.mark); err == nil {
		_ = writeFileAtomic(s.markPath(), data)
	}
}

// endSessionRecord is the clean shutdown: the marks of this run are removed.
func (a *app) endSessionRecord() {
	s := a.session
	if s == nil {
		return
	}
	close(s.stop)
	if s.crash != nil {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		s.crash.Close()
	}
	_ = os.Remove(crashPathFor(s.dir, s.mark.PID))
	_ = os.Remove(s.markPath())
}

// reportEndedSessions handles the marks left by processes that are gone.
func (a *app) reportEndedSessions(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	boot := bootTime(now)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "run_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		var m sessionMark
		if err != nil || json.Unmarshal(data, &m) != nil || m.PID == 0 {
			_ = os.Remove(path)
			continue
		}
		if processAliveSince(m.PID, m.Started) {
			continue // another window, or the old version during an update
		}
		crashText := ""
		cp := crashPathFor(dir, m.PID)
		if b, err := os.ReadFile(cp); err == nil {
			crashText = strings.TrimSpace(string(b))
		}
		summary, record := describeEndedSession(m, boot, crashText)
		a.postLog(summary + "\r\n")
		a.appendCrashLog(record)
		_ = os.Remove(cp)
		_ = os.Remove(path)
	}
	// crash files whose run mark is already gone (e.g. a failed write)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "crash_") {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "crash_"), ".txt"))
		if _, err := os.Stat(filepath.Join(dir, "run_"+strconv.Itoa(pid)+".json")); os.IsNotExist(err) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// describeEndedSession gives the program-log line and the crash-log record.
func describeEndedSession(m sessionMark, boot time.Time, crashText string) (string, string) {
	const layout = "2006-01-02 15:04"
	var why string
	switch {
	case crashText != "":
		why = "程式發生錯誤而結束（錯誤內容已存到「" + crashLogFileName + "」，回報時會自動附上）"
	case !boot.IsZero() && m.LastSeen.Before(boot):
		why = "電腦在程式執行中關機或重新開機（這次開機時間 " + boot.Format(layout) + "，例如 Windows 更新後自動重新開機）"
	default:
		why = "程式被結束，沒有留下錯誤訊息（可能是被工作管理員、其他程式或系統結束）"
	}
	summary := fmt.Sprintf("⚠ 上次執行（v%s，%s 開啟）沒有正常關閉，最後確認仍在執行是 %s：%s。未完成的任務可以按「繼續」接著下載。",
		m.Version, m.Started.Format(layout), m.LastSeen.Format(layout), why)
	var b strings.Builder
	b.WriteString("==== " + time.Now().Format("2006-01-02 15:04:05") + " 發現上次執行沒有正常關閉 ====\r\n")
	b.WriteString(fmt.Sprintf("版本：v%s\r\n開啟時間：%s\r\n最後確認執行中：%s\r\n",
		m.Version, m.Started.Format("2006-01-02 15:04:05"), m.LastSeen.Format("2006-01-02 15:04:05")))
	if !boot.IsZero() {
		b.WriteString("這次開機時間：" + boot.Format("2006-01-02 15:04:05") + "\r\n")
	}
	b.WriteString("判斷：" + why + "\r\n")
	if crashText != "" {
		if len(crashText) > maxCrashTextBytes {
			crashText = "…（前段省略）\n" + crashText[len(crashText)-maxCrashTextBytes:]
		}
		b.WriteString("錯誤內容：\r\n" + strings.ReplaceAll(strings.ReplaceAll(crashText, "\r\n", "\n"), "\n", "\r\n") + "\r\n")
	}
	b.WriteString("\r\n")
	return summary, b.String()
}

func (a *app) crashLogPath() string {
	dir := a.dataFolder()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, crashLogFileName)
}

func (a *app) appendCrashLog(record string) {
	path := a.crashLogPath()
	if path == "" {
		return
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxCrashLogBytes {
		_ = os.Rename(path, strings.TrimSuffix(path, ".txt")+"_"+time.Now().Format("20060102_150405")+".txt")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		_, _ = f.WriteString("\uFEFF")
	}
	_, _ = f.WriteString(record)
}

// recentCrashLog is the crash log when it changed in the last week ("" otherwise).
func (a *app) recentCrashLog() string {
	path := a.crashLogPath()
	if path == "" {
		return ""
	}
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < crashAttachMaxAge {
		return path
	}
	return ""
}

// bootTime is when Windows last started (zero when unknown).
func bootTime(now time.Time) time.Time {
	if procGetTickCount64.Find() != nil {
		return time.Time{}
	}
	ms, _, _ := procGetTickCount64.Call()
	return now.Add(-time.Duration(ms) * time.Millisecond)
}

// processAliveSince reports whether pid is still running and was created
// around started (a reused PID after a restart belongs to another program).
func processAliveSince(pid int, started time.Time) bool {
	h, err := syscall.OpenProcess(processQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if syscall.GetExitCodeProcess(h, &code) != nil || code != 259 { // STILL_ACTIVE
		return false
	}
	var created, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(h, &created, &exit, &kernel, &user) != nil {
		return true
	}
	diff := time.Unix(0, created.Nanoseconds()).Sub(started)
	return diff > -time.Minute && diff < time.Minute
}
