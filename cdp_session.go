//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

	// sessMu guards sessionID and targetID, which change when the capture
	// tab is replaced (see reopenTab).
	sessMu    sync.Mutex
	sessionID string
	targetID  string
	// networkEvents counts the capture tab's Network events; detached is set
	// when the browser drops the tab's session (the tab was closed).
	networkEvents atomic.Int64
	detached      atomic.Bool

	// handler receives page events for the capture currently in progress.
	handlerMu sync.Mutex
	handler   func(cdpWireMessage)

	tiktokLoginChecked bool

	// windowID identifies the browser window for Browser.setWindowBounds.
	windowID int
	visible  bool
	// scale is the display scaling, for the window size on screen.
	scale float64
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
	return b.rawCall(method, params, b.session())
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
	reply, err := b.browserCall("Target.getTargetInfo", map[string]interface{}{"targetId": b.target()})
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
		if id != "" && id != b.target() {
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
	if b.alive() && b.session() != "" {
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
	b := &cdpBrowser{name: browserName, cmd: cmd, conn: conn, exited: make(chan struct{}), readerDone: make(chan struct{}), nextID: 1, pending: map[int]chan cdpWireMessage{}, scale: a.scale}
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
			if msg.SessionID == "" {
				if msg.Method == "Target.detachedFromTarget" && cdpString(msg.Params, "sessionId") == b.session() {
					b.detached.Store(true)
				}
				continue
			}
			if msg.SessionID != b.session() {
				continue
			}
			if strings.HasPrefix(msg.Method, "Network.") {
				b.networkEvents.Add(1)
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
	var firstTab string
	waitTarget := time.Now().Add(12 * time.Second)
	for firstTab == "" && time.Now().Before(waitTarget) {
		if ids := b.pageTargets(); len(ids) > 0 {
			firstTab = ids[0]
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
	if firstTab == "" {
		return fail(errors.New("瀏覽器已啟動，但找不到可用的分頁"))
	}
	if err := b.attachTab(firstTab); err != nil {
		return fail(err)
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
	b.hide()
	a.postLog("✓ 嗅探瀏覽器在背景執行（已最小化，不會搶走您的滑鼠與鍵盤）；只有需要您登入或操作時才會顯示。\r\n")
	return b, nil
}

func (b *cdpBrowser) session() string {
	b.sessMu.Lock()
	defer b.sessMu.Unlock()
	return b.sessionID
}

func (b *cdpBrowser) target() string {
	b.sessMu.Lock()
	defer b.sessMu.Unlock()
	return b.targetID
}

// attachTab makes a tab the capture tab: attaches a flattened session to it
// and turns on what every capture needs.
func (b *cdpBrowser) attachTab(targetID string) error {
	attach, err := b.browserCall("Target.attachToTarget", map[string]interface{}{"targetId": targetID, "flatten": true})
	if err != nil {
		return fmt.Errorf("連接瀏覽器分頁失敗：%w", err)
	}
	sessionID := cdpString(attach.Result, "sessionId")
	if sessionID == "" {
		return errors.New("連接瀏覽器分頁失敗：沒有取得 DevTools session")
	}
	b.sessMu.Lock()
	b.sessionID, b.targetID = sessionID, targetID
	b.sessMu.Unlock()
	b.detached.Store(false)
	if err := b.send("Network.enable", map[string]interface{}{"maxResourceBufferSize": 32 << 20, "maxTotalBufferSize": 128 << 20}); err != nil {
		return err
	}
	_ = b.send("Network.setCacheDisabled", map[string]interface{}{"cacheDisabled": true})
	_ = b.send("Page.enable", nil)
	// Runtime.enable is not sent: nothing here listens to its events
	// (Runtime.evaluate works without it), and anti-bot checks detect it and
	// then keep repeating their "verify you are human" step.
	_ = b.send("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{"source": cdpAutoplayScript()})
	// Room for the page's own request list (pageResourceSummary): busy pages
	// pass the default 250 entries within seconds.
	_ = b.send("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{"source": "try{performance.setResourceTimingBufferSize(5000)}catch(e){}"})
	if win, err := b.browserCall("Browser.getWindowForTarget", map[string]interface{}{"targetId": targetID}); err == nil {
		b.windowID = int(cdpFloat(win.Result, "windowId"))
	}
	return nil
}

// reopenTab replaces the capture tab with a new one, for a tab that was
// closed or that loads pages without the capture seeing any of its traffic.
func (b *cdpBrowser) reopenTab() error {
	old := b.target()
	// background: a new active tab would bring the minimised window on screen.
	created, err := b.browserCall("Target.createTarget", map[string]interface{}{"url": "about:blank", "background": true})
	if err != nil {
		return fmt.Errorf("開新分頁失敗：%w", err)
	}
	id := cdpString(created.Result, "targetId")
	if id == "" {
		return errors.New("開新分頁失敗：瀏覽器沒有回傳分頁")
	}
	if err := b.attachTab(id); err != nil {
		return err
	}
	if old != "" && old != id {
		_, _ = b.browserCall("Target.closeTarget", map[string]interface{}{"targetId": old})
	}
	if !b.visible {
		b.hide()
	}
	return nil
}

// pageResourceScript reads the capture tab's own record of the requests it
// made (counts and the page path only).
const pageResourceScript = `(() => {
  const es = performance.getEntriesByType('resource');
  const api = es.filter(e => /tiktok\.com\/api\//i.test(e.name));
  const ep = api.filter(e => /\/api\/(drama\/episode\/item_list|item\/detail)/i.test(e.name));
  return JSON.stringify({all: es.length, api: api.length, ep: ep.length, path: location.pathname, vis: document.visibilityState});
})()`

// pageResourceSummary tells whether the page itself made API requests the
// capture did not see: requests the page lists but the capture never got
// mean its network events are lost, none at all mean the page made none.
func (b *cdpBrowser) pageResourceSummary() string {
	reply, err := b.pageCall("Runtime.evaluate", map[string]interface{}{"expression": pageResourceScript, "returnByValue": true})
	if err != nil {
		return "無法讀取頁面記錄：" + err.Error()
	}
	res, _ := reply.Result["result"].(map[string]interface{})
	var v struct {
		All  int    `json:"all"`
		API  int    `json:"api"`
		Ep   int    `json:"ep"`
		Path string `json:"path"`
		Vis  string `json:"vis"`
	}
	if json.Unmarshal([]byte(cdpString(res, "value")), &v) != nil {
		return "無法讀取頁面記錄"
	}
	if r := []rune(v.Path); len(r) > 80 {
		v.Path = string(r[:80])
	}
	return fmt.Sprintf("頁面自己記錄的請求 %d 個、TikTok API %d 個（分集資料 %d 個）；頁面路徑 %s；顯示狀態 %s", v.All, v.API, v.Ep, v.Path, v.Vis)
}

// tabSummary lists the browser's tabs (site names only) for the diagnostics.
func (b *cdpBrowser) tabSummary() string {
	reply, err := b.browserCall("Target.getTargets", nil)
	if err != nil {
		return "無法列出分頁：" + err.Error()
	}
	infos, _ := reply.Result["targetInfos"].([]interface{})
	current := b.target()
	var parts []string
	for _, raw := range infos {
		info, _ := raw.(map[string]interface{})
		kind := cdpString(info, "type")
		if strings.HasSuffix(kind, "worker") {
			continue
		}
		where := hostOnly(cdpString(info, "url"))
		if where == "" {
			where = cdpString(info, "url")
		}
		if sub := cdpString(info, "subtype"); sub != "" {
			kind += "/" + sub
		}
		item := kind + " " + where
		if cdpString(info, "targetId") == current {
			item += "（擷取中）"
		}
		if len(parts) < 8 {
			parts = append(parts, item)
		}
	}
	if len(parts) == 0 {
		return "沒有分頁"
	}
	return strings.Join(parts, "、")
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
