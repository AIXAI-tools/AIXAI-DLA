//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// cdpBrowser is one capture browser driven over a private DevTools pipe. It is
// kept open for a whole download task, so a 50-episode batch reuses one window
// and one login instead of relaunching the browser for every episode: fewer
// requests, fewer resources, and traffic that looks like one viewer binge-watching.
type cdpBrowser struct {
	name   string
	cmd    *exec.Cmd
	conn   *cdpPipe
	exited chan struct{}
	// readerDone is closed when the DevTools pipe stops delivering messages.
	readerDone chan struct{}

	idMu    sync.Mutex
	nextID  int
	pending map[int]chan cdpWireMessage

	sessionID string
	targetID  string

	// handler receives page events for the capture currently in progress.
	handlerMu sync.Mutex
	handler   func(cdpWireMessage)

	tiktokLoginChecked bool

	// windowID identifies the browser window for Browser.setWindowBounds.
	windowID int
	visible  bool
}

// Background mode: the capture browser works out of the user's way (minimised,
// never taking keyboard focus) and is only brought forward when the user has
// to act in it — signing in, solving a check, or pressing play.

func (b *cdpBrowser) setWindowState(state string) {
	if b.windowID == 0 {
		return
	}
	_, _ = b.browserCall("Browser.setWindowBounds", map[string]interface{}{"windowId": b.windowID, "bounds": map[string]interface{}{"windowState": state}})
}

// hide sends the window to the background without stealing focus.
func (b *cdpBrowser) hide() {
	b.setWindowState("minimized")
	b.visible = false
}

// reveal brings the window on screen, centred on the work area, for user action.
func (b *cdpBrowser) reveal(scale float64) {
	if b.windowID == 0 {
		return
	}
	b.setWindowState("normal")
	var work rect
	procSystemParametersInfoW.Call(SPI_GETWORKAREA, 0, uintptr(unsafe.Pointer(&work)), 0)
	ww, wh := int(float64(1180)*scale), int(float64(820)*scale)
	if ww > int(work.Right-work.Left) {
		ww = int(work.Right - work.Left)
	}
	if wh > int(work.Bottom-work.Top) {
		wh = int(work.Bottom - work.Top)
	}
	left := int(work.Left) + (int(work.Right-work.Left)-ww)/2
	top := int(work.Top) + (int(work.Bottom-work.Top)-wh)/2
	_, _ = b.browserCall("Browser.setWindowBounds", map[string]interface{}{"windowId": b.windowID,
		"bounds": map[string]interface{}{"left": left, "top": top, "width": ww, "height": wh}})
	_ = b.send("Page.bringToFront", nil)
	b.visible = true
	b.forceToFront()
}

func (b *cdpBrowser) rawCall(method string, params map[string]interface{}, session string) (chan cdpWireMessage, error) {
	b.idMu.Lock()
	id := b.nextID
	b.nextID++
	ch := make(chan cdpWireMessage, 1)
	b.pending[id] = ch
	b.idMu.Unlock()
	return ch, b.conn.WriteJSON(cdpCommand(id, method, params, session))
}

func (b *cdpBrowser) await(ch chan cdpWireMessage, err error, timeout time.Duration) (cdpWireMessage, error) {
	if err != nil {
		return cdpWireMessage{}, err
	}
	select {
	case reply := <-ch:
		if reply.Error != nil {
			return reply, fmt.Errorf("DevTools：%v", reply.Error["message"])
		}
		return reply, nil
	case <-time.After(timeout):
		return cdpWireMessage{}, errors.New("DevTools 回應逾時")
	case <-b.readerDone:
		return cdpWireMessage{}, errors.New("瀏覽器已關閉")
	}
}

// browserCall runs a browser-level command (Target.*, Browser.*) and waits.
func (b *cdpBrowser) browserCall(method string, params map[string]interface{}) (cdpWireMessage, error) {
	ch, err := b.rawCall(method, params, "")
	return b.await(ch, err, 8*time.Second)
}

// sendWithReply sends a command to the attached page session.
func (b *cdpBrowser) sendWithReply(method string, params map[string]interface{}) (chan cdpWireMessage, error) {
	return b.rawCall(method, params, b.sessionID)
}

// pageCall sends a page-session command and waits for its reply.
func (b *cdpBrowser) pageCall(method string, params map[string]interface{}) (cdpWireMessage, error) {
	ch, err := b.sendWithReply(method, params)
	return b.await(ch, err, 10*time.Second)
}

func (b *cdpBrowser) send(method string, params map[string]interface{}) error {
	_, err := b.sendWithReply(method, params)
	return err
}

func (b *cdpBrowser) alive() bool {
	select {
	case <-b.readerDone:
		return false
	case <-b.exited:
		return false
	default:
		return true
	}
}

func (b *cdpBrowser) setHandler(h func(cdpWireMessage)) {
	b.handlerMu.Lock()
	b.handler = h
	b.handlerMu.Unlock()
}

func (b *cdpBrowser) pageTargets() []string {
	reply, err := b.browserCall("Target.getTargets", nil)
	if err != nil {
		return nil
	}
	infos, _ := reply.Result["targetInfos"].([]interface{})
	var ids []string
	for _, raw := range infos {
		info, _ := raw.(map[string]interface{})
		if cdpString(info, "type") == "page" {
			ids = append(ids, cdpString(info, "targetId"))
		}
	}
	return ids
}

// closeOtherPages closes every tab except the capture tab, so tabs restored
// from a previous run do not pile up and waste resources.
func (b *cdpBrowser) closeOtherPages() int {
	closed := 0
	for _, id := range b.pageTargets() {
		if id != "" && id != b.targetID {
			if _, err := b.browserCall("Target.closeTarget", map[string]interface{}{"targetId": id}); err == nil {
				closed++
			}
		}
	}
	return closed
}

// navigateBlank stops the current page (and its media traffic) between captures.
func (b *cdpBrowser) navigateBlank() {
	_ = b.send("Page.navigate", map[string]interface{}{"url": "about:blank"})
	time.Sleep(500 * time.Millisecond)
}

// close leaves a single blank tab behind (Edge restores the last session even
// after a clean exit), then shuts the browser down cleanly so the profile and
// a one-time TikTok login are flushed before the process tree is cleaned up.
func (b *cdpBrowser) close() {
	if b.alive() && b.sessionID != "" {
		b.navigateBlank()
		b.closeOtherPages()
		time.Sleep(300 * time.Millisecond)
	}
	_, _ = b.rawCall("Browser.close", nil, "")
	select {
	case <-b.exited:
	case <-time.After(5 * time.Second):
		killStandaloneProcessTree(b.cmd)
	}
	b.conn.Close()
}

// startCaptureBrowser launches the dedicated capture browser, attaches to its
// tab and enables network monitoring before any page is opened.
func (a *app) startCaptureBrowser(ctx context.Context) (*cdpBrowser, error) {
	browserName, browserPath, err := findCaptureBrowser()
	if err != nil {
		return nil, err
	}
	profileDir := a.captureProfileDir()
	_ = os.MkdirAll(profileDir, 0755)
	prepareCaptureProfile(profileDir)
	args := []string{
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--autoplay-policy=no-user-gesture-required",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-session-crashed-bubble",
		"--hide-crash-restore-bubble",
		"--window-size=1180,820",
		"--window-position=-32000,-32000",
		"--new-window",
		"about:blank",
	}
	if strings.Contains(strings.ToLower(browserName), "edge") {
		args = append([]string{"--disable-features=msEdgeFirstRunExperience"}, args...)
	}
	// Start hidden: measured 0 foreground switches and 0 on-screen frames while
	// downloading; the window is revealed only when the user has to act in it.
	cmd, conn, err := startBrowserWithPipe(browserPath, args, true)
	if err != nil {
		return nil, fmt.Errorf("啟動 %s DevTools 媒體監聽失敗：%w", browserName, err)
	}
	b := &cdpBrowser{name: browserName, cmd: cmd, conn: conn, exited: make(chan struct{}), readerDone: make(chan struct{}), nextID: 1, pending: map[int]chan cdpWireMessage{}}
	go func() {
		_ = cmd.Wait()
		close(b.exited)
	}()
	go func() {
		defer close(b.readerDone)
		for {
			payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg cdpWireMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				continue
			}
			if msg.Method == "" {
				if msg.ID > 0 {
					b.idMu.Lock()
					ch := b.pending[msg.ID]
					delete(b.pending, msg.ID)
					b.idMu.Unlock()
					if ch != nil {
						ch <- msg
					}
				}
				continue
			}
			if msg.SessionID == "" || msg.SessionID != b.sessionID {
				continue
			}
			b.handlerMu.Lock()
			h := b.handler
			b.handlerMu.Unlock()
			if h != nil {
				h(msg)
			}
		}
	}()
	fail := func(err error) (*cdpBrowser, error) {
		b.close()
		return nil, err
	}

	a.postLog("→ 啟動 " + browserName + " DevTools 網路監聽（私有管線，不開放任何網路連接埠）。同一批下載會共用這個視窗。\r\n")
	a.postStatus("狀態：正在建立瀏覽器網路監聽…")

	// Find the tab to drive and attach to it with a flattened session.
	waitTarget := time.Now().Add(12 * time.Second)
	for b.targetID == "" && time.Now().Before(waitTarget) {
		if ids := b.pageTargets(); len(ids) > 0 {
			b.targetID = ids[0]
			break
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-b.exited:
			return fail(errBrowserExitedEarly)
		case <-time.After(250 * time.Millisecond):
		}
	}
	if b.targetID == "" {
		return fail(errors.New("瀏覽器已啟動，但找不到可用的分頁"))
	}
	attach, err := b.browserCall("Target.attachToTarget", map[string]interface{}{"targetId": b.targetID, "flatten": true})
	if err != nil {
		return fail(fmt.Errorf("連接瀏覽器分頁失敗：%w", err))
	}
	b.sessionID = cdpString(attach.Result, "sessionId")
	if b.sessionID == "" {
		return fail(errors.New("連接瀏覽器分頁失敗：沒有取得 DevTools session"))
	}
	// Close leftover tabs now and again shortly after, since restored tabs can
	// appear a few seconds after the first one.
	if n := b.closeOtherPages(); n > 0 {
		a.postLog(fmt.Sprintf("✓ 已關閉 %d 個上次殘留的分頁，只保留本次下載用的分頁。\r\n", n))
	}
	for _, delay := range []time.Duration{2 * time.Second, 6 * time.Second} {
		time.AfterFunc(delay, func() {
			if b.alive() {
				b.closeOtherPages()
			}
		})
	}
	if err := b.send("Network.enable", map[string]interface{}{"maxResourceBufferSize": 32 << 20, "maxTotalBufferSize": 128 << 20}); err != nil {
		return fail(err)
	}
	_ = b.send("Network.setCacheDisabled", map[string]interface{}{"cacheDisabled": true})
	_ = b.send("Page.enable", nil)
	_ = b.send("Runtime.enable", nil)
	// Pages must behave as if focused and visible even while minimised, or
	// players may refuse to start.
	_ = b.send("Emulation.setFocusEmulationEnabled", map[string]interface{}{"enabled": true})
	_ = b.send("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{"source": cdpAutoplayScript()})
	if win, err := b.browserCall("Browser.getWindowForTarget", map[string]interface{}{"targetId": b.targetID}); err == nil {
		b.windowID = int(cdpFloat(win.Result, "windowId"))
	}
	b.hide()
	a.postLog("✓ 嗅探瀏覽器在背景執行（已最小化，不會搶走您的滑鼠與鍵盤）；只有需要您登入或操作時才會顯示。\r\n")
	return b, nil
}

// captureBrowser returns the task's shared capture browser, starting it if
// needed or if the user closed its window.
func (a *app) captureBrowser(ctx context.Context) (*cdpBrowser, error) {
	if a.capBrowser != nil && a.capBrowser.alive() {
		return a.capBrowser, nil
	}
	if a.capBrowser != nil {
		a.capBrowser.close()
		a.capBrowser = nil
	}
	// A hidden capture browser from an interrupted earlier run would make the
	// new launch hand over to it and exit; stop such leftovers first.
	a.cleanupLeftoverCaptureBrowsers()
	b, err := a.startCaptureBrowser(ctx)
	if errors.Is(err, errBrowserExitedEarly) && ctx.Err() == nil {
		a.postLog("⚠ 擷取用的瀏覽器啟動後立即結束，正在清理背景程序後重試一次…\r\n")
		killCaptureProfileBrowsers(a.captureProfileDir())
		time.Sleep(2500 * time.Millisecond)
		b, err = a.startCaptureBrowser(ctx)
	}
	if err != nil {
		a.captureStartFailed.Store(true)
		return nil, err
	}
	a.capBrowser = b
	a.captureRunning.Store(true)
	return b, nil
}

// closeCaptureBrowser is called when a download task ends.
func (a *app) closeCaptureBrowser() {
	if a.capBrowser != nil {
		a.capBrowser.close()
		a.capBrowser = nil
	}
	a.captureRunning.Store(false)
}

// tiktokLoggedIn asks the capture browser whether its persistent profile holds
// a TikTok session cookie. Cookie values are only inspected for presence.
func (b *cdpBrowser) tiktokLoggedIn() bool {
	reply, err := b.pageCall("Network.getCookies", map[string]interface{}{"urls": []string{"https://www.tiktok.com/"}})
	if err != nil {
		return false
	}
	list, _ := reply.Result["cookies"].([]interface{})
	for _, raw := range list {
		c, _ := raw.(map[string]interface{})
		name := cdpString(c, "name")
		if (name == "sessionid" || name == "sessionid_ss") && cdpString(c, "value") != "" {
			return true
		}
	}
	return false
}

// ensureTikTokLogin makes sure the dedicated capture profile is signed in to
// TikTok before the episode page is opened. The first time, it opens TikTok's
// login page in the capture window and continues automatically as soon as the
// login completes; the session is kept in the profile for all later downloads.
func (a *app) ensureTikTokLogin(ctx context.Context, b *cdpBrowser) error {
	if b.tiktokLoginChecked {
		return nil
	}
	if b.tiktokLoggedIn() {
		b.tiktokLoginChecked = true
		a.postLog("✓ 已沿用 AIXAI 專用瀏覽器中的網站登入。\r\n")
		return nil
	}
	if err := b.send("Page.navigate", map[string]interface{}{"url": "https://www.tiktok.com/login"}); err != nil {
		return err
	}
	b.reveal(a.scale)
	a.loginBrowser.Store(b)
	defer a.loginBrowser.Store(nil)
	a.emit("login", map[string]interface{}{"waiting": true})
	defer a.emit("login", map[string]interface{}{"waiting": false})
	a.postLog("ℹ 這個頁面需要登入才能播放：已在 Edge 視窗開啟登入頁。請登入一次（建議使用專用帳號，不要用主帳號），偵測到登入後會自動繼續。\r\n")
	a.postLog("ℹ 程式會一直等待，直到登入完成或按「停止」；若找不到登入視窗，按任務區的「顯示登入視窗」。登入狀態會保存，之後不必再登入。\r\n")
	start := time.Now()
	for {
		mins := int(time.Since(start).Minutes())
		if mins == 0 {
			a.postStatus("狀態：等待您在 Edge 視窗登入（不限時，可按「停止」取消）…")
		} else {
			a.postStatus(fmt.Sprintf("狀態：等待您在 Edge 視窗登入（已等待 %d 分鐘，不限時，可按「停止」取消）…", mins))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.readerDone:
			a.loginCancelled.Store(true)
			return errLoginCancelled
		case <-time.After(2 * time.Second):
		}
		if a.stopRequested.Load() {
			return context.Canceled
		}
		if b.tiktokLoggedIn() {
			b.tiktokLoginChecked = true
			a.postLog("✓ 偵測到登入成功，已保存登入狀態；瀏覽器回到背景繼續下載。\r\n")
			time.Sleep(1500 * time.Millisecond)
			b.hide()
			return nil
		}
	}
}

// captureMediaWithCDP opens one page in the shared capture browser and waits
// for its real media stream.
func (a *app) captureMediaWithCDP(ctx context.Context, rawURL, cookieFile string) (browserMediaCandidate, error) {
	b, err := a.captureBrowser(ctx)
	if err != nil {
		return browserMediaCandidate{}, err
	}
	_, tiktokEpisode, isTikTokDrama := parseTikTokShortDramaURL(rawURL)

	states := map[string]*cdpRequestState{}
	candidates := make(chan browserMediaCandidate, 128)
	var stateMu sync.Mutex
	emitFor := func(requestID string) {
		stateMu.Lock()
		st := states[requestID]
		if st == nil || !st.SeenResponse || st.URL == "" || st.Status >= 400 {
			stateMu.Unlock()
			return
		}
		reqHeaders := map[string]string{}
		for k, v := range st.RequestHeaders {
			reqHeaders[k] = v
		}
		respHeaders := map[string]string{}
		for k, v := range st.ResponseHeaders {
			respHeaders[k] = v
		}
		if captureHeader(respHeaders, "content-type") == "" && st.MimeType != "" {
			respHeaders["content-type"] = st.MimeType
		}
		ev := browserCaptureEvent{Kind: "cdp", URL: st.URL, PageURL: rawURL, RequestType: st.RequestType, Initiator: st.Initiator, RequestHeaders: reqHeaders, ResponseHeaders: respHeaders}
		stateMu.Unlock()
		if c, ok := mediaCandidateScore(ev); ok {
			select {
			case candidates <- c:
			default:
			}
		}
	}
	// inspectTikTokAPI reads a finished TikTok API response body and emits the
	// requested episode's stream. It runs in its own goroutine because the reply
	// to Network.getResponseBody arrives through the reader loop.
	inspectTikTokAPI := func(requestID, apiURL string, reqHeaders map[string]string) {
		reply, err := b.pageCall("Network.getResponseBody", map[string]interface{}{"requestId": requestID})
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
		c, ok := tiktokCandidateFromAPIBody([]byte(body), apiURL, tiktokEpisode, reqHeaders)
		if !ok {
			return
		}
		c.PageURL = rawURL
		if captureHeader(c.RequestHeaders, "referer") == "" {
			c.RequestHeaders["referer"] = rawURL
		}
		select {
		case candidates <- c:
		default:
		}
	}
	handler := func(msg cdpWireMessage) {
		p := msg.Params
		reqID := cdpString(p, "requestId")
		if reqID == "" {
			return
		}
		switch msg.Method {
		case "Network.requestWillBeSent":
			req, _ := p["request"].(map[string]interface{})
			initiator, _ := p["initiator"].(map[string]interface{})
			stateMu.Lock()
			st := states[reqID]
			if st == nil {
				st = &cdpRequestState{}
				states[reqID] = st
			}
			st.URL = cdpString(req, "url")
			st.RequestType = cdpString(p, "type")
			st.Initiator = cdpString(initiator, "url")
			st.RequestHeaders = mergeStringMaps(st.RequestHeaders, cdpMapString(req["headers"]))
			stateMu.Unlock()
		case "Network.requestWillBeSentExtraInfo":
			stateMu.Lock()
			st := states[reqID]
			if st == nil {
				st = &cdpRequestState{}
				states[reqID] = st
			}
			st.RequestHeaders = mergeStringMaps(st.RequestHeaders, cdpMapString(p["headers"]))
			stateMu.Unlock()
			emitFor(reqID)
		case "Network.responseReceived":
			resp, _ := p["response"].(map[string]interface{})
			stateMu.Lock()
			st := states[reqID]
			if st == nil {
				st = &cdpRequestState{}
				states[reqID] = st
			}
			if u := cdpString(resp, "url"); u != "" {
				st.URL = u
			}
			if typ := cdpString(p, "type"); typ != "" {
				st.RequestType = typ
			}
			st.MimeType = cdpString(resp, "mimeType")
			st.Status = int(cdpFloat(resp, "status"))
			st.ResponseHeaders = mergeStringMaps(st.ResponseHeaders, cdpMapString(resp["headers"]))
			st.SeenResponse = true
			stateMu.Unlock()
			emitFor(reqID)
		case "Network.responseReceivedExtraInfo":
			stateMu.Lock()
			st := states[reqID]
			if st == nil {
				st = &cdpRequestState{}
				states[reqID] = st
			}
			st.ResponseHeaders = mergeStringMaps(st.ResponseHeaders, cdpMapString(p["headers"]))
			stateMu.Unlock()
			emitFor(reqID)
		case "Network.loadingFinished":
			if !isTikTokDrama {
				return
			}
			stateMu.Lock()
			st := states[reqID]
			ok := st != nil && st.Status == 200 && isTikTokDramaAPI(st.URL)
			var apiURL string
			reqHeaders := map[string]string{}
			if ok {
				apiURL = st.URL
				for k, v := range st.RequestHeaders {
					reqHeaders[k] = v
				}
			}
			stateMu.Unlock()
			if ok {
				go inspectTikTokAPI(reqID, apiURL, reqHeaders)
			}
		}
	}

	if isTikTokDrama {
		if err := a.ensureTikTokLogin(ctx, b); err != nil {
			return browserMediaCandidate{}, err
		}
	}
	// Start from a blank page so nothing from the previous episode can leak into
	// this capture, then install this capture's handler and open the page.
	b.navigateBlank()
	b.setHandler(handler)
	defer func() {
		b.setHandler(nil)
		if b.alive() {
			b.navigateBlank() // stop playback and media traffic between episodes
			if b.visible {
				b.hide()
			}
		}
	}()
	if cookies := cdpCookiesFromFile(cookieFile, rawURL); len(cookies) > 0 {
		if err := b.send("Network.setCookies", map[string]interface{}{"cookies": cookies}); err == nil {
			a.postLog(fmt.Sprintf("✓ 已從 cookies.txt 匯入 %d 個 %s 的登入 Cookie 到嗅探瀏覽器。\r\n", len(cookies), hostOnly(rawURL)))
		}
	}
	if err := b.send("Page.navigate", map[string]interface{}{"url": rawURL}); err != nil {
		return browserMediaCandidate{}, err
	}
	a.postLog("✓ DevTools 網路監聽已先於目標頁面啟用；正在等待播放器的實際媒體請求。\r\n")
	if isTikTokDrama {
		a.postStatus("狀態：已登入，正在取得本集影片…")
	} else {
		a.postStatus("狀態：正在監聽實際播放的 HLS／DASH／MP4…")
	}

	maxWait := 45 * time.Second
	if isTikTokDrama {
		maxWait = 90 * time.Second
	}
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	hint := time.NewTimer(25 * time.Second)
	defer hint.Stop()
	var best browserMediaCandidate
	var settle *time.Timer
	var settleC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return browserMediaCandidate{}, ctx.Err()
		case <-b.readerDone:
			if best.URL != "" {
				return best, nil
			}
			return browserMediaCandidate{}, errors.New("DevTools 網路監聽中斷（瀏覽器視窗可能被關閉）")
		case <-hint.C:
			if best.URL == "" {
				b.reveal(a.scale)
				a.postLog("ℹ 25 秒內尚未偵測到影片串流，已把瀏覽器視窗叫到前面：若畫面要求登入、驗證或需要按播放，請直接在該視窗操作，程式會繼續監聽。\r\n")
			}
		case c := <-candidates:
			// Episode-matched TikTok API streams are authoritative: return at once
			// instead of risking a preloaded neighbouring episode.
			if c.Score >= 200 {
				a.postLog(fmt.Sprintf("✓ 已從頁面資料取得第 %d 集影片（%s）。\r\n", tiktokEpisode, hostOnly(c.URL)))
				return c, nil
			}
			// For episode pages that require sign-in only the episode-matched stream is
			// trusted. Generic media requests on that page are preloaded
			// recommendations or other clips, never reliably the episode.
			if isTikTokDrama {
				continue
			}
			if betterCaptureCandidate(c, best) {
				best = c
				if best.Score >= 125 {
					wait := 3500 * time.Millisecond
					if settle == nil {
						settle = time.NewTimer(wait)
						settleC = settle.C
					} else {
						if !settle.Stop() {
							select {
							case <-settle.C:
							default:
							}
						}
						settle.Reset(wait)
					}
				}
			}
		case <-settleC:
			if best.URL != "" {
				a.postLog(fmt.Sprintf("✓ DevTools 捕捉到 %s 媒體（%s）。\r\n", best.Kind, hostOnly(best.URL)))
				return best, nil
			}
		case <-deadline.C:
			if best.URL != "" {
				return best, nil
			}
			if isTikTokDrama {
				return browserMediaCandidate{}, fmt.Errorf("已登入，但 90 秒內沒有取得第 %d 集的影片網址；這一集可能無法以你的帳號存取", tiktokEpisode)
			}
			return browserMediaCandidate{}, errors.New("DevTools 網路監聽已正常啟動，但 45 秒內未出現可下載的 HLS／DASH／MP4；若網站畫面要求登入或按播放，請在自動開啟的瀏覽器完成一次操作後重試")
		}
	}
}
