package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// The background capture browser is user-selectable (進階設定 → 擷取用瀏覽器).
// The default follows the Windows default browser; when that is not a
// Chromium browser (DevTools pipe + extension loading are Chromium-only) or is
// not installed, the first installed browser in captureBrowsers is used.

type captureBrowserInfo struct {
	Key   string
	Name  string
	Paths []string
}

func captureBrowsers() []captureBrowserInfo {
	pf := os.Getenv("ProgramFiles")
	pf86 := os.Getenv("ProgramFiles(x86)")
	local := os.Getenv("LOCALAPPDATA")
	join := func(base string, parts ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, parts...)...)
	}
	return []captureBrowserInfo{
		{"edge", "Microsoft Edge", []string{
			join(pf86, "Microsoft", "Edge", "Application", "msedge.exe"),
			join(pf, "Microsoft", "Edge", "Application", "msedge.exe"),
			join(local, "Microsoft", "Edge", "Application", "msedge.exe"),
		}},
		{"chrome", "Google Chrome", []string{
			join(pf, "Google", "Chrome", "Application", "chrome.exe"),
			join(pf86, "Google", "Chrome", "Application", "chrome.exe"),
			join(local, "Google", "Chrome", "Application", "chrome.exe"),
		}},
		{"brave", "Brave", []string{
			join(pf, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			join(pf86, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			join(local, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		}},
		{"vivaldi", "Vivaldi", []string{
			join(local, "Vivaldi", "Application", "vivaldi.exe"),
			join(pf, "Vivaldi", "Application", "vivaldi.exe"),
		}},
	}
}

// installedPath returns the first existing executable of the browser.
func (b captureBrowserInfo) installedPath() string {
	for _, p := range b.Paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func captureBrowserByKey(key string) (captureBrowserInfo, bool) {
	for _, b := range captureBrowsers() {
		if b.Key == key {
			return b, true
		}
	}
	return captureBrowserInfo{}, false
}

// systemDefaultBrowser returns the key ("edge", "chrome", "firefox", …) and a
// display name of the browser Windows opens https links with.
func systemDefaultBrowser() (string, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`, registry.QUERY_VALUE)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	progID, _, err := k.GetStringValue("ProgId")
	if err != nil {
		return "", ""
	}
	return browserKeyForProgID(progID)
}

func browserKeyForProgID(progID string) (string, string) {
	p := strings.ToLower(progID)
	switch {
	case strings.HasPrefix(p, "msedgehtm"):
		return "edge", "Microsoft Edge"
	case strings.HasPrefix(p, "chromehtml"):
		return "chrome", "Google Chrome"
	case strings.HasPrefix(p, "bravehtml"):
		return "brave", "Brave"
	case strings.HasPrefix(p, "vivaldihtm"):
		return "vivaldi", "Vivaldi"
	case strings.HasPrefix(p, "firefoxurl"):
		return "firefox", "Firefox"
	case p == "":
		return "", ""
	}
	return "other", progID
}

// resolveCaptureBrowser picks the browser for pref ("", "default" or a key).
// note explains a substitution; it is empty when the choice was used as is.
func resolveCaptureBrowser(pref string) (b captureBrowserInfo, path, note string, err error) {
	pref = strings.TrimSpace(pref)
	want := pref
	wantName := ""
	if pref == "" || pref == "default" {
		want, wantName = systemDefaultBrowser()
	} else if info, ok := captureBrowserByKey(pref); ok {
		wantName = info.Name
	}
	if info, ok := captureBrowserByKey(want); ok {
		if p := info.installedPath(); p != "" {
			return info, p, "", nil
		}
	}
	// Fallback order keeps Chrome last: its branded build ignores
	// --load-extension, so the extension backup path cannot run there.
	for _, key := range []string{"edge", "brave", "vivaldi", "chrome"} {
		info, _ := captureBrowserByKey(key)
		if p := info.installedPath(); p != "" {
			switch {
			case wantName == "":
				note = "無法判斷系統預設瀏覽器，改用 " + info.Name
			case want == "firefox" || want == "other":
				note = "系統預設瀏覽器（" + wantName + "）不支援背景擷取，改用 " + info.Name
			default:
				note = "找不到 " + wantName + "，改用 " + info.Name
			}
			return info, p, note, nil
		}
	}
	return captureBrowserInfo{}, "", "", errors.New("找不到 Microsoft Edge／Chrome／Brave／Vivaldi，無法啟動背景瀏覽器擷取")
}

// findCaptureBrowser resolves the user's choice for this task.
func (a *app) findCaptureBrowser() (captureBrowserInfo, string, error) {
	b, path, note, err := resolveCaptureBrowser(a.capturePrefValue())
	if err != nil {
		return b, "", err
	}
	if note != "" {
		a.postLog("ℹ 擷取用瀏覽器：" + note + "。\r\n")
	}
	return b, path, nil
}

func (a *app) capturePrefValue() string {
	if v, ok := a.capturePref.Load().(string); ok {
		return v
	}
	return ""
}

// captureProfileDirFor keeps one dedicated profile per browser: profiles are
// not safely interchangeable between browsers and versions. Edge keeps the
// original folder so sign-ins made with earlier versions stay valid.
func (a *app) captureProfileDirFor(key string) string {
	if dir := strings.TrimSpace(os.Getenv("AIXAI_CAPTURE_PROFILE")); dir != "" {
		return dir
	}
	if key == "" || key == "edge" {
		return filepath.Join(a.appDir, "capture-cdp-profile")
	}
	return filepath.Join(a.appDir, "capture-cdp-profile-"+key)
}

// allCaptureProfileDirs lists every capture profile a leftover browser could hold.
func (a *app) allCaptureProfileDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	for _, b := range captureBrowsers() {
		d := a.captureProfileDirFor(b.Key)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// captureBrowserOptions are the choices shown in the UI. Browsers that are not
// installed are listed with a note so the user knows why they cannot be used.
func captureBrowserOptions() []uiOption {
	defKey, defName := systemDefaultBrowser()
	label := "系統預設瀏覽器"
	if defName != "" {
		label += "（目前：" + defName + "）"
	}
	hint := ""
	if _, ok := captureBrowserByKey(defKey); defKey != "" && !ok {
		hint = defName + " 不支援背景擷取，會自動改用已安裝的 Edge／Chrome 等瀏覽器"
	}
	opts := []uiOption{{Value: "default", Label: label, Hint: hint}}
	for _, b := range captureBrowsers() {
		l := b.Name
		if b.installedPath() == "" {
			l += "（未安裝）"
		}
		opts = append(opts, uiOption{Value: b.Key, Label: l})
	}
	return opts
}
