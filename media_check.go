package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Media check: every file a download step writes is opened with ffprobe
// before the step counts as done. A step that "succeeds" with the wrong
// thing — a few-second preview or ad, an audio-only track saved as MP4, a
// file that cannot be read, the same video as an earlier episode — then
// fails instead, so the caller can try the next candidate or the next way of
// getting the video (see runUnsupportedFallback, runBrowserCaptureFallback).
//
// Rules:
//   - always blocking: unreadable file; no picture in a video download; no
//     sound in an audio download;
//   - blocking only for "strict" steps (results guessed from a web page:
//     yt-dlp's generic extractor, page candidates, browser capture, backup
//     engines): shorter than shortClipSeconds, or a picture without sound;
//     elsewhere these are only reported;
//   - a playlist/collection download is only reported, never failed;
//   - user's own format choice (-f / -x in 進階參數) is respected: the
//     picture/sound rules are skipped.
// A failed file is never deleted: it is moved to the failedCheckDir subfolder
// (with failedCheckSuffix) so the next attempt does not mistake it for an
// existing download.

const (
	shortClipSeconds  = 10.0
	failedCheckSuffix = "（未通過檢查）"
	failedCheckDir    = "AIXAI_未通過檢查"
	probeTimeout      = 45 * time.Second
)

var (
	mediaExts = map[string]bool{
		".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".mov": true, ".flv": true, ".ts": true, ".avi": true, ".3gp": true,
		".mp3": true, ".m4a": true, ".aac": true, ".opus": true, ".ogg": true, ".oga": true, ".wav": true, ".flac": true, ".wma": true,
	}
	audioExts = map[string]bool{
		".mp3": true, ".m4a": true, ".aac": true, ".opus": true, ".ogg": true, ".oga": true, ".wav": true, ".flac": true, ".wma": true,
	}
	// yt-dlp's per-format intermediate files (title.f137.mp4) are merged and
	// removed afterwards; a kept one (-k) is one half of the video.
	intermediateFormatRE = regexp.MustCompile(`(?i)\.f[0-9a-z_-]+\.[a-z0-9]+$`)
)

// seqFile is a checked file of a sequence batch: the item that saved it.
type seqFile struct {
	item int
	path string
}

// mediaInfo is what ffprobe reports about one file.
type mediaInfo struct {
	Duration   float64
	Width      int
	Height     int
	VideoCodec string
	AudioCodec string
	HasVideo   bool
	HasAudio   bool
	Size       int64
}

func (m mediaInfo) summary() string {
	var parts []string
	if m.HasVideo {
		parts = append(parts, fmt.Sprintf("%d×%d", m.Width, m.Height))
	}
	var codecs []string
	if m.VideoCodec != "" {
		codecs = append(codecs, m.VideoCodec)
	}
	if m.AudioCodec != "" {
		codecs = append(codecs, m.AudioCodec)
	}
	if len(codecs) > 0 {
		parts = append(parts, strings.Join(codecs, "/"))
	}
	if m.Duration > 0 {
		parts = append(parts, formatSeconds(m.Duration))
	}
	if m.Size > 0 {
		parts = append(parts, fmt.Sprintf("%.1f MB", float64(m.Size)/(1<<20)))
	}
	return strings.Join(parts, "，")
}

func formatSeconds(s float64) string {
	if s < 60 {
		return fmt.Sprintf("%.0f 秒", s)
	}
	total := int(s + 0.5)
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, total/60%60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// parseProbeJSON reads `ffprobe -show_format -show_streams -of json` output.
func parseProbeJSON(data []byte) (mediaInfo, error) {
	var raw struct {
		Streams []struct {
			CodecType   string         `json:"codec_type"`
			CodecName   string         `json:"codec_name"`
			Width       int            `json:"width"`
			Height      int            `json:"height"`
			Duration    string         `json:"duration"`
			Disposition map[string]int `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return mediaInfo{}, err
	}
	var info mediaInfo
	streamDur := 0.0
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			// Cover art in audio files is a one-frame "video" stream.
			if s.Disposition["attached_pic"] == 1 {
				continue
			}
			if !info.HasVideo {
				info.HasVideo, info.VideoCodec, info.Width, info.Height = true, s.CodecName, s.Width, s.Height
			}
		case "audio":
			if !info.HasAudio {
				info.HasAudio, info.AudioCodec = true, s.CodecName
			}
		default:
			continue
		}
		if d, err := strconv.ParseFloat(s.Duration, 64); err == nil && d > streamDur {
			streamDur = d
		}
	}
	if d, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil && d > 0 {
		info.Duration = d
	} else {
		info.Duration = streamDur
	}
	info.Size, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	if !info.HasVideo && !info.HasAudio {
		return info, errors.New("沒有影像或聲音軌")
	}
	return info, nil
}

// errProbeUnavailable: ffprobe itself could not run; the file is not judged.
var errProbeUnavailable = errors.New("ffprobe 無法執行")

func (a *app) probeMedia(ctx context.Context, path string) (mediaInfo, error) {
	if !validFile(a.ffprobePath, 1<<20) {
		return mediaInfo{}, errProbeUnavailable
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, a.ffprobePath, "-v", "error", "-show_format", "-show_streams", "-of", "json", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	cmd.Dir = a.binDir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && pctx.Err() == nil {
			return mediaInfo{}, errors.New("ffprobe 讀不出媒體內容")
		}
		return mediaInfo{}, errProbeUnavailable
	}
	return parseProbeJSON(out)
}

// checkPolicy says which rules apply to one file.
type checkPolicy struct {
	needVideo bool // a video download must contain a picture
	needAudio bool // an audio download must contain sound
	strict    bool // short clips and silent videos block instead of warning
	warnOnly  bool // report only (playlists / collections)
}

// userChoosesFormat reports whether the user's 進階參數 pick the format or
// extract audio themselves; their choice is never second-guessed.
func userChoosesFormat(extra string) bool {
	args, err := splitArgs(extra)
	if err != nil {
		return true
	}
	for _, a := range args {
		switch {
		case a == "-f", a == "--format", strings.HasPrefix(a, "--format="), strings.HasPrefix(a, "-f") && len(a) > 2 && !strings.HasPrefix(a, "--"),
			a == "-x", a == "--extract-audio":
			return true
		}
	}
	return false
}

func policyFor(path string, mode int, cfg settings, strict bool) checkPolicy {
	p := checkPolicy{strict: strict, warnOnly: mode == 2}
	if mode == 3 || mode == 4 || userChoosesFormat(cfg.ExtraArgs) {
		return p
	}
	if audioExts[strings.ToLower(filepath.Ext(path))] {
		p.needAudio = true
	} else if mode == 0 || mode == 2 || mode == 5 {
		p.needVideo = true
	}
	return p
}

// mediaProblem is one finding about a file.
type mediaProblem struct {
	Text     string
	Blocking bool
}

func judgeMedia(info mediaInfo, p checkPolicy) []mediaProblem {
	var out []mediaProblem
	add := func(text string, hard bool) {
		out = append(out, mediaProblem{Text: text, Blocking: !p.warnOnly && (hard || p.strict)})
	}
	if p.needVideo && !info.HasVideo {
		add("只有聲音、沒有影像", true)
	}
	if p.needAudio && !info.HasAudio {
		add("沒有聲音", true)
	}
	if p.needVideo && info.HasVideo && !info.HasAudio {
		add("影片沒有聲音（可能只抓到影像軌）", false)
	}
	if info.Duration > 0 && info.Duration < shortClipSeconds {
		add(fmt.Sprintf("長度只有 %.1f 秒，可能是預告、片頭或廣告", info.Duration), false)
	}
	return out
}

// mediaCheckError: a step finished but its file did not pass the check.
type mediaCheckError struct {
	File   string
	Reason string
}

func (e *mediaCheckError) Error() string {
	return fmt.Sprintf("下載的檔案未通過檢查：%s（%s）", e.Reason, filepath.Base(e.File))
}

func isMediaCheckFailure(err error) bool {
	var m *mediaCheckError
	return errors.As(err, &m)
}

// resetOutputs starts collecting the files of a new download step; the
// previous step's yt-dlp run is forgotten too, so the truncation repair in
// finishStep never re-runs an earlier step's command.
func (a *app) resetOutputs() {
	a.outputs = nil
	a.lastYtArgs, a.lastYtFiles = nil, nil
}

// noteOutput records a media file written by the current step.
func (a *app) noteOutput(path string) {
	if strings.TrimSpace(path) != "" {
		a.outputs = append(a.outputs, path)
	}
}

// sideFileExts are files a download may write next to the video (subtitles,
// cover, metadata); they are neither checked nor taken for a wrong result.
var sideFileExts = map[string]bool{
	".vtt": true, ".srt": true, ".ass": true, ".lrc": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".json": true, ".description": true, ".txt": true, ".part": true, ".ytdl": true,
}

// nonMediaOutputs returns existing files of the current step that are not
// media at all, e.g. the web page itself saved by a backup engine.
func (a *app) nonMediaOutputs() []string {
	var files []string
	for _, f := range a.outputs {
		ext := strings.ToLower(filepath.Ext(f))
		if mediaExts[ext] || sideFileExts[ext] || !validFile(f, 1) {
			continue
		}
		files = append(files, f)
	}
	return files
}

// pendingOutputs returns the existing final media files of the current step.
func (a *app) pendingOutputs() []string {
	seen := map[string]bool{}
	var files []string
	for _, f := range a.outputs {
		key := strings.ToLower(filepath.Clean(f))
		if seen[key] || !mediaExts[strings.ToLower(filepath.Ext(f))] || intermediateFormatRE.MatchString(filepath.Base(f)) || !validFile(f, 1) {
			continue
		}
		seen[key] = true
		files = append(files, f)
	}
	return files
}

// markFailedCheck moves a file that did not pass into the failedCheckDir
// subfolder (renamed in place when that folder cannot be made), so the
// download folder holds only good files and the next attempt does not take
// it for an existing download. Nothing is deleted.
func markFailedCheck(path string) string {
	ext := filepath.Ext(path)
	name := strings.TrimSuffix(filepath.Base(path), ext) + failedCheckSuffix
	dir := filepath.Join(filepath.Dir(path), failedCheckDir)
	if os.MkdirAll(dir, 0755) != nil {
		dir = filepath.Dir(path)
	}
	base := filepath.Join(dir, name)
	target := base + ext
	for n := 2; n < 100; n++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = fmt.Sprintf("%s_%d%s", base, n, ext)
	}
	if os.Rename(path, target) != nil {
		return ""
	}
	return target
}

// verifyOutputs checks the files of the current step and consumes the list,
// so an outer step does not check the same files again. It returns a
// *mediaCheckError when a file must not count as the download.
func (a *app) verifyOutputs(ctx context.Context, mode int, cfg settings, strict bool) error {
	files := a.pendingOutputs()
	other := a.nonMediaOutputs()
	a.outputs = nil
	if a.stopRequested.Load() {
		return nil
	}
	if len(files) == 0 && len(other) > 0 && strict && mode != 3 && mode != 2 {
		// The step "succeeded" with a file that is no video or audio at all.
		reason := "下載到的不是影音檔（可能是網頁本身）"
		a.diagNote("檔案檢查 ✗ " + reason + "：" + filepath.Base(other[0]))
		a.rejectFile(other[0], reason)
		return &mediaCheckError{File: other[0], Reason: reason}
	}
	if len(files) == 0 {
		return nil
	}
	type checked struct {
		path string
		info mediaInfo
	}
	var passed []checked
	var failure *mediaCheckError
	for _, f := range files {
		p := policyFor(f, mode, cfg, strict)
		info, err := a.probeMedia(ctx, f)
		if errors.Is(err, errProbeUnavailable) {
			a.postLog("ℹ 無法執行 ffprobe，略過檔案檢查：" + filepath.Base(f) + "\r\n")
			a.diagNote("檔案檢查略過（ffprobe 無法執行）")
			continue
		}
		if err != nil {
			reason := "無法讀取媒體內容（檔案可能損毀或不是影音檔）"
			a.diagNote("檔案檢查 ✗ " + reason)
			if p.warnOnly {
				a.postLog("⚠ 檔案檢查：" + filepath.Base(f) + " " + reason + "\r\n")
				continue
			}
			if failure == nil {
				failure = &mediaCheckError{File: f, Reason: reason}
			}
			a.rejectFile(f, reason)
			continue
		}
		problems := judgeMedia(info, p)
		var blocking, warnings []string
		for _, pr := range problems {
			if pr.Blocking {
				blocking = append(blocking, pr.Text)
			} else {
				warnings = append(warnings, pr.Text)
			}
		}
		if len(blocking) > 0 {
			reason := strings.Join(blocking, "；")
			a.diagNote(fmt.Sprintf("檔案檢查 ✗ %s（%s）", reason, info.summary()))
			if failure == nil {
				failure = &mediaCheckError{File: f, Reason: reason}
			}
			a.rejectFile(f, reason)
			continue
		}
		if len(warnings) > 0 {
			a.postLog(fmt.Sprintf("⚠ 檔案檢查：%s（%s）— %s\r\n", filepath.Base(f), info.summary(), strings.Join(warnings, "；")))
			a.diagNote(fmt.Sprintf("檔案檢查 ⚠ %s（%s）", strings.Join(warnings, "；"), info.summary()))
		} else {
			a.postLog(fmt.Sprintf("✓ 檔案檢查通過：%s（%s）\r\n", filepath.Base(f), info.summary()))
			a.diagNote("檔案檢查 ✓ " + info.summary())
		}
		passed = append(passed, checked{f, info})
	}
	if failure != nil {
		return failure
	}
	// Episodes of one batch must differ: the same file again means the page
	// served another episode (often episode 1) for this address.
	if a.seqHashes != nil && mode != 2 {
		sums := map[string]string{}
		for _, c := range passed {
			sum, err := fileSHA256(c.path)
			if err != nil {
				continue
			}
			if first, ok := a.seqHashes[sum]; ok && first.item != a.curItem {
				reason := fmt.Sprintf("與第 %d 項下載到的影片完全相同", first.item+1)
				a.diagNote("檔案檢查 ✗ " + reason)
				if strings.EqualFold(filepath.Clean(first.path), filepath.Clean(c.path)) {
					// The same file name as the earlier item: the download was
					// skipped as existing. That file belongs to the earlier
					// item and is left as it is.
					a.postLog(fmt.Sprintf("⚠ 檔案檢查未通過：%s — %s（檔名也相同，未另存新檔）\r\n", filepath.Base(c.path), reason))
				} else {
					a.rejectFile(c.path, reason)
				}
				return &mediaCheckError{File: c.path, Reason: reason}
			}
			sums[sum] = c.path
		}
		for sum, path := range sums {
			a.seqHashes[sum] = seqFile{item: a.curItem, path: path}
		}
	}
	return nil
}

// finishStep ends a download step: a cut-short yt-dlp result is completed
// first (truncation.go), then the step's files are checked.
func (a *app) finishStep(ctx context.Context, mode int, cfg settings, strict bool) error {
	if err := a.repairTruncatedDownload(ctx, cfg); err != nil {
		a.outputs = nil
		return err
	}
	return a.verifyOutputs(ctx, mode, cfg, strict)
}

func (a *app) rejectFile(path, reason string) {
	if marked := markFailedCheck(path); marked != "" {
		a.postLog(fmt.Sprintf("⚠ 檔案檢查未通過：%s — %s。檔案已移到 %s 資料夾保留\r\n", filepath.Base(path), reason, filepath.Base(filepath.Dir(marked))))
	} else {
		a.postLog(fmt.Sprintf("⚠ 檔案檢查未通過：%s — %s\r\n", filepath.Base(path), reason))
	}
	if a.lastCaptureFile == path {
		a.lastCaptureFile = ""
	}
}
