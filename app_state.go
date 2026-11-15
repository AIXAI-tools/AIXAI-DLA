//go:build windows

package main

import (
	"context"
	"os/exec"
	"sync"
	"sync/atomic"
)

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
