//go:build windows

package main

import (
	"fmt"
	"time"
)

func (capture *cdpCapture) isStatic(raw string) bool {
	if !capture.sharedAddress {
		return false
	}
	capture.stateMu.Lock()
	defer capture.stateMu.Unlock()
	k := adURLKey(raw)
	if capture.staticKeys[k] {
		return true
	}
	for _, src := range capture.redirectFrom[k] {
		if capture.staticKeys[src] {
			return true
		}
	}
	return false
}

func (capture *cdpCapture) finish(c browserMediaCandidate) browserMediaCandidate {
	c = capture.withPageJSON(c)
	c.Alternates = captureAlternates(capture.seen, c.URL, func(u string) bool { return capture.isAd(u) || capture.isStatic(u) })
	// Read now: the tab goes back to a blank page when the capture ends.
	c.PageTitle = capture.b.pageTitle()
	return c
}

// notStatic returns the best stream seen that is not the page's own
// fallback video (see isStatic).
func (capture *cdpCapture) notStatic() (browserMediaCandidate, bool) {
	var out browserMediaCandidate
	for _, c := range capture.seen {
		if !capture.isAd(c.URL) && !capture.isStatic(c.URL) && betterCaptureCandidate(c, out) {
			out = c
		}
	}
	return out, out.URL != ""
}

// staticSources: the HTML addresses a stream came from (itself or the
// addresses that redirected to it).
func (capture *cdpCapture) staticSources(raw string) []string {
	capture.stateMu.Lock()
	defer capture.stateMu.Unlock()
	var out []string
	k := adURLKey(raw)
	for _, s := range append([]string{k}, capture.redirectFrom[k]...) {
		if capture.staticKeys[s] {
			out = append(out, s)
		}
	}
	return out
}

// onlyStatic: the page has played nothing but its built-in video.
func (capture *cdpCapture) onlyStatic() bool {
	if capture.best.URL == "" || !capture.isStatic(capture.best.URL) {
		return false
	}
	_, other := capture.notStatic()
	return !other
}

func (capture *cdpCapture) onCandidate(c browserMediaCandidate) (browserMediaCandidate, error, bool) {
	if capture.isAd(c.URL) {
		return browserMediaCandidate{}, nil, false
	}
	// Episode-matched TikTok API streams are authoritative: return at once
	// instead of risking a preloaded neighbouring episode.
	if c.Score >= 200 {
		capture.a.postLog(fmt.Sprintf("✓ 已從頁面資料取得第 %d 集影片（%s；%s）。\r\n", capture.tiktokEpisode, hostOnly(c.URL), c.Kind))
		return c, nil, true
	}
	// For episode pages that require sign-in only the episode-matched stream is
	// trusted. Generic media requests on that page are preloaded
	// recommendations or other clips, never reliably the episode.
	if capture.isTikTokDrama {
		capture.tikDiag.media()
		return browserMediaCandidate{}, nil, false
	}
	c.EpisodeMatch = streamHasEpisode(c.URL, capture.pageEpisode) || capture.listed.matches(c.URL)
	if other := capture.listed.otherEpisode(c.URL); !c.EpisodeMatch && other > 0 {
		// The page data lists this stream for another episode: a
		// preloaded neighbour, never this one. Keep it for that
		// episode's turn, as its signature is current.
		if capture.a.rememberFreshStream(c.URL, other) {
			capture.a.postLog(fmt.Sprintf("ℹ 頁面預先載入了第 %d 集的串流，先記下來，輪到該集時直接使用。\r\n", other))
		}
		return browserMediaCandidate{}, nil, false
	}
	c.UsedByEpisode = capture.a.episodeGuard.streamUsedBy(c.URL)
	if key := candidateKey(c.URL); !capture.seenKeys[key] && len(capture.seen) < 40 {
		capture.seenKeys[key] = true
		capture.seen = append(capture.seen, c)
	}
	// The page's own fallback video ranks below any other stream.
	if better := betterCaptureCandidate(c, capture.best); (better && (!capture.isStatic(c.URL) || capture.best.URL == "" || capture.isStatic(capture.best.URL))) ||
		(capture.best.URL != "" && capture.isStatic(capture.best.URL) && !capture.isStatic(c.URL)) {
		capture.best = c
		if capture.best.Score >= 125 {
			wait := 3500 * time.Millisecond
			if capture.pageEpisode > 0 && !capture.best.EpisodeMatch && (streamHasNumberSegment(capture.best.URL) || capture.hasListed || capture.best.UsedByEpisode > 0) {
				// The stream is numbered, but not with this page's
				// episode: likely a preloaded neighbouring episode, so
				// give the page's own episode stream time to show up.
				wait = 12 * time.Second
			}
			if capture.settle == nil {
				capture.settle = time.NewTimer(wait)
				capture.settleC = capture.settle.C
			} else {
				if !capture.settle.Stop() {
					select {
					case <-capture.settle.C:
					default:
					}
				}
				capture.settle.Reset(wait)
			}
		}
	}
	return browserMediaCandidate{}, nil, false
}

func (capture *cdpCapture) onSettle() (browserMediaCandidate, error, bool) {
	if capture.best.URL != "" && !capture.isTikTokDrama {
		// Ad <video> elements are checked on a timer; check once more
		// right before accepting, so an ad clip is never returned.
		capture.checkAdVideos()
		if capture.isAd(capture.best.URL) {
			capture.best = browserMediaCandidate{}
			return browserMediaCandidate{}, nil, false
		}
	}
	if capture.best.URL != "" && capture.sharedAddress {
		capture.waitReads(2 * time.Second)
		if capture.isStatic(capture.best.URL) {
			if c, ok := capture.notStatic(); ok {
				capture.best = c
			} else {
				if !capture.staticNoticed {
					capture.staticNoticed = true
					capture.a.postLog(fmt.Sprintf("ℹ 網頁先播放了頁面內建的影片（常是每一集都相同的預覽），先等待播放器載入第 %d 集。\r\n", capture.wantEp))
				}
				return browserMediaCandidate{}, nil, false
			}
		}
	}
	if capture.best.URL != "" {
		capture.a.postLog(fmt.Sprintf("✓ DevTools 捕捉到 %s 媒體（%s）。\r\n", capture.best.Kind, hostOnly(capture.best.URL)))
		if capture.pageEpisode > 0 && capture.best.EpisodeMatch {
			capture.a.postLog(fmt.Sprintf("✓ 串流網址與第 %d 集相符。\r\n", capture.pageEpisode))
		} else if capture.best.UsedByEpisode > 0 {
			capture.a.postLog(fmt.Sprintf("⚠ 頁面只載入了第 %d 集已下載過的串流，仍先下載並比對內容。\r\n", capture.best.UsedByEpisode))
		} else if capture.pageEpisode > 0 && (streamHasNumberSegment(capture.best.URL) || capture.hasListed) {
			capture.a.postLog(fmt.Sprintf("⚠ 沒有找到確定屬於第 %d 集的串流，改用目前播放的串流。\r\n", capture.pageEpisode))
		}
		capture.awaitSubtitles()
		capture.awaitHLSKey()
		return capture.finish(capture.best), nil, true
	}
	return browserMediaCandidate{}, nil, false
}
