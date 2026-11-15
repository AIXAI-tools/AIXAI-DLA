//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests use event messages and in-memory channels only. They never start
// a browser, connect to a site, or create an application window.
func captureFixture(t *testing.T) *cdpCapture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	a := &app{appShared: &appShared{}}
	a.closing.Store(true) // Keep fixture logging away from disk and the UI.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	writer.Close() // Page queries fail immediately without a browser connection.
	c := &cdpCapture{
		a: a, b: &cdpBrowser{readerDone: make(chan struct{}), conn: &cdpPipe{w: writer}, pending: map[int]chan cdpWireMessage{}}, ctx: ctx,
		rawURL: "https://site.example/watch?ep=3", pageEpisode: 3,
		states: map[string]*cdpRequestState{}, candidates: make(chan browserMediaCandidate, 128),
		challengeCh: make(chan struct{}, 1), adURLs: map[string]bool{},
		subSeen: map[string]bool{}, staticKeys: map[string]bool{},
		redirectFrom: map[string][]string{}, seenKeys: map[string]bool{},
		deadline: time.NewTimer(time.Hour), hint: time.NewTimer(time.Hour),
		silentCheck: time.NewTimer(time.Hour), adCheck: time.NewTicker(time.Hour),
	}
	t.Cleanup(func() {
		c.deadline.Stop()
		c.hint.Stop()
		c.silentCheck.Stop()
		c.adCheck.Stop()
		if c.settle != nil {
			c.settle.Stop()
		}
	})
	return c
}

func captureEvent(c *cdpCapture, method, id string, params map[string]interface{}) {
	params["requestId"] = id
	c.handler(cdpWireMessage{Method: "Network." + method, Params: params})
}

func TestCDPCaptureEventsKeepLateHeadersAndRefusedManifest(t *testing.T) {
	c := captureFixture(t)
	// Extra headers may arrive before the ordinary request/response events.
	captureEvent(c, "requestWillBeSentExtraInfo", "video", map[string]interface{}{
		"headers": map[string]interface{}{"X-Early": "early"},
	})
	captureEvent(c, "requestWillBeSent", "video", map[string]interface{}{
		"type": "Media", "request": map[string]interface{}{
			"url": "https://cdn.example/ep3.mp4", "headers": map[string]interface{}{"Referer": c.rawURL},
		},
	})
	captureEvent(c, "responseReceived", "video", map[string]interface{}{
		"response": map[string]interface{}{"status": float64(200), "mimeType": "video/mp4"},
	})
	first := <-c.candidates
	if first.PageURL != c.rawURL || captureHeader(first.RequestHeaders, "X-Early") != "early" ||
		first.ContentType != "video/mp4" {
		t.Fatalf("candidate lost event state: %+v", first)
	}
	captureEvent(c, "responseReceivedExtraInfo", "video", map[string]interface{}{
		"headers": map[string]interface{}{"Content-Length": "50000000"},
	})
	second := <-c.candidates
	if second.ContentLength != 50000000 || first.ContentLength != 0 {
		t.Fatal("late response headers must enrich the new candidate")
	}
	captureEvent(c, "requestWillBeSentExtraInfo", "video", map[string]interface{}{
		"headers": map[string]interface{}{"X-Late": "late"},
	})
	third := <-c.candidates
	if captureHeader(third.RequestHeaders, "X-Late") != "late" || captureHeader(first.RequestHeaders, "X-Late") != "" {
		t.Fatal("late request headers must not mutate a previous candidate's header snapshot")
	}
	captureEvent(c, "requestWillBeSent", "manifest", map[string]interface{}{
		"type": "Fetch", "request": map[string]interface{}{"url": "https://cdn.example/ep3.m3u8"},
	})
	captureEvent(c, "loadingFailed", "manifest", map[string]interface{}{})
	refused := <-c.candidates
	if !c.states["manifest"].Refused || refused.URL != "https://cdn.example/ep3.m3u8" || refused.Kind != "HLS" || refused.Score != 130 {
		t.Fatalf("blocked manifest was lost: %+v", refused)
	}
}

func TestCDPCaptureRedirectsSubtitlesAndChallengeShareState(t *testing.T) {
	c := captureFixture(t)
	c.sharedAddress = true
	from, to := "https://site.example/fallback.mp4", "https://cdn.example/shared.mp4"
	c.staticKeys[adURLKey(from)] = true
	for index, url := range []string{from, to} {
		p := map[string]interface{}{"type": "Media", "request": map[string]interface{}{"url": url}}
		if index > 0 {
			p["redirectResponse"] = map[string]interface{}{}
		}
		captureEvent(c, "requestWillBeSent", "redirect", p)
	}
	if !c.isStatic(to) || len(c.staticSources(to)) != 1 || c.staticSources(to)[0] != adURLKey(from) {
		t.Fatal("the redirected stream lost its static HTML origin")
	}
	for range 2 {
		captureEvent(c, "responseReceived", "subtitle", map[string]interface{}{
			"response": map[string]interface{}{"url": "https://cdn.example/ep3.vtt", "status": float64(200), "mimeType": "text/vtt"},
		})
	}
	if len(c.subFiles) != 1 || !c.subSeen["https://cdn.example/ep3.vtt"] {
		t.Fatal("subtitle state was not retained/deduplicated across events")
	}
	c.notifyChallenge("https://challenges.cloudflare.com/turnstile/v0/api.js")
	c.notifyChallenge("https://challenges.cloudflare.com/turnstile/v0/api.js")
	if len(c.challengeCh) != 1 {
		t.Fatal("challenge notifications must remain buffered and nonblocking")
	}
}

func TestCDPCaptureCandidateSelectionRetainsStateAcrossCalls(t *testing.T) {
	c := captureFixture(t)
	c.sharedAddress = true
	fallback := browserMediaCandidate{URL: "https://cdn.example/shared.mp4", Score: 180}
	c.staticKeys[adURLKey(fallback.URL)] = true
	if _, err, done := c.onCandidate(fallback); done || err != nil || !c.onlyStatic() {
		t.Fatalf("fallback should stay pending: done=%v err=%v", done, err)
	}
	settle := c.settle
	episode := browserMediaCandidate{URL: "https://cdn.example/ep3.mp4", Score: 125}
	if _, err, done := c.onCandidate(episode); done || err != nil {
		t.Fatalf("normal media should settle before returning: done=%v err=%v", done, err)
	}
	if c.best.URL != episode.URL || c.onlyStatic() || len(c.seen) != 2 || c.settle != settle || c.settleC != settle.C {
		t.Fatalf("selection state was copied/reset: best=%+v seen=%+v", c.best, c.seen)
	}
	if _, _, done := c.onCandidate(episode); done || len(c.seen) != 2 {
		t.Fatal("duplicate candidate should not add another alternate")
	}
	c.adURLs[adURLKey(episode.URL)] = true
	if _, _, done := c.onSettle(); done || c.best.URL != "" {
		t.Fatal("a candidate recognized as an ad must resume waiting")
	}
}

func TestCDPCaptureWaitForStreamTerminalPaths(t *testing.T) {
	t.Run("matched API returns unchanged", func(t *testing.T) {
		c := captureFixture(t)
		c.isTikTokDrama = true
		c.tiktokEpisode = 3
		want := browserMediaCandidate{URL: "https://cdn.example/matched.mp4", Score: 200, PageTitle: "from API"}
		c.candidates <- want
		got, err := c.waitForStream()
		if err != nil || got.URL != want.URL || got.PageTitle != want.PageTitle {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("reader stopped", func(t *testing.T) {
		c := captureFixture(t)
		close(c.b.readerDone)
		if _, err := c.waitForStream(); err == nil || !strings.Contains(err.Error(), "DevTools 網路監聽中斷") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
	t.Run("deadline with only ads", func(t *testing.T) {
		c := captureFixture(t)
		c.b.networkEvents.Store(1)
		c.adNoticed = true
		c.deadline.Reset(0)
		if _, err := c.waitForStream(); err == nil || !strings.Contains(err.Error(), "頁面只播放了廣告") {
			t.Fatalf("unexpected result: %v", err)
		}
	})
	t.Run("context canceled", func(t *testing.T) {
		c := captureFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.ctx = ctx
		if _, err := c.waitForStream(); !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected result: %v", err)
		}
	})
}

func TestCDPCaptureCanceledSemaphoreWaitDoesNotStartCapture(t *testing.T) {
	a := &app{appShared: &appShared{captureSem: make(chan struct{}, 1)}}
	a.captureSem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.captureMediaWithCDP(ctx, "https://site.example/watch", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected result: %v", err)
	}
	if len(a.captureSem) != 1 {
		t.Fatal("canceling a queued capture must leave the active capture's semaphore untouched")
	}
}

func TestCDPCaptureNavigationFailureRunsParentDefers(t *testing.T) {
	c := captureFixture(t)
	c.a.capBrowser = c.b // Reuse the in-memory fake; never launch a browser.
	c.a.captureSem = make(chan struct{}, 1)
	c.a.noteCaptureKey("https://site.example/old-key", make([]byte, aesKeySize))
	previousHook := captureNavigateHook
	t.Cleanup(func() { captureNavigateHook = previousHook })
	hookCalled := false
	captureNavigateHook = func(b *cdpBrowser) {
		hookCalled = true
		b.handlerMu.Lock()
		installed := b.handler != nil
		b.handlerMu.Unlock()
		if !installed || len(c.a.captureSem) != 1 || c.a.captureKeyCount() != 0 || b.nextID != 1 {
			t.Error("navigation hook must run after blanking/installing the handler/resetting keys, while holding the semaphore")
		}
	}
	_, err := c.a.captureMediaWithCDP(c.ctx, c.rawURL, "")
	if !errors.Is(err, os.ErrClosed) || !hookCalled {
		t.Fatalf("expected navigation failure after the hook: hook=%v err=%v", hookCalled, err)
	}
	c.b.handlerMu.Lock()
	installed := c.b.handler != nil
	c.b.handlerMu.Unlock()
	if installed || len(c.a.captureSem) != 0 || c.b.nextID != 3 {
		t.Fatal("capture return must clear the handler, blank the page, and release the semaphore")
	}
}
