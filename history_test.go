//go:build windows

package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestURLsInText(t *testing.T) {
	csvText := "時間,結果,標題,網址\r\n2026-10-10,成功,\"a, b\",https://example.com/v/1\r\n" +
		"2026-10-10,失敗,x,https://example.com/v/2?x=1&y=2\r\nhttps://example.com/v/1\r\n"
	got := urlsInText(csvText)
	want := []string{"https://example.com/v/1", "https://example.com/v/2?x=1&y=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := urlsInText("沒有網址"); len(got) != 0 {
		t.Errorf("expected none, got %v", got)
	}
}

func TestDecodeTextFile(t *testing.T) {
	bom := append([]byte{0xEF, 0xBB, 0xBF}, []byte("https://example.com/a")...)
	if got := decodeTextFile(bom); got != "https://example.com/a" {
		t.Errorf("utf-8 bom: %q", got)
	}
	le := []byte{0xFF, 0xFE}
	for _, r := range "網址 https://e.com/x" {
		le = append(le, byte(r), byte(r>>8))
	}
	if got := decodeTextFile(le); got != "網址 https://e.com/x" {
		t.Errorf("utf-16le: %q", got)
	}
}

func TestHistoryCSV(t *testing.T) {
	when := time.Date(2026, 10, 10, 13, 4, 33, 0, time.Local)
	data := historyCSV([]historyEntry{
		{Time: when, URL: "https://example.com/v/1", State: "done", Title: "影片, 一", File: `C:\d\影片.mp4`},
		{Time: when, URL: "https://example.com/v/2", State: "failed", Folder: `C:\d`, Reason: "無法解析"},
		{Time: when, URL: "https://example.com/v/3", State: "failed", Folder: `C:\d`, Reason: "ERROR: HTTP Error 404: Not Found"},
	})
	if !bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("CSV must start with a UTF-8 BOM for Excel")
	}
	text := string(data)
	for _, want := range []string{
		"時間,結果,標題,網址,存放位置,失敗說明,原始錯誤訊息\r\n",
		"2026-10-10 13:04:33,成功,\"影片, 一\",https://example.com/v/1,C:\\d\\影片.mp4,,\r\n",
		"2026-10-10 13:04:33,失敗,,https://example.com/v/2,C:\\d,,無法解析\r\n",
		"2026-10-10 13:04:33,失敗,,https://example.com/v/3,C:\\d,找不到影片（404）：可能已刪除，或網址錯誤,ERROR: HTTP Error 404: Not Found\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("CSV missing %q in:\n%s", want, text)
		}
	}
}

func TestURLLinesDedup(t *testing.T) {
	data := urlLines([]string{" https://e.com/1 ", "", "https://e.com/2", "https://e.com/1"})
	if got := string(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})); got != "https://e.com/1\r\nhttps://e.com/2\r\n" {
		t.Errorf("got %q", got)
	}
}
