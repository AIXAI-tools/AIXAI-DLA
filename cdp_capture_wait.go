//go:build windows

package main

import (
	"errors"
	"fmt"
	"time"
)

func (capture *cdpCapture) preparePage() error {
	if capture.isTikTokDrama {
		if err := capture.a.ensureTikTokLogin(capture.ctx, capture.b); err != nil {
			return err
		}
	}
	// Start from a blank page so nothing from the previous episode can leak into
	// this capture, then install this capture's handler and open the page.
	capture.b.navigateBlank()
	capture.b.setHandler(capture.handler)
	return nil
}

func (capture *cdpCapture) navigate() error {
	if cookies := cdpCookiesFromFile(capture.cookieFile, capture.rawURL); len(cookies) > 0 {
		if err := capture.b.send("Network.setCookies", map[string]interface{}{"cookies": cookies}); err == nil {
			capture.a.postLog(fmt.Sprintf("✓ 已從 cookies.txt 匯入 %d 個 %s 的登入 Cookie 到嗅探瀏覽器。\r\n", len(cookies), hostOnly(capture.rawURL)))
		}
	}
	if captureNavigateHook != nil {
		captureNavigateHook(capture.b)
	}
	// A page load always shows network traffic within seconds; none at all
	// means the capture is attached to a tab that is not the one loading.
	capture.navEvents = capture.b.networkEvents.Load()
	if err := capture.b.send("Page.navigate", map[string]interface{}{"url": capture.rawURL}); err != nil {
		return err
	}
	capture.a.postLog("✓ DevTools 網路監聽已先於目標頁面啟用；正在等待播放器的實際媒體請求。\r\n")
	if capture.isTikTokDrama {
		capture.a.postStatus("狀態：已登入，正在取得本集影片…")
	} else {
		capture.a.postStatus("狀態：正在監聽實際播放的 HLS／DASH／MP4…")
	}
	return nil
}

// reopenAndReload moves the capture to a new background tab and opens the
// page again there, with the full waiting time.
func (capture *cdpCapture) reopenAndReload() error {
	capture.reopened = true
	capture.challengeSeen, capture.challengePrompted = false, false
	if err := capture.b.reopenTab(); err != nil {
		return fmt.Errorf("擷取用的分頁沒有回應，開新分頁也失敗：%w", err)
	}
	capture.navEvents = capture.b.networkEvents.Load()
	if err := capture.b.send("Page.navigate", map[string]interface{}{"url": capture.rawURL}); err != nil {
		return err
	}
	capture.hint.Reset(25 * time.Second)
	if !capture.deadline.Stop() {
		select {
		case <-capture.deadline.C:
		default:
		}
	}
	capture.deadline.Reset(capture.maxWait)
	return nil
}

// Branch helpers return done=true only when the capture must end.
// A false result resumes the select loop, including former continue paths.
func (capture *cdpCapture) waitForStream() (browserMediaCandidate, error) {
	capture.seenKeys = map[string]bool{}
	capture.wantEp = capture.pageEpisode
	if capture.wantEp == 0 {
		capture.wantEp = episodeFromQuery(capture.rawURL)
	}
	for {
		select {
		case <-capture.ctx.Done():
			return browserMediaCandidate{}, capture.ctx.Err()
		case <-capture.b.readerDone:
			if capture.best.URL != "" && !capture.isAd(capture.best.URL) {
				return capture.finish(capture.best), nil
			}
			return browserMediaCandidate{}, errors.New("DevTools 網路監聽中斷（瀏覽器視窗可能被關閉）")
		case <-capture.adCheck.C:
			if !capture.isTikTokDrama {
				go capture.checkAdVideos()
			}
			capture.b.followUserRestore()
		case <-capture.silentCheck.C:
			if capture.reopened || capture.b.networkEvents.Load() != capture.navEvents {
				continue
			}
			tabs := capture.b.tabSummary()
			capture.a.postLog("⚠ 擷取用的分頁 12 秒內沒有任何網路活動，改開一個新分頁重新載入（當時的分頁：" + tabs + "）。\r\n")
			capture.a.diagNote("擷取分頁沒有網路活動，已改開新分頁；當時的分頁：" + tabs)
			if err := capture.reopenAndReload(); err != nil {
				return browserMediaCandidate{}, err
			}
		case <-capture.tiktokCheckC:
			if capture.reopened || capture.tikDiag.apiRequested() || capture.best.URL != "" {
				continue
			}
			page := capture.b.pageResourceSummary()
			tabs := capture.b.tabSummary()
			capture.a.postLog("⚠ 擷取用的分頁 15 秒內沒有送出任何分集資料請求，改開一個新分頁重新載入（" + page + "；當時的分頁：" + tabs + "）。\r\n")
			capture.a.diagNote("15 秒沒有分集資料請求，已改開新分頁；" + capture.tikDiag.summary(capture.tiktokEpisode) + "；" + page + "；當時的分頁：" + tabs)
			if err := capture.reopenAndReload(); err != nil {
				return browserMediaCandidate{}, err
			}
			// Count only what the new tab sends.
			capture.tikDiag.reset()
		case <-capture.challengeCh:
			// 很多網站在背景載入不需要點的驗證元件：先等 8 秒，仍沒有影片才叫使用者
			if !capture.challengeSeen && !capture.challengePrompted && capture.best.URL == "" && !capture.isTikTokDrama {
				capture.challengeSeen = true
				if !capture.hint.Stop() {
					select {
					case <-capture.hint.C:
					default:
					}
				}
				capture.hint.Reset(8 * time.Second)
			}
		case <-capture.hint.C:
			if candidate, err, done := capture.onHint(); done {
				return candidate, err
			}
		case c := <-capture.candidates:
			if candidate, err, done := capture.onCandidate(c); done {
				return candidate, err
			}
		case <-capture.settleC:
			if candidate, err, done := capture.onSettle(); done {
				return candidate, err
			}
		case <-capture.deadline.C:
			return capture.onDeadline()
		}
	}
}

func (capture *cdpCapture) onHint() (browserMediaCandidate, error, bool) {
	if capture.challengePrompted {
		return browserMediaCandidate{}, nil, false
	}
	if capture.challengeSeen && !capture.isTikTokDrama && (capture.best.URL == "" || capture.onlyStatic()) {
		// 驗證畫面還在：不重新整理（會洗掉驗證框），直接請使用者親手完成
		capture.challengePrompted = true
		capture.userWait = true
		capture.b.reveal(capture.a.scale)
		capture.a.loginBrowser.Store(capture.b)
		capture.a.emit("login", map[string]interface{}{"waiting": true, "reason": "action"})
		capture.a.flashMainWindow()
		procMessageBeep.Call(mbIconAsterisk)
		capture.a.postLog("ℹ 網頁正在顯示「確認您是真人」驗證，已把瀏覽器視窗叫到前面：請在該視窗親手點選驗證框，通過後程式會繼續擷取（本工具不會自動操作驗證）。若點了之後驗證一直重複出現，表示這個網站不接受本工具的擷取方式，請改用一般瀏覽器觀看。程式最多再等 10 分鐘。\r\n")
		capture.a.postStatus("狀態：等待真人驗證完成（可在瀏覽器視窗操作，最多 10 分鐘）…")
		if !capture.deadline.Stop() {
			select {
			case <-capture.deadline.C:
			default:
			}
		}
		capture.deadline.Reset(10 * time.Minute)
		return browserMediaCandidate{}, nil, false
	}
	if !capture.isTikTokDrama && !capture.reloaded && (capture.best.URL == "" || capture.onlyStatic()) {
		// Usually the site was just slow: reload the page once before
		// calling the user.
		capture.reloaded = true
		if capture.best.URL == "" {
			capture.a.postLog("ℹ 25 秒內沒有偵測到影片，重新整理一次頁面。\r\n")
		} else {
			capture.a.postLog(fmt.Sprintf("ℹ 網頁還沒載入第 %d 集的播放器，重新整理一次頁面。\r\n", capture.wantEp))
		}
		_ = capture.b.send("Page.navigate", map[string]interface{}{"url": capture.rawURL})
		capture.hint.Reset(25 * time.Second)
		if !capture.deadline.Stop() {
			select {
			case <-capture.deadline.C:
			default:
			}
		}
		capture.deadline.Reset(40 * time.Second)
		return browserMediaCandidate{}, nil, false
	}
	if !capture.isTikTokDrama && capture.onlyStatic() {
		if builtInVideoIsEpisode(capture.ctx, capture.rawURL, capture.wantEp, capture.staticSources(capture.best.URL)) {
			capture.a.postLog(fmt.Sprintf("✓ 網頁內建的影片每一集都不同，就是第 %d 集的影片。\r\n", capture.wantEp))
			capture.awaitSubtitles()
			capture.awaitHLSKey()
			return capture.finish(capture.best), nil, true
		}
	}
	if (capture.best.URL == "" || capture.isStatic(capture.best.URL)) && !capture.isTikTokDrama {
		capture.userWait = true
		capture.b.reveal(capture.a.scale)
		capture.a.loginBrowser.Store(capture.b)
		capture.a.emit("login", map[string]interface{}{"waiting": true, "reason": "action"})
		// Call the user back to the computer: the taskbar button
		// flashes and the system information sound plays.
		capture.a.flashMainWindow()
		procMessageBeep.Call(mbIconAsterisk)
		capture.a.postLog("ℹ 重新整理後仍未偵測到正片串流，已把瀏覽器視窗叫到前面：若畫面正在播廣告、要求按播放、登入或驗證，請直接在該視窗操作（本工具不會自動點擊廣告）。程式最多再等 10 分鐘。\r\n")
		capture.a.postStatus("狀態：等待正片開始播放（可在瀏覽器視窗操作，最多 10 分鐘）…")
		if !capture.deadline.Stop() {
			select {
			case <-capture.deadline.C:
			default:
			}
		}
		capture.deadline.Reset(10 * time.Minute)
	} else if capture.best.URL == "" {
		capture.b.reveal(capture.a.scale)
		capture.a.postLog("ℹ 25 秒內尚未偵測到影片串流，已把瀏覽器視窗叫到前面：若畫面要求登入、驗證或需要按播放，請直接在該視窗操作，程式會繼續監聽。\r\n")
	}
	return browserMediaCandidate{}, nil, false
}

func (capture *cdpCapture) onDeadline() (browserMediaCandidate, error) {
	if capture.best.URL != "" && capture.isStatic(capture.best.URL) {
		c, ok := capture.notStatic()
		if !ok && builtInVideoIsEpisode(capture.ctx, capture.rawURL, capture.wantEp, capture.staticSources(capture.best.URL)) {
			capture.a.postLog(fmt.Sprintf("✓ 網頁內建的影片每一集都不同，就是第 %d 集的影片。\r\n", capture.wantEp))
			c, ok = capture.best, true
		}
		if !ok {
			return browserMediaCandidate{}, fmt.Errorf("網頁沒有載入第 %d 集的影片，只播放了頁面內建的影片（每一集都相同），為避免存成錯的集數，沒有下載", capture.wantEp)
		}
		capture.best = c
	}
	if capture.best.URL != "" && !capture.isAd(capture.best.URL) {
		capture.awaitSubtitles()
		capture.awaitHLSKey()
		return capture.finish(capture.best), nil
	}
	if capture.b.networkEvents.Load() == capture.navEvents {
		capture.a.diagNote("擷取分頁：" + capture.b.tabSummary())
		return browserMediaCandidate{}, errors.New("擷取用的瀏覽器分頁沒有任何網路活動（程式看不到頁面載入）；請關閉擷取用的瀏覽器視窗後重試，程式會重新開啟")
	}
	if capture.isTikTokDrama {
		summary := capture.tikDiag.summary(capture.tiktokEpisode) + "；" + capture.b.pageResourceSummary()
		if capture.reopened {
			summary += "（已換過新分頁）"
		}
		capture.a.postLog("ℹ 分集擷取診斷：" + summary + "\r\n")
		capture.a.diagNote("分集擷取：" + summary + "；擷取分頁：" + capture.b.tabSummary())
		if capture.tikDiag.noKnownAPI() {
			return browserMediaCandidate{}, fmt.Errorf("已登入，但 90 秒內頁面沒有送出第 %d 集的分集資料；網站的資料格式可能已變更", capture.tiktokEpisode)
		}
		return browserMediaCandidate{}, fmt.Errorf("已登入，但 90 秒內沒有取得第 %d 集的影片網址；這一集可能無法以你的帳號存取", capture.tiktokEpisode)
	}
	capture.adMu.Lock()
	sawAd := capture.adNoticed
	capture.adMu.Unlock()
	if sawAd {
		return browserMediaCandidate{}, errors.New("頁面只播放了廣告，等待期間正片沒有開始播放；可在瀏覽器視窗看完或關閉廣告後重試")
	}
	return browserMediaCandidate{}, errors.New("DevTools 網路監聽已正常啟動，但等待期間未出現可下載的 HLS／DASH／MP4；若網站畫面要求登入或按播放，請在自動開啟的瀏覽器完成一次操作後重試")
}
