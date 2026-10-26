//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDescribeEndedSession(t *testing.T) {
	boot := time.Date(2026, 10, 26, 7, 50, 0, 0, time.Local)
	m := sessionMark{PID: 1, Version: "4.2.22", Started: boot.Add(-5 * time.Hour), LastSeen: boot.Add(-time.Hour)}
	if s, _ := describeEndedSession(m, boot, ""); !strings.Contains(s, "關機或重新開機") {
		t.Fatalf("reboot: %s", s)
	}
	m.LastSeen = boot.Add(time.Hour)
	if s, _ := describeEndedSession(m, boot, ""); !strings.Contains(s, "沒有留下錯誤訊息") {
		t.Fatalf("killed: %s", s)
	}
	s, rec := describeEndedSession(m, boot, "fatal error: concurrent map writes\ngoroutine 1")
	if !strings.Contains(s, "程式發生錯誤") || !strings.Contains(rec, "concurrent map writes\r\ngoroutine 1") {
		t.Fatalf("crash: %s / %s", s, rec)
	}
}

func TestSessionRecordLifecycle(t *testing.T) {
	a := &app{appShared: &appShared{appDir: t.TempDir()}}
	a.main = a
	a.setDataFolderPref(filepath.Join(a.appDir, "data"))
	dir := filepath.Join(a.appDir, sessionsSubdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	// 已結束的程序留下的標記與當機訊息（PID 不存在）
	dead := sessionMark{PID: 999999, Version: "4.2.22", Started: time.Now().Add(-time.Hour), LastSeen: time.Now()}
	data, _ := json.Marshal(dead)
	os.WriteFile(filepath.Join(dir, "run_999999.json"), data, 0644)
	os.WriteFile(crashPathFor(dir, 999999), []byte("panic: boom"), 0644)
	// 仍在執行的程序（自己）不能被當成已結束
	self := sessionMark{PID: os.Getpid(), Version: "x", Started: testStart, LastSeen: time.Now()}
	data, _ = json.Marshal(self)
	os.WriteFile(filepath.Join(dir, "run_self.json"), data, 0644)

	a.startSessionRecord()
	if a.session == nil {
		t.Fatal("no session")
	}
	log, err := os.ReadFile(filepath.Join(a.appDir, "data", crashLogFileName))
	if err != nil || !strings.Contains(string(log), "panic: boom") {
		t.Fatalf("crash log: %v %q", err, log)
	}
	if _, err := os.Stat(filepath.Join(dir, "run_999999.json")); !os.IsNotExist(err) {
		t.Fatal("dead mark kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "run_self.json")); err != nil {
		t.Fatal("live mark removed")
	}
	if a.recentCrashLog() == "" {
		t.Fatal("crash log not offered for reports")
	}
	mine := a.session.markPath()
	if _, err := os.Stat(mine); err != nil {
		t.Fatal("own mark missing")
	}
	a.endSessionRecord()
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("own mark kept after clean exit")
	}
	if _, err := os.Stat(crashPathFor(dir, os.Getpid())); !os.IsNotExist(err) {
		t.Fatal("own crash file kept after clean exit")
	}
}

// testStart 近似測試程序的建立時間（容許 1 分鐘）
var testStart = time.Now()
