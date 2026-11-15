//go:build windows

package main

import (
	"context"
	"fmt"
	"time"
)

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
