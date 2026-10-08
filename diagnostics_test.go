package main

import (
	"errors"
	"strings"
	"testing"
)

func TestDiagTrail(t *testing.T) {
	a := &app{}
	a.diagStartItem(2, 5, "https://example.com/play/3")
	a.diagRecord("yt-dlp", errors.New("ERROR: Unsupported URL: https://example.com/play/3"))
	a.diagBegin("瀏覽器擷取")
	a.diagNote("擷取到 2 個串流")
	a.diagNote("檔案檢查 ✗ 長度只有 4.0 秒")
	a.diagEnd(&mediaCheckError{File: "x.mp4", Reason: "長度只有 4.0 秒"})
	d := a.diag
	if len(d.Steps) != 2 || d.Steps[0].Mark != "✗" || d.Steps[1].Mark != "✗" || len(d.Steps[1].Notes) != 2 {
		t.Fatalf("trail: %+v", d.Steps)
	}
	block := d.block(errors.New("所有方式都失敗"))
	for _, want := range []string{diagBlockStart, "第 3/5 項", "1. yt-dlp ✗", "2. 瀏覽器擷取 ✗", "· 擷取到 2 個串流", "結果：失敗", diagBlockEnd} {
		if !strings.Contains(block, want) {
			t.Fatalf("block lacks %q:\n%s", want, block)
		}
	}

	a.diagStartItem(0, 1, "https://example.com/v")
	a.diagRecord("yt-dlp", nil)
	a.diagNote("檔案檢查 ✓ 1920×1080")
	if c := a.diag.compact(); c != "yt-dlp ✓（檔案檢查 ✓ 1920×1080）" {
		t.Fatalf("compact: %q", c)
	}
}

func TestDiagMediaURL(t *testing.T) {
	got := diagMediaURL("https://cdn.example.com/v/7/index.m3u8?sign=SECRET123&t=999")
	if strings.Contains(got, "SECRET123") || strings.Contains(got, "999") || !strings.HasPrefix(got, "cdn.example.com/v/7/index.m3u8?") {
		t.Fatalf("query values must be dropped: %q", got)
	}
}

func TestReportFromLog(t *testing.T) {
	log := strings.Join([]string{
		"[2026-10-08 10:00:00] --- 任務 1/2 ---",
		"[2026-10-08 10:00:01] [download]  42.0% of 10MiB",
		"[2026-10-08 10:00:02] 診斷：yt-dlp ✓（檔案檢查 ✓ 1920×1080）",
		"[2026-10-08 10:00:03] === 診斷摘要（第 2/2 項）===",
		"[2026-10-08 10:00:03] 網址：https://example.com/2",
		"[2026-10-08 10:00:03] 1. 瀏覽器擷取 ✗：下載的檔案未通過檢查",
		"[2026-10-08 10:00:03] · 檔案檢查 ✗ 長度只有 4.0 秒",
		"[2026-10-08 10:00:03] 結果：失敗 — x",
		"[2026-10-08 10:00:03] " + diagBlockEnd,
		"[2026-10-08 10:00:04] 錯誤：第 2 個任務失敗",
	}, "\r\n")
	diag, tail := reportFromLog(log, 3)
	joined := strings.Join(diag, "\n")
	for _, want := range []string{"診斷：yt-dlp ✓", "=== 診斷摘要（第 2/2 項）===", "1. 瀏覽器擷取 ✗", "   · 檔案檢查 ✗", diagBlockEnd} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diag lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "[2026") {
		t.Fatalf("time stamps must be removed from the summary:\n%s", joined)
	}
	if len(tail) != 3 || !strings.Contains(tail[2], "錯誤：") {
		t.Fatalf("tail: %v", tail)
	}
	for _, l := range tail {
		if strings.Contains(l, "42.0%") {
			t.Fatal("progress lines must not be in the tail")
		}
	}
}
