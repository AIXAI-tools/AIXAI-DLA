package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// rangeServer serves data with Range support; failFirst makes the first
// request of every piece break off halfway, to exercise the retries.
func rangeServer(t *testing.T, data []byte, ranges bool, failFirst bool) (*httptest.Server, *atomic.Int64) {
	var maxConc, cur atomic.Int64
	var mu sync.Mutex
	failed := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://example.com/page" {
			http.Error(w, "no referer", http.StatusForbidden)
			return
		}
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			m := maxConc.Load()
			if n <= m || maxConc.CompareAndSwap(m, n) {
				break
			}
		}
		rg := r.Header.Get("Range")
		if !ranges || rg == "" {
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(data)
			return
		}
		var s, e int64
		fmt.Sscanf(rg, "bytes=%d-%d", &s, &e)
		if e >= int64(len(data)) {
			e = int64(len(data)) - 1
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		mu.Lock()
		breakOff := failFirst && e > 0 && !failed[rg]
		failed[rg] = true
		mu.Unlock()
		if breakOff {
			w.Write(data[s : s+(e-s+1)/2])
			panic(http.ErrAbortHandler) // connection drops mid-piece
		}
		w.Write(data[s : e+1])
	}))
	t.Cleanup(srv.Close)
	return srv, &maxConc
}

func testData(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return b
}

var testHeaders = map[string]string{"Referer": "https://example.com/page"}

func TestParallelDownloadWhole(t *testing.T) {
	data := testData(parallelMinSize + 3*parallelChunkSize + 12345)
	srv, maxConc := rangeServer(t, data, true, false)
	dest := filepath.Join(t.TempDir(), "v.mp4")
	if err := testApp(t).parallelDownload(context.Background(), srv.URL+"/v.mp4", testHeaders, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded file differs")
	}
	if maxConc.Load() < 2 || maxConc.Load() > parallelConnections {
		t.Fatalf("connections at once: %d", maxConc.Load())
	}
	if _, err := os.Stat(dest + parallelStateSuffix); !os.IsNotExist(err) {
		t.Fatal("progress record left behind")
	}
}

func TestParallelDownloadRetriesBrokenPieces(t *testing.T) {
	data := testData(parallelMinSize + 5)
	srv, _ := rangeServer(t, data, true, true)
	dest := filepath.Join(t.TempDir(), "v.mp4")
	if err := testApp(t).parallelDownload(context.Background(), srv.URL+"/v.mp4", testHeaders, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("downloaded file differs after retries")
	}
}

func TestParallelDownloadResumesYtDlpPart(t *testing.T) {
	data := testData(parallelMinSize + 7)
	srv, _ := rangeServer(t, data, true, false)
	dest := filepath.Join(t.TempDir(), "v.mp4")
	// yt-dlp left a plain prefix of 2.5 pieces; the leading 2 count as done.
	// Their bytes are poisoned so a re-download would show.
	prefix := append([]byte(nil), data[:5*parallelChunkSize/2]...)
	copy(prefix[:10], "KEEPMEKEEP")
	os.WriteFile(dest+".part", prefix, 0644)
	if err := testApp(t).parallelDownload(context.Background(), srv.URL+"/v.mp4", testHeaders, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.HasPrefix(got, []byte("KEEPMEKEEP")) || !bytes.Equal(got[10:], data[10:]) {
		t.Fatal("resume did not keep the finished pieces or got the rest wrong")
	}
}

func TestParallelDownloadNotSupported(t *testing.T) {
	small := testData(1 << 20)
	srv, _ := rangeServer(t, small, true, false)
	dest := filepath.Join(t.TempDir(), "v.mp4")
	if err := testApp(t).parallelDownload(context.Background(), srv.URL+"/v.mp4", testHeaders, dest); !errors.Is(err, errNotParallel) {
		t.Fatalf("small file: %v", err)
	}
	noRange, _ := rangeServer(t, testData(parallelMinSize+1), false, false)
	if err := testApp(t).parallelDownload(context.Background(), noRange.URL+"/v.mp4", testHeaders, dest); !errors.Is(err, errNotParallel) {
		t.Fatalf("no ranges: %v", err)
	}
	for _, f := range []string{dest, dest + ".part", dest + parallelStateSuffix} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s was written although nothing was downloaded", filepath.Base(f))
		}
	}
}

func TestParallelDownloadStopKeepsProgress(t *testing.T) {
	data := testData(parallelMinSize + 9)
	srv, _ := rangeServer(t, data, true, false)
	dest := filepath.Join(t.TempDir(), "v.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	a := testApp(t)
	var calls atomic.Int64
	srv.Config.Handler = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 4 {
				cancel() // stop after a few pieces
			}
			next.ServeHTTP(w, r)
		})
	}(srv.Config.Handler)
	if err := a.parallelDownload(ctx, srv.URL+"/v.mp4", testHeaders, dest); !errors.Is(err, errStopped) {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(dest + parallelStateSuffix); err != nil {
		t.Fatal("progress record missing after stop")
	}
	if err := a.parallelDownload(context.Background(), srv.URL+"/v.mp4", testHeaders, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("file differs after stop and resume")
	}
}

func TestCapturedDirectFile(t *testing.T) {
	mp4 := browserMediaCandidate{Kind: "MP4", ContentType: "video/mp4"}
	if !capturedDirectFile(mp4, 0) || capturedDirectFile(mp4, 1) || capturedDirectFile(mp4, 4) {
		t.Fatal("MP4 modes")
	}
	if capturedDirectFile(browserMediaCandidate{Kind: "HLS"}, 0) || capturedDirectFile(browserMediaCandidate{Kind: "MP4", ContentType: "application/vnd.apple.mpegurl"}, 0) {
		t.Fatal("a playlist is not a plain file")
	}
	h := capturedRequestHeaders("https://example.com/page", browserMediaCandidate{RequestHeaders: map[string]string{"user-agent": "UA", "range": "bytes=0-"}})
	if h["Referer"] != "https://example.com/page" || h["User-Agent"] != "UA" || strings.Contains(fmt.Sprint(h), "bytes=0-") {
		t.Fatalf("headers: %v", h)
	}
}

func testApp(t *testing.T) *app {
	return &app{appShared: &appShared{appDir: t.TempDir()}}
}
