//go:build windows

package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	appTitle   = "AIXAI 萬能下載工具"
	appVersion = "4.0.3"

	ytDlpURL               = "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp.exe"
	ytDlpChecksumURL       = "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/SHA2-256SUMS"
	ytDlpLatestAPI         = "https://api.github.com/repos/yt-dlp/yt-dlp-nightly-builds/releases/latest"
	luxLatestAPI           = "https://api.github.com/repos/iawia002/lux/releases/latest"
	tiktokCrawlerLatestAPI = "https://api.github.com/repos/hatienl0i2612/tiktok-crawler/releases/latest"
	denoURL                = "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip"
	denoChecksumURL        = "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip.sha256sum"
	ffmpegURL              = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
	ffmpegChecksumURL      = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip.sha256"

	defaultTemplate  = "%(title).200B [%(id)s].%(ext)s"
	playlistTemplate = "%(playlist_title).180B/%(playlist_index)03d - %(title).170B [%(id)s].%(ext)s"

	// RichEdit stores text internally as UTF-16. Keeping about five million
	// UTF-16 code units limits the visible log to roughly 10 MiB. Once the
	// limit is crossed, the oldest records at the bottom are removed.
	maxLogUTF16Units   = 5 * 1024 * 1024
	trimLogUTF16Units  = 4 * 1024 * 1024
	maxPendingLogBytes = 10 * 1024 * 1024
)

const (
	WS_OVERLAPPED       = 0x00000000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	WS_THICKFRAME       = 0x00040000
	WS_MINIMIZEBOX      = 0x00020000
	WS_MAXIMIZEBOX      = 0x00010000
	WS_OVERLAPPEDWINDOW = WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU | WS_THICKFRAME | WS_MINIMIZEBOX | WS_MAXIMIZEBOX
	WS_CHILD            = 0x40000000
	WS_VISIBLE          = 0x10000000
	WS_TABSTOP          = 0x00010000
	WS_BORDER           = 0x00800000
	WS_HSCROLL          = 0x00100000
	WS_VSCROLL          = 0x00200000
	WS_CLIPCHILDREN     = 0x02000000

	CS_VREDRAW = 0x0001
	CS_HREDRAW = 0x0002

	WS_EX_CLIENTEDGE = 0x00000200

	ES_LEFT        = 0x0000
	ES_MULTILINE   = 0x0004
	ES_AUTOVSCROLL = 0x0040
	ES_AUTOHSCROLL = 0x0080
	ES_WANTRETURN  = 0x1000
	ES_READONLY    = 0x0800
	ES_NOHIDESEL   = 0x0100

	BS_PUSHBUTTON    = 0x00000000
	BS_DEFPUSHBUTTON = 0x00000001
	BS_AUTOCHECKBOX  = 0x00000003

	CBS_DROPDOWNLIST = 0x0003
	CBS_HASSTRINGS   = 0x0200

	SS_LEFT           = 0x00000000
	SS_LEFTNOWORDWRAP = 0x0000000C
	SS_ENDELLIPSIS    = 0x00004000
	SS_CENTER         = 0x00000001

	SW_HIDE       = 0
	SW_SHOWNORMAL = 1
	SW_SHOW       = 5
	SW_RESTORE    = 9

	CW_USEDEFAULT = ^uint32(0x7fffffff)

	WM_CREATE         = 0x0001
	WM_DESTROY        = 0x0002
	WM_SIZE           = 0x0005
	WM_DPICHANGED     = 0x02E0
	designWidth       = 1120
	designHeight      = 760
	WM_CLOSE          = 0x0010
	WM_COMMAND        = 0x0111
	WM_SETFONT        = 0x0030
	WM_SETREDRAW      = 0x000B
	WM_APP            = 0x8000
	WM_CTLCOLORSTATIC = 0x0138
	WM_EXITSIZEMOVE   = 0x0232
	WM_SETICON        = 0x0080

	WM_APP_LOG    = WM_APP + 1
	WM_APP_STATUS = WM_APP + 2
	WM_APP_DONE   = WM_APP + 3
	WM_APP_INIT   = WM_APP + 4

	EM_SETSEL              = 0x00B1
	EM_REPLACESEL          = 0x00C2
	EM_SETREADONLY         = 0x00CF
	EM_GETLINECOUNT        = 0x00BA
	EM_GETFIRSTVISIBLELINE = 0x00CE
	EM_LINEINDEX           = 0x00BB
	EM_LINEFROMCHAR        = 0x00C9
	EM_LINESCROLL          = 0x00B6
	EM_SETLIMITTEXT        = 0x00C5
	EM_EXLIMITTEXT         = 0x0435
	EM_EXLINEFROMCHAR      = 0x0436
	EM_EXSETSEL            = 0x0437
	EM_SETBKGNDCOLOR       = 0x0443

	BM_GETCHECK = 0x00F0
	BM_SETCHECK = 0x00F1
	BST_CHECKED = 1

	CB_ADDSTRING       = 0x0143
	CB_GETCURSEL       = 0x0147
	CB_SETCURSEL       = 0x014E
	CB_SETDROPPEDWIDTH = 0x0160

	BN_CLICKED    = 0
	CBN_SELCHANGE = 1

	MB_OK              = 0x00000000
	MB_ICONERROR       = 0x00000010
	MB_ICONQUESTION    = 0x00000020
	MB_ICONWARNING     = 0x00000030
	MB_ICONINFORMATION = 0x00000040
	MB_YESNO           = 0x00000004
	IDYES              = 6

	IDC_ARROW = 32512

	IMAGE_ICON           = 1
	ICON_SMALL           = 0
	ICON_BIG             = 1
	APP_ICON_RESOURCE_ID = 1

	COLOR_WINDOW = 5

	SM_CXSCREEN = 0
	SM_CYSCREEN = 1

	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
	SWP_NOCOPYBITS = 0x0100

	SPI_GETWORKAREA = 0x0030

	BIF_RETURNONLYFSDIRS = 0x0001
	BIF_EDITBOX          = 0x0010
	BIF_NEWDIALOGSTYLE   = 0x0040

	OFN_PATHMUSTEXIST = 0x00000800
	OFN_FILEMUSTEXIST = 0x00001000
	OFN_EXPLORER      = 0x00080000
	OFN_HIDEREADONLY  = 0x00000004

	COINIT_APARTMENTTHREADED = 0x2

	RDW_INVALIDATE  = 0x0001
	RDW_ERASE       = 0x0004
	RDW_ALLCHILDREN = 0x0080
	RDW_UPDATENOW   = 0x0100
	RDW_FRAME       = 0x0400

	OPAQUE   = 2
	ASFW_ANY = 0xffffffff

	CREATE_NO_WINDOW         = 0x08000000
	CREATE_NEW_PROCESS_GROUP = 0x00000200
)

const (
	idURL = 1001 + iota
	idMode
	idFormat
	idOutput
	idBrowse
	idNoPlaylist
	idPlaylistFolder
	idExtra
	idExtraPreset
	idCookieBrowser
	idCookieFileBrowse
	idCookieFileClear
	idSequence
	idSequenceStart
	idSequenceEnd
	idResume
	idStart
	idStop
	idUpdate
	idOpenFolder
	idLog
	idStatus
	idEnv
	idCopyLog
	idSafeMode
	idAdvanced
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type charRange struct{ CpMin, CpMax int32 }
type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type browseInfo struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName *uint16
	LpszTitle      *uint16
	UlFlags        uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
}

type openFileName struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

type settings struct {
	OutputFolder   string `json:"output_folder"`
	Mode           int    `json:"mode"`
	NoPlaylist     bool   `json:"no_playlist"`
	PlaylistFolder bool   `json:"playlist_folder"`
	ExtraArgs      string `json:"extra_args"`
	CookieBrowser  string `json:"cookie_browser"`
	CookieFile     string `json:"cookie_file"`
	Sequence       bool   `json:"sequence"`
	SequenceStart  int    `json:"sequence_start"`
	SequenceEnd    int    `json:"sequence_end"`
	// UnsafeMode turns 帳號安全模式 off. Stored inverted so that settings files
	// written by older versions (without the key) get safety mode enabled.
	UnsafeMode bool `json:"unsafe_mode"`
	// AdvancedOpen remembers whether 進階設定 is expanded in the narrow layout.
	AdvancedOpen bool `json:"advanced_open"`
	// DisclaimerAccepted is the version of the terms the user agreed to; the
	// UI asks again whenever the terms change.
	DisclaimerAccepted string `json:"disclaimer_accepted"`
}

type doneInfo struct {
	Kind    string
	Err     error
	Stopped bool
}

type commandRunError struct {
	Cause   error
	Summary string
}

func (e *commandRunError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Summary) != "" {
		return strings.TrimSpace(e.Summary)
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "下載程序執行失敗"
}

func (e *commandRunError) Unwrap() error { return e.Cause }

type batchPartialError struct {
	Failures []string
}

func (e *batchPartialError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return ""
	}
	preview := e.Failures
	if len(preview) > 8 {
		preview = preview[:8]
	}
	extra := ""
	if len(e.Failures) > len(preview) {
		extra = fmt.Sprintf("\n…另有 %d 項", len(e.Failures)-len(preview))
	}
	return fmt.Sprintf("連續序號下載已處理完畢，但有 %d 項失敗。\n%s%s", len(e.Failures), strings.Join(preview, "\n"), extra)
}

type parameterPreset struct {
	Label string
	Args  string
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

type ytDlpReleaseInfo struct {
	Version     string
	BinaryURL   string
	ChecksumURL string
	Digest      string
}

type luxReleaseInfo struct {
	Version     string
	ZipURL      string
	ChecksumURL string
	AssetName   string
}

type tiktokCrawlerReleaseInfo struct {
	Version   string
	BinaryURL string
	Digest    string
	AssetName string
}

type haokanStream struct {
	Key    string
	URL    string
	Height int
	SizeMB float64
}

var parameterPresets = []parameterPreset{
	{Label: "▼ 點選常用參數，會自動加入上方欄位", Args: ""},
	{Label: "-x｜只擷取音訊（通常搭配 --audio-format mp3）", Args: "-x"},
	{Label: "--audio-format mp3｜將音訊轉成 MP3（需搭配 -x）", Args: "--audio-format mp3"},
	{Label: "--audio-quality 0｜MP3 使用最佳可變位元率品質", Args: "--audio-quality 0"},
	{Label: "--write-subs｜下載影片提供的人工字幕", Args: "--write-subs"},
	{Label: "--write-auto-subs｜下載平台自動產生的字幕", Args: "--write-auto-subs"},
	{Label: "--sub-langs zh-Hant,zh-Hans,en｜下載繁中、簡中與英文字幕", Args: "--sub-langs zh-Hant,zh-Hans,en"},
	{Label: "--embed-subs｜將字幕嵌入影片檔（需 FFmpeg）", Args: "--embed-subs"},
	{Label: "--write-thumbnail｜另外下載影片縮圖", Args: "--write-thumbnail"},
	{Label: "--embed-thumbnail｜將縮圖嵌入媒體檔", Args: "--embed-thumbnail"},
	{Label: "--embed-metadata｜嵌入標題、作者等媒體資訊", Args: "--embed-metadata"},
	{Label: "--embed-chapters｜將影片章節嵌入媒體檔", Args: "--embed-chapters"},
	{Label: "--no-overwrites｜已有同名檔案時不要覆蓋", Args: "--no-overwrites"},
	{Label: "--download-archive downloaded.txt｜記錄已下載項目，避免重複", Args: "--download-archive downloaded.txt"},
	{Label: "--retries 10｜一般下載失敗時最多重試 10 次", Args: "--retries 10"},
	{Label: "--fragment-retries 10｜片段下載失敗時最多重試 10 次", Args: "--fragment-retries 10"},
	{Label: "--concurrent-fragments 4｜同時下載 4 個影音片段", Args: "--concurrent-fragments 4"},
	{Label: "--limit-rate 5M｜將下載速度限制為每秒約 5 MiB", Args: "--limit-rate 5M"},
	{Label: "--sleep-interval 2｜每個下載開始前等待 2 秒", Args: "--sleep-interval 2"},
	{Label: "--restrict-filenames｜檔名只使用較安全的英數字元", Args: "--restrict-filenames"},
	{Label: "--keep-video｜轉成音訊後保留原始影片檔", Args: "--keep-video"},
	{Label: "--write-info-json｜另存影片完整資訊 JSON 檔", Args: "--write-info-json"},
	{Label: "--force-ipv4｜只使用 IPv4，部分網站連線異常時可嘗試", Args: "--force-ipv4"},
}

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

type app struct {
	// ui is the WebView2 front end (see webui.go).
	ui *webUI

	hwnd      uintptr
	hInstance uintptr
	dpi       int
	scale     float64

	appDir            string
	binDir            string
	configPath        string
	ytDlpPath         string
	luxPath           string
	tiktokCrawlerPath string
	ffmpegPath        string
	ffprobePath       string
	denoPath          string
	resumePath        string

	statusQueue chan string
	doneQueue   chan doneInfo

	// The pending log queue is capped as a second layer of memory protection.
	logMu           sync.Mutex
	logPending      []string
	logHead         int
	logPendingBytes int
	logDropped      int

	// Chronological copy of this session's log, used by the 複製 Log button and
	// mirrored (with secrets redacted) to a text log file in the output folder.
	sessionMu    sync.Mutex
	sessionLog   []string
	sessionBytes int
	logFileMu    sync.Mutex
	logDir       atomic.Value

	// capBrowser is the capture browser shared by all URLs of one download task.
	capBrowser *cdpBrowser
	// captureRunning is true while a capture browser may be alive (checked at
	// shutdown); captureStartFailed stops a batch when it cannot be started.
	captureRunning     atomic.Bool
	captureStartFailed atomic.Bool
	// loginBrowser is the capture browser while it waits for the user to sign
	// in (used by the 顯示登入視窗 button); loginCancelled stops the batch when
	// the user closes that window.
	loginBrowser   atomic.Pointer[cdpBrowser]
	loginCancelled atomic.Bool

	// Coalesce UI wake-up messages so heavy command output cannot flood
	// the Win32 message queue and make the window appear unresponsive.
	logWakePending    atomic.Bool
	statusWakePending atomic.Bool

	busy          atomic.Bool
	stopRequested atomic.Bool
	closing       atomic.Bool
	shutdownOnce  sync.Once
	resourceOnce  sync.Once
	workerWG      sync.WaitGroup

	rootCtx    context.Context
	rootCancel context.CancelFunc
	taskMu     sync.Mutex
	taskCancel context.CancelFunc

	cmdMu       sync.Mutex
	currentCmd  *exec.Cmd
	currentDone chan struct{}

	cfg settings
}

var globalApp *app

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	shcore   = syscall.NewLazyDLL("shcore.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procUpdateWindow                  = user32.NewProc("UpdateWindow")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procSendMessageW                  = user32.NewProc("SendMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procSetWindowTextW                = user32.NewProc("SetWindowTextW")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = user32.NewProc("GetWindowTextLengthW")
	procEnableWindow                  = user32.NewProc("EnableWindow")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procRedrawWindow                  = user32.NewProc("RedrawWindow")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow      = user32.NewProc("AllowSetForegroundWindow")
	procGetClientRect                 = user32.NewProc("GetClientRect")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	procSetFocus                      = user32.NewProc("SetFocus")
	procSetBkMode                     = gdi32.NewProc("SetBkMode")
	procSetBkColor                    = gdi32.NewProc("SetBkColor")
	procSetTextColor                  = gdi32.NewProc("SetTextColor")
	procCreateSolidBrush              = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW                   = gdi32.NewProc("CreateFontW")
	procDeleteObject                  = gdi32.NewProc("DeleteObject")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	procLoadLibraryW                  = kernel32.NewProc("LoadLibraryW")
	procFreeLibrary                   = kernel32.NewProc("FreeLibrary")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procSHBrowseForFolderW            = shell32.NewProc("SHBrowseForFolderW")
	procGetOpenFileNameW              = comdlg32.NewProc("GetOpenFileNameW")
	procSHGetPathFromIDListW          = shell32.NewProc("SHGetPathFromIDListW")
	procShellExecuteW                 = shell32.NewProc("ShellExecuteW")
	procCoTaskMemFree                 = ole32.NewProc("CoTaskMemFree")
	procCoInitializeEx                = ole32.NewProc("CoInitializeEx")
	procCoUninitialize                = ole32.NewProc("CoUninitialize")
)

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func rgb(r, g, b byte) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }
func loword(v uintptr) uint16  { return uint16(v & 0xffff) }
func hiword(v uintptr) uint16  { return uint16((v >> 16) & 0xffff) }

func main() {
	// Win32 windows, their message queue, and COM STA calls are thread-affine.
	// Without this lock the Go scheduler may move the main goroutine to a
	// different OS thread, leaving the window's original message queue
	// unpumped and causing intermittent "(沒有回應)" freezes.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

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
		rootCtx:           rootCtx,
		rootCancel:        rootCancel,
		cfg:               settings{OutputFolder: defaultOut, Mode: 0, NoPlaylist: true, PlaylistFolder: true, SequenceStart: 1, SequenceEnd: 50},
	}
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

func (a *app) ensureYtDlp(ctx context.Context, checkUpdate bool) error {
	exists := validFile(a.ytDlpPath, 1<<20)
	localVersion := ""
	if exists {
		localVersion = normalizeVersion(firstLine(runSmallContext(ctx, a.ytDlpPath, "--version")))
	}

	if exists && !checkUpdate {
		if localVersion != "" {
			a.postLog("✓ yt-dlp 已存在：" + localVersion + "。\r\n")
		} else {
			a.postLog("✓ yt-dlp 執行檔已存在。\r\n")
		}
		return nil
	}

	a.postStatus("狀態：正在比對 yt-dlp 本機與官方版本…")
	a.postLog("檢查 yt-dlp 官方 nightly 版本（只在版本不同時下載）…\r\n")
	release, releaseErr := fetchLatestYtDlpRelease(ctx)
	if releaseErr != nil {
		if exists && localVersion != "" {
			a.postLog("⚠ 無法取得官方最新版本資訊，暫時沿用本機 yt-dlp " + localVersion + "：" + releaseErr.Error() + "\r\n")
			return nil
		}
		release = ytDlpReleaseInfo{BinaryURL: ytDlpURL, ChecksumURL: ytDlpChecksumURL}
		a.postLog("⚠ 無法取得版本資訊，首次安裝改用官方 nightly latest 下載點。\r\n")
	}

	latestVersion := normalizeVersion(release.Version)
	if exists && localVersion != "" && latestVersion != "" && localVersion == latestVersion {
		a.postLog("✓ yt-dlp 已是最新版本：" + localVersion + "，不需重新下載。\r\n")
		return nil
	}

	if exists && localVersion != "" && latestVersion != "" {
		a.postLog("發現新版：本機 " + localVersion + " → 官方 nightly " + latestVersion + "。\r\n")
	} else if !exists {
		a.postLog("尚未安裝 yt-dlp，開始首次配置。\r\n")
	} else {
		a.postLog("本機版本無法辨識，將重新安裝官方版本。\r\n")
	}

	binaryURL := release.BinaryURL
	checksumURL := release.ChecksumURL
	if binaryURL == "" {
		binaryURL = ytDlpURL
	}
	if checksumURL == "" {
		checksumURL = ytDlpChecksumURL
	}

	a.postStatus("狀態：正在下載 yt-dlp…")
	newPath := a.ytDlpPath + ".new"
	_ = os.Remove(newPath)
	if err := downloadFile(ctx, binaryURL, newPath, "yt-dlp", a.postLog, a.postStatus); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("yt-dlp 下載失敗：%w", err)
	}

	if release.Digest != "" {
		if err := verifyDigest(newPath, release.Digest); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("yt-dlp 完整性校驗失敗：%w", err)
		}
	} else if err := verifyRemoteSHA256(ctx, newPath, checksumURL, "yt-dlp.exe"); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("yt-dlp 完整性校驗失敗：%w", err)
	}
	a.postLog("✓ yt-dlp SHA-256 校驗通過。\r\n")

	backup := a.ytDlpPath + ".old"
	_ = os.Remove(backup)
	if validFile(a.ytDlpPath, 1) {
		if err := os.Rename(a.ytDlpPath, backup); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("無法備份舊版 yt-dlp：%w", err)
		}
	}
	if err := os.Rename(newPath, a.ytDlpPath); err != nil {
		_ = os.Rename(backup, a.ytDlpPath)
		_ = os.Remove(newPath)
		return fmt.Errorf("無法安裝新版 yt-dlp：%w", err)
	}
	_ = os.Remove(backup)

	installed := normalizeVersion(firstLine(runSmallContext(ctx, a.ytDlpPath, "--version")))
	if installed != "" {
		a.postLog("✓ yt-dlp 已安裝／更新為 " + installed + "。\r\n")
	} else {
		a.postLog("✓ yt-dlp 已完成安裝／更新。\r\n")
	}
	return nil
}

func fetchLatestYtDlpRelease(ctx context.Context) (ytDlpReleaseInfo, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ytDlpLatestAPI, nil)
	if err != nil {
		return ytDlpReleaseInfo{}, err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return ytDlpReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ytDlpReleaseInfo{}, fmt.Errorf("GitHub API HTTP %s", resp.Status)
	}
	var release githubRelease
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024))
	if err := dec.Decode(&release); err != nil {
		return ytDlpReleaseInfo{}, err
	}
	info := ytDlpReleaseInfo{Version: release.TagName}
	for _, asset := range release.Assets {
		switch strings.ToLower(asset.Name) {
		case "yt-dlp.exe":
			info.BinaryURL = asset.BrowserDownloadURL
			info.Digest = asset.Digest
		case "sha2-256sums":
			info.ChecksumURL = asset.BrowserDownloadURL
		}
	}
	if info.BinaryURL == "" {
		return ytDlpReleaseInfo{}, errors.New("官方 nightly 最新版本中找不到 yt-dlp.exe")
	}
	return info, nil
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if fields := strings.Fields(v); len(fields) > 0 {
		v = fields[0]
	}
	if i := strings.LastIndex(v, "@"); i >= 0 && i+1 < len(v) {
		v = v[i+1:]
	}
	v = strings.TrimPrefix(v, "v")
	return strings.TrimSpace(v)
}

func verifyDigest(path, digest string) error {
	parts := strings.SplitN(strings.TrimSpace(digest), ":", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") {
		return fmt.Errorf("不支援的官方摘要格式：%s", digest)
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, strings.TrimSpace(parts[1])) {
		return fmt.Errorf("SHA-256 不符（預期 %s，實際 %s）", strings.TrimSpace(parts[1]), actual)
	}
	return nil
}

func (a *app) ensureEnvironment(ctx context.Context, checkYtUpdate bool) error {
	if err := os.MkdirAll(a.binDir, 0755); err != nil {
		return err
	}

	if err := a.ensureYtDlp(ctx, checkYtUpdate); err != nil {
		return err
	}

	if !validFile(a.denoPath, 1<<20) {
		a.postLog("下載 Deno JavaScript 執行環境…\r\n")
		a.postStatus("狀態：正在下載 Deno…")
		zipPath := filepath.Join(a.appDir, "deno.download.zip")
		if err := downloadFile(ctx, denoURL, zipPath, "Deno", a.postLog, a.postStatus); err != nil {
			return fmt.Errorf("Deno 下載失敗：%w", err)
		}
		if err := verifyRemoteSHA256(ctx, zipPath, denoChecksumURL, "deno-x86_64-pc-windows-msvc.zip"); err != nil {
			_ = os.Remove(zipPath)
			return fmt.Errorf("Deno 完整性校驗失敗：%w", err)
		}
		a.postLog("✓ Deno SHA-256 校驗通過（已支援 PowerShell Format-List 校驗格式）。\r\n")
		if err := extractSelected(zipPath, a.binDir, map[string]string{"deno.exe": "deno.exe"}); err != nil {
			return fmt.Errorf("Deno 解壓縮失敗：%w", err)
		}
		_ = os.Remove(zipPath)
		a.postLog("✓ Deno 已完成配置。\r\n")
	} else {
		a.postLog("✓ Deno 已存在。\r\n")
	}

	if !validFile(a.ffmpegPath, 1<<20) || !validFile(a.ffprobePath, 1<<20) {
		a.postLog("下載 FFmpeg／ffprobe（檔案較大，請保持網路連線）…\r\n")
		a.postStatus("狀態：正在下載 FFmpeg，首次使用可能需要較久…")
		zipPath := filepath.Join(a.appDir, "ffmpeg.download.zip")
		if err := downloadFile(ctx, ffmpegURL, zipPath, "FFmpeg", a.postLog, a.postStatus); err != nil {
			return fmt.Errorf("FFmpeg 下載失敗：%w", err)
		}
		if err := verifyRemoteSHA256(ctx, zipPath, ffmpegChecksumURL, ""); err != nil {
			_ = os.Remove(zipPath)
			return fmt.Errorf("FFmpeg 完整性校驗失敗：%w", err)
		}
		a.postLog("✓ FFmpeg SHA-256 校驗通過。\r\n")
		wanted := map[string]string{"ffmpeg.exe": "ffmpeg.exe", "ffprobe.exe": "ffprobe.exe"}
		if err := extractSelected(zipPath, a.binDir, wanted); err != nil {
			return fmt.Errorf("FFmpeg 解壓縮失敗：%w", err)
		}
		_ = os.Remove(zipPath)
		a.postLog("✓ FFmpeg 與 ffprobe 已完成配置。\r\n")
	} else {
		a.postLog("✓ FFmpeg 與 ffprobe 已存在。\r\n")
	}

	if !validFile(a.ytDlpPath, 1<<20) || !validFile(a.denoPath, 1<<20) || !validFile(a.ffmpegPath, 1<<20) || !validFile(a.ffprobePath, 1<<20) {
		return errors.New("必要元件檢查未通過")
	}

	if validFile(a.tiktokCrawlerPath, 1<<20) && checkYtUpdate {
		if err := a.ensureTikTokCrawler(ctx); err != nil {
			a.postLog("⚠ TikTok Crawler 更新檢查失敗，暫時沿用現有版本：" + err.Error() + "\r\n")
		}
	}

	impersonationTargets := runSmallContext(ctx, a.ytDlpPath, "--list-impersonate-targets")
	if strings.Contains(strings.ToLower(impersonationTargets), "chrome") {
		a.postLog("✓ yt-dlp 內建 curl_cffi 瀏覽器模擬支援，可用於 Vimeo／Bilibili 相容模式。\r\n")
	} else {
		a.postLog("⚠ 目前 yt-dlp 未列出 Chrome 模擬目標；網站相容重試仍會執行，但可能需要更新 yt-dlp。\r\n")
	}

	notices := `AIXAI YT-DLP OneClick - Third-party components\r\n\r\nyt-dlp: https://github.com/yt-dlp/yt-dlp\r\nTikTok Crawler (on-demand TikTok fallback, MIT): https://github.com/hatienl0i2612/tiktok-crawler\r\nLux (on-demand fallback engine): https://github.com/iawia002/lux\r\nDeno: https://github.com/denoland/deno\r\nFFmpeg Windows build: https://www.gyan.dev/ffmpeg/builds/\r\nFFmpeg project: https://ffmpeg.org/\r\n\r\nPlease refer to each upstream project for its current license terms.\r\n`
	_ = os.WriteFile(filepath.Join(a.appDir, "THIRD_PARTY_NOTICES.txt"), []byte(notices), 0644)

	ytVer := firstLine(runSmallContext(ctx, a.ytDlpPath, "--version"))
	denoVer := firstLine(runSmallContext(ctx, a.denoPath, "--version"))
	ffVer := firstLine(runSmallContext(ctx, a.ffmpegPath, "-version"))
	envText := "環境狀態：已就緒"
	if ytVer != "" {
		envText += "｜yt-dlp " + ytVer
	}
	if denoVer != "" {
		envText += "｜" + denoVer
	}
	if ffVer != "" {
		fields := strings.Fields(ffVer)
		if len(fields) >= 3 {
			envText += "｜FFmpeg " + fields[2]
		}
	}
	if validFile(a.luxPath, 1<<20) {
		luxVer := strings.TrimSpace(firstLine(runSmallContext(ctx, a.luxPath, "-v")))
		if luxVer != "" {
			envText += "｜Lux 備援已就緒"
		}
	}
	if validFile(a.tiktokCrawlerPath, 1<<20) {
		tkVer := strings.TrimSpace(firstLine(runSmallContext(ctx, a.tiktokCrawlerPath, "-version")))
		if tkVer != "" {
			envText += "｜TikTok 備援已就緒"
		}
	}
	a.postEnv(envText)
	a.postLog("✓ 執行環境已就緒。\r\n")
	return nil
}

func (a *app) runURLs(ctx context.Context, urls []string, mode int, formatID, output string, cfg settings) error {
	batchFailures := make([]string, 0)
	safe := !cfg.UnsafeMode
	// All URLs of this task share one capture browser; close it when done.
	defer a.closeCaptureBrowser()
	a.captureStartFailed.Store(false)
	a.loginCancelled.Store(false)
	pacer := newSafePacer()
	if safe {
		a.postLog(safeModeIntro(urls, cfg))
	} else {
		a.postLog("⚠ 帳號安全模式已關閉：不加間隔、失敗會自動重試。大量下載需要登入的網站時，帳號可能被限制或封鎖。\r\n")
	}
	for i, rawURL := range urls {
		if a.stopRequested.Load() {
			return nil
		}
		if safe && !pacer.beforeItem(ctx, a, i, rawURL, cfg) {
			return nil
		}
		a.postLog(fmt.Sprintf("\r\n--- 任務 %d/%d ---\r\n網址：%s\r\n", i+1, len(urls), rawURL))
		a.emitTask(i, "running", "")
		a.postStatus(fmt.Sprintf("狀態：正在處理第 %d/%d 個網址…", i+1, len(urls)))
		site := classifySiteURL(rawURL)

		// These URL families are currently known to fall through yt-dlp's generic
		// extractor. Route them directly to AIXAI's dedicated pipeline so a 50-episode
		// batch does not waste time failing and checking for updates on every episode.
		if (site == "tiktok" && func() bool { _, _, ok := parseTikTokShortDramaURL(rawURL); return ok }()) || site == "dramatip" {
			err := a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg)
			if a.stopRequested.Load() {
				return nil
			}
			if err == nil {
				a.postLog("✓ 本項任務完成。\r\n")
				a.emitTask(i, "done", "")
				continue
			}
			if a.stopRequested.Load() {
				return nil
			}
			if safe && isBlockSignal(err) {
				return blockStopError(err)
			}
			if a.captureStartFailed.Load() {
				return captureStartStopError(err)
			}
			if a.loginCancelled.Load() {
				return &commandRunError{Cause: err, Summary: "已關閉登入視窗、取消登入，因此停止整批下載。需要時重新按「開始下載」即可再次登入。"}
			}
			if cfg.Sequence {
				msg := fmt.Sprintf("第 %d/%d 項失敗：%s", i+1, len(urls), firstLine(err.Error()))
				batchFailures = append(batchFailures, msg)
				a.postLog("⚠ " + msg + "；連續序號模式會跳過此集並繼續下一集。\r\n")
				a.emitTask(i, "failed", firstLine(err.Error()))
				continue
			}
			return fmt.Errorf("第 %d 個任務失敗：%w", i+1, err)
		}

		args, err := a.buildArgs(rawURL, mode, formatID, output, cfg)
		if err != nil {
			return err
		}
		err = a.runCommand(ctx, args)
		if err != nil && safe && isBlockSignal(err) && !a.stopRequested.Load() {
			return blockStopError(err)
		}

		// Any real yt-dlp execution error first triggers the same environment
		// check/update flow as the manual button. This specifically covers cases
		// where a site changes and an older yt-dlp starts returning HTTP 403/
		// extractor errors. The original task is rebuilt and retried exactly once
		// after the environment refresh; only if that still fails do we enter
		// authentication or site-specific compatibility recovery below.
		if err != nil && !a.stopRequested.Load() {
			a.postLog("⚠ 偵測到下載錯誤，正在自動執行「檢查／更新環境」後重試原任務。\r\n")
			a.postStatus("狀態：下載失敗，正在自動檢查／更新環境…")
			refreshErr := a.ensureEnvironment(ctx, true)
			if refreshErr != nil {
				a.postLog("⚠ 自動檢查／更新環境未完整完成，將先以現有元件重試一次：" + refreshErr.Error() + "\r\n")
			} else {
				a.postLog("✓ 自動檢查／更新環境完成，正在重新執行原任務。\r\n")
			}

			retryArgs, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg)
			if buildErr != nil {
				return buildErr
			}
			a.postStatus(fmt.Sprintf("狀態：環境檢查完成，正在重試第 %d/%d 個網址…", i+1, len(urls)))
			retryErr := a.runCommand(ctx, retryArgs)
			if retryErr == nil {
				a.postLog("✓ 更新／檢查環境後，自動重試成功。\r\n")
				err = nil
			} else {
				err = retryErr
				a.postLog("⚠ 更新／檢查環境後重試仍失敗，正在進一步判斷登入驗證或網站相容性。\r\n")
			}
		}

		// Authentication retry is intentionally deferred until a public/no-cookie
		// attempt fails with an authentication-specific message. This avoids
		// reading browser cookies for ordinary public downloads.
		if err != nil && !a.stopRequested.Load() && (isAuthenticationRequired(err) || isCookieAccessFailure(err)) {
			if site == "youtube" || site == "vimeo" || site == "bilibili" || site == "tiktok" {
				a.postLog("⚠ 網站要求登入或年齡驗證，進入登入重試流程。\r\n")
				a.postStatus("狀態：正在嘗試可用的網站登入狀態…")
				err = a.retryWithAuthentication(ctx, rawURL, mode, formatID, output, cfg, err)
			}
		}

		// Vimeo and Bilibili may also need transport/extractor compatibility
		// options that are unrelated to authentication.
		if err != nil && !a.stopRequested.Load() && (site == "bilibili" || site == "vimeo") && !isAuthenticationRequired(err) {
			a.postLog("⚠ 一般自動重試仍失敗，正在啟用網站相容模式再重試一次。\r\n")
			a.postStatus("狀態：正在啟用 " + siteDisplayName(site) + " 相容模式重試…")
			retryArgs, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg)
			if buildErr == nil {
				retryArgs = addSiteCompatibilityArgs(retryArgs, site)
				err = a.runCommand(ctx, retryArgs)
				if err == nil {
					a.postLog("✓ 網站相容模式重試成功。\r\n")
				}
			}
		}

		// TikTok has several URL families that change independently of yt-dlp.
		// For non-Unsupported errors, give the TikTok-specific crawler one chance
		// before surfacing the failure. Unsupported URLs are handled by the common
		// fallback chain below so Lux remains a final fallback.
		if err != nil && !a.stopRequested.Load() && site == "tiktok" && !isUnsupportedURL(err) {
			a.postLog("⚠ TikTok 一般下載仍失敗，正在改用 TikTok 專用備援引擎。\r\n")
			a.postStatus("狀態：正在使用 TikTok 專用備援引擎重試…")
			if crawlerErr := a.runTikTokCrawlerFallback(ctx, rawURL, mode, formatID, output, cfg); crawlerErr == nil {
				err = nil
				a.postLog("✓ TikTok 專用備援引擎處理成功。\r\n")
			} else {
				err = &commandRunError{Cause: crawlerErr, Summary: "yt-dlp 與 TikTok Crawler 都未能完成此 TikTok 任務。\n\nTikTok Crawler：" + firstLine(crawlerErr.Error())}
			}
		}

		// yt-dlp intentionally does not support every website. When it reports
		// Unsupported URL after the normal update/retry flow, automatically
		// switch to AIXAI's fallback chain instead of failing immediately.
		if err != nil && !a.stopRequested.Load() && isUnsupportedURL(err) {
			a.postLog("⚠ yt-dlp 回報 Unsupported URL，啟動 AIXAI 多引擎備援流程。\r\n")
			a.postStatus("狀態：yt-dlp 不支援此網址，正在嘗試備援解析…")
			fallbackErr := a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg)
			if fallbackErr == nil {
				err = nil
				a.postLog("✓ 備援流程處理成功。\r\n")
			} else {
				err = fallbackErr
			}
		}
		if err != nil {
			if a.stopRequested.Load() {
				return nil
			}
			if safe && isBlockSignal(err) {
				return blockStopError(err)
			}
			if a.captureStartFailed.Load() {
				return captureStartStopError(err)
			}
			if a.loginCancelled.Load() {
				return &commandRunError{Cause: err, Summary: "已關閉登入視窗、取消登入，因此停止整批下載。需要時重新按「開始下載」即可再次登入。"}
			}
			if cfg.Sequence {
				msg := fmt.Sprintf("第 %d/%d 項失敗：%s", i+1, len(urls), firstLine(err.Error()))
				batchFailures = append(batchFailures, msg)
				a.postLog("⚠ " + msg + "；連續序號模式會跳過此集並繼續下一集。\r\n")
				a.emitTask(i, "failed", firstLine(err.Error()))
				continue
			}
			return fmt.Errorf("第 %d 個任務失敗：%w", i+1, err)
		}
		a.postLog("✓ 本項任務完成。\r\n")
		a.emitTask(i, "done", "")
	}
	if len(batchFailures) > 0 {
		return &batchPartialError{Failures: batchFailures}
	}
	return nil
}

func isAuthenticationRequired(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	needles := []string{
		"sign in to confirm your age", "sign in to confirm", "login required",
		"authentication required", "members-only", "members only", "private video",
		"age-restricted", "age restricted", "use --cookies-from-browser", "use --cookies",
		"confirm you're not a bot", "confirm you’re not a bot", "requires authentication",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func isCookieAccessFailure(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	needles := []string{
		"could not copy chrome cookie database", "permission denied", "failed to decrypt",
		"could not decrypt", "cookie database", "cookies could not be decrypted",
		"no such table: meta", "browser cookies",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func isUnsupportedURL(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unsupported url")
}

func (a *app) ensureTikTokCrawler(ctx context.Context) error {
	info, infoErr := fetchLatestTikTokCrawlerRelease(ctx)
	exists := validFile(a.tiktokCrawlerPath, 1<<20)
	if infoErr != nil {
		if exists {
			a.postLog("⚠ 無法取得 TikTok Crawler 最新版本資訊，暫時沿用本機版本：" + infoErr.Error() + "\r\n")
			return nil
		}
		return fmt.Errorf("TikTok Crawler 版本資訊取得失敗：%w", infoErr)
	}

	if exists {
		local := strings.TrimSpace(firstLine(runSmallContext(ctx, a.tiktokCrawlerPath, "-version")))
		if local != "" && info.Version != "" && strings.Contains(strings.ToLower(local), strings.ToLower(info.Version)) {
			a.postLog("✓ TikTok Crawler 已是最新版本：" + info.Version + "。\r\n")
			return nil
		}
		if local != "" {
			a.postLog("發現 TikTok Crawler 新版：" + local + " → " + info.Version + "。\r\n")
		}
	} else {
		a.postLog("首次需要 TikTok 專用備援：正在自動配置 TikTok Crawler…\r\n")
	}

	a.postStatus("狀態：正在配置 TikTok 專用備援引擎…")
	newPath := a.tiktokCrawlerPath + ".new"
	_ = os.Remove(newPath)
	if err := downloadFile(ctx, info.BinaryURL, newPath, "TikTok Crawler", a.postLog, a.postStatus); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("TikTok Crawler 下載失敗：%w", err)
	}
	if strings.TrimSpace(info.Digest) == "" {
		_ = os.Remove(newPath)
		return errors.New("TikTok Crawler 官方 Release 未提供 SHA-256 digest，為安全起見不執行")
	}
	if err := verifyDigest(newPath, info.Digest); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("TikTok Crawler 完整性校驗失敗：%w", err)
	}
	a.postLog("✓ TikTok Crawler SHA-256 校驗通過。\r\n")

	backup := a.tiktokCrawlerPath + ".old"
	_ = os.Remove(backup)
	if exists {
		if err := os.Rename(a.tiktokCrawlerPath, backup); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("TikTok Crawler 舊版備份失敗：%w", err)
		}
	}
	if err := os.Rename(newPath, a.tiktokCrawlerPath); err != nil {
		if exists {
			_ = os.Rename(backup, a.tiktokCrawlerPath)
		}
		_ = os.Remove(newPath)
		return fmt.Errorf("TikTok Crawler 更新替換失敗：%w", err)
	}
	_ = os.Remove(backup)
	a.postLog("✓ TikTok Crawler 已完成配置（" + info.Version + "）。\r\n")
	return nil
}

func fetchLatestTikTokCrawlerRelease(ctx context.Context) (tiktokCrawlerReleaseInfo, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tiktokCrawlerLatestAPI, nil)
	if err != nil {
		return tiktokCrawlerReleaseInfo{}, err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return tiktokCrawlerReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tiktokCrawlerReleaseInfo{}, fmt.Errorf("TikTok Crawler GitHub API HTTP %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&release); err != nil {
		return tiktokCrawlerReleaseInfo{}, err
	}
	info := tiktokCrawlerReleaseInfo{Version: release.TagName}
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if name == "tiktok_crawler-windows-amd64.exe" || (strings.Contains(name, "tiktok_crawler") && strings.Contains(name, "windows") && strings.Contains(name, "amd64") && strings.HasSuffix(name, ".exe")) {
			info.BinaryURL = asset.BrowserDownloadURL
			info.Digest = asset.Digest
			info.AssetName = asset.Name
			break
		}
	}
	if info.BinaryURL == "" {
		return tiktokCrawlerReleaseInfo{}, errors.New("TikTok Crawler 最新 Release 中找不到 Windows amd64 執行檔")
	}
	return info, nil
}

type tiktokDramaPlaybackAddress struct {
	URL          string   `json:"url"`
	URLList      []string `json:"urlList"`
	URLListAlt   []string `json:"url_list"`
	URLListUpper []string `json:"UrlList"`
	DataSize     int64    `json:"dataSize"`
	DataSizeAlt  int64    `json:"data_size"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
}

func (a *tiktokDramaPlaybackAddress) UnmarshalJSON(data []byte) error {
	data = bytesTrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &a.URL)
	}
	type alias tiktokDramaPlaybackAddress
	return json.Unmarshal(data, (*alias)(a))
}

func bytesTrimSpace(data []byte) []byte {
	start, end := 0, len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}

func (a tiktokDramaPlaybackAddress) urls() []string {
	all := []string{a.URL}
	all = append(all, a.URLList...)
	all = append(all, a.URLListAlt...)
	all = append(all, a.URLListUpper...)
	seen := map[string]bool{}
	out := make([]string, 0, len(all))
	for _, value := range all {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || (!strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://")) {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

type tiktokDramaBitrate struct {
	GearName       string                     `json:"GearName"`
	GearNameAlt    string                     `json:"gear_name"`
	Bitrate        int64                      `json:"Bitrate"`
	BitrateAlt     int64                      `json:"bit_rate"`
	PlayAddress    tiktokDramaPlaybackAddress `json:"PlayAddr"`
	PlayAddressAlt tiktokDramaPlaybackAddress `json:"playAddr"`
}

type tiktokDramaItem struct {
	ID    string `json:"id"`
	Video struct {
		Width       int                        `json:"width"`
		Height      int                        `json:"height"`
		Bitrate     int64                      `json:"bitrate"`
		PlayAddress tiktokDramaPlaybackAddress `json:"playAddr"`
		BitrateInfo []tiktokDramaBitrate       `json:"bitrateInfo"`
	} `json:"video"`
	DramaInfo struct {
		DramaID        string `json:"dramaID"`
		DramaName      string `json:"dramaName"`
		NumVideos      int    `json:"numVideos"`
		DramaVideoData struct {
			EpisodeNumber int `json:"EpisodeNumber"`
		} `json:"DramaVideoData"`
	} `json:"dramaInfo"`
}

type tiktokItemDetailResponse struct {
	StatusCode int `json:"statusCode"`
	ItemInfo   struct {
		ItemStruct tiktokDramaItem `json:"itemStruct"`
	} `json:"itemInfo"`
}

// tiktokDramaItemsFromJSON accepts either an /api/drama/episode/item_list/ or an
// /api/item/detail/ response body, as observed by the logged-in capture browser.
func tiktokDramaItemsFromJSON(body []byte) []tiktokDramaItem {
	var list tiktokDramaResponse
	if err := json.Unmarshal(body, &list); err == nil {
		items := append([]tiktokDramaItem{}, list.ItemList...)
		items = append(items, list.ItemListAlt...)
		if len(items) > 0 {
			return items
		}
	}
	var detail tiktokItemDetailResponse
	if err := json.Unmarshal(body, &detail); err == nil && detail.ItemInfo.ItemStruct.ID != "" {
		return []tiktokDramaItem{detail.ItemInfo.ItemStruct}
	}
	return nil
}

func tiktokDramaItemStreams(item tiktokDramaItem) []tiktokDramaStream {
	streams := []tiktokDramaStream{}
	seen := map[string]bool{}
	for _, b := range item.Video.BitrateInfo {
		addr := b.PlayAddress
		if len(addr.urls()) == 0 {
			addr = b.PlayAddressAlt
		}
		urls := addr.urls()
		if len(urls) == 0 || seen[urls[0]] {
			continue
		}
		seen[urls[0]] = true
		label := strings.TrimSpace(b.GearName)
		if label == "" {
			label = strings.TrimSpace(b.GearNameAlt)
		}
		bitrate := b.Bitrate
		if bitrate == 0 {
			bitrate = b.BitrateAlt
		}
		streams = append(streams, tiktokDramaStream{Label: label, URLs: urls, Width: addr.Width, Height: addr.Height, Bitrate: bitrate})
	}
	if len(streams) == 0 {
		if urls := item.Video.PlayAddress.urls(); len(urls) > 0 {
			streams = append(streams, tiktokDramaStream{Label: "default", URLs: urls, Width: item.Video.Width, Height: item.Video.Height, Bitrate: item.Video.Bitrate})
		}
	}
	sortTikTokDramaStreams(streams)
	return streams
}

type tiktokDramaResponse struct {
	StatusCode    int               `json:"statusCode"`
	StatusCodeAlt int               `json:"status_code"`
	StatusMessage string            `json:"statusMsg"`
	StatusMsgAlt  string            `json:"status_msg"`
	ItemList      []tiktokDramaItem `json:"itemList"`
	ItemListAlt   []tiktokDramaItem `json:"item_list"`
}

type tiktokDramaStream struct {
	Label   string
	URLs    []string
	Width   int
	Height  int
	Bitrate int64
}

func parseTikTokShortDramaURL(rawURL string) (string, int, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if host != "tiktok.com" && !strings.HasSuffix(host, ".tiktok.com") {
		return "", 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0], "shortdrama") || !strings.EqualFold(parts[1], "episode") || !allDigits(parts[2]) || !allDigits(parts[3]) {
		return "", 0, false
	}
	ep, err := strconv.Atoi(parts[3])
	if err != nil || ep <= 0 {
		return "", 0, false
	}
	return parts[2], ep, true
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
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fields[0])), ".")
		if domain == "" || !(host == domain || strings.HasSuffix(host, "."+domain)) {
			continue
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

func resolveTikTokShortDrama(ctx context.Context, rawURL string, cfg settings) (string, int, string, []tiktokDramaStream, error) {
	dramaID, episode, ok := parseTikTokShortDramaURL(rawURL)
	if !ok {
		return "", 0, "", nil, errors.New("不是支援的分集網址")
	}
	endpoint, _ := url.Parse("https://www.tiktok.com/api/drama/episode/item_list/")
	q := endpoint.Query()
	q.Set("dramaID", dramaID)
	q.Set("cursor", strconv.Itoa(episode-1))
	q.Set("count", "1")
	q.Set("aid", "1988")
	q.Set("language", "en")
	q.Set("region", "")
	q.Set("storeRegion", "")
	endpoint.RawQuery = q.Encode()

	client := &http.Client{Timeout: 40 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return dramaID, episode, "", nil, err
	}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", rawURL)
	req.Header.Set("Origin", "https://www.tiktok.com")
	if cookie := cookieHeaderFromFile(cfg.CookieFile, "tiktok.com"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return dramaID, episode, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return dramaID, episode, "", nil, fmt.Errorf("分集資訊查詢 HTTP %s", resp.Status)
	}
	var payload tiktokDramaResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return dramaID, episode, "", nil, fmt.Errorf("分集資訊解析失敗：%w", err)
	}
	status := payload.StatusCode
	if status == 0 {
		status = payload.StatusCodeAlt
	}
	if status != 0 {
		msg := payload.StatusMessage
		if msg == "" {
			msg = payload.StatusMsgAlt
		}
		return dramaID, episode, "", nil, fmt.Errorf("分集資訊查詢狀態 %d：%s", status, msg)
	}
	items := payload.ItemList
	if len(items) == 0 {
		items = payload.ItemListAlt
	}
	if len(items) == 0 {
		return dramaID, episode, "", nil, errors.New("分集資訊沒有回傳本集資料")
	}
	item := items[0]
	title := strings.TrimSpace(item.DramaInfo.DramaName)
	return dramaID, episode, title, tiktokDramaItemStreams(item), nil
}

func sortTikTokDramaStreams(streams []tiktokDramaStream) {
	for i := 0; i < len(streams); i++ {
		for j := i + 1; j < len(streams); j++ {
			left, right := streams[i], streams[j]
			ls := left.Height*1000000 + int(left.Bitrate/1000)
			rs := right.Height*1000000 + int(right.Bitrate/1000)
			if rs > ls {
				streams[i], streams[j] = streams[j], streams[i]
			}
		}
	}
}

func chooseTikTokDramaStream(streams []tiktokDramaStream, maxHeight int) (tiktokDramaStream, error) {
	if len(streams) == 0 {
		return tiktokDramaStream{}, errors.New("分集資訊中沒有可直接取得的影片網址")
	}
	if maxHeight <= 0 {
		return streams[0], nil
	}
	for _, st := range streams {
		if st.Height > 0 && st.Height <= maxHeight {
			return st, nil
		}
	}
	return streams[len(streams)-1], nil
}

func (a *app) downloadDirectMedia(ctx context.Context, urls []string, dest, referer, cookie string) error {
	if len(urls) == 0 {
		return errors.New("沒有可下載的媒體 URL")
	}
	if validFile(dest, 1024) {
		a.postLog("✓ 檔案已存在，跳過重複下載：" + dest + "\r\n")
		return nil
	}
	var lastErr error
	for idx, mediaURL := range urls {
		part := dest + ".part"
		var offset int64
		if st, err := os.Stat(part); err == nil {
			offset = st.Size()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "*/*")
		if strings.Contains(strings.ToLower(referer), "tiktok.com") {
			req.Header.Set("Origin", "https://www.tiktok.com")
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := (&http.Client{Timeout: 90 * time.Minute}).Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			lastErr = fmt.Errorf("媒體來源 %d HTTP %s", idx+1, resp.Status)
			resp.Body.Close()
			continue
		}
		flags := os.O_CREATE | os.O_WRONLY
		if resp.StatusCode == http.StatusPartialContent && offset > 0 {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
			offset = 0
		}
		f, err := os.OpenFile(part, flags, 0644)
		if err != nil {
			resp.Body.Close()
			return err
		}
		total := resp.ContentLength
		if total > 0 {
			total += offset
		}
		downloaded := offset
		buf := make([]byte, 256*1024)
		lastReport := time.Now().Add(-time.Hour)
		copyErr := error(nil)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := f.Write(buf[:n]); werr != nil {
					copyErr = werr
					break
				}
				downloaded += int64(n)
				if time.Since(lastReport) > time.Second {
					if total > 0 {
						a.postStatus(fmt.Sprintf("狀態：直連下載 %.1f / %.1f MB（%.0f%%）", float64(downloaded)/(1024*1024), float64(total)/(1024*1024), float64(downloaded)*100/float64(total)))
					} else {
						a.postStatus(fmt.Sprintf("狀態：直連下載 %.1f MB", float64(downloaded)/(1024*1024)))
					}
					lastReport = time.Now()
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				copyErr = rerr
				break
			}
		}
		_ = f.Sync()
		_ = f.Close()
		_ = resp.Body.Close()
		if copyErr != nil {
			lastErr = copyErr
			continue
		}
		_ = os.Remove(dest)
		if err := os.Rename(part, dest); err != nil {
			return err
		}
		a.postLog("✓ 直連媒體下載完成：" + dest + "\r\n")
		return nil
	}
	return lastErr
}

func (a *app) runTikTokShortDramaDirect(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	if _, _, ok := parseTikTokShortDramaURL(rawURL); !ok {
		return errors.New("不是支援的分集網址")
	}
	if mode == 4 {
		return errors.New("此類網址的內建解析不使用格式 ID；請改選最佳畫質或 720p")
	}
	dramaID, episode, title, streams, err := resolveTikTokShortDrama(ctx, rawURL, cfg)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return errors.New("此頁面需要登入才能取得影片，改用瀏覽器登入後擷取")
	}
	if mode == 3 {
		a.postLog(fmt.Sprintf("%s 第 %d 集可用格式：\r\n", dramaID, episode))
		for i, st := range streams {
			a.postLog(fmt.Sprintf("  %d｜%s｜%dx%d｜bitrate=%d\r\n", i+1, st.Label, st.Width, st.Height, st.Bitrate))
		}
		return nil
	}
	maxHeight := 0
	if mode == 5 {
		maxHeight = 720
	}
	selected, err := chooseTikTokDramaStream(streams, maxHeight)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	base := "Video_" + dramaID + "_E" + fmt.Sprintf("%03d", episode)
	if clean := safeWindowsBaseName(title); clean != "" {
		base = safeWindowsBaseName(clean + "_E" + fmt.Sprintf("%03d", episode))
	}
	dest := filepath.Join(output, base+".mp4")
	cookie := cookieHeaderFromFile(cfg.CookieFile, "tiktok.com")
	a.postLog(fmt.Sprintf("→ 內建解析成功：第 %d 集，選擇 %dp。\r\n", episode, selected.Height))
	if err := a.downloadDirectMedia(ctx, selected.URLs, dest, rawURL, cookie); err != nil {
		return err
	}
	if mode == 1 {
		return a.convertMediaToMP3(ctx, dest)
	}
	return nil
}

func (a *app) runTikTokCrawlerFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	if err := a.ensureTikTokCrawler(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ExtraArgs) != "" {
		a.postLog("ℹ 進階參數是 yt-dlp 專用語法；切換 TikTok Crawler 時不會直接套用。\r\n")
	}
	if mode == 4 {
		return errors.New("TikTok Crawler 不使用 yt-dlp 格式 ID；請改選『下載最佳畫質影片』或『最高 720p MP4』")
	}

	buildArgs := func(browser string) ([]string, string, error) {
		args := []string{}
		if mode == 3 {
			args = append(args, "-json")
		} else {
			if err := os.MkdirAll(output, 0755); err != nil {
				return nil, "", err
			}
			quality := "best"
			if mode == 5 {
				quality = "720"
			}
			outputPath := filepath.Join(output, tiktokCrawlerOutputName(rawURL))
			args = append(args, "-output", outputPath, "-quality", quality)
			if mode == 2 {
				// Profiles/hashtags are batch outputs. The crawler interprets -output
				// as a directory for these URL types.
				args = []string{"-output", output, "-quality", quality}
			}
			if mode == 1 {
				// The crawler downloads the best available video; FFmpeg converts it
				// to MP3 after a successful download.
			}
		}
		if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
			args = append(args, "-cookies-file", cookieFile)
		} else if browser != "" {
			args = append(args, "-cookies-from-browser", browser)
		} else if selected := strings.TrimSpace(cfg.CookieBrowser); selected != "" && selected != "auto" && selected != "none" {
			args = append(args, "-cookies-from-browser", selected)
		}
		args = append(args, rawURL)
		return args, output, nil
	}

	runOnce := func(browser string) error {
		args, _, err := buildArgs(browser)
		if err != nil {
			return err
		}
		started := time.Now().Add(-2 * time.Second)
		if browser != "" {
			a.postLog("→ TikTok Crawler 正在嘗試 " + browserDisplayName(browser) + " 登入狀態…\r\n")
		}
		if err := a.runTikTokCrawlerCommand(ctx, args); err != nil {
			return err
		}
		if mode == 1 {
			mediaPath, err := findNewestMediaFile(output, started)
			if err != nil {
				return fmt.Errorf("TikTok Crawler 已完成下載，但找不到可轉換的媒體檔：%w", err)
			}
			if strings.EqualFold(filepath.Ext(mediaPath), ".mp3") {
				return nil
			}
			return a.convertMediaToMP3(ctx, mediaPath)
		}
		return nil
	}

	a.postLog("→ 正在改用 TikTok Crawler 備援引擎。\r\n")
	a.postStatus("狀態：正在使用 TikTok 專用備援引擎…")
	err := runOnce("")
	if err == nil {
		return nil
	}

	if strings.TrimSpace(cfg.CookieFile) == "" && strings.TrimSpace(cfg.CookieBrowser) == "auto" {
		lastErr := err
		browsers := installedBrowserCandidates()
		if len(browsers) == 0 {
			browsers = []string{"firefox", "edge", "chrome"}
		}
		for _, browser := range browsers {
			if a.stopRequested.Load() {
				return nil
			}
			if retryErr := runOnce(browser); retryErr == nil {
				a.postLog("✓ TikTok Crawler 使用 " + browserDisplayName(browser) + " 登入狀態重試成功。\r\n")
				return nil
			} else {
				lastErr = retryErr
			}
		}
		return lastErr
	}
	return err
}

func tiktokCrawlerOutputName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
		if len(parts) >= 4 && strings.EqualFold(parts[0], "shortdrama") && strings.EqualFold(parts[1], "episode") {
			return safeWindowsBaseName("Video_"+parts[2]+"_E"+parts[3]) + ".mp4"
		}
		for i := len(parts) - 1; i >= 0; i-- {
			if allDigits(parts[i]) {
				return "TikTok_" + parts[i] + ".mp4"
			}
		}
	}
	return "TikTok_" + time.Now().Format("20060102_150405") + ".mp4"
}

func (a *app) runTikTokCrawlerCommand(ctx context.Context, args []string) error {
	return a.runExternalCommand(ctx, a.tiktokCrawlerPath, args, "TikTok Crawler")
}

type webMediaCandidate struct {
	URL  string
	Kind string
}

func fetchHTMLPage(ctx context.Context, rawURL string, cfg settings, referer string) (string, string, error) {
	client := &http.Client{Timeout: 35 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", rawURL, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,zh-TW;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if u, err := url.Parse(rawURL); err == nil {
		if cookie := cookieHeaderFromFile(cfg.CookieFile, u.Hostname()); cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", rawURL, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", rawURL, fmt.Errorf("網頁 HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12*1024*1024))
	if err != nil {
		return "", rawURL, err
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return string(body), finalURL, nil
}

func resolveCandidateURL(baseURL, value string) string {
	value = strings.TrimSpace(html.UnescapeString(value))
	value = strings.ReplaceAll(value, `\/`, `/`)
	value = strings.ReplaceAll(value, `\u0026`, `&`)
	value = strings.Trim(value, " \t\r\n\\\"'")
	if value == "" || strings.HasPrefix(value, "blob:") || strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "data:") {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		if u.Scheme == "http" || u.Scheme == "https" {
			return u.String()
		}
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

func candidateKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	return u.String()
}

func looksLikeMediaProvider(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	providers := []string{"dailymotion.com", "dai.ly", "vimeo.com", "youtube.com", "youtu.be", "tiktok.com", "bilibili.com", "facebook.com", "instagram.com", "streamable.com", "loom.com"}
	for _, domain := range providers {
		if h == domain || strings.HasSuffix(h, "."+domain) {
			return true
		}
	}
	return false
}

func looksLikeDirectMedia(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, ".m3u8") || strings.Contains(lower, ".mp4") || strings.Contains(lower, ".m4v") || strings.Contains(lower, ".webm") || strings.Contains(lower, ".mpd")
}

func extractWebMediaCandidates(body, baseURL string) []webMediaCandidate {
	body = html.UnescapeString(body)
	body = strings.ReplaceAll(body, `\/`, `/`)
	seen := map[string]bool{}
	out := []webMediaCandidate{}
	add := func(value, kind string) {
		resolved := resolveCandidateURL(baseURL, value)
		if resolved == "" {
			return
		}
		key := candidateKey(resolved)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, webMediaCandidate{URL: resolved, Kind: kind})
	}
	patterns := []struct {
		re   *regexp.Regexp
		kind string
	}{
		{regexp.MustCompile(`(?is)<iframe[^>]+src\s*=\s*["']([^"']+)["']`), "iframe"},
		{regexp.MustCompile(`(?is)<(?:video|source)[^>]+src\s*=\s*["']([^"']+)["']`), "media"},
		{regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)\s*=\s*["'](?:og:video(?::url)?|twitter:player(?::stream)?)["'][^>]+content\s*=\s*["']([^"']+)["']`), "meta"},
		{regexp.MustCompile(`(?is)["'](?:file|src|source|playbackUrl|playback_url|videoUrl|video_url|hls|hlsUrl|hls_url|contentUrl)["']\s*:\s*["']([^"']+)["']`), "json"},
	}
	for _, p := range patterns {
		for _, m := range p.re.FindAllStringSubmatch(body, 80) {
			if len(m) > 1 {
				add(m[1], p.kind)
			}
		}
	}
	// Catch absolute URLs hidden inside framework JSON. Only accept recognized
	// media extensions/providers so ordinary links, ads, and analytics URLs are ignored.
	urlRe := regexp.MustCompile(`https?://[^\s"'<>\\]+`)
	for _, match := range urlRe.FindAllString(body, 160) {
		candidate := strings.TrimRight(match, "),.;]")
		if looksLikeDirectMedia(candidate) {
			add(candidate, "direct")
		} else if looksLikeMediaProvider(candidate) {
			add(candidate, "provider")
		}
	}
	return out
}

func (a *app) tryWebCandidate(ctx context.Context, candidate, referer string, mode int, formatID, output string, cfg settings) error {
	candidateMode := mode
	if mode == 2 && looksLikeDirectMedia(candidate) {
		candidateMode = 0
	}
	args, err := a.buildArgs(candidate, candidateMode, formatID, output, cfg)
	if err != nil {
		return err
	}
	extras := []string{"--referer", referer, "--impersonate", "chrome"}
	if base, err := url.Parse(referer); err == nil && base.Scheme != "" && base.Host != "" {
		extras = append(extras, "--add-header", "Origin:"+base.Scheme+"://"+base.Host)
	}
	args = insertBeforeLast(args, extras...)
	return a.runCommand(ctx, args)
}

func (a *app) runGenericWebPageFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	a.postLog("→ 正在掃描網頁中的 iframe、video/source、HLS/MP4 與播放器 JSON…\r\n")
	body, finalURL, err := fetchHTMLPage(ctx, rawURL, cfg, "")
	if err != nil {
		return fmt.Errorf("讀取網頁失敗：%w", err)
	}
	candidates := extractWebMediaCandidates(body, finalURL)
	if len(candidates) == 0 {
		return errors.New("網頁中沒有找到可辨識的公開播放器或媒體 URL")
	}
	a.postLog(fmt.Sprintf("✓ 網頁解析找到 %d 個候選媒體／播放器，開始逐一驗證。\r\n", len(candidates)))
	var lastErr error
	limit := len(candidates)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		c := candidates[i]
		if a.stopRequested.Load() {
			return nil
		}
		a.postLog(fmt.Sprintf("→ 候選 %d/%d [%s]：%s\r\n", i+1, limit, c.Kind, c.URL))
		if err := a.tryWebCandidate(ctx, c.URL, finalURL, mode, formatID, output, cfg); err == nil {
			a.postLog("✓ 網頁嵌入媒體候選下載成功。\r\n")
			return nil
		} else {
			lastErr = err
		}
		// Some sites embed an intermediate player page. Scan one nested level for
		// direct HLS/MP4/provider links and hand those back to yt-dlp.
		if !looksLikeDirectMedia(c.URL) {
			nestedBody, nestedFinal, fetchErr := fetchHTMLPage(ctx, c.URL, cfg, finalURL)
			if fetchErr == nil {
				nested := extractWebMediaCandidates(nestedBody, nestedFinal)
				nestedLimit := len(nested)
				if nestedLimit > 12 {
					nestedLimit = 12
				}
				for j := 0; j < nestedLimit; j++ {
					n := nested[j]
					if candidateKey(n.URL) == candidateKey(c.URL) {
						continue
					}
					a.postLog(fmt.Sprintf("  ↳ 內嵌候選 %d/%d：%s\r\n", j+1, nestedLimit, n.URL))
					if err := a.tryWebCandidate(ctx, n.URL, nestedFinal, mode, formatID, output, cfg); err == nil {
						return nil
					} else {
						lastErr = err
					}
				}
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("已找到播放器／媒體候選，但都無法下載：%w", lastErr)
	}
	return errors.New("網頁解析沒有找到可下載的公開媒體")
}

func (a *app) runUnsupportedFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	site := classifySiteURL(rawURL)
	var lastErr error

	if site == "tiktok" {
		if _, _, isShortDrama := parseTikTokShortDramaURL(rawURL); isShortDrama {
			a.postLog("→ 偵測到分集網址，先使用內建解析器…\r\n")
			if err := a.runTikTokShortDramaDirect(ctx, rawURL, mode, formatID, output, cfg); err == nil {
				return nil
			} else {
				lastErr = err
				a.postLog("⚠ 內建解析未成功，改以瀏覽器實際播放擷取：" + firstLine(err.Error()) + "\r\n")
			}
			a.postLog("→ 啟動瀏覽器播放擷取；直接觀察頁面實際載入的 HLS／MP4。\r\n")
			if err := a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg); err == nil {
				return nil
			} else {
				lastErr = err
				if !cfg.UnsafeMode {
					// Extra engines would re-request the same login-bound episode
					// several more times without the login; that only adds risk.
					return err
				}
				a.postLog("⚠ 瀏覽器播放擷取未成功，再改用 TikTok Crawler：" + firstLine(err.Error()) + "\r\n")
			}
		}
		a.postLog("→ 啟動 TikTok Crawler 專用解析器…\r\n")
		if err := a.runTikTokCrawlerFallback(ctx, rawURL, mode, formatID, output, cfg); err == nil {
			return nil
		} else {
			lastErr = err
			a.postLog("⚠ TikTok Crawler 未成功，最後再嘗試 Lux：" + firstLine(err.Error()) + "\r\n")
		}
	}

	// For a site yt-dlp does not support yet, AIXAI first tries a small
	// site-specific resolver based on the page's public PRELOADED_STATE data.
	if site == "haokan" {
		a.postLog("→ 先使用內建解析器尋找公開媒體網址…\r\n")
		if err := a.runHaokanFallback(ctx, rawURL, mode, formatID, output, cfg); err == nil {
			return nil
		} else {
			lastErr = err
			a.postLog("⚠ 內建解析未成功，改用瀏覽器播放擷取：" + firstLine(err.Error()) + "\r\n")
		}
		if err := a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg); err == nil {
			return nil
		} else {
			lastErr = err
			a.postLog("⚠ 瀏覽器網路嗅探未成功，再嘗試其他備援引擎：" + firstLine(err.Error()) + "\r\n")
		}
	}

	if site == "dramatip" || site == "" {
		a.postLog("→ 啟動網頁播放器解析；適用於 yt-dlp 尚未支援的公開網頁。\r\n")
		if err := a.runGenericWebPageFallback(ctx, rawURL, mode, formatID, output, cfg); err == nil {
			return nil
		} else {
			lastErr = err
			a.postLog("⚠ 靜態網頁解析未找到串流，改用瀏覽器網路嗅探：" + firstLine(err.Error()) + "\r\n")
		}
		if err := a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg); err == nil {
			return nil
		} else {
			lastErr = err
			a.postLog("⚠ 瀏覽器網路嗅探未成功，繼續嘗試 Lux：" + firstLine(err.Error()) + "\r\n")
		}
	}

	if mode == 5 {
		if site == "tiktok" && lastErr != nil {
			return &commandRunError{Cause: lastErr, Summary: "TikTok 專用備援引擎已嘗試 720p 下載但仍失敗：" + firstLine(lastErr.Error())}
		}
		return &commandRunError{Cause: lastErr, Summary: "yt-dlp 不支援此網址，而 Lux 備援引擎無法可靠保證『最高 720p』。可改選『下載最佳畫質影片』後重試。"}
	}

	if err := a.ensureLux(ctx); err != nil {
		if lastErr != nil {
			return &commandRunError{Cause: err, Summary: "網站專用解析失敗，且 Lux 備援引擎配置失敗：" + err.Error() + "\n\n原始解析錯誤：" + firstLine(lastErr.Error())}
		}
		return &commandRunError{Cause: err, Summary: "yt-dlp 不支援此網址，Lux 備援引擎配置也失敗：" + err.Error()}
	}

	a.postLog("→ 正在改用 Lux 備援引擎處理此網址。Lux 對部分中文影音網站有獨立解析器。\r\n")
	a.postStatus("狀態：正在使用 Lux 備援引擎…")
	if strings.TrimSpace(cfg.ExtraArgs) != "" {
		a.postLog("ℹ 進階參數是 yt-dlp 專用語法，切換 Lux 時不會直接套用，以避免參數不相容。\r\n")
	}
	if err := a.runLuxFallback(ctx, rawURL, mode, formatID, output, cfg); err != nil {
		previous := ""
		if lastErr != nil {
			previous = "\n\n前一層解析：" + firstLine(lastErr.Error())
		}
		return &commandRunError{Cause: err, Summary: "所有可用下載引擎都未能處理此網址。" + previous + "\nLux：" + firstLine(err.Error()) + "\n\n本工具會依序嘗試 yt-dlp → 內建解析 → 瀏覽器播放擷取 → 備援引擎。若仍失敗，常見原因是網站改版、需要登入，或內容受 DRM／加密保護（本工具不處理受保護的內容）。"}
	}
	return nil
}

func (a *app) ensureLux(ctx context.Context) error {
	if validFile(a.luxPath, 1<<20) {
		ver := strings.TrimSpace(firstLine(runSmallContext(ctx, a.luxPath, "-v")))
		if ver != "" {
			a.postLog("✓ Lux 備援引擎已存在：" + ver + "。\r\n")
		} else {
			a.postLog("✓ Lux 備援引擎已存在。\r\n")
		}
		return nil
	}

	a.postLog("首次需要備援引擎：正在從 Lux 官方 GitHub Release 自動配置 Windows x64 版本…\r\n")
	a.postStatus("狀態：正在配置 Lux 備援下載引擎…")
	info, err := fetchLatestLuxRelease(ctx)
	if err != nil {
		return err
	}
	zipPath := filepath.Join(a.appDir, "lux.download.zip")
	_ = os.Remove(zipPath)
	if err := downloadFile(ctx, info.ZipURL, zipPath, "Lux", a.postLog, a.postStatus); err != nil {
		return fmt.Errorf("Lux 下載失敗：%w", err)
	}
	if info.ChecksumURL != "" && info.AssetName != "" {
		if err := verifyRemoteSHA256(ctx, zipPath, info.ChecksumURL, info.AssetName); err != nil {
			_ = os.Remove(zipPath)
			return fmt.Errorf("Lux 完整性校驗失敗：%w", err)
		}
		a.postLog("✓ Lux SHA-256 校驗通過。\r\n")
	}
	if err := extractSelected(zipPath, a.binDir, map[string]string{"lux.exe": "lux.exe"}); err != nil {
		_ = os.Remove(zipPath)
		return fmt.Errorf("Lux 解壓縮失敗：%w", err)
	}
	_ = os.Remove(zipPath)
	if !validFile(a.luxPath, 1<<20) {
		return errors.New("Lux 壓縮檔中找不到 lux.exe")
	}
	a.postLog("✓ Lux 備援引擎已完成配置" + func() string {
		if info.Version != "" {
			return "（" + info.Version + "）"
		}
		return ""
	}() + "。\r\n")
	return nil
}

func fetchLatestLuxRelease(ctx context.Context) (luxReleaseInfo, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, luxLatestAPI, nil)
	if err != nil {
		return luxReleaseInfo{}, err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return luxReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return luxReleaseInfo{}, fmt.Errorf("Lux GitHub API HTTP %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&release); err != nil {
		return luxReleaseInfo{}, err
	}
	info := luxReleaseInfo{Version: release.TagName}
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "checksums") && strings.HasSuffix(name, ".txt") {
			info.ChecksumURL = asset.BrowserDownloadURL
		}
		if strings.Contains(name, "windows") && (strings.Contains(name, "x86_64") || strings.Contains(name, "amd64")) && strings.HasSuffix(name, ".zip") {
			info.ZipURL = asset.BrowserDownloadURL
			info.AssetName = asset.Name
		}
	}
	if info.ZipURL == "" {
		return luxReleaseInfo{}, errors.New("Lux 最新 Release 中找不到 Windows x64 ZIP")
	}
	return info, nil
}

func (a *app) runLuxFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	args, err := a.buildLuxArgs(rawURL, mode, formatID, output, cfg)
	if err != nil {
		return err
	}
	started := time.Now().Add(-2 * time.Second)
	if err := a.runLuxCommand(ctx, args); err != nil {
		return err
	}
	if mode == 1 {
		mediaPath, err := findNewestMediaFile(output, started)
		if err != nil {
			return fmt.Errorf("Lux 已完成音訊下載，但找不到可轉換的輸出檔：%w", err)
		}
		if strings.EqualFold(filepath.Ext(mediaPath), ".mp3") {
			return nil
		}
		return a.convertMediaToMP3(ctx, mediaPath)
	}
	return nil
}

func (a *app) buildLuxArgs(rawURL string, mode int, formatID, output string, cfg settings) ([]string, error) {
	args := []string{"-o", output, "-retry", "10"}
	if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
		args = append(args, "-c", cookieFile)
	}
	switch mode {
	case 0:
		args = append(args, rawURL)
	case 1:
		args = append(args, "--audio-only", rawURL)
	case 2:
		args = append(args, "-p", rawURL)
	case 3:
		args = append(args, "-i", rawURL)
	case 4:
		if strings.TrimSpace(formatID) == "" {
			return nil, errors.New("格式 ID 不可空白")
		}
		args = append(args, "-f", strings.TrimSpace(formatID), rawURL)
	default:
		return nil, errors.New("此處理模式目前不適用 Lux 備援引擎")
	}
	return args, nil
}

func findNewestMediaFile(dir string, since time.Time) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".download") || strings.HasSuffix(lower, ".part") || strings.HasSuffix(lower, ".ytdl") || strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp") || strings.HasSuffix(lower, ".srt") || strings.HasSuffix(lower, ".vtt") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(since) {
			continue
		}
		if best == "" || info.ModTime().After(bestTime) {
			best = filepath.Join(dir, name)
			bestTime = info.ModTime()
		}
	}
	if best == "" {
		return "", errors.New("沒有找到剛完成的媒體檔")
	}
	return best, nil
}

func (a *app) convertMediaToMP3(ctx context.Context, input string) error {
	base := strings.TrimSuffix(input, filepath.Ext(input))
	out := base + ".mp3"
	a.postLog("→ 備援引擎已取得媒體，正在用 FFmpeg 轉成 MP3…\r\n")
	args := []string{"-y", "-i", input, "-vn", "-q:a", "0", out}
	if err := a.runExternalCommand(ctx, a.ffmpegPath, args, "FFmpeg"); err != nil {
		return fmt.Errorf("轉成 MP3 失敗：%w", err)
	}
	if validFile(out, 1024) {
		_ = os.Remove(input)
		a.postLog("✓ 已轉成 MP3：" + out + "\r\n")
		return nil
	}
	return errors.New("FFmpeg 執行完成但沒有產生 MP3")
}

func (a *app) runHaokanFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	title, streams, err := resolveHaokanPage(ctx, rawURL)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return errors.New("頁面中找不到公開的 clarityUrl 媒體資訊")
	}
	if mode == 3 {
		a.postLog("可用格式：\r\n")
		for _, st := range streams {
			size := ""
			if st.SizeMB > 0 {
				size = fmt.Sprintf("，約 %.1f MB", st.SizeMB)
			}
			a.postLog(fmt.Sprintf("  %s｜%dp%s\r\n", st.Key, st.Height, size))
		}
		return nil
	}
	selected, err := chooseHaokanStream(streams, mode, formatID)
	if err != nil {
		return err
	}
	directMode := mode
	if mode == 4 || mode == 5 || mode == 2 {
		directMode = 0
	}
	args, err := a.buildArgs(selected.URL, directMode, "", output, cfg)
	if err != nil {
		return err
	}
	u, _ := url.Parse(rawURL)
	origin := "https://haokan.baidu.com"
	if u != nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	extra := []string{"--referer", "https://haokan.baidu.com/", "--user-agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1", "--add-header", "Origin:" + origin}
	if clean := safeWindowsBaseName(title); clean != "" {
		extra = append(extra, "-o", clean+".%(ext)s")
	}
	args = insertBeforeLast(args, extra...)
	a.postLog(fmt.Sprintf("✓ 內建解析到 %s（%dp），交由 yt-dlp/FFmpeg 下載。\r\n", selected.Key, selected.Height))
	return a.runCommand(ctx, args)
}

func resolveHaokanPage(ctx context.Context, rawURL string) (string, []haokanStream, error) {
	vid, candidates := haokanPageCandidates(rawURL)
	if len(candidates) == 0 {
		candidates = []string{rawURL}
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		for _, candidate := range candidates {
			body, err := fetchHaokanPage(ctx, candidate)
			if err != nil {
				lastErr = err
				continue
			}

			jsonText := extractAssignedJSONObject(body, "window.__PRELOADED_STATE__")
			if jsonText == "" {
				jsonText = extractAssignedJSONObject(body, "__PRELOADED_STATE__")
			}
			if jsonText == "" {
				lastErr = errors.New("頁面中找不到 PRELOADED_STATE 公開資料")
				continue
			}

			dec := json.NewDecoder(strings.NewReader(jsonText))
			dec.UseNumber()
			var root any
			if err := dec.Decode(&root); err != nil {
				lastErr = fmt.Errorf("頁面資料解析失敗：%w", err)
				continue
			}

			cur := findNestedObject(root, "curVideoMeta")
			if cur == nil {
				lastErr = errors.New("找不到 curVideoMeta")
				continue
			}
			if vid != "" {
				pageID := stringFromAny(cur["id"])
				if pageID != "" && pageID != vid {
					lastErr = fmt.Errorf("頁面影片 ID 不符（預期 %s，取得 %s）", vid, pageID)
					continue
				}
			}

			streams := collectHaokanStreams(cur)
			if len(streams) == 0 {
				lastErr = errors.New("頁面中找不到 clarityUrl 或 videoInfoExt 公開媒體資訊")
				continue
			}

			title := firstNonEmptyString(cur, "title", "seo_title")
			if title == "" {
				title = extractHTMLTitle(body)
			}
			return title, streams, nil
		}
	}

	if lastErr == nil {
		lastErr = errors.New("無法取得公開影片資訊")
	}
	return "", nil, lastErr
}

func haokanPageCandidates(rawURL string) (string, []string) {
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", []string{rawURL}
	}

	q := u.Query()
	vid := strings.TrimSpace(q.Get("vid"))
	if vid == "" {
		contextText := strings.TrimSpace(q.Get("context"))
		if contextText != "" {
			var contextData map[string]any
			dec := json.NewDecoder(strings.NewReader(contextText))
			dec.UseNumber()
			if dec.Decode(&contextData) == nil {
				nid := stringFromAny(contextData["nid"])
				if strings.HasPrefix(nid, "sv_") {
					candidate := strings.TrimPrefix(nid, "sv_")
					if allDigits(candidate) {
						vid = candidate
					}
				}
			}
			if vid == "" {
				re := regexp.MustCompile(`(?i)sv_(\d{6,32})`)
				if m := re.FindStringSubmatch(contextText); len(m) == 2 {
					vid = m[1]
				}
			}
		}
	}
	if vid != "" && !allDigits(vid) {
		vid = ""
	}

	candidates := make([]string, 0, 2)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		candidates = append(candidates, v)
	}
	add(rawURL)
	if vid != "" {
		add("https://haokan.baidu.com/v?vid=" + url.QueryEscape(vid))
	}
	return vid, candidates
}

func fetchHaokanPage(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1")
	req.Header.Set("Referer", "https://haokan.baidu.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.6")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("網頁 HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func collectHaokanStreams(meta map[string]any) []haokanStream {
	streams := make([]haokanStream, 0, 8)
	seen := map[string]bool{}
	push := func(entry map[string]any, fallbackKey string) {
		if entry == nil {
			return
		}
		mediaURL := stringFromAny(entry["url"])
		if mediaURL == "" {
			for k, v := range entry {
				if strings.HasSuffix(strings.ToLower(k), "urlhttp") {
					if candidate := stringFromAny(v); strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
						mediaURL = candidate
						break
					}
				}
			}
		}
		mediaURL = html.UnescapeString(strings.ReplaceAll(strings.TrimSpace(mediaURL), `\/`, `/`))
		if (!strings.HasPrefix(mediaURL, "http://") && !strings.HasPrefix(mediaURL, "https://")) || seen[mediaURL] {
			return
		}
		seen[mediaURL] = true

		key := stringFromAny(entry["key"])
		if key == "" {
			key = fallbackKey
		}
		height := haokanEntryHeight(entry, key)
		streams = append(streams, haokanStream{
			Key: key, URL: mediaURL, Height: height, SizeMB: floatFromAny(entry["videoSize"]),
		})
	}

	if list, ok := meta["clarityUrl"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				push(m, "")
			}
		}
	}
	if ext, ok := meta["videoInfoExt"].(map[string]any); ok {
		for key, item := range ext {
			if m, ok := item.(map[string]any); ok {
				push(m, key)
			}
		}
	}
	return streams
}

func haokanEntryHeight(entry map[string]any, key string) int {
	for _, field := range []string{"key", "title"} {
		if value := stringFromAny(entry[field]); value != "" {
			if h := heightFromLabel(value); h > 0 {
				return h
			}
		}
	}
	if h := heightFromLabel(key); h > 0 {
		return h
	}

	hw := stringFromAny(entry["vodVideoHW"])
	if hw != "" {
		parts := strings.Split(hw, "$$")
		values := make([]int, 0, len(parts))
		for _, part := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil && n > 0 {
				values = append(values, n)
			}
		}
		if len(values) == 1 {
			return values[0]
		}
		if len(values) >= 2 {
			// Typical dimensions are width x height. Prefer the smaller dimension
			// because it is normally the vertical resolution (e.g. 1920 x 1080).
			best := values[0]
			for _, n := range values[1:] {
				if n < best {
					best = n
				}
			}
			return best
		}
	}
	return haokanHeight(key)
}

func heightFromLabel(label string) int {
	lower := strings.ToLower(strings.TrimSpace(label))
	if lower == "sd" {
		return 360
	}
	if lower == "hd" {
		return 480
	}
	if lower == "sc" {
		return 720
	}
	re := regexp.MustCompile(`(?i)(\d{3,4})p?`)
	if m := re.FindStringSubmatch(lower); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func firstNonEmptyString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringFromAny(m[key]); value != "" {
			return value
		}
	}
	return ""
}

func fetchWebPage(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,en;q=0.7")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("網頁 HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func extractAssignedJSONObject(text, marker string) string {
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	tail := text[idx+len(marker):]
	start := strings.IndexByte(tail, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(tail); i++ {
		c := tail[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return tail[start : i+1]
			}
		}
	}
	return ""
}

func findNestedObject(node any, key string) map[string]any {
	switch v := node.(type) {
	case map[string]any:
		if found, ok := v[key].(map[string]any); ok {
			return found
		}
		for _, child := range v {
			if found := findNestedObject(child, key); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := findNestedObject(child, key); found != nil {
				return found
			}
		}
	}
	return nil
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return ""
	}
}

func floatFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func haokanHeight(key string) int {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch lower {
	case "sd":
		return 360
	case "hd":
		return 480
	case "sc":
		return 720
	case "1080p":
		return 1080
	}
	re := regexp.MustCompile(`(\d{3,4})`)
	if m := re.FindStringSubmatch(lower); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func chooseHaokanStream(streams []haokanStream, mode int, formatID string) (haokanStream, error) {
	if len(streams) == 0 {
		return haokanStream{}, errors.New("沒有可用格式")
	}
	if mode == 4 {
		want := strings.ToLower(strings.TrimSpace(formatID))
		for _, st := range streams {
			if strings.ToLower(st.Key) == want || strconv.Itoa(st.Height) == want || strconv.Itoa(st.Height)+"p" == want {
				return st, nil
			}
		}
		return haokanStream{}, fmt.Errorf("找不到指定格式 %s；可先使用『只查看可用格式』", formatID)
	}
	best := haokanStream{}
	for _, st := range streams {
		if mode == 5 && (st.Height == 0 || st.Height > 720) {
			continue
		}
		if best.URL == "" || st.Height > best.Height || (st.Height == best.Height && st.SizeMB > best.SizeMB) {
			best = st
		}
	}
	if best.URL == "" {
		return haokanStream{}, errors.New("找不到符合條件的畫質")
	}
	return best, nil
}

func extractHTMLTitle(body string) string {
	re := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[1], "")))
	}
	return ""
}

func safeWindowsBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	invalid := regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)
	name = invalid.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if len([]rune(name)) > 160 {
		r := []rune(name)
		name = string(r[:160])
	}
	return name
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
			return nil
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

func (a *app) buildArgs(rawURL string, mode int, formatID, output string, cfg settings) ([]string, error) {
	collection := mode == 2 || isCollectionURL(rawURL)
	args := []string{
		"--ignore-config",
		"--newline", "--no-colors", "--encoding", "utf-8",
		"--windows-filenames",
		"--continue", "--part",
		"--retries", "10",
		"--fragment-retries", "20",
		"--retry-sleep", "http:exp=1:20",
		"--retry-sleep", "fragment:exp=1:20",
		"--ffmpeg-location", a.binDir,
		"--js-runtimes", "deno:" + a.denoPath,
	}
	if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
		args = append(args, "--cookies", cookieFile)
	} else if browser := strings.TrimSpace(cfg.CookieBrowser); browser != "" && browser != "auto" && browser != "none" {
		args = append(args, "--cookies-from-browser", browser)
	}
	if mode != 3 {
		args = append(args, "-P", output)
		if collection && cfg.PlaylistFolder {
			args = append(args, "-o", playlistTemplate)
		} else {
			args = append(args, "-o", defaultTemplate)
		}
	}
	if collection {
		args = append(args, "--yes-playlist", "--no-abort-on-error")
	} else if cfg.NoPlaylist && mode != 2 {
		args = append(args, "--no-playlist")
	}
	extra, err := splitArgs(cfg.ExtraArgs)
	if err != nil {
		return nil, fmt.Errorf("進階參數格式錯誤：%w", err)
	}
	args = append(args, extra...)

	switch mode {
	case 0:
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", rawURL)
	case 1:
		args = append(args, "-x", "--audio-format", "mp3", "--audio-quality", "0", rawURL)
	case 2:
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", rawURL)
	case 3:
		args = append(args, "-F", rawURL)
	case 4:
		if strings.TrimSpace(formatID) == "" {
			return nil, errors.New("格式 ID 不可空白")
		}
		args = append(args, "-f", strings.TrimSpace(formatID), rawURL)
	case 5:
		args = append(args, "-f", "bv*[height<=720]+ba/b[height<=720]", "--merge-output-format", "mp4", rawURL)
	default:
		args = append(args, rawURL)
	}
	return args, nil
}

func siteDisplayName(site string) string {
	switch site {
	case "youtube":
		return "YouTube"
	case "bilibili":
		return "Bilibili"
	case "vimeo":
		return "Vimeo"
	case "haokan":
		return "此網站"
	case "tiktok":
		return "TikTok"
	case "dramatip":
		return "網頁播放器"
	default:
		return site
	}
}

func addSiteCompatibilityArgs(args []string, site string) []string {
	if len(args) == 0 {
		return args
	}
	extras := []string{"--impersonate", "chrome"}
	switch site {
	case "bilibili":
		extras = append(extras, "--force-ipv4")
	case "vimeo":
		extras = append(extras, "--extractor-args", "vimeo:client=web")
	}
	return insertBeforeLast(args, extras...)
}

func insertBeforeLast(args []string, extras ...string) []string {
	if len(args) == 0 || len(extras) == 0 {
		return append([]string(nil), args...)
	}
	out := make([]string, 0, len(args)+len(extras))
	out = append(out, args[:len(args)-1]...)
	out = append(out, extras...)
	out = append(out, args[len(args)-1])
	return out
}

func expandSequenceURL(raw string, startN, endN int) ([]string, error) {
	if startN <= 0 || endN < startN {
		return nil, errors.New("連續序號範圍無效")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("無法解析連續序號網址：%s", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return nil, fmt.Errorf("網址中找不到可替換的集數：%s", raw)
	}

	kind := ""
	index := len(parts) - 1
	prefix := ""
	if len(parts) >= 4 && strings.EqualFold(parts[0], "shortdrama") && strings.EqualFold(parts[1], "episode") && allDigits(parts[2]) && allDigits(parts[3]) {
		kind = "segment"
		index = 3
	} else if m := regexp.MustCompile(`(?i)^(episode[-_]?)(\d+)$`).FindStringSubmatch(parts[index]); len(m) == 3 {
		kind = "episode-prefix"
		prefix = m[1]
	} else if allDigits(parts[index]) {
		kind = "segment"
	} else {
		for _, key := range []string{"episode", "ep", "episode_id"} {
			if value := u.Query().Get(key); allDigits(value) {
				kind = "query:" + key
				break
			}
		}
	}
	if kind == "" {
		return nil, fmt.Errorf("無法判斷集數位置。支援網址結尾為 /episode/數字、episode-數字，或最後一段為數字的格式：%s", raw)
	}

	result := make([]string, 0, endN-startN+1)
	for n := startN; n <= endN; n++ {
		clone := *u
		if strings.HasPrefix(kind, "query:") {
			q := clone.Query()
			q.Set(strings.TrimPrefix(kind, "query:"), strconv.Itoa(n))
			clone.RawQuery = q.Encode()
		} else {
			newParts := append([]string(nil), parts...)
			if kind == "episode-prefix" {
				newParts[index] = prefix + strconv.Itoa(n)
			} else {
				newParts[index] = strconv.Itoa(n)
			}
			clone.Path = "/" + strings.Join(newParts, "/")
		}
		result = append(result, clone.String())
	}
	return result, nil
}

func normalizeInputURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("網址不可空白")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("無法辨識網址：%s", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("目前只接受 http 或 https 網址：%s", raw)
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if host == "space.bilibili.com" {
		parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
		if len(parts) == 0 || !allDigits(parts[0]) {
			return "", errors.New("Bilibili 空間網址需要包含使用者 UID，例如：https://space.bilibili.com/123456/video")
		}
		if len(parts) == 1 {
			u.Path = "/" + parts[0] + "/video"
		}
	}
	return u.String(), nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func classifySiteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	switch {
	case host == "youtube.com" || host == "youtu.be" || strings.HasSuffix(host, ".youtube.com"):
		return "youtube"
	case host == "b23.tv" || strings.HasSuffix(host, ".bilibili.com") || host == "bilibili.com":
		return "bilibili"
	case host == "vimeo.com" || strings.HasSuffix(host, ".vimeo.com"):
		return "vimeo"
	case host == "haokan.baidu.com":
		return "haokan"
	case host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com"):
		return "tiktok"
	case host == "dramatip.net" || strings.HasSuffix(host, ".dramatip.net"):
		return "dramatip"
	default:
		return ""
	}
}

func isCollectionURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	path := strings.ToLower(strings.Trim(u.Path, "/"))
	if host == "space.bilibili.com" {
		return true
	}
	if host == "vimeo.com" || strings.HasSuffix(host, ".vimeo.com") {
		prefixes := []string{"user", "channels/", "showcase/", "album/", "groups/", "ondemand/"}
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
	}
	return false
}

func (a *app) runCommand(ctx context.Context, args []string) error {
	return a.runExternalCommand(ctx, a.ytDlpPath, args, "yt-dlp")
}

func (a *app) runLuxCommand(ctx context.Context, args []string) error {
	return a.runExternalCommand(ctx, a.luxPath, args, "Lux")
}

func (a *app) runExternalCommand(ctx context.Context, exePath string, args []string, engine string) error {
	cmd := exec.CommandContext(ctx, exePath, args...)
	cmd.Dir = a.binDir
	cmd.Env = append(os.Environ(), "PATH="+a.binDir+";"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP}

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return &commandRunError{Cause: err, Summary: "無法啟動 " + engine + "：" + err.Error()}
	}
	done := make(chan struct{})
	a.cmdMu.Lock()
	a.currentCmd = cmd
	a.currentDone = done
	a.cmdMu.Unlock()

	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		waitCh <- err
	}()

	reader := bufio.NewReaderSize(pr, 64*1024)
	var readErr error
	var outputTail string
	for {
		line, lineErr := reader.ReadString('\n')
		if len(line) > 0 {
			appendOutputTail(&outputTail, line, 96*1024)
			displayLine := strings.ReplaceAll(strings.ReplaceAll(line, "\r\n", "\n"), "\n", "\r\n")
			a.postLog(displayLine)
		}
		if lineErr != nil {
			if !errors.Is(lineErr, io.EOF) {
				readErr = lineErr
			}
			break
		}
		if a.stopRequested.Load() {
			break
		}
	}
	_ = pr.Close()
	err := <-waitCh
	a.cmdMu.Lock()
	a.currentCmd = nil
	a.currentDone = nil
	close(done)
	a.cmdMu.Unlock()

	if a.stopRequested.Load() {
		return nil
	}
	if err != nil {
		return &commandRunError{Cause: err, Summary: summarizeCommandOutputForEngine(outputTail, err, engine)}
	}
	if readErr != nil {
		return &commandRunError{Cause: readErr, Summary: "讀取 " + engine + " 執行訊息失敗：" + readErr.Error()}
	}
	return nil
}

func appendOutputTail(dst *string, value string, limit int) {
	if value == "" || limit <= 0 {
		return
	}
	*dst += value
	if len(*dst) > limit {
		*dst = (*dst)[len(*dst)-limit:]
		if i := strings.IndexByte(*dst, '\n'); i >= 0 && i+1 < len(*dst) {
			*dst = (*dst)[i+1:]
		}
	}
}

func summarizeCommandOutputForEngine(output string, cause error, engine string) string {
	result := summarizeCommandOutput(output, cause)
	if engine != "" && engine != "yt-dlp" {
		result = strings.ReplaceAll(result, "yt-dlp 結束時回報", engine+" 結束時回報")
		result = strings.ReplaceAll(result, "yt-dlp 執行失敗", engine+" 執行失敗")
	}
	return result
}

func summarizeCommandOutput(output string, cause error) string {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var important []string
	var fallback []string
	for _, raw := range lines {
		line := strings.TrimSpace(stripANSI(raw))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "[download]") || strings.Contains(lower, "error") {
			fallback = append(fallback, line)
			if len(fallback) > 14 {
				fallback = fallback[len(fallback)-14:]
			}
		}
		if strings.Contains(lower, "error:") ||
			strings.Contains(lower, "unsupported url") ||
			strings.Contains(lower, "unable to") ||
			strings.Contains(lower, "http error") ||
			strings.Contains(lower, "sign in") ||
			strings.Contains(lower, "login required") ||
			strings.Contains(lower, "password") ||
			strings.Contains(lower, "no video formats") ||
			strings.Contains(lower, "precondition failed") ||
			strings.Contains(lower, "forbidden") ||
			strings.Contains(lower, "too many requests") {
			important = append(important, line)
		}
	}
	chosen := important
	if len(chosen) == 0 {
		chosen = fallback
	}
	if len(chosen) > 8 {
		chosen = chosen[len(chosen)-8:]
	}
	if len(chosen) == 0 {
		if cause != nil {
			return "yt-dlp 結束時回報：" + cause.Error()
		}
		return "yt-dlp 執行失敗，但沒有提供可辨識的錯誤訊息。"
	}
	result := strings.Join(chosen, "\n")
	lowerAll := strings.ToLower(result)
	var tip string
	switch {
	case strings.Contains(lowerAll, "sign in") || strings.Contains(lowerAll, "login required") || strings.Contains(lowerAll, "cookies"):
		tip = "\n\n建議：先選『網站登入狀態 → 自動偵測』或 Firefox；也可指定 Netscape 格式 cookies.txt。若 Edge／Chrome Cookie 無法讀取，請先完全關閉瀏覽器或改用 Firefox。"
	case strings.Contains(lowerAll, "vimeo") && (strings.Contains(lowerAll, "404") || strings.Contains(lowerAll, "403") || strings.Contains(lowerAll, "password")):
		tip = "\n\n建議：Vimeo 私有、嵌入或密碼影片可能需要瀏覽器登入狀態、--referer 原始網頁網址或 --video-password。"
	case strings.Contains(lowerAll, "bilibili") && (strings.Contains(lowerAll, "412") || strings.Contains(lowerAll, "403") || strings.Contains(lowerAll, "no video formats")):
		tip = "\n\n建議：選擇已登入 Bilibili 的瀏覽器；空間頁請使用含 UID 的完整網址，例如 https://space.bilibili.com/123456/video。"
	case strings.Contains(lowerAll, "unsupported url"):
		tip = "\n\nyt-dlp 本身尚未支援此網址。AIXAI 會自動嘗試網站專用解析與 Lux 備援引擎；若仍失敗，可能是網站已改版、需要登入，或媒體採 DRM/加密保護。"
	}
	return result + tip
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansiPattern.ReplaceAllString(s, "") }

func (a *app) stopCurrent() {
	if !a.busy.Load() {
		return
	}
	a.stopRequested.Store(true)
	a.postStatus("狀態：正在停止目前任務…")
	a.postLog("\r\n使用者要求停止。\r\n")
	a.cancelCurrentTask()
	a.killCurrentProcessTree(4 * time.Second)
}

func (a *app) killCurrentProcessTree(timeout time.Duration) {
	a.cmdMu.Lock()
	cmd := a.currentCmd
	done := a.currentDone
	a.cmdMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	killer := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	_ = killer.Run()
	_ = cmd.Process.Kill()

	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
}

func (a *app) postLog(s string) {
	if s == "" || a.closing.Load() {
		return
	}
	a.recordLog(s)
	a.logMu.Lock()
	a.logPending = append(a.logPending, s)
	a.logPendingBytes += len(s)

	// If the UI is temporarily unable to drain output, retain the newest data
	// and release the oldest queued records once the pending queue exceeds
	// roughly 10 MiB. This prevents an unresponsive or resized window from
	// allowing Go-side memory to grow without bound.
	for a.logPendingBytes > maxPendingLogBytes && a.logHead < len(a.logPending) {
		a.logPendingBytes -= len(a.logPending[a.logHead])
		a.logPending[a.logHead] = ""
		a.logHead++
		a.logDropped++
	}
	a.logMu.Unlock()
	a.signalLogDrain()
}

func (a *app) queueLatestStatus(s string) {
	if a.closing.Load() {
		return
	}
	select {
	case a.statusQueue <- s:
	default:
		// Status text is transient. Drop one old entry so the newest progress
		// message remains visible instead of blocking the worker.
		select {
		case <-a.statusQueue:
		default:
		}
		select {
		case a.statusQueue <- s:
		default:
		}
	}
	a.signalStatusDrain()
}

func (a *app) postStatus(s string) {
	a.queueLatestStatus(s)
}

func (a *app) postEnv(s string) {
	// Prefix lets UI distinguish environment status.
	a.queueLatestStatus("__ENV__" + s)
}

func (a *app) beginTask() context.Context {
	ctx, cancel := context.WithCancel(a.rootCtx)
	a.taskMu.Lock()
	if a.taskCancel != nil {
		a.taskCancel()
	}
	a.taskCancel = cancel
	a.taskMu.Unlock()
	return ctx
}

func (a *app) endTask() {
	a.taskMu.Lock()
	cancel := a.taskCancel
	a.taskCancel = nil
	a.taskMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *app) cancelCurrentTask() {
	a.taskMu.Lock()
	cancel := a.taskCancel
	a.taskMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *app) beginShutdown() {
	a.shutdownOnce.Do(func() {
		a.closing.Store(true)
		a.stopRequested.Store(true)
		a.cancelCurrentTask()
		if a.rootCancel != nil {
			a.rootCancel()
		}
		a.killCurrentProcessTree(3 * time.Second)

		workersDone := make(chan struct{})
		go func() {
			a.workerWG.Wait()
			close(workersDone)
		}()
		select {
		case <-workersDone:
		case <-time.After(3 * time.Second):
		}
		if a.captureRunning.Load() {
			killCaptureProfileBrowsers(a.captureProfileDir())
		}

		a.logMu.Lock()
		a.logPending = nil
		a.logHead = 0
		a.logPendingBytes = 0
		a.logDropped = 0
		a.logMu.Unlock()
	})
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
}

func downloadFile(ctx context.Context, url, dest, label string, logFn func(string), statusFn func(string)) error {
	tmp := dest + ".part"
	_ = os.Remove(tmp)
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(tmp)
		}
	}()
	client := &http.Client{Timeout: 45 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 256*1024)
	var downloaded int64
	total := resp.ContentLength
	lastReport := time.Now().Add(-time.Hour)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			downloaded += int64(n)
			if time.Since(lastReport) >= 2*time.Second {
				if total > 0 {
					pct := float64(downloaded) * 100 / float64(total)
					text := fmt.Sprintf("%s：%.1f MB / %.1f MB（%.0f%%）", label, float64(downloaded)/(1024*1024), float64(total)/(1024*1024), pct)
					statusFn("狀態：正在下載 " + text)
				} else {
					statusFn(fmt.Sprintf("狀態：正在下載 %s：%.1f MB", label, float64(downloaded)/(1024*1024)))
				}
				lastReport = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && downloaded != total {
		return fmt.Errorf("下載不完整：預期 %d bytes，實際 %d bytes", total, downloaded)
	}
	_ = os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	completed = true
	logFn(fmt.Sprintf("✓ %s 下載完成（%.1f MB）。\r\n", label, float64(downloaded)/(1024*1024)))
	return nil
}

var sha256TokenPattern = regexp.MustCompile(`(?i)[0-9a-f]{64}`)
var powershellHashPattern = regexp.MustCompile(`(?im)^\s*Hash\s*:\s*([0-9a-f]{64})\s*$`)

func verifyRemoteSHA256(ctx context.Context, path, checksumURL, wantedName string) error {
	text, err := fetchText(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("無法取得官方校驗碼：%w", err)
	}
	expected, err := parseOfficialSHA256(text, wantedName)
	if err != nil {
		return err
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA-256 不符（預期 %s，實際 %s）", expected, actual)
	}
	return nil
}

// parseOfficialSHA256 supports the common sha256sum format used by yt-dlp
// and FFmpeg, plus Deno's Windows checksum files generated by PowerShell
// Get-FileHash | Format-List, whose hash appears on a separate "Hash :" line.
func parseOfficialSHA256(text, wantedName string) (string, error) {
	normalized := strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	wantedBase := strings.ToLower(filepath.Base(strings.TrimSpace(wantedName)))

	// 1) Standard format: <hash> [*]filename. This may be one entry among many.
	for _, rawLine := range strings.Split(normalized, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		matches := sha256TokenPattern.FindAllString(line, -1)
		if len(matches) == 0 {
			continue
		}
		if wantedBase == "" || strings.Contains(strings.ToLower(line), wantedBase) {
			return strings.ToLower(matches[0]), nil
		}
	}

	// 2) Windows Deno releases use PowerShell Format-List, for example:
	//    Algorithm : SHA256
	//    Hash      : <64 hex characters>
	//    Path      : ...\deno-x86_64-pc-windows-msvc.zip
	if wantedBase == "" || strings.Contains(strings.ToLower(normalized), wantedBase) {
		if m := powershellHashPattern.FindStringSubmatch(normalized); len(m) == 2 {
			return strings.ToLower(m[1]), nil
		}
	}

	// 3) Dedicated checksum assets should contain exactly one unique SHA-256.
	// This fallback remains strict: ambiguous files with multiple hashes fail.
	unique := make(map[string]struct{})
	for _, token := range sha256TokenPattern.FindAllString(normalized, -1) {
		token = strings.ToLower(token)
		if _, err := hex.DecodeString(token); err == nil {
			unique[token] = struct{}{}
		}
	}
	if len(unique) == 1 {
		for token := range unique {
			return token, nil
		}
	}

	return "", errors.New("官方校驗檔中找不到唯一且可辨識的 SHA-256")
}

func fetchText(ctx context.Context, url string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractSelected(zipPath, destDir string, wanted map[string]string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := make(map[string]bool)
	for _, zf := range zr.File {
		base := strings.ToLower(filepath.Base(filepath.FromSlash(zf.Name)))
		outName, ok := wanted[base]
		if !ok {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		tmp := filepath.Join(destDir, outName+".extracting")
		out, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr1 := out.Close()
		closeErr2 := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr1 != nil {
			return closeErr1
		}
		if closeErr2 != nil {
			return closeErr2
		}
		final := filepath.Join(destDir, outName)
		_ = os.Remove(final)
		if err := os.Rename(tmp, final); err != nil {
			return err
		}
		found[base] = true
	}
	for name := range wanted {
		if !found[strings.ToLower(name)] {
			return fmt.Errorf("壓縮檔中找不到 %s", name)
		}
	}
	return nil
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

func getText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, int(n)+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}
func setText(hwnd uintptr, text string) {
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))))
}
func isChecked(hwnd uintptr) bool {
	r, _, _ := procSendMessageW.Call(hwnd, BM_GETCHECK, 0, 0)
	return r == BST_CHECKED
}
func boolToUintptr(v bool) uintptr {
	if v {
		return 1
	}
	return 0
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
