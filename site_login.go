//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Sign-in kept in the dedicated capture browser. Some posts only open for a
// signed-in account (for example age-restricted ones) and the site does not
// say so in a way yt-dlp recognises. When such a page fails, the user signs in
// once in the AIXAI capture browser (its own profile, never the everyday
// browser); the session stays in that profile. For each retry the site's
// cookies are handed to yt-dlp through a temporary cookies file in the app
// folder, removed right after the run. Cookie values are never logged.

type siteLogin struct {
	domains    []string // hosts this sign-in covers
	loginURL   string   // page opened for the user to sign in
	cookieURLs []string // addresses whose cookies are exported
	session    []string // cookies that must all be present when signed in
}

var siteLogins = []siteLogin{
	{
		domains:    []string{"facebook.com", "fb.watch"},
		loginURL:   "https://www.facebook.com/login",
		cookieURLs: []string{"https://www.facebook.com/", "https://m.facebook.com/", "https://web.facebook.com/"},
		session:    []string{"c_user", "xs"},
	},
}

func siteLoginFor(rawURL string) *siteLogin {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	for i := range siteLogins {
		if hostMatches(host, siteLogins[i].domains...) {
			return &siteLogins[i]
		}
	}
	return nil
}

// wantsCaptureLogin reports whether a failed yt-dlp run may be fixed by
// signing in, and whether the user's sign-in setting allows trying it: an
// explicit choice (不使用登入, a browser, a cookies file) is respected.
func wantsCaptureLogin(rawURL string, err error, cfg settings) bool {
	if err == nil || siteLoginFor(rawURL) == nil {
		return false
	}
	if strings.TrimSpace(cfg.CookieFile) != "" {
		return false
	}
	if m := strings.TrimSpace(cfg.CookieBrowser); m != "" && m != "auto" {
		return false
	}
	return isExtractorFailure(err) || isAuthenticationRequired(err)
}

// siteCookies returns the capture browser's cookies for the site.
func (b *cdpBrowser) siteCookies(s *siteLogin) []map[string]interface{} {
	reply, err := b.pageCall("Network.getCookies", map[string]interface{}{"urls": s.cookieURLs})
	if err != nil {
		return nil
	}
	list, _ := reply.Result["cookies"].([]interface{})
	var out []map[string]interface{}
	for _, raw := range list {
		if c, ok := raw.(map[string]interface{}); ok {
			out = append(out, c)
		}
	}
	return out
}

func signedIn(cookies []map[string]interface{}, s *siteLogin) bool {
	have := map[string]bool{}
	for _, c := range cookies {
		if cdpString(c, "value") != "" {
			have[cdpString(c, "name")] = true
		}
	}
	for _, name := range s.session {
		if !have[name] {
			return false
		}
	}
	return len(s.session) > 0
}

// netscapeCookies writes cookies in the cookies.txt format yt-dlp reads.
func netscapeCookies(cookies []map[string]interface{}) string {
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	for _, c := range cookies {
		domain := cdpString(c, "domain")
		name := cdpString(c, "name")
		if domain == "" || name == "" {
			continue
		}
		sub := "FALSE"
		if strings.HasPrefix(domain, ".") {
			sub = "TRUE"
		}
		path := cdpString(c, "path")
		if path == "" {
			path = "/"
		}
		secure := "FALSE"
		if v, _ := c["secure"].(bool); v {
			secure = "TRUE"
		}
		expires := int64(0)
		if v, ok := c["expires"].(float64); ok && v > 0 {
			expires = int64(v)
		}
		prefix := ""
		if v, _ := c["httpOnly"].(bool); v {
			prefix = "#HttpOnly_"
		}
		fmt.Fprintf(&b, "%s%s\t%s\t%s\t%s\t%d\t%s\t%s\n", prefix, domain, sub, path, secure, expires, name, cdpString(c, "value"))
	}
	return b.String()
}

// captureLoginCookies makes sure the capture browser is signed in to the
// site (asking the user once) and returns its cookies.
func (a *app) captureLoginCookies(ctx context.Context, s *siteLogin) ([]map[string]interface{}, error) {
	select {
	case a.captureSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.captureSem }()
	b, err := a.captureBrowser(ctx)
	if err != nil {
		return nil, err
	}
	if cookies := b.siteCookies(s); signedIn(cookies, s) {
		a.postLog("✓ 沿用 AIXAI 專用瀏覽器中保存的網站登入。\r\n")
		return cookies, nil
	}
	if err := b.send("Page.navigate", map[string]interface{}{"url": s.loginURL}); err != nil {
		return nil, err
	}
	b.reveal(a.scale)
	a.loginBrowser.Store(b)
	defer a.loginBrowser.Store(nil)
	a.emit("login", map[string]interface{}{"waiting": true})
	defer a.emit("login", map[string]interface{}{"waiting": false})
	a.postLog("ℹ 這部影片可能要登入才能觀看（例如有年齡限制）：已在 " + b.name + " 視窗開啟登入頁。請登入一次（建議使用專用帳號，不要用主帳號），偵測到登入後會自動繼續。\r\n")
	a.postLog("ℹ 這是 AIXAI 專用的瀏覽器，不會讀取您日常瀏覽器的資料；登入狀態會保存，之後不必再登入。關閉這個視窗即取消登入。\r\n")
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
			return nil, ctx.Err()
		case <-b.readerDone:
			a.loginCancelled.Store(true)
			return nil, errLoginCancelled
		case <-time.After(2 * time.Second):
		}
		if a.stopRequested.Load() {
			return nil, errStopped
		}
		if cookies := b.siteCookies(s); signedIn(cookies, s) {
			a.postLog("✓ 偵測到登入成功，已保存登入狀態；瀏覽器回到背景繼續下載。\r\n")
			time.Sleep(1500 * time.Millisecond)
			b.navigateBlank()
			b.hide()
			return b.siteCookies(s), nil
		}
	}
}

// retryWithCaptureLogin runs yt-dlp again with the capture browser's sign-in.
func (a *app) retryWithCaptureLogin(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	s := siteLoginFor(rawURL)
	if s == nil {
		return errors.New("這個網站沒有登入流程")
	}
	a.postStatus("狀態：確認專用瀏覽器的網站登入…")
	cookies, err := a.captureLoginCookies(ctx, s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(a.appDir, "login-cookies-*.txt")
	if err != nil {
		return fmt.Errorf("無法建立暫存登入檔：%w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // holds a session: never left behind
	_, werr := f.WriteString(netscapeCookies(cookies))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("無法寫入暫存登入檔：%w", werr)
	}
	retryCfg := cfg
	retryCfg.CookieFile = tmp
	args, err := a.buildArgs(rawURL, mode, formatID, output, retryCfg)
	if err != nil {
		return err
	}
	a.postLog("→ 使用專用瀏覽器的登入狀態重新下載…\r\n")
	a.postStatus("狀態：使用登入狀態重新下載…")
	return a.runCommand(ctx, args)
}
