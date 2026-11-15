//go:build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// inspectTikTokAPI reads a finished TikTok API response body and emits the
// requested episode's stream. It runs in its own goroutine because the reply
// to Network.getResponseBody arrives through the reader loop.
func (capture *cdpCapture) inspectTikTokAPI(requestID, apiURL string, reqHeaders map[string]string) {
	reply, err := capture.b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil || reply.Result == nil {
		capture.tikDiag.read(false, 0, nil)
		return
	}
	body := cdpString(reply.Result, "body")
	if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			capture.tikDiag.read(false, 0, nil)
			return
		}
		body = string(raw)
	}
	c, res, listedEps := tiktokCandidateFromAPIBodyResult([]byte(body), apiURL, capture.tiktokEpisode, capture.a.captureMaxShortSide, reqHeaders)
	capture.tikDiag.read(true, res, listedEps)
	if res != tiktokAPIMatched {
		capture.tikDiag.apiStatus([]byte(body))
	}
	if res != tiktokAPIMatched {
		return
	}
	c.PageURL = capture.rawURL
	if captureHeader(c.RequestHeaders, "referer") == "" {
		c.RequestHeaders["referer"] = capture.rawURL
	}
	select {
	case capture.candidates <- c:
	default:
	}
}

func (capture *cdpCapture) keepPageJSON(requestID string) {
	defer capture.pageJSONReads.Done()
	reply, err := capture.b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil || reply.Result == nil {
		return
	}
	body := cdpString(reply.Result, "body")
	if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return
		}
		body = string(raw)
	}
	if len(body) == 0 || len(body) > maxPageJSONBytes {
		return
	}
	capture.pageJSONMu.Lock()
	if len(capture.pageJSON) < maxPageJSONCount {
		capture.pageJSON = append(capture.pageJSON, []byte(body))
	}
	capture.pageJSONMu.Unlock()
}

func (capture *cdpCapture) readManifest(requestID string) {
	reply, err := capture.b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil || reply.Result == nil {
		return
	}
	body := cdpString(reply.Result, "body")
	if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return
		}
		body = string(raw)
	}
	if _, ok := playerOnlyKey(body); ok {
		capture.keyNeeded.Store(true)
	}
}

// keepHLSKey reads a small binary response the player fetched; a 16-byte one
// may be the AES-128 key of an encrypted stream (capture_hls_key.go).
func (capture *cdpCapture) keepHLSKey(requestID, keyURL string) {
	reply, err := capture.b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil || reply.Result == nil {
		return
	}
	body := cdpString(reply.Result, "body")
	if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return
		}
		body = string(raw)
	}
	if len(body) == aesKeySize {
		capture.a.noteCaptureKey(keyURL, []byte(body))
	}
}

func (capture *cdpCapture) noteAds(urls []string) {
	if len(urls) == 0 {
		return
	}
	capture.adMu.Lock()
	for _, u := range urls {
		capture.adURLs[adURLKey(u)] = true
	}
	first := !capture.adNoticed
	capture.adNoticed = true
	capture.adMu.Unlock()
	if first {
		capture.a.postLog("ℹ 頁面正在播放影片廣告：廣告影片不會被下載，等待正片開始播放。\r\n")
	}
}

func (capture *cdpCapture) isAd(raw string) bool {
	capture.adMu.Lock()
	defer capture.adMu.Unlock()
	return capture.adURLs[adURLKey(raw)]
}

func (capture *cdpCapture) inspectVAST(requestID string) {
	reply, err := capture.b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil || reply.Result == nil {
		return
	}
	body := cdpString(reply.Result, "body")
	if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return
		}
		body = string(raw)
	}
	capture.noteAds(vastMediaFiles(body))
}

// checkAdVideos asks the page which <video> elements are ads (marked as such
// by their own or a parent's id/class) and records what they play.
func (capture *cdpCapture) checkAdVideos() {
	reply, err := capture.b.pageCall("Runtime.evaluate", map[string]interface{}{"expression": cdpAdVideoScript, "returnByValue": true})
	if err != nil {
		return
	}
	res, _ := reply.Result["result"].(map[string]interface{})
	var urls []string
	_ = json.Unmarshal([]byte(cdpString(res, "value")), &urls)
	capture.noteAds(urls)
}

func (capture *cdpCapture) readStaticHTML(requestID, docURL string) {
	defer capture.pageJSONReads.Done()
	reply, err := capture.b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
	if err != nil || reply.Result == nil {
		return
	}
	body := cdpString(reply.Result, "body")
	if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return
		}
		body = string(raw)
	}
	cands := extractWebMediaCandidates(body, docURL)
	capture.stateMu.Lock()
	for _, c := range cands {
		capture.staticKeys[adURLKey(c.URL)] = true
	}
	capture.stateMu.Unlock()
}

// waitReads lets the response bodies still being read arrive.
func (capture *cdpCapture) waitReads(limit time.Duration) {
	read := make(chan struct{})
	go func() {
		capture.pageJSONReads.Wait()
		close(read)
	}()
	select {
	case <-read:
	case <-time.After(limit):
	case <-capture.ctx.Done():
	}
}

func (capture *cdpCapture) withPageJSON(c browserMediaCandidate) browserMediaCandidate {
	capture.pageJSONMu.Lock()
	c.PageJSON = append([][]byte(nil), capture.pageJSON...)
	capture.pageJSONMu.Unlock()
	capture.stateMu.Lock()
	loaded := append([]string(nil), capture.subFiles...)
	capture.stateMu.Unlock()
	// Keep the subtitles the stream came with (TikTok API) when the page
	// showed none of the usual kind.
	if subs := captureSubtitles(c.PageJSON, loaded, capture.rawURL); len(subs) > 0 || len(c.Subtitles) == 0 {
		c.Subtitles = subs
	}
	return c
}

// awaitHLSKey: a manifest of this capture names a key only the player can
// fetch; the page is kept open until the player has fetched it.
func (capture *cdpCapture) awaitHLSKey() {
	if !capture.keyNeeded.Load() || capture.a.captureKeyCount() > 0 {
		return
	}
	capture.a.postLog("ℹ 串流有加密，等待播放器取得解密金鑰…\r\n")
	end := time.Now().Add(hlsKeyWait)
	for time.Now().Before(end) && capture.a.captureKeyCount() == 0 {
		select {
		case <-capture.ctx.Done():
			return
		case <-capture.b.readerDone:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// awaitSubtitles runs once the stream is chosen. It lets the JSON responses
// still being read arrive, then, while the page lists subtitles but the
// player has not loaded any yet (some load the track only after the video
// starts), waits a little for it: the loaded file tells which list is this
// episode's.
func (capture *cdpCapture) awaitSubtitles() {
	capture.waitReads(2 * time.Second)
	end := time.Now().Add(subtitleLoadWait)
	for time.Now().Before(end) {
		capture.stateMu.Lock()
		loaded := len(capture.subFiles) > 0
		capture.stateMu.Unlock()
		if loaded {
			return
		}
		listed := false
		capture.pageJSONMu.Lock()
		for _, body := range capture.pageJSON {
			if len(jsonSubtitleList(body, capture.rawURL)) > 0 {
				listed = true
				break
			}
		}
		capture.pageJSONMu.Unlock()
		if !listed {
			return
		}
		select {
		case <-capture.ctx.Done():
			return
		case <-capture.b.readerDone:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
