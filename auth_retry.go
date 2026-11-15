//go:build windows

// Authentication retries, browser cookie discovery, and cookie headers.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type cookieBrowserOption struct {
	Label string
	Value string
}

var cookieBrowserOptions = []cookieBrowserOption{
	{Label: "自動偵測（建議；只有網站要求登入時才嘗試）", Value: "auto"},
	{Label: "不使用登入狀態", Value: "none"},
	{Label: "Mozilla Firefox（Windows 上優先建議）", Value: "firefox"},
	{Label: "Microsoft Edge（需可讀取該瀏覽器 Cookie）", Value: "edge"},
	{Label: "Google Chrome（需可讀取該瀏覽器 Cookie）", Value: "chrome"},
	{Label: "Brave（需可讀取該瀏覽器 Cookie）", Value: "brave"},
	{Label: "Vivaldi（需可讀取該瀏覽器 Cookie）", Value: "vivaldi"},
}

func cookieHeaderFromFile(path, host string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(strings.ToLower(text), "cookie:") {
		return strings.TrimSpace(text[len("cookie:"):])
	}
	if !strings.Contains(text, "\t") && strings.Contains(text, "=") && strings.Contains(text, ";") {
		return text
	}
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	pairs := []string{}
	seen := map[string]bool{}
	now := time.Now().Unix()
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		// "#HttpOnly_" marks an HttpOnly cookie (often the sign-in one), not
		// a comment; cdpCookiesFromFile reads it the same way.
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fields[0])), ".")
		subdomains := !strings.EqualFold(strings.TrimSpace(fields[1]), "FALSE")
		if domain == "" || !(host == domain || subdomains && strings.HasSuffix(host, "."+domain)) {
			continue
		}
		if exp, err := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64); err == nil && exp > 0 && exp < now {
			continue // expired
		}
		name := strings.TrimSpace(fields[5])
		value := strings.TrimSpace(fields[6])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		pairs = append(pairs, name+"="+value)
	}
	return strings.Join(pairs, "; ")
}

func (a *app) retryWithAuthentication(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings, originalErr error) error {
	// If the user explicitly provided a cookies.txt, it was already tried by
	// buildArgs. Do not combine --cookies with --cookies-from-browser because
	// yt-dlp may write browser cookies back into the cookie jar.
	if strings.TrimSpace(cfg.CookieFile) != "" {
		a.postLog("⚠ 已使用指定 Cookie 檔仍未通過登入驗證。Cookie 可能已失效或被網站輪替。\r\n")
	}

	modeSetting := strings.TrimSpace(cfg.CookieBrowser)
	if modeSetting == "none" {
		return &commandRunError{Cause: originalErr, Summary: authHelpMessage(originalErr.Error())}
	}

	// An explicitly selected browser was already used on the first attempt,
	// unless a cookie file took precedence. If a cookie file was used, try the
	// selected browser once as a fallback.
	if modeSetting != "" && modeSetting != "auto" && modeSetting != "none" {
		if strings.TrimSpace(cfg.CookieFile) == "" {
			return &commandRunError{Cause: originalErr, Summary: authHelpMessage(originalErr.Error())}
		}
		retryCfg := cfg
		retryCfg.CookieFile = ""
		return a.tryBrowserAuth(ctx, rawURL, mode, formatID, output, retryCfg, modeSetting, originalErr)
	}

	if !cfg.UnsafeMode {
		a.postLog("🛡 帳號安全模式：不會自動使用您日常瀏覽器中的登入（避免主帳號因大量下載被限制）。\r\n")
		return &commandRunError{Cause: originalErr, Summary: authHelpMessage(originalErr.Error()) +
			"\n\n🛡 帳號安全模式不會自動借用日常瀏覽器的登入。若確定要登入，請在「網站登入狀態」明確選擇一個瀏覽器（建議用只登入下載專用帳號的瀏覽器），或指定 cookies.txt。"}
	}
	browsers := installedBrowserCandidates()
	if len(browsers) == 0 {
		// Still try Firefox first so portable/profile edge cases can work.
		browsers = []string{"firefox", "edge", "chrome"}
	}
	lastErr := originalErr
	for _, browser := range browsers {
		if a.stopRequested.Load() {
			return errStopped
		}
		retryCfg := cfg
		retryCfg.CookieFile = ""
		retryCfg.CookieBrowser = browser
		err := a.tryBrowserAuth(ctx, rawURL, mode, formatID, output, retryCfg, browser, lastErr)
		if err == nil {
			return nil
		}
		lastErr = err
		// Cookie database/decryption failures are browser-specific; continue to
		// the next installed browser. Authentication failures may simply mean the
		// account in that browser is not signed in, so continue as well.
	}
	return &commandRunError{Cause: lastErr, Summary: authHelpMessage(lastErr.Error())}
}

func (a *app) tryBrowserAuth(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings, browser string, previous error) error {
	name := browserDisplayName(browser)
	a.postLog("→ 嘗試使用 " + name + " 的登入狀態重新下載…\r\n")
	a.postStatus("狀態：正在讀取 " + name + " 登入狀態並重試…")
	args, err := a.buildArgs(rawURL, mode, formatID, output, cfg)
	if err != nil {
		return err
	}
	err = a.runCommand(ctx, args)
	if err == nil {
		a.postLog("✓ 使用 " + name + " 登入狀態重試成功。\r\n")
		return nil
	}
	if errors.Is(err, errStopped) {
		return err
	}
	if isCookieAccessFailure(err) {
		a.postLog("⚠ " + name + " Cookie 無法讀取；若瀏覽器正在執行請先完全關閉，或改用 Firefox／cookies.txt。\r\n")
	} else if isAuthenticationRequired(err) {
		a.postLog("⚠ " + name + " 的登入狀態仍未通過網站驗證，繼續嘗試其他可用瀏覽器。\r\n")
	} else {
		a.postLog("⚠ 使用 " + name + " 重試仍失敗：" + firstLine(err.Error()) + "\r\n")
	}
	return err
}

func browserDisplayName(browser string) string {
	switch browser {
	case "firefox":
		return "Firefox"
	case "edge":
		return "Microsoft Edge"
	case "chrome":
		return "Google Chrome"
	case "brave":
		return "Brave"
	case "vivaldi":
		return "Vivaldi"
	default:
		return browser
	}
}

func browserCookieStoreExists(browser, root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	if browser == "firefox" {
		entries, err := os.ReadDir(root)
		if err != nil {
			return false
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if validFile(filepath.Join(root, entry.Name(), "cookies.sqlite"), 1024) {
				return true
			}
		}
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name != "Default" && !strings.HasPrefix(name, "Profile ") {
			continue
		}
		for _, rel := range []string{filepath.Join("Network", "Cookies"), "Cookies"} {
			if validFile(filepath.Join(root, name, rel), 1024) {
				return true
			}
		}
	}
	return false
}

func installedBrowserCandidates() []string {
	appData := os.Getenv("APPDATA")
	local := os.Getenv("LOCALAPPDATA")
	checks := []struct {
		name string
		path string
	}{
		{"firefox", filepath.Join(appData, "Mozilla", "Firefox", "Profiles")},
		{"edge", filepath.Join(local, "Microsoft", "Edge", "User Data")},
		{"chrome", filepath.Join(local, "Google", "Chrome", "User Data")},
		{"brave", filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data")},
		{"vivaldi", filepath.Join(local, "Vivaldi", "User Data")},
	}
	var out []string
	for _, c := range checks {
		if browserCookieStoreExists(c.name, c.path) {
			out = append(out, c.name)
		}
	}
	return out
}

func authHelpMessage(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "網站要求登入或年齡驗證。"
	}
	return base + "\n\n這不是下載元件損壞，而是網站要求帳號驗證。請在『網站登入狀態』選『自動偵測』或已登入該網站的 Firefox；也可按『選擇…』指定 Netscape 格式 cookies.txt。Windows 上 Edge／Chrome 的 Cookie 可能因資料庫鎖定或加密而無法由 yt-dlp 直接讀取。"
}
