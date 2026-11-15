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
	"sync"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	appTitle   = "AIXAI 萬能下載工具"
	appVersion = "4.2.38"

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
	// CaptureBrowser is the background capture browser: "default" (the
	// Windows default browser) or edge/chrome/brave/vivaldi.
	CaptureBrowser string `json:"capture_browser"`
	Sequence       bool   `json:"sequence"`
	SequenceStart  int    `json:"sequence_start"`
	SequenceEnd    int    `json:"sequence_end"`
	// EpisodeFormat and EpisodeSep name 連續集數 files (episode_naming.go);
	// SeriesName is per task only and never saved to settings.json.
	EpisodeFormat string `json:"episode_format,omitempty"`
	EpisodeSep    string `json:"episode_sep,omitempty"`
	SeriesName    string `json:"series_name,omitempty"`
	// Episodes numbers each item of a retried 連續集數 task (failed.go): the
	// retried addresses are no longer one run from SequenceStart. Per task
	// only, like SeriesName.
	Episodes []int `json:"episodes,omitempty"`
	// FileName is the 自訂檔名 of a one-address task (custom_name.go). Per
	// task only, like SeriesName.
	FileName string `json:"file_name,omitempty"`
	// UnsafeMode turns 帳號安全模式 off. Stored inverted so that settings files
	// written by older versions (without the key) get safety mode enabled.
	UnsafeMode bool `json:"unsafe_mode"`
	// AdvancedOpen remembers whether 進階設定 is expanded in the narrow layout.
	AdvancedOpen bool `json:"advanced_open"`
	// DisclaimerAccepted is the version of the terms the user agreed to; the
	// UI asks again whenever the terms change.
	DisclaimerAccepted string `json:"disclaimer_accepted"`
	// MaxJobs is how many download tasks may run at the same time.
	MaxJobs int `json:"max_jobs"`
	// DataFolder holds run logs and report packs; empty = 文件\AIXAI 萬能下載工具.
	DataFolder string `json:"data_folder,omitempty"`
	// NoStartupUpdateCheck turns off the quiet update check at startup. Stored
	// inverted so that settings files from older versions keep it enabled.
	NoStartupUpdateCheck bool `json:"no_startup_update_check"`
	// ConvertH264 re-encodes AV1/VP9 downloads to H.264 (compat.go); off by
	// default since current players read AV1/VP9 and conversion is slow.
	ConvertH264 bool `json:"convert_h264"`
	// SubtitlesOnly downloads only subtitle files (.srt) without downloading video.
	SubtitlesOnly bool `json:"subtitles_only"`
}

type doneInfo struct {
	Kind    string
	Err     error
	Stopped bool
}

// appShared is what every download task shares: the window, the tools, the
// log display queue and the capture browser. Each task runs on its own *app
// (see jobs.go) that embeds the same *appShared, so stop flags, the running
// process, the log file and the per-run state below stay separate per task.
type appShared struct {
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

	statusQueue chan string
	doneQueue   chan doneInfo

	// The pending log queue is capped as a second layer of memory protection.
	logMu           sync.Mutex
	logPending      []string
	logHead         int
	logPendingBytes int
	logDropped      int

	// capBrowser is the capture browser shared by all tasks; captureSem lets
	// one task use it at a time (a capture may wait for the user).
	capBrowser *cdpBrowser
	captureSem chan struct{}
	// envSem serialises tool checks/updates between tasks.
	envSem chan struct{}
	// captureRunning is true while a capture browser may be alive (checked at
	// shutdown).
	captureRunning atomic.Bool
	// loginBrowser is the capture browser while it waits for the user to sign
	// in or act (used by the 顯示登入視窗 button).
	loginBrowser atomic.Pointer[cdpBrowser]
	// capturePref is the capture browser chosen in 進階設定 (string; "" or
	// "default" = the system default browser). See capture_browser.go.
	capturePref atomic.Value

	// Coalesce UI wake-up messages so heavy command output cannot flood
	// the Win32 message queue and make the window appear unresponsive.
	logWakePending    atomic.Bool
	statusWakePending atomic.Bool

	// installing is set while an update or rollback runs (updater.go): a
	// second one would share the .new/.old files and resume the paused tasks.
	installing atomic.Bool
	// persistWarned: a failed save was already reported (persist.go).
	persistWarned atomic.Bool

	closing      atomic.Bool
	shutdownOnce sync.Once
	resourceOnce sync.Once
	workerWG     sync.WaitGroup

	rootCtx    context.Context
	rootCancel context.CancelFunc

	// Download tasks (jobs.go). main is the window's own *app; maxJobs is
	// settings.MaxJobs for the scheduler.
	main         *app
	maxJobs      atomic.Int32
	jobsMu       sync.Mutex
	jobs         []*job
	loadJobsOnce sync.Once
	nextJobID    int

	// Addresses that could not be downloaded (failed.go).
	failedMu       sync.Mutex
	failed         []failedEntry
	failedNextID   int
	loadFailedOnce sync.Once

	// Download history (history.go) and the folder of logs and report packs
	// (datadir.go).
	historyMu       sync.Mutex
	history         []historyEntry
	historyNextID   int
	loadHistoryOnce sync.Once
	dataFolderPref  atomic.Value
	session         *sessionRecorder // crash_record.go
}

type app struct {
	*appShared

	// job is the task this *app runs; nil for the window's own instance.
	job *job
	// itemOffset maps runURLs' item numbers to the task's full item list.
	itemOffset int

	resumePath string

	// Chronological copy of this run's log, used by the 複製 Log button and
	// mirrored (with secrets redacted) to the run's log file.
	sessionMu    sync.Mutex
	sessionLog   []string
	sessionBytes int
	logFileMu    sync.Mutex
	runLogPath   string // file of the running task; guarded by logFileMu
	lastRunLog   string // file of the running or most recent task
	// runPos is the index of the item the running task is on.
	runPos atomic.Int32
	// itemFiles are the checked files of the current item (download history).
	itemFiles []string

	// captureStartFailed stops a batch when the capture browser cannot be
	// started; loginCancelled stops it when the user closes the login window.
	captureStartFailed atomic.Bool
	loginCancelled     atomic.Bool

	// lastYtArgs / lastYtFiles describe the most recent yt-dlp run (only used
	// by the worker goroutine) so a truncated result can be fetched again.
	lastYtArgs  []string
	lastYtFiles []string
	// lastCmdOutput is the output tail of the most recent external command.
	lastCmdOutput string
	// captureKeys are the 16-byte responses the page fetched during the latest
	// browser capture (possible AES-128 stream keys; capture_hls_key.go).
	captureKeysMu sync.Mutex
	captureKeys   []capturedKey
	// lastCaptureFile is the file saved by the latest browser-capture download;
	// lastCaptureStreams the stream address(es) it came from.
	lastCaptureFile    string
	lastCaptureStreams []string
	// hlsSubs: the subtitle tracks of the HLS stream being downloaded
	// (audio_language.go), saved when the page lists no subtitle files.
	hlsSubs []pageSubtitle
	// episodeGuard is set while one ?ep=N episode is captured (episode_capture.go).
	episodeGuard *episodeGuard
	// captureMaxShortSide is the 最高 720p limit of the current browser
	// capture (0 = best), for streams the page data lists by size.
	captureMaxShortSide int
	// lastYtGeneric: the latest yt-dlp run used its generic (any web page)
	// extractor, so its result is a guess and is checked strictly.
	lastYtGeneric bool
	// lastYtErrors sorts the error lines of the latest yt-dlp run
	// (playlist_skips.go).
	lastYtErrors ytRunErrors
	// outputs are the media files written by the current download step, for
	// the media check (media_check.go). seqHashes (sequence batches only)
	// maps each checked file's SHA-256 to the item that saved it; curItem is
	// the item being downloaded.
	outputs   []string
	seqHashes map[string]seqFile
	curItem   int
	// itemSubs: subtitles saved for the current item in subtitles-only mode.
	itemSubs []savedSubtitle
	// lastYtSubs: subtitles written by the latest yt-dlp run.
	lastYtSubs []string
	// diag is the current item's trail of tried ways (diagnostics.go).
	diag *itemDiag
	// pageEpisodeHint: the stream the page data lists for the current page
	// (page_episodes.go); only used while Page equals the item's address.
	pageEpisodeHint pageEpisodeHint
	// deferWithheld: an episode the site withholds is put off to the end of
	// the batch instead of opening the capture browser (first visit only).
	deferWithheld bool
	// freshStreams: other episodes' streams the capture browser saw preloaded
	// during this job (page_episodes.go), used when their turn comes.
	freshMu      sync.Mutex
	freshStreams []freshEpisodeStream
	// pageStreamDownload is an optional test hook used by downloadPageEpisode
	// unit tests; nil in production.
	pageStreamDownload func(ctx context.Context, stream string) ([]string, error)

	busy          atomic.Bool
	stopRequested atomic.Bool

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
	procGetWindowPlacement            = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement            = user32.NewProc("SetWindowPlacement")
	procIsIconic                      = user32.NewProc("IsIconic")
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
