//go:build windows

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// captureMediaWithCDP opens one page in the shared capture browser and waits
// for its real media stream.
func (a *app) captureMediaWithCDP(ctx context.Context, rawURL, cookieFile string) (browserMediaCandidate, error) {
	// One capture browser serves every task: tasks take turns using it.
	select {
	case a.captureSem <- struct{}{}:
	case <-ctx.Done():
		return browserMediaCandidate{}, ctx.Err()
	}
	defer func() { <-a.captureSem }()
	capture, err := newCDPCapture(a, ctx, rawURL, cookieFile)
	if err != nil {
		return browserMediaCandidate{}, err
	}
	if err := capture.preparePage(); err != nil {
		return browserMediaCandidate{}, err
	}
	defer func() {
		capture.b.setHandler(nil)
		if capture.b.alive() {
			capture.b.navigateBlank() // stop playback and media traffic between episodes
			if capture.b.visible {
				capture.b.hide()
			}
		}
	}()
	if err := capture.navigate(); err != nil {
		return browserMediaCandidate{}, err
	}
	capture.maxWait = 45 * time.Second
	if capture.isTikTokDrama {
		capture.maxWait = 90 * time.Second
	}
	capture.deadline = time.NewTimer(capture.maxWait)
	defer capture.deadline.Stop()
	capture.hint = time.NewTimer(25 * time.Second)
	defer capture.hint.Stop()
	capture.adCheck = time.NewTicker(1500 * time.Millisecond)
	defer capture.adCheck.Stop()
	capture.silentCheck = time.NewTimer(12 * time.Second)
	defer capture.silentCheck.Stop()
	// A TikTok episode page asks the site's API within seconds of loading; a
	// capture that sees no such request (while seeing other traffic, or none)
	// is not watching the page that plays.
	if capture.isTikTokDrama {
		tiktokCheck := time.NewTimer(15 * time.Second)
		defer tiktokCheck.Stop()
		capture.tiktokCheckC = tiktokCheck.C
	}
	defer func() {
		if capture.userWait {
			capture.a.loginBrowser.Store(nil)
			capture.a.emit("login", map[string]interface{}{"waiting": false})
		}
	}()
	return capture.waitForStream()
}

// cdpCapture owns one capture's shared state. Event and body-reader methods
// keep the original mutex/atomic boundaries; wait-loop fields stay on its caller.
type cdpCapture struct {
	a                          *app
	b                          *cdpBrowser
	ctx                        context.Context
	rawURL, cookieFile         string
	tiktokEpisode, pageEpisode int
	isTikTokDrama              bool
	// The page data named this episode's streams but they could not be fetched
	// directly: the stream with the same path is the episode, whatever else
	// the page preloads.
	listed    pageEpisodeHint
	hasListed bool
	tikDiag   *tiktokCaptureDiag

	states                           map[string]*cdpRequestState
	candidates                       chan browserMediaCandidate
	challengeCh                      chan struct{}
	challengeSeen, challengePrompted bool
	stateMu                          sync.Mutex
	// The page's own JSON responses (player data) are kept in memory for this
	// capture only, so a larger rendition listed next to the played one can be
	// chosen afterwards. They are never written to disk or to the log.
	pageJSON   [][]byte
	pageJSONMu sync.Mutex
	// pageJSONReads counts the response bodies still being read.
	pageJSONReads sync.WaitGroup
	keyNeeded     atomic.Bool
	// Video ads: the clips listed in the page's VAST responses and the sources
	// of ad <video> elements are never the episode. Kept for this capture only.
	adURLs    map[string]bool
	adMu      sync.Mutex
	adNoticed bool
	// Subtitle files the player loaded during this capture.
	subFiles []string
	subSeen  map[string]bool
	// On a page whose episodes share one address (episode_capture.go), a video
	// written into the page's HTML -- a fallback player for visitors without
	// scripts, or the address declared for search engines -- is the same for
	// every episode, so it is never taken for the requested one. Redirects are
	// followed: such an address often forwards to the media server.
	sharedAddress bool
	staticKeys    map[string]bool
	redirectFrom  map[string][]string
	docRead       bool

	navEvents                   int64
	maxWait                     time.Duration
	deadline, hint, silentCheck *time.Timer
	adCheck                     *time.Ticker
	tiktokCheckC                <-chan time.Time
	reopened                    bool
	// userWait is set once the window was shown for the user to act in (press
	// play, close an ad, finish a check); the capture then waits longer.
	userWait bool
	best     browserMediaCandidate
	// seen keeps every non-ad stream of this capture (best one aside, they are
	// the alternates tried when the chosen file fails the media check).
	seen                    []browserMediaCandidate
	seenKeys                map[string]bool
	wantEp                  int
	staticNoticed, reloaded bool
	settle                  *time.Timer
	settleC                 <-chan time.Time
}

func newCDPCapture(a *app, ctx context.Context, rawURL, cookieFile string) (*cdpCapture, error) {
	a.resetCaptureKeys()
	b, err := a.captureBrowser(ctx)
	if err != nil {
		return nil, err
	}
	if b.detached.Load() {
		a.postLog("ℹ 擷取用的分頁已被關閉，改開一個新分頁。\r\n")
		if err := b.reopenTab(); err != nil {
			return nil, err
		}
	}
	capture := &cdpCapture{a: a, b: b, ctx: ctx, rawURL: rawURL, cookieFile: cookieFile}
	_, capture.tiktokEpisode, capture.isTikTokDrama = parseTikTokShortDramaURL(capture.rawURL)
	capture.pageEpisode = pageEpisodeNumber(capture.rawURL)
	if capture.a.pageEpisodeHint.Page == capture.rawURL {
		capture.listed = capture.a.pageEpisodeHint
	}
	capture.hasListed = len(capture.listed.Streams) > 0 || len(capture.listed.Others) > 0
	if capture.isTikTokDrama {
		capture.tikDiag = newTikTokCaptureDiag()
	}

	capture.states = map[string]*cdpRequestState{}
	capture.candidates = make(chan browserMediaCandidate, 128)
	capture.challengeCh = make(chan struct{}, 1)
	capture.adURLs = map[string]bool{}
	capture.subSeen = map[string]bool{}
	capture.sharedAddress = capture.a.episodeGuard != nil
	capture.staticKeys = map[string]bool{}
	capture.redirectFrom = map[string][]string{}
	return capture, nil
}
