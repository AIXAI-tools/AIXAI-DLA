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
	b.setFocusEmulation(true)
	b.setWindowState("minimized")
	b.visible = false
}

// setFocusEmulation makes the page act focused and visible even while
// minimised (players may refuse to start otherwise). It is turned off while
// the user acts in the window, so pages that check the window really lost
// focus (e.g. while an ad opened in another tab) see what actually happens.
func (b *cdpBrowser) setFocusEmulation(on bool) {
	if b.sessionID == "" {
		return
	}
	_ = b.send("Emulation.setFocusEmulationEnabled", map[string]interface{}{"enabled": on})
}

// reveal brings the window on screen, centred on the work area, for user action.
func (b *cdpBrowser) reveal(scale float64) {
	if b.windowID == 0 {
		return
	}
	b.setWindowState("normal")
	b.setFocusEmulation(false)
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
	// The window starts off screen (--window-position); when the DevTools
	// move above does not take (seen with display scaling), move it with
	// Windows itself so it never stays out of reach.
	b.moveOnScreen(work, left, top, ww, wh)
}

// moveOnScreen places the browser's top windows at (left, top, w, h), in
// screen pixels, when they do not lie wholly inside the work area (DevTools
// bounds are in the browser's own scaled units, so with display scaling the
// window may come out too large or off screen).
func (b *cdpBrowser) moveOnScreen(work rect, left, top, w, h int) {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	for _, hw := range browserTopWindows(uint32(b.cmd.Process.Pid)) {
		var r rect
		procGetWindowRect.Call(hw, uintptr(unsafe.Pointer(&r)))
		// A few pixels of slack: maximised and snapped windows reach just past
		// the work area's edges.
		const slack = 16
		if r.Left >= work.Left-slack && r.Top >= work.Top-slack && r.Right <= work.Right+slack && r.Bottom <= work.Bottom+slack {
			continue
		}
		procShowWindow.Call(hw, swRestore)
		procSetWindowPos.Call(hw, 0, uintptr(left), uintptr(top), uintptr(w), uintptr(h), swpShowWindow)
	}
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

// pageTitle is the capture tab's title, or "" when the page has none (the tab
// then shows its address).
func (b *cdpBrowser) pageTitle() string {
	reply, err := b.browserCall("Target.getTargetInfo", map[string]interface{}{"targetId": b.targetID})
	if err != nil {
		return ""
	}
	info, _ := reply.Result["targetInfo"].(map[string]interface{})
	title := strings.TrimSpace(cdpString(info, "title"))
	if title == "" || title == strings.TrimSpace(cdpString(info, "url")) || strings.HasPrefix(strings.ToLower(title), "http") {
		return ""
	}
	return title
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
	info, browserPath, err := a.findCaptureBrowser()
	if err != nil {
		return nil, err
	}
	browserName := info.Name
	profileDir := a.captureProfileDirFor(info.Key)
	_ = os.MkdirAll(profileDir, 0755)
	prepareCaptureProfile(profileDir)
	args := []string{
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--autoplay-policy=no-user-gesture-required",
		// The capture plays videos in the background: keep them silent.
		"--mute-audio",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		// Extensions synced into the profile (ad blockers in particular) block
		// the popups that ad-gated players open while the user unlocks them.
		"--disable-extensions",
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
	// Runtime.enable is not sent: nothing here listens to its events
	// (Runtime.evaluate works without it), and anti-bot checks detect it and
	// then keep repeating their "verify you are human" step.
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
		a.killAllCaptureProfileBrowsers()
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

// closeCaptureBrowser is called when the last running task ends.
func (a *app) closeCaptureBrowser() {
	a.captureSem <- struct{}{}
	defer func() { <-a.captureSem }()
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
	a.postLog("ℹ 這個頁面需要登入才能播放：已在 " + b.name + " 視窗開啟登入頁。請登入一次（建議使用專用帳號，不要用主帳號），偵測到登入後會自動繼續。\r\n")
	a.postLog("ℹ 程式會一直等待，直到登入完成或按「停止」；若找不到登入視窗，按任務區的「顯示登入視窗」。登入狀態會保存，之後不必再登入。\r\n")
	start := time.Now()
	for {
		mins := int(time.Since(start).Minutes())
		if mins == 0 {
			a.postStatus("狀態：等待您在 " + b.name + " 視窗登入（不限時，可按「停止」取消）…")
		} else {
			a.postStatus(fmt.Sprintf("狀態：等待您在 "+b.name+" 視窗登入（已等待 %d 分鐘，不限時，可按「停止」取消）…", mins))
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
	// One capture browser serves every task: tasks take turns using it.
	select {
	case a.captureSem <- struct{}{}:
	case <-ctx.Done():
		return browserMediaCandidate{}, ctx.Err()
	}
	defer func() { <-a.captureSem }()
	b, err := a.captureBrowser(ctx)
	if err != nil {
		return browserMediaCandidate{}, err
	}
	_, tiktokEpisode, isTikTokDrama := parseTikTokShortDramaURL(rawURL)
	pageEpisode := pageEpisodeNumber(rawURL)
	// The page data named this episode's streams but they could not be fetched
	// directly: the stream with the same path is the episode, whatever else
	// the page preloads.
	var listed pageEpisodeHint
	if a.pageEpisodeHint.Page == rawURL {
		listed = a.pageEpisodeHint
	}
	hasListed := len(listed.Streams) > 0 || len(listed.Others) > 0
	var tikDiag *tiktokCaptureDiag
	if isTikTokDrama {
		tikDiag = newTikTokCaptureDiag()
	}

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
			tikDiag.read(false, 0, nil)
			return
		}
		body := cdpString(reply.Result, "body")
		if encoded, _ := reply.Result["base64Encoded"].(bool); encoded {
			raw, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				tikDiag.read(false, 0, nil)
				return
			}
			body = string(raw)
		}
		c, res, listedEps := tiktokCandidateFromAPIBodyResult([]byte(body), apiURL, tiktokEpisode, a.captureMaxShortSide, reqHeaders)
		tikDiag.read(true, res, listedEps)
		if res != tiktokAPIMatched {
			tikDiag.apiStatus([]byte(body))
		}
		if res != tiktokAPIMatched {
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
	// The page's own JSON responses (player data) are kept in memory for this
	// capture only, so a larger rendition listed next to the played one can be
	// chosen afterwards. They are never written to disk or to the log.
	var pageJSON [][]byte
	var pageJSONMu sync.Mutex
	// pageJSONReads counts the response bodies still being read.
	var pageJSONReads sync.WaitGroup
	keepPageJSON := func(requestID string) {
		defer pageJSONReads.Done()
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
		if len(body) == 0 || len(body) > maxPageJSONBytes {
			return
		}
		pageJSONMu.Lock()
		if len(pageJSON) < maxPageJSONCount {
			pageJSON = append(pageJSON, []byte(body))
		}
		pageJSONMu.Unlock()
	}
	// Video ads: the clips listed in the page's VAST responses and the sources
	// of ad <video> elements are never the episode. Kept for this capture only.
	adURLs := map[string]bool{}
	var adMu sync.Mutex
	adNoticed := false
	noteAds := func(urls []string) {
		if len(urls) == 0 {
			return
		}
		adMu.Lock()
		for _, u := range urls {
			adURLs[adURLKey(u)] = true
		}
		first := !adNoticed
		adNoticed = true
		adMu.Unlock()
		if first {
			a.postLog("ℹ 頁面正在播放影片廣告：廣告影片不會被下載，等待正片開始播放。\r\n")
		}
	}
	isAd := func(raw string) bool {
		adMu.Lock()
		defer adMu.Unlock()
		return adURLs[adURLKey(raw)]
	}
	inspectVAST := func(requestID string) {
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
		noteAds(vastMediaFiles(body))
	}
	// checkAdVideos asks the page which <video> elements are ads (marked as such
	// by their own or a parent's id/class) and records what they play.
	checkAdVideos := func() {
		reply, err := b.pageCall("Runtime.evaluate", map[string]interface{}{"expression": cdpAdVideoScript, "returnByValue": true})
		if err != nil {
			return
		}
		res, _ := reply.Result["result"].(map[string]interface{})
		var urls []string
		_ = json.Unmarshal([]byte(cdpString(res, "value")), &urls)
		noteAds(urls)
	}
	// Subtitle files the player loaded during this capture.
	var subFiles []string
	subSeen := map[string]bool{}
	// On a page whose episodes share one address (episode_capture.go), a video
	// written into the page's HTML -- a fallback player for visitors without
	// scripts, or the address declared for search engines -- is the same for
	// every episode, so it is never taken for the requested one. Redirects are
	// followed: such an address often forwards to the media server.
	sharedAddress := a.episodeGuard != nil
	staticKeys := map[string]bool{}
	redirectFrom := map[string][]string{}
	docRead := false
	readStaticHTML := func(requestID, docURL string) {
		defer pageJSONReads.Done()
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
		cands := extractWebMediaCandidates(body, docURL)
		stateMu.Lock()
		for _, c := range cands {
			staticKeys[adURLKey(c.URL)] = true
		}
		stateMu.Unlock()
	}
	isStatic := func(raw string) bool {
		if !sharedAddress {
			return false
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		k := adURLKey(raw)
		if staticKeys[k] {
			return true
		}
		for _, src := range redirectFrom[k] {
			if staticKeys[src] {
				return true
			}
		}
		return false
	}
	// waitReads lets the response bodies still being read arrive.
	waitReads := func(limit time.Duration) {
		read := make(chan struct{})
		go func() {
			pageJSONReads.Wait()
			close(read)
		}()
		select {
		case <-read:
		case <-time.After(limit):
		case <-ctx.Done():
		}
	}
	withPageJSON := func(c browserMediaCandidate) browserMediaCandidate {
		pageJSONMu.Lock()
		c.PageJSON = append([][]byte(nil), pageJSON...)
		pageJSONMu.Unlock()
		stateMu.Lock()
		loaded := append([]string(nil), subFiles...)
		stateMu.Unlock()
		// Keep the subtitles the stream came with (TikTok API) when the page
		// showed none of the usual kind.
		if subs := captureSubtitles(c.PageJSON, loaded, rawURL); len(subs) > 0 || len(c.Subtitles) == 0 {
			c.Subtitles = subs
		}
		return c
	}
	// awaitSubtitles runs once the stream is chosen. It lets the JSON responses
	// still being read arrive, then, while the page lists subtitles but the
	// player has not loaded any yet (some load the track only after the video
	// starts), waits a little for it: the loaded file tells which list is this
	// episode's.
	awaitSubtitles := func() {
		waitReads(2 * time.Second)
		end := time.Now().Add(subtitleLoadWait)
		for time.Now().Before(end) {
			stateMu.Lock()
			loaded := len(subFiles) > 0
			stateMu.Unlock()
			if loaded {
				return
			}
			listed := false
			pageJSONMu.Lock()
			for _, body := range pageJSON {
				if len(jsonSubtitleList(body, rawURL)) > 0 {
					listed = true
					break
				}
			}
			pageJSONMu.Unlock()
			if !listed {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-b.readerDone:
				return
			case <-time.After(250 * time.Millisecond):
			}
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
			newURL := cdpString(req, "url")
			if _, redirected := p["redirectResponse"]; redirected && st.URL != "" {
				// The same request continues at another address.
				from, to := adURLKey(st.URL), adURLKey(newURL)
				if from != to && len(redirectFrom[to]) < 16 {
					redirectFrom[to] = append(redirectFrom[to], append([]string{from}, redirectFrom[from]...)...)
				}
			}
			st.URL = newURL
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
			if st.Status > 0 && st.Status < 400 && isSubtitleResponse(st.URL, st.MimeType) && !subSeen[st.URL] && len(subFiles) <= maxCaptureSubtitleFiles {
				subSeen[st.URL] = true
				subFiles = append(subFiles, st.URL)
			}
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
				stateMu.Lock()
				st := states[reqID]
				keep := st != nil && st.Status == 200 && strings.Contains(strings.ToLower(st.MimeType), "json") &&
					(st.RequestType == "XHR" || st.RequestType == "Fetch") && cdpFloat(p, "encodedDataLength") <= maxPageJSONBytes
				vast := st != nil && st.Status == 200 && (st.RequestType == "XHR" || st.RequestType == "Fetch") &&
					(strings.Contains(strings.ToLower(st.MimeType), "xml") || strings.Contains(strings.ToLower(st.URL), "vast")) &&
					cdpFloat(p, "encodedDataLength") <= maxPageJSONBytes
				// The first document is the page itself (frames come later).
				doc := sharedAddress && !docRead && st != nil && st.Status == 200 && st.RequestType == "Document"
				var docURL string
				if doc {
					docRead, docURL = true, st.URL
				}
				stateMu.Unlock()
				if doc {
					pageJSONReads.Add(1)
					go readStaticHTML(reqID, docURL)
				}
				if keep {
					pageJSONReads.Add(1)
					go keepPageJSON(reqID)
				}
				if vast {
					go inspectVAST(reqID)
				}
				return
			}
			stateMu.Lock()
			st := states[reqID]
			if st != nil && strings.Contains(strings.ToLower(st.URL), "tiktok.com/api/") {
				tikDiag.api(st.URL, st.Status)
			}
			ok := st != nil && st.Status == 200 && isTikTokDramaAPI(st.URL)
			var apiURL string
			reqHeaders := map[string]string{}
			if ok {
				tikDiag.knownAPI()
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
	adCheck := time.NewTicker(1500 * time.Millisecond)
	defer adCheck.Stop()
	// userWait is set once the window was shown for the user to act in (press
	// play, close an ad, finish a check); the capture then waits longer.
	userWait := false
	defer func() {
		if userWait {
			a.loginBrowser.Store(nil)
			a.emit("login", map[string]interface{}{"waiting": false})
		}
	}()
	var best browserMediaCandidate
	// seen keeps every non-ad stream of this capture (best one aside, they are
	// the alternates tried when the chosen file fails the media check).
	var seen []browserMediaCandidate
	seenKeys := map[string]bool{}
	finish := func(c browserMediaCandidate) browserMediaCandidate {
		c = withPageJSON(c)
		c.Alternates = captureAlternates(seen, c.URL, func(u string) bool { return isAd(u) || isStatic(u) })
		// Read now: the tab goes back to a blank page when the capture ends.
		c.PageTitle = b.pageTitle()
		return c
	}
	// notStatic returns the best stream seen that is not the page's own
	// fallback video (see isStatic).
	notStatic := func() (browserMediaCandidate, bool) {
		var out browserMediaCandidate
		for _, c := range seen {
			if !isAd(c.URL) && !isStatic(c.URL) && betterCaptureCandidate(c, out) {
				out = c
			}
		}
		return out, out.URL != ""
	}
	wantEp := pageEpisode
	if wantEp == 0 {
		wantEp = episodeFromQuery(rawURL)
	}
	staticNoticed, reloaded := false, false
	// staticSources: the HTML addresses a stream came from (itself or the
	// addresses that redirected to it).
	staticSources := func(raw string) []string {
		stateMu.Lock()
		defer stateMu.Unlock()
		var out []string
		k := adURLKey(raw)
		for _, s := range append([]string{k}, redirectFrom[k]...) {
			if staticKeys[s] {
				out = append(out, s)
			}
		}
		return out
	}
	// onlyStatic: the page has played nothing but its built-in video.
	onlyStatic := func() bool {
		if best.URL == "" || !isStatic(best.URL) {
			return false
		}
		_, other := notStatic()
		return !other
	}
	var settle *time.Timer
	var settleC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return browserMediaCandidate{}, ctx.Err()
		case <-b.readerDone:
			if best.URL != "" && !isAd(best.URL) {
				return finish(best), nil
			}
			return browserMediaCandidate{}, errors.New("DevTools 網路監聽中斷（瀏覽器視窗可能被關閉）")
		case <-adCheck.C:
			if !isTikTokDrama {
				go checkAdVideos()
			}
		case <-hint.C:
			if !isTikTokDrama && !reloaded && (best.URL == "" || onlyStatic()) {
				// Usually the site was just slow: reload the page once before
				// calling the user.
				reloaded = true
				if best.URL == "" {
					a.postLog("ℹ 25 秒內沒有偵測到影片，重新整理一次頁面。\r\n")
				} else {
					a.postLog(fmt.Sprintf("ℹ 網頁還沒載入第 %d 集的播放器，重新整理一次頁面。\r\n", wantEp))
				}
				_ = b.send("Page.navigate", map[string]interface{}{"url": rawURL})
				hint.Reset(25 * time.Second)
				if !deadline.Stop() {
					select {
					case <-deadline.C:
					default:
					}
				}
				deadline.Reset(40 * time.Second)
				continue
			}
			if !isTikTokDrama && onlyStatic() {
				if builtInVideoIsEpisode(ctx, rawURL, wantEp, staticSources(best.URL)) {
					a.postLog(fmt.Sprintf("✓ 網頁內建的影片每一集都不同，就是第 %d 集的影片。\r\n", wantEp))
					awaitSubtitles()
					return finish(best), nil
				}
			}
			if (best.URL == "" || isStatic(best.URL)) && !isTikTokDrama {
				userWait = true
				b.reveal(a.scale)
				a.loginBrowser.Store(b)
				a.emit("login", map[string]interface{}{"waiting": true, "reason": "action"})
				// Call the user back to the computer: the taskbar button
				// flashes and the system information sound plays.
				a.flashMainWindow()
				procMessageBeep.Call(mbIconAsterisk)
				a.postLog("ℹ 重新整理後仍未偵測到正片串流，已把瀏覽器視窗叫到前面：若畫面正在播廣告、要求按播放、登入或驗證，請直接在該視窗操作（本工具不會自動點擊廣告）。程式最多再等 10 分鐘。\r\n")
				a.postStatus("狀態：等待正片開始播放（可在瀏覽器視窗操作，最多 10 分鐘）…")
				if !deadline.Stop() {
					select {
					case <-deadline.C:
					default:
					}
				}
				deadline.Reset(10 * time.Minute)
			} else if best.URL == "" {
				b.reveal(a.scale)
				a.postLog("ℹ 25 秒內尚未偵測到影片串流，已把瀏覽器視窗叫到前面：若畫面要求登入、驗證或需要按播放，請直接在該視窗操作，程式會繼續監聽。\r\n")
			}
		case c := <-candidates:
			if isAd(c.URL) {
				continue
			}
			// Episode-matched TikTok API streams are authoritative: return at once
			// instead of risking a preloaded neighbouring episode.
			if c.Score >= 200 {
				a.postLog(fmt.Sprintf("✓ 已從頁面資料取得第 %d 集影片（%s；%s）。\r\n", tiktokEpisode, hostOnly(c.URL), c.Kind))
				return c, nil
			}
			// For episode pages that require sign-in only the episode-matched stream is
			// trusted. Generic media requests on that page are preloaded
			// recommendations or other clips, never reliably the episode.
			if isTikTokDrama {
				tikDiag.media()
				continue
			}
			c.EpisodeMatch = streamHasEpisode(c.URL, pageEpisode) || listed.matches(c.URL)
			if other := listed.otherEpisode(c.URL); !c.EpisodeMatch && other > 0 {
				// The page data lists this stream for another episode: a
				// preloaded neighbour, never this one. Keep it for that
				// episode's turn, as its signature is current.
				if a.rememberFreshStream(c.URL, other) {
					a.postLog(fmt.Sprintf("ℹ 頁面預先載入了第 %d 集的串流，先記下來，輪到該集時直接使用。\r\n", other))
				}
				continue
			}
			c.UsedByEpisode = a.episodeGuard.streamUsedBy(c.URL)
			if key := candidateKey(c.URL); !seenKeys[key] && len(seen) < 40 {
				seenKeys[key] = true
				seen = append(seen, c)
			}
			// The page's own fallback video ranks below any other stream.
			if better := betterCaptureCandidate(c, best); (better && (!isStatic(c.URL) || best.URL == "" || isStatic(best.URL))) ||
				(best.URL != "" && isStatic(best.URL) && !isStatic(c.URL)) {
				best = c
				if best.Score >= 125 {
					wait := 3500 * time.Millisecond
					if pageEpisode > 0 && !best.EpisodeMatch && (streamHasNumberSegment(best.URL) || hasListed || best.UsedByEpisode > 0) {
						// The stream is numbered, but not with this page's
						// episode: likely a preloaded neighbouring episode, so
						// give the page's own episode stream time to show up.
						wait = 12 * time.Second
					}
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
			if best.URL != "" && !isTikTokDrama {
				// Ad <video> elements are checked on a timer; check once more
				// right before accepting, so an ad clip is never returned.
				checkAdVideos()
				if isAd(best.URL) {
					best = browserMediaCandidate{}
					continue
				}
			}
			if best.URL != "" && sharedAddress {
				waitReads(2 * time.Second)
				if isStatic(best.URL) {
					if c, ok := notStatic(); ok {
						best = c
					} else {
						if !staticNoticed {
							staticNoticed = true
							a.postLog(fmt.Sprintf("ℹ 網頁先播放了頁面內建的影片（常是每一集都相同的預覽），先等待播放器載入第 %d 集。\r\n", wantEp))
						}
						continue
					}
				}
			}
			if best.URL != "" {
				a.postLog(fmt.Sprintf("✓ DevTools 捕捉到 %s 媒體（%s）。\r\n", best.Kind, hostOnly(best.URL)))
				if pageEpisode > 0 && best.EpisodeMatch {
					a.postLog(fmt.Sprintf("✓ 串流網址與第 %d 集相符。\r\n", pageEpisode))
				} else if best.UsedByEpisode > 0 {
					a.postLog(fmt.Sprintf("⚠ 頁面只載入了第 %d 集已下載過的串流，仍先下載並比對內容。\r\n", best.UsedByEpisode))
				} else if pageEpisode > 0 && (streamHasNumberSegment(best.URL) || hasListed) {
					a.postLog(fmt.Sprintf("⚠ 沒有找到確定屬於第 %d 集的串流，改用目前播放的串流。\r\n", pageEpisode))
				}
				awaitSubtitles()
				return finish(best), nil
			}
		case <-deadline.C:
			if best.URL != "" && isStatic(best.URL) {
				c, ok := notStatic()
				if !ok && builtInVideoIsEpisode(ctx, rawURL, wantEp, staticSources(best.URL)) {
					a.postLog(fmt.Sprintf("✓ 網頁內建的影片每一集都不同，就是第 %d 集的影片。\r\n", wantEp))
					c, ok = best, true
				}
				if !ok {
					return browserMediaCandidate{}, fmt.Errorf("網頁沒有載入第 %d 集的影片，只播放了頁面內建的影片（每一集都相同），為避免存成錯的集數，沒有下載", wantEp)
				}
				best = c
			}
			if best.URL != "" && !isAd(best.URL) {
				awaitSubtitles()
				return finish(best), nil
			}
			if isTikTokDrama {
				summary := tikDiag.summary(tiktokEpisode)
				a.postLog("ℹ 分集擷取診斷：" + summary + "\r\n")
				a.diagNote("分集擷取：" + summary)
				if tikDiag.noKnownAPI() {
					return browserMediaCandidate{}, fmt.Errorf("已登入，但 90 秒內頁面沒有送出第 %d 集的分集資料；網站的資料格式可能已變更", tiktokEpisode)
				}
				return browserMediaCandidate{}, fmt.Errorf("已登入，但 90 秒內沒有取得第 %d 集的影片網址；這一集可能無法以你的帳號存取", tiktokEpisode)
			}
			adMu.Lock()
			sawAd := adNoticed
			adMu.Unlock()
			if sawAd {
				return browserMediaCandidate{}, errors.New("頁面只播放了廣告，等待期間正片沒有開始播放；可在瀏覽器視窗看完或關閉廣告後重試")
			}
			return browserMediaCandidate{}, errors.New("DevTools 網路監聽已正常啟動，但等待期間未出現可下載的 HLS／DASH／MP4；若網站畫面要求登入或按播放，請在自動開啟的瀏覽器完成一次操作後重試")
		}
	}
}
