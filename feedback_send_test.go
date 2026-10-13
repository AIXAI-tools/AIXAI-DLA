package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// relayStub fails the first `fail` requests (dropped connection, or HTTP
// 503 when status5xx) and then answers ok; it records the report IDs.
func relayStub(t *testing.T, fail int, status5xx bool) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var up feedbackUpload
		_ = json.Unmarshal(raw, &up)
		mu.Lock()
		ids = append(ids, up.ID)
		n := len(ids)
		mu.Unlock()
		if n <= fail {
			if status5xx {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close() // client sees unexpected EOF
			}
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &ids
}

func withRelay(t *testing.T, url string) {
	t.Helper()
	oldURL, oldDelay, oldTimeout := feedbackSendURL, feedbackRetryDelay, feedbackAttemptTimeout
	feedbackSendURL, feedbackRetryDelay, feedbackAttemptTimeout = url, 0, 5*time.Second
	t.Cleanup(func() { feedbackSendURL, feedbackRetryDelay, feedbackAttemptTimeout = oldURL, oldDelay, oldTimeout })
}

func TestSendFeedbackDirectRetriesDroppedConnection(t *testing.T) {
	for _, status5xx := range []bool{false, true} {
		srv, ids := relayStub(t, 2, status5xx)
		withRelay(t, srv.URL)
		if err := (&app{}).sendFeedbackDirect("t", "body", nil); err != nil {
			t.Fatalf("5xx=%v: want success on third attempt, got %v", status5xx, err)
		}
		if len(*ids) != 3 || (*ids)[0] == "" || (*ids)[0] != (*ids)[1] || (*ids)[1] != (*ids)[2] {
			t.Fatalf("5xx=%v: want 3 attempts with one report ID, got %q", status5xx, *ids)
		}
	}
}

func TestSendFeedbackDirectGivesUp(t *testing.T) {
	srv, ids := relayStub(t, 99, false)
	withRelay(t, srv.URL)
	err := (&app{}).sendFeedbackDirect("t", "body", nil)
	if err == nil || !strings.Contains(err.Error(), "無法連到回報服務") {
		t.Fatalf("want connection error, got %v", err)
	}
	if len(*ids) != feedbackAttempts {
		t.Fatalf("want %d attempts, got %d", feedbackAttempts, len(*ids))
	}
}

func TestSendFeedbackDirectNoRetryOnRelayError(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"ok":false,"error":"daily_limit"}`))
	}))
	defer srv.Close()
	withRelay(t, srv.URL)
	err := (&app{}).sendFeedbackDirect("t", "body", nil)
	if err == nil || err.Error() != feedbackSendErrors["daily_limit"] || n != 1 {
		t.Fatalf("want one attempt and the daily-limit message, got %d attempts, %v", n, err)
	}
}
