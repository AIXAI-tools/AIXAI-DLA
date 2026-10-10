package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Playback compatibility: the best format of a site is often AV1 or VP9,
// which many players (and older Windows setups) cannot open. After a video
// download passes the media check, such a file is re-encoded to H.264 with
// the bundled FFmpeg, keeping its resolution. The original is replaced only
// after the new file is complete and readable; on any failure the original
// stays as it is.
//
// Skipped when the user picks the format (格式 ID mode, or -f / -x in 進階參數).

// compatVideoCodecs are ffprobe codec names converted to H.264.
var compatVideoCodecs = map[string]bool{"av1": true, "vp9": true, "vp8": true}

// compatAudioCodecs can stay in an MP4 next to H.264 without re-encoding.
var compatAudioCodecs = map[string]bool{"aac": true, "mp3": true, "ac3": true, "eac3": true}

const compatTempSuffix = ".h264-converting.mp4"

// needsCompatConvert reports whether a checked file should become H.264.
func needsCompatConvert(path string, info mediaInfo, mode int, cfg settings) bool {
	if mode != 0 && mode != 2 && mode != 5 {
		return false
	}
	if userChoosesFormat(cfg.ExtraArgs) {
		return false
	}
	if !strings.EqualFold(filepath.Ext(path), ".mp4") {
		return false
	}
	return info.HasVideo && compatVideoCodecs[strings.ToLower(info.VideoCodec)]
}

// compatFFmpegArgs builds the FFmpeg command converting in to out.
func compatFFmpegArgs(in, out string, info mediaInfo) []string {
	args := []string{"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-i", in, "-map", "0:v:0", "-map", "0:a:0?", "-map_metadata", "0",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p"}
	if !info.HasAudio || compatAudioCodecs[strings.ToLower(info.AudioCodec)] {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	}
	return append(args, "-movflags", "+faststart", out)
}

// convertForPlayback re-encodes one file to H.264 in place. An error leaves
// the original file untouched.
func (a *app) convertForPlayback(ctx context.Context, path string, info mediaInfo) error {
	if !validFile(a.ffmpegPath, 1<<20) {
		return errors.New("找不到 FFmpeg")
	}
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + compatTempSuffix
	a.postLog(fmt.Sprintf("→ 影片編碼是 %s，部分播放器無法開啟；正在轉成 H.264（相容所有播放器，解析度不變）…\r\n", strings.ToUpper(info.VideoCodec)))
	if info.Duration > 0 {
		a.postStatus(fmt.Sprintf("狀態：正在轉成 H.264（影片長度 %s，較長的影片需要較久）…", formatSeconds(info.Duration)))
	} else {
		a.postStatus("狀態：正在轉成 H.264…")
	}
	err := a.runExternalCommand(ctx, a.ffmpegPath, compatFFmpegArgs(path, tmp, info), "FFmpeg")
	if err == nil {
		var out mediaInfo
		out, err = a.probeMedia(ctx, tmp)
		if err == nil && (!out.HasVideo || out.VideoCodec != "h264" || (info.HasAudio && !out.HasAudio)) {
			err = errors.New("轉換結果不完整")
		}
	}
	if err != nil {
		_ = os.Remove(tmp) // our own unfinished temp file
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("無法取代原檔：%w", err)
	}
	return nil
}

// makePlayable converts the passed files of a step that need it.
func (a *app) makePlayable(ctx context.Context, files []string, infos []mediaInfo, mode int, cfg settings) {
	for i, f := range files {
		if a.stopRequested.Load() {
			return
		}
		if !needsCompatConvert(f, infos[i], mode, cfg) {
			continue
		}
		if err := a.convertForPlayback(ctx, f, infos[i]); err != nil {
			if a.stopRequested.Load() {
				a.postLog("ℹ 已停止轉檔，保留原本的 " + strings.ToUpper(infos[i].VideoCodec) + " 檔案。\r\n")
				return
			}
			a.postLog("⚠ 轉成 H.264 未成功，保留原本的檔案（可改用 VLC 等支援 " + strings.ToUpper(infos[i].VideoCodec) + " 的播放器開啟）：" + firstLine(err.Error()) + "\r\n")
			a.diagNote("轉成 H.264 ✗ " + firstLine(err.Error()))
			continue
		}
		a.postLog("✓ 已轉成 H.264：" + filepath.Base(f) + "\r\n")
		a.diagNote("轉成 H.264 ✓（原為 " + infos[i].VideoCodec + "）")
	}
}
