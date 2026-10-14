package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplyIntervalFastForFreshReports(t *testing.T) {
	t.Setenv("AIXAI_TEST_REPLY_POLL_SECONDS", "")
	now := time.Now()
	for _, tc := range []struct {
		name string
		list []replyThread
		want time.Duration
	}{
		{"no reports", nil, replyPollInterval},
		{"sent 2h ago", []replyThread{{Sent: now.Add(-2 * time.Hour)}}, replyFastInterval},
		{"sent 23h ago", []replyThread{{Sent: now.Add(-23 * time.Hour)}}, replyFastInterval},
		{"sent 25h ago", []replyThread{{Sent: now.Add(-25 * time.Hour)}}, replyPollInterval},
		{"fresh but closed", []replyThread{{Sent: now.Add(-time.Hour), Closed: true}}, replyPollInterval},
		{"one old, one fresh", []replyThread{{Sent: now.Add(-72 * time.Hour)}, {Sent: now.Add(-time.Hour)}}, replyFastInterval},
	} {
		if got := replyInterval(tc.list, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	t.Setenv("AIXAI_TEST_REPLY_POLL_SECONDS", "15")
	if got := replyInterval(nil, now); got != 15*time.Second {
		t.Errorf("test override: got %v", got)
	}
}

// Sending a report restarts the wait with the fast interval, instead of
// leaving the loop in a long wait that started before the report existed.
func TestReplyPollingKickedByNewReport(t *testing.T) {
	t.Setenv("AIXAI_TEST_REPLY_POLL_SECONDS", "")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"ok":true,"replies":{}}`))
	}))
	defer srv.Close()
	oldURL, oldFirst, oldFast, oldSlow := replyLookupURL, replyFirstPoll, replyFastInterval, replyPollInterval
	replyLookupURL, replyFirstPoll, replyFastInterval, replyPollInterval = srv.URL, 10*time.Millisecond, 300*time.Millisecond, time.Hour
	replyPollOnce = sync.Once{}
	defer func() {
		replyLookupURL, replyFirstPoll, replyFastInterval, replyPollInterval = oldURL, oldFirst, oldFast, oldSlow
		replyPollOnce = sync.Once{}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &app{appShared: &appShared{appDir: t.TempDir(), rootCtx: ctx}}
	a.startReplyPolling()
	time.Sleep(200 * time.Millisecond) // first poll done; no reports, so now waiting the slow hour
	if n := hits.Load(); n != 0 {
		t.Fatalf("polled %d times without reports", n)
	}
	a.recordReplyThread("0123456789abcdef01234567", "test", "")
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := hits.Load(); n < 2 {
		t.Fatalf("after sending a report: polled %d times in 3s, want hourly (here 300ms) polling", n)
	}
}
