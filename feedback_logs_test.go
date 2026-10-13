//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A report sent while another task is still running must also carry the
// newest failed run log, not only the newest (running) one.
func TestLatestFeedbackLogsAddsFailedRun(t *testing.T) {
	tmp := t.TempDir()
	a := &app{appShared: &appShared{appDir: tmp}}
	a.dataFolderPref.Store(tmp)
	dir := filepath.Join(tmp, runLogsSubdir, "2026-10")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(name, text string, age time.Duration) {
		p := filepath.Join(dir, runLogPrefix+name+".txt")
		if err := os.WriteFile(p, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	write("20261013_200000", "[2026-10-13 20:00:00] 錯誤：第 1 個任務失敗：HTTP Error 403\r\n", 3*time.Hour)
	write("20261013_211500", "[2026-10-13 21:15:00] 結果：失敗 — HTTP Error 403\r\n", 10*time.Minute)
	write("20261013_212119", "[2026-10-13 21:21:19] [download] Destination: a.mp4\r\n", time.Minute)

	names := func(ls []feedbackLog) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Name)
		}
		return out
	}
	got := names(a.latestFeedbackLogs())
	want := []string{runLogPrefix + "20261013_212119.txt", runLogPrefix + "20261013_211500.txt"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("logs = %v, want %v", got, want)
	}

	// The newest log failed itself: it alone is enough.
	write("20261013_213000", "[2026-10-13 21:30:00] ⚠ 第 2/5 項失敗：HTTP Error 403；跳過此項，繼續下一項。\r\n", 0)
	got = names(a.latestFeedbackLogs())
	if len(got) != 1 || got[0] != runLogPrefix+"20261013_213000.txt" {
		t.Fatalf("logs = %v, want only the newest", got)
	}
}

func TestLatestFeedbackLogsNoLogs(t *testing.T) {
	tmp := t.TempDir()
	a := &app{appShared: &appShared{appDir: tmp}}
	a.dataFolderPref.Store(tmp)
	if got := a.latestFeedbackLogs(); len(got) != 0 {
		t.Fatalf("logs = %v, want none", got)
	}
}
