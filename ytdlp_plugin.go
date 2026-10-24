package main

import (
	"bytes"
	_ "embed"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// aixaiFixesPlugin is a temporary yt-dlp extractor fix for a site whose page
// layout changed before yt-dlp shipped the matching update. The bundled
// extractor still runs first; the plugin only takes over when it fails, and
// it overrides nothing once yt-dlp itself contains the fix
// (ytdlp_plugins/aixai_fixes.py).
//
//go:embed ytdlp_plugins/aixai_fixes.py
var aixaiFixesPlugin []byte

var ytDlpPluginMu sync.Mutex

// ytDlpPluginDir is the folder given to --plugin-dirs; yt-dlp looks for
// <dir>\<package>\yt_dlp_plugins\extractor\*.py inside it.
func (a *appShared) ytDlpPluginDir() string {
	return filepath.Join(a.binDir, "yt-dlp-plugins")
}

// ensureYtDlpPlugin writes the embedded plugin when it is missing or differs
// from this build's copy.
func (a *appShared) ensureYtDlpPlugin() error {
	ytDlpPluginMu.Lock()
	defer ytDlpPluginMu.Unlock()
	dir := filepath.Join(a.ytDlpPluginDir(), "aixai", "yt_dlp_plugins", "extractor")
	path := filepath.Join(dir, "aixai_fixes.py")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, aixaiFixesPlugin) {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, aixaiFixesPlugin, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// needsYtDlpPlugin reports whether the plugin covers this address.
func needsYtDlpPlugin(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	return host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com") || host == "b23.tv"
}

// ytDlpPluginArgs returns the options that load the plugin for rawURL, or nil
// when it does not apply or cannot be written. "default" keeps the user's
// own plugin folders searched as before.
func (a *app) ytDlpPluginArgs(rawURL string) []string {
	if !needsYtDlpPlugin(rawURL) {
		return nil
	}
	if err := a.ensureYtDlpPlugin(); err != nil {
		a.postLog("⚠ 無法準備下載元件的暫時修正，改用原本的元件：" + err.Error() + "\r\n")
		return nil
	}
	return []string{"--plugin-dirs", "default", "--plugin-dirs", a.ytDlpPluginDir()}
}
