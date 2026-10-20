package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Replies to reports: when the user sends a report with 直接送出 and keeps
// 「接收開發者的回覆」 checked, the report's random ID is kept here. While the
// program runs it asks the relay for replies to those IDs (only the IDs are
// sent; nothing else) at startup and every few hours, so a window that stays
// open for days still gets them. A reply is one of:
//   fixed   — fixed in a version: update, or retry the URLs that failed;
//   test    — please try a test build: install it, then tell how it went;
//   info    — the developer needs more information: reply with a new report;
//   wontfix — cannot be handled (reason in the text).
// Reply text is shown as plain text; buttons only map to fixed actions.

const (
	replyFileName   = "feedback_replies.json"
	replyFastWindow = 24 * time.Hour // a report counts as fresh this long after sending
	replyKeepDays   = 60
	replyMaxThreads = 20 // the relay answers at most 20 IDs per request
	replyMaxURLs    = 10
)

// Variables only so tests can shorten them.
var (
	replyPollInterval = 6 * time.Hour
	replyFastInterval = time.Hour // while a report is fresh
	replyFirstPoll    = 20 * time.Second
)

var (
	replyPollOnce sync.Once
	// replyKick wakes the polling loop when a new report is sent, so the
	// fast interval starts right away instead of after the current wait.
	replyKick = make(chan struct{}, 1)
	replyMu   sync.Mutex
	replyIDRE = regexp.MustCompile(`^[a-f0-9]{24}$`)
	// Variable so tests can point it at a local server.
	replyLookupURL = feedbackEndpoint
)

type replyItem struct {
	RID     string   `json:"rid"`
	Kind    string   `json:"kind"`
	Version string   `json:"version"`
	Text    string   `json:"text"`
	URLs    []string `json:"urls"`
	Created string   `json:"created"`
}

// replyThread is one sent report that waits for replies.
type replyThread struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Sent       time.Time   `json:"sent"`
	Version    string      `json:"version"`
	URLs       []string    `json:"urls,omitempty"` // URLs that had failed when the report was sent (kept locally only)
	Ref        string      `json:"ref,omitempty"`
	Seen       []string    `json:"seen,omitempty"` // reply IDs the user has acted on
	TestResult string      `json:"testResult,omitempty"`
	Closed     bool        `json:"closed,omitempty"`
	Replies    []replyItem `json:"replies,omitempty"`
	LastActive time.Time   `json:"lastActive"`
}

// replyNotice is one reply the page should show.
type replyNotice struct {
	Thread  string    `json:"thread"`
	Title   string    `json:"title"`
	Sent    string    `json:"sent"`
	URLs    []string  `json:"urls"`
	Reply   replyItem `json:"reply"`
	Stage   string    `json:"stage"` // new | testFollowup
	Current string    `json:"current"`
	Newer   bool      `json:"newer"` // reply.Version is newer than this program
}

func (a *app) replyPath() string { return filepath.Join(a.appDir, replyFileName) }

func (a *app) loadReplyThreads() []replyThread {
	data, err := os.ReadFile(a.replyPath())
	if err != nil {
		return nil
	}
	var list []replyThread
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	return list
}

func (a *app) saveReplyThreads(list []replyThread) {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	a.notePersistError("回報回覆紀錄", writeFileAtomic(a.replyPath(), data))
}

// pruneReplyThreads drops threads idle for replyKeepDays and keeps the newest.
func pruneReplyThreads(list []replyThread, now time.Time) []replyThread {
	out := list[:0]
	for _, t := range list {
		if now.Sub(t.LastActive) <= replyKeepDays*24*time.Hour {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastActive.After(out[j].LastActive) })
	if len(out) > replyMaxThreads {
		out = out[:replyMaxThreads]
	}
	return out
}

// recordReplyThread remembers a report sent with 接收回覆 checked.
func (a *app) recordReplyThread(id, title, ref string) {
	if !replyIDRE.MatchString(id) {
		return
	}
	var urls []string
	for _, f := range a.failedViews() {
		if len(urls) >= replyMaxURLs {
			break
		}
		urls = append(urls, f.URL)
	}
	now := time.Now()
	replyMu.Lock()
	defer replyMu.Unlock()
	list := a.loadReplyThreads()
	list = append(list, replyThread{ID: id, Title: title, Sent: now, Version: appVersion, URLs: urls, Ref: ref, LastActive: now})
	a.saveReplyThreads(pruneReplyThreads(list, now))
	select {
	case replyKick <- struct{}{}:
	default:
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// replyNotices lists what the page should show: unseen replies, and a
// follow-up for a test build the user is now running.
func replyNotices(list []replyThread, current string) []replyNotice {
	var out []replyNotice
	for _, t := range list {
		base := replyNotice{Thread: t.ID, Title: t.Title, Sent: t.Sent.Format("01/02 15:04"), URLs: t.URLs, Current: current}
		for _, r := range t.Replies {
			if containsString(t.Seen, r.RID) {
				continue
			}
			n := base
			n.Reply, n.Stage = r, "new"
			n.Newer = r.Version != "" && compareVersions(r.Version, current) > 0
			out = append(out, n)
		}
		if k := len(t.Replies); k > 0 && !t.Closed && t.TestResult == "" {
			last := t.Replies[k-1]
			if last.Kind == "test" && containsString(t.Seen, last.RID) && last.Version != "" && compareVersions(last.Version, current) == 0 {
				n := base
				n.Reply, n.Stage = last, "testFollowup"
				out = append(out, n)
			}
		}
	}
	return out
}

func (a *app) emitReplyNotices() {
	replyMu.Lock()
	notices := replyNotices(a.loadReplyThreads(), appVersion)
	replyMu.Unlock()
	if len(notices) == 0 {
		return
	}
	a.emit("replies", notices)
	a.flashMainWindow()
}

func (a *app) flashMainWindow() {
	if a.ui == nil || a.ui.hwnd == 0 {
		return
	}
	fi := flashWInfo{CbSize: uint32(unsafe.Sizeof(flashWInfo{})), Hwnd: a.ui.hwnd, DwFlags: flashwAll | flashwTimerNoF}
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

// replyInterval is how long to wait before the next poll: replyFastInterval
// while an open report was sent within replyFastWindow, else
// replyPollInterval; AIXAI_TEST_REPLY_POLL_SECONDS (at least 10) overrides
// both when testing that a running program picks up replies.
func replyInterval(list []replyThread, now time.Time) time.Duration {
	if n, err := strconv.Atoi(os.Getenv("AIXAI_TEST_REPLY_POLL_SECONDS")); err == nil && n >= 10 {
		return time.Duration(n) * time.Second
	}
	for _, t := range list {
		if !t.Closed && now.Sub(t.Sent) < replyFastWindow {
			return replyFastInterval
		}
	}
	return replyPollInterval
}

// startReplyPolling runs for the life of the program.
func (a *app) startReplyPolling() {
	replyPollOnce.Do(func() {
		go func() {
			select {
			case <-a.rootCtx.Done():
				return
			case <-time.After(replyFirstPoll):
			}
			a.emitReplyNotices() // replies fetched earlier but not acted on yet
			for {
				if a.pollRepliesOnce(a.rootCtx) {
					a.emitReplyNotices()
				}
			wait:
				for {
					replyMu.Lock()
					interval := replyInterval(a.loadReplyThreads(), time.Now())
					replyMu.Unlock()
					select {
					case <-a.rootCtx.Done():
						return
					case <-replyKick:
						// 剛送出回報：重新計時，從現在起每小時查一次
					case <-time.After(interval):
						break wait
					}
				}
			}
		}()
	})
}

// pendingReplyIDs are the threads still waiting for something.
func pendingReplyIDs(list []replyThread) []string {
	var ids []string
	for _, t := range list {
		if !t.Closed && replyIDRE.MatchString(t.ID) {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

// pollRepliesOnce asks the relay once; true when a new reply arrived.
// Failures are silent: the relay sometimes hangs, and the next poll retries.
func (a *app) pollRepliesOnce(ctx context.Context) bool {
	replyMu.Lock()
	list := pruneReplyThreads(a.loadReplyThreads(), time.Now())
	ids := pendingReplyIDs(list)
	replyMu.Unlock()
	if len(ids) == 0 || feedbackEndpoint == "" {
		return false
	}
	got, err := fetchReplies(ctx, ids)
	if err != nil {
		return false
	}
	replyMu.Lock()
	defer replyMu.Unlock()
	list = a.loadReplyThreads()
	changed := false
	for i := range list {
		items, ok := got[list[i].ID]
		if !ok {
			continue
		}
		for _, r := range items {
			if !replyKnown(list[i].Replies, r.RID) {
				changed = true
				list[i].LastActive = time.Now()
				list[i].TestResult = "" // a new reply (e.g. another test build) asks again
			}
		}
		list[i].Replies = sanitizeReplies(items)
	}
	if changed {
		a.saveReplyThreads(list)
	}
	return changed
}

func replyKnown(items []replyItem, rid string) bool {
	for _, r := range items {
		if r.RID == rid {
			return true
		}
	}
	return false
}

var replyKinds = map[string]bool{"fixed": true, "test": true, "info": true, "wontfix": true}

// sanitizeReplies keeps only well-formed replies with http(s) URLs.
func sanitizeReplies(items []replyItem) []replyItem {
	var out []replyItem
	for _, r := range items {
		if r.RID == "" || !replyKinds[r.Kind] || strings.TrimSpace(r.Text) == "" {
			continue
		}
		if len(r.Text) > 4000 {
			r.Text, _ = truncateUTF8(r.Text, 4000)
		}
		var urls []string
		for _, u := range r.URLs {
			if p, err := url.Parse(u); err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != "" && len(urls) < replyMaxURLs {
				urls = append(urls, u)
			}
		}
		r.URLs = urls
		out = append(out, r)
	}
	return out
}

func fetchReplies(ctx context.Context, ids []string) (map[string][]replyItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	q := url.Values{"action": {"replies"}, "tool": {updateRepoName}, "ids": {strings.Join(ids, ",")}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, replyLookupURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", appTitle+"/"+appVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	var res struct {
		OK      bool                   `json:"ok"`
		Error   string                 `json:"error"`
		Replies map[string][]replyItem `json:"replies"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &res) != nil || !res.OK {
		return nil, errors.New("reply lookup failed")
	}
	return res.Replies, nil
}

// replyTitle is the first line of the report text, short enough for a heading.
func replyTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r := []rune(line)
			if len(r) > 60 {
				return string(r[:60]) + "…"
			}
			return line
		}
	}
	return "（無標題）"
}

// replyAction records what the user did with a notice.
//
//	ack       — seen (fixed / wontfix close the thread);
//	testOK / testFail — answer to the test follow-up;
//	later     — nothing is stored (shown again next time).
func (a *app) replyAction(thread, rid, action string) {
	if action == "later" {
		return
	}
	replyMu.Lock()
	defer replyMu.Unlock()
	list := a.loadReplyThreads()
	for i := range list {
		t := &list[i]
		if t.ID != thread {
			continue
		}
		t.LastActive = time.Now()
		switch action {
		case "ack":
			if rid != "" && !containsString(t.Seen, rid) {
				t.Seen = append(t.Seen, rid)
			}
			for _, r := range t.Replies {
				if r.RID == rid && (r.Kind == "fixed" || r.Kind == "wontfix") {
					t.Closed = true
				}
			}
		case "testOK", "testFail":
			t.TestResult = strings.TrimPrefix(action, "test")
			if action == "testOK" {
				t.Closed = true
			}
		}
		a.saveReplyThreads(list)
		return
	}
}
