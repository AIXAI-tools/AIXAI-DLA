//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupStreamsIDs(t *testing.T) {
	cases := []struct {
		in, ep, aid string
		ok          bool
	}{
		{"https://www.bilibili.tv/en/video/123456789", "", "123456789", true},
		{"https://www.bilibili.tv/video/123", "", "123", true},
		{"https://www.bilibili.tv/th/play/34613/341736", "341736", "", true},
		{"https://www.biliintl.com/en/play/1/2", "2", "", true},
		{"https://www.bilibili.com/video/BV1xx411c7mD", "", "", false},
		{"https://example.com/video/1", "", "", false},
	}
	for _, c := range cases {
		ep, aid, ok := backupStreamsIDs(c.in)
		if ep != c.ep || aid != c.aid || ok != c.ok {
			t.Errorf("%s → %q %q %v", c.in, ep, aid, ok)
		}
	}
}

func TestPageServed(t *testing.T) {
	if pageServed(map[string]interface{}{"id": "123456789", "extractor": "BiliIntl", "title": "BiliIntl video #123456789"}) {
		t.Error("blank page (fallback title) counted as served")
	}
	if pageServed(map[string]interface{}{"id": "1", "title": ""}) {
		t.Error("empty title counted as served")
	}
	if !pageServed(map[string]interface{}{"id": "123456789", "extractor": "BiliIntl", "title": "Example Title"}) {
		t.Error("real title not counted as served")
	}
}

func TestIsHTTPForbidden(t *testing.T) {
	if !isHTTPForbidden(errors.New("ERROR: unable to download video data: HTTP Error 403: Forbidden")) {
		t.Error("403 not detected")
	}
	if isHTTPForbidden(errors.New("ERROR: HTTP Error 404: Not Found")) || isHTTPForbidden(nil) {
		t.Error("non-403 detected")
	}
}

const samplePlayData = `{"code":0,"data":{"playurl":{"video":[
 {"video_resource":{"url":"","backup_url":null,"height":2160},"stream_info":{"desc_words":"4K"}},
 {"video_resource":{"url":"https://main/v1270","backup_url":["https://backup/v1270"],"width":720,"height":1270,"bandwidth":900,"codecs":"hev1.1.6.L120.90","size":100},"stream_info":{"desc_words":"720P"}},
 {"video_resource":{"url":"https://main/v846","backup_url":[],"height":846},"stream_info":{"desc_words":"480P"}}],
 "audio_resource":[{"url":"https://main/a","backup_url":["https://backup/a"],"bandwidth":128,"codecs":"mp4a.40.2","size":10}]}}}`

func TestBackupFormatsUseBackupAddresses(t *testing.T) {
	var pd backupPlayData
	if err := json.Unmarshal([]byte(samplePlayData), &pd); err != nil {
		t.Fatal(err)
	}
	f := backupFormats(pd)
	if len(f) != 2 {
		t.Fatalf("formats = %v", f)
	}
	if f[0]["url"] != "https://backup/v1270" || f[0]["vcodec"] != "hev1.1.6.L120.90" || f[0]["acodec"] != "none" || f[0]["height"] != 1270 {
		t.Errorf("video = %v", f[0])
	}
	if f[1]["url"] != "https://backup/a" || f[1]["vcodec"] != "none" {
		t.Errorf("audio = %v", f[1])
	}

	page := map[string]interface{}{"id": "1", "title": "T", "extractor": "BiliIntl",
		"formats": []interface{}{"old"}, "requested_formats": []interface{}{"old"}, "format_id": "12+2", "url": "https://main/x"}
	info := backupInfo(page, f)
	if info["_type"] != "video" || info["title"] != "T" {
		t.Errorf("info = %v", info)
	}
	for _, k := range []string{"requested_formats", "format_id", "url"} {
		if _, ok := info[k]; ok {
			t.Errorf("info keeps %s", k)
		}
	}
}

func TestWithInfoJSON(t *testing.T) {
	args := []string{"--ignore-config", "-f", "bv*+ba/b", "https://www.bilibili.tv/en/video/1"}
	got := withInfoJSON(args, `C:\x.info.json`)
	want := []string{"--ignore-config", "-f", "bv*+ba/b", "--load-info-json", `C:\x.info.json`}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	if args[len(args)-1] != "https://www.bilibili.tv/en/video/1" {
		t.Error("input args changed")
	}
}

func TestClearOldInfoFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "backup-1.info.json")
	fresh := filepath.Join(dir, "backup-2.info.json")
	other := filepath.Join(dir, "keep.txt")
	for _, p := range []string{old, fresh, other} {
		if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-7 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(other, past, past)
	clearOldInfoFiles(dir, 6*time.Hour)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old info file kept")
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed", p)
		}
	}
}

func TestNotServedMessages(t *testing.T) {
	raw := "WARNING: [BiliIntl] unable to extract og:title\nERROR: unable to download video data: HTTP Error 403: Forbidden"
	if got := errorLine(raw); got != "ERROR: unable to download video data: HTTP Error 403: Forbidden" {
		t.Errorf("errorLine = %q", got)
	}
	summary := errBackupNotServed.Error() + "。\n\n" + errorLine(raw)
	if got := errorBrief(summary); got != "這部影片在你所在的地區無法觀看" {
		t.Errorf("brief = %q", got)
	}
	if got := diagErrorText(&commandRunError{Summary: raw}); got != "ERROR: unable to download video data: HTTP Error 403: Forbidden" {
		t.Errorf("diag = %q", got)
	}
}
