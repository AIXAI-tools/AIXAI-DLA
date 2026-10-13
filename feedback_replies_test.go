package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testThreadA = "aaaaaaaaaaaaaaaaaaaaaaaa"
	testThreadB = "bbbbbbbbbbbbbbbbbbbbbbbb"
)

func testReplyApp(t *testing.T) *app {
	t.Helper()
	return &app{appShared: &appShared{appDir: t.TempDir()}}
}

func TestReplyNoticesKinds(t *testing.T) {
	sent := time.Date(2026, 10, 14, 1, 14, 0, 0, time.Local)
	list := []replyThread{{
		ID: testThreadA, Title: "影片打不開", Sent: sent,
		Replies: []replyItem{
			{RID: "r1", Kind: "fixed", Version: "v4.1.9", Text: "已修正"},
			{RID: "r2", Kind: "info", Text: "請問儲存位置？"},
		},
		Seen: []string{"r2"},
	}}
	got := replyNotices(list, "4.1.8")
	if len(got) != 1 || got[0].Reply.RID != "r1" || got[0].Stage != "new" || !got[0].Newer || got[0].Sent != "10/14 01:14" {
		t.Fatalf("notices = %+v", got)
	}
	if got := replyNotices(list, "4.1.9"); len(got) != 1 || got[0].Newer {
		t.Fatalf("on the fixed version: %+v", got)
	}
}

func TestReplyNoticesTestFollowup(t *testing.T) {
	th := replyThread{ID: testThreadA, Replies: []replyItem{{RID: "t1", Kind: "test", Version: "v4.2.0-beta.1", Text: "請試試"}}}
	if got := replyNotices([]replyThread{th}, "4.1.9"); len(got) != 1 || got[0].Stage != "new" || !got[0].Newer {
		t.Fatalf("before install: %+v", got)
	}
	th.Seen = []string{"t1"}
	if got := replyNotices([]replyThread{th}, "4.1.9"); len(got) != 0 {
		t.Fatalf("seen, not installed yet: %+v", got)
	}
	got := replyNotices([]replyThread{th}, "4.2.0-beta.1")
	if len(got) != 1 || got[0].Stage != "testFollowup" {
		t.Fatalf("running the test build: %+v", got)
	}
	th.TestResult = "OK"
	if got := replyNotices([]replyThread{th}, "4.2.0-beta.1"); len(got) != 0 {
		t.Fatalf("answered: %+v", got)
	}
}

func TestPruneReplyThreads(t *testing.T) {
	now := time.Now()
	list := []replyThread{
		{ID: testThreadA, LastActive: now.Add(-61 * 24 * time.Hour)},
		{ID: testThreadB, LastActive: now.Add(-time.Hour)},
	}
	got := pruneReplyThreads(list, now)
	if len(got) != 1 || got[0].ID != testThreadB {
		t.Fatalf("pruned = %+v", got)
	}
}

func TestSanitizeReplies(t *testing.T) {
	got := sanitizeReplies([]replyItem{
		{RID: "a", Kind: "test", Text: "x", URLs: []string{"https://example.com/v", "javascript:alert(1)", "file:///c:/x"}},
		{RID: "b", Kind: "run", Text: "x"},
		{RID: "", Kind: "info", Text: "x"},
		{RID: "c", Kind: "info", Text: "  "},
	})
	if len(got) != 1 || got[0].RID != "a" || len(got[0].URLs) != 1 || got[0].URLs[0] != "https://example.com/v" {
		t.Fatalf("sanitized = %+v", got)
	}
}

func TestPollRepliesOnce(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("ids")
		if r.URL.Query().Get("action") != "replies" || r.URL.Query().Get("tool") != updateRepoName {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "replies": map[string][]replyItem{
			testThreadA: {{RID: "r1", Kind: "fixed", Version: "v9.9.9", Text: "已修正，謝謝回報"}},
		}})
	}))
	defer srv.Close()
	old := replyLookupURL
	replyLookupURL = srv.URL
	defer func() { replyLookupURL = old }()

	a := testReplyApp(t)
	a.recordReplyThread(testThreadA, "影片打不開", "")
	a.recordReplyThread(testThreadB, "另一個", "")
	a.recordReplyThread("not-an-id", "x", "")
	if !a.pollRepliesOnce(context.Background()) {
		t.Fatal("first poll should report a new reply")
	}
	if !strings.Contains(asked, testThreadA) || !strings.Contains(asked, testThreadB) {
		t.Fatalf("asked ids = %q", asked)
	}
	if a.pollRepliesOnce(context.Background()) {
		t.Fatal("second poll has nothing new")
	}
	notices := replyNotices(a.loadReplyThreads(), appVersion)
	if len(notices) != 1 || notices[0].Reply.Text != "已修正，謝謝回報" {
		t.Fatalf("notices = %+v", notices)
	}

	// Seeing a fixed reply closes the thread: it is no longer asked about.
	a.replyAction(testThreadA, "r1", "ack")
	a.pollRepliesOnce(context.Background())
	if strings.Contains(asked, testThreadA) || !strings.Contains(asked, testThreadB) {
		t.Fatalf("after ack asked ids = %q", asked)
	}
	if n := replyNotices(a.loadReplyThreads(), appVersion); len(n) != 0 {
		t.Fatalf("after ack notices = %+v", n)
	}
}

func TestPollRepliesFailureIsQuiet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	old := replyLookupURL
	replyLookupURL = srv.URL
	defer func() { replyLookupURL = old }()

	a := testReplyApp(t)
	a.recordReplyThread(testThreadA, "x", "")
	if a.pollRepliesOnce(context.Background()) {
		t.Fatal("a failed lookup reports nothing")
	}
	if len(a.loadReplyThreads()) != 1 {
		t.Fatal("thread must be kept after a failed lookup")
	}
}

func TestPollRepliesSkipsWithoutThreads(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	old := replyLookupURL
	replyLookupURL = srv.URL
	defer func() { replyLookupURL = old }()

	if testReplyApp(t).pollRepliesOnce(context.Background()) || called {
		t.Fatal("nothing to ask: no connection")
	}
}

func TestReplyTitle(t *testing.T) {
	if got := replyTitle("\n  附的圖檔打不開 \n細節"); got != "附的圖檔打不開" {
		t.Fatalf("title = %q", got)
	}
	if got := replyTitle(strings.Repeat("字", 70)); len([]rune(got)) != 61 {
		t.Fatalf("long title = %q", got)
	}
}
