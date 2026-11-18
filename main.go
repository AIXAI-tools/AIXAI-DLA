//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	appTitle   = "AIXAI 萬能下載工具"
	appVersion = "4.2.40"

	ytDlpURL               = "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp.exe"
	ytDlpChecksumURL       = "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/SHA2-256SUMS"
	ytDlpLatestAPI         = "https://api.github.com/repos/yt-dlp/yt-dlp-nightly-builds/releases/latest"
	luxLatestAPI           = "https://api.github.com/repos/iawia002/lux/releases/latest"
	tiktokCrawlerLatestAPI = "https://api.github.com/repos/hatienl0i2612/tiktok-crawler/releases/latest"
	denoURL                = "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip"
	denoChecksumURL        = "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip.sha256sum"
	ffmpegURL              = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
	ffmpegChecksumURL      = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip.sha256"

	// RichEdit stores text internally as UTF-16. Keeping about five million
	// UTF-16 code units limits the visible log to roughly 10 MiB. Once the
	// limit is crossed, the oldest records at the bottom are removed.
	maxLogUTF16Units   = 5 * 1024 * 1024
	trimLogUTF16Units  = 4 * 1024 * 1024
	maxPendingLogBytes = 10 * 1024 * 1024
)

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func main() {
	// Win32 windows, their message queue, and COM STA calls are thread-affine.
	// Without this lock the Go scheduler may move the main goroutine to a
	// different OS thread, leaving the window's original message queue
	// unpumped and causing intermittent "(沒有回應)" freezes.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	installForeignContinueHandler()

	if runtime.GOARCH != "amd64" {
		// This binary is currently distributed for Windows x64.
	}
	setDPIAwareness()
	procCoInitializeEx.Call(0, COINIT_APARTMENTTHREADED)
	defer procCoUninitialize.Call()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	a, err := newApp(hInst)
	if err != nil {
		messageBox(0, "啟動失敗", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	globalApp = a
	if err := a.runWebUI(); err != nil {
		messageBox(0, "程式錯誤", err.Error(), MB_OK|MB_ICONERROR)
	}
}

func setDPIAwareness() {
	// PER_MONITOR_AWARE_V2 = -4
	if err := procSetProcessDpiAwarenessContext.Find(); err == nil {
		procSetProcessDpiAwarenessContext.Call(^uintptr(3))
		return
	}
	if err := procSetProcessDpiAwareness.Find(); err == nil {
		procSetProcessDpiAwareness.Call(1)
	}
}

func newApp(hInst uintptr) (*app, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		var err error
		local, err = os.UserConfigDir()
		if err != nil {
			return nil, err
		}
	}
	appDir := filepath.Join(local, "AIXAI-YTDLP-OneClick")
	binDir := filepath.Join(appDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()
	defaultOut := filepath.Join(home, "Downloads", "AIXAI-YTDLP")

	rootCtx, rootCancel := context.WithCancel(context.Background())
	a := &app{
		appShared: &appShared{
			hInstance:         hInst,
			dpi:               96,
			scale:             1,
			appDir:            appDir,
			binDir:            binDir,
			configPath:        filepath.Join(appDir, "settings.json"),
			ytDlpPath:         filepath.Join(binDir, "yt-dlp.exe"),
			luxPath:           filepath.Join(binDir, "lux.exe"),
			tiktokCrawlerPath: filepath.Join(binDir, "tiktok_crawler.exe"),
			ffmpegPath:        filepath.Join(binDir, "ffmpeg.exe"),
			ffprobePath:       filepath.Join(binDir, "ffprobe.exe"),
			denoPath:          filepath.Join(binDir, "deno.exe"),
			statusQueue:       make(chan string, 128),
			doneQueue:         make(chan doneInfo, 8),
			captureSem:        make(chan struct{}, 1),
			envSem:            make(chan struct{}, 1),
			rootCtx:           rootCtx,
			rootCancel:        rootCancel,
		},
		cfg: settings{OutputFolder: defaultOut, Mode: 0, NoPlaylist: true, PlaylistFolder: true, SequenceStart: 1, SequenceEnd: 50, MaxJobs: 2},
	}
	a.main = a
	a.loadSettings()
	if a.cfg.OutputFolder == "" {
		a.cfg.OutputFolder = defaultOut
	}
	return a, nil
}

func validateCookieFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("無法讀取 Cookie 檔：%w", err)
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	head := strings.TrimPrefix(string(buf[:n]), "\ufeff")
	if strings.Contains(head, "# Netscape HTTP Cookie File") || strings.Contains(head, "# HTTP Cookie File") {
		return nil
	}
	return errors.New("沒有偵測到 Netscape Cookie 檔標頭。yt-dlp 通常需要第一行為 # Netscape HTTP Cookie File 或 # HTTP Cookie File")
}

func (a *app) initializeLocalState() {
	ready := validFile(a.ytDlpPath, 1<<20) && validFile(a.denoPath, 1<<20) &&
		validFile(a.ffmpegPath, 1<<20) && validFile(a.ffprobePath, 1<<20)
	if ready {
		a.postEnv("下載元件已就緒")
		a.postStatus("準備完成，貼上網址即可開始。")
		return
	}
	a.postEnv("首次使用：開始下載時會自動準備 yt-dlp、FFmpeg 與 Deno")
	a.postStatus("準備完成，貼上網址即可開始。")
}

func (a *app) loadSettings() {
	// v2.7 defaults to privacy-conscious automatic authentication: public
	// downloads are tried without cookies first, and browser cookies are only
	// attempted after the site explicitly asks for sign-in.
	a.cfg.CookieBrowser = "auto"
	data, err := os.ReadFile(a.configPath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &a.cfg)
	// Migrate v2.6's empty value to the new automatic mode. Users can still
	// explicitly choose "none" in the UI.
	if strings.TrimSpace(a.cfg.CookieBrowser) == "" {
		a.cfg.CookieBrowser = "auto"
	}
	if a.cfg.SequenceStart <= 0 {
		a.cfg.SequenceStart = 1
	}
	if a.cfg.SequenceEnd <= 0 {
		a.cfg.SequenceEnd = 50
	}
	if strings.TrimSpace(a.cfg.CaptureBrowser) == "" {
		a.cfg.CaptureBrowser = "default"
	}
	a.cfg.MaxJobs = clampMaxJobs(a.cfg.MaxJobs)
	a.maxJobs.Store(int32(a.cfg.MaxJobs))
	a.capturePref.Store(a.cfg.CaptureBrowser)
	a.setDataFolderPref(a.cfg.DataFolder)
}

func validFile(path string, minSize int64) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() >= minSize
}

func runSmallContext(ctx context.Context, exe string, args ...string) string {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	cmd.Dir = filepath.Dir(exe)
	b, err := cmd.CombinedOutput()
	if err != nil && len(b) == 0 {
		return ""
	}
	return strings.TrimSpace(string(b))
}
func firstLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func splitArgs(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}
	var args []string
	var b strings.Builder
	inQuotes := false
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			args = append(args, b.String())
			b.Reset()
		}
	}
	for _, r := range input {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && inQuotes {
			escaped = true
			continue
		}
		if r == '"' {
			inQuotes = !inQuotes
			continue
		}
		if (r == ' ' || r == '\t') && !inQuotes {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteRune('\\')
	}
	if inQuotes {
		return nil, errors.New("雙引號未成對")
	}
	flush()
	return args, nil
}

func multiString(parts ...string) []uint16 {
	var out []uint16
	for _, part := range parts {
		out = append(out, utf16.Encode([]rune(part))...)
		out = append(out, 0)
	}
	out = append(out, 0)
	return out
}

func messageBox(hwnd uintptr, title, text string, flags uint32) int {
	r, _, _ := procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))), uintptr(unsafe.Pointer(utf16Ptr(title))), uintptr(flags))
	return int(r)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Ensure unicode/utf16 stays linked on some older Go toolchains used by downstream builders.
var _ = utf16.Encode
