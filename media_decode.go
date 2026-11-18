//go:build windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// 檔頭正常、但畫面資料被網站自己加密的檔案：ffprobe 讀得出 1080×1920 h264，
// 播放器卻完全解不出畫面（2026-11-18 回報，網址檔名含 .encrypt）。
// 試解碼開頭幾秒，錯誤多到不可能是偶發雜訊時判定未通過。
// 正常檔實測 0 行錯誤、加密檔前 4 秒約 2000 行。

const (
	decodeCheckSeconds   = "4"
	decodeCheckTimeout   = 30 * time.Second
	decodeErrorThreshold = 50
)

const undecodableReason = "畫面資料無法解碼（網站可能對影片內容加密，只能在網站播放器觀看）"

// countDecodeErrors decodes the first seconds of the video track and returns
// how many error lines FFmpeg printed; ok is false when FFmpeg could not run.
func (a *app) countDecodeErrors(ctx context.Context, path string) (int, bool) {
	if !validFile(a.ffmpegPath, 1<<20) {
		return 0, false
	}
	dctx, cancel := context.WithTimeout(ctx, decodeCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(dctx, a.ffmpegPath, "-nostdin", "-v", "error", "-t", decodeCheckSeconds, "-i", path, "-map", "0:v:0", "-f", "null", "-")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	cmd.Dir = a.binDir
	out, _ := cmd.CombinedOutput()
	if dctx.Err() != nil {
		return 0, false
	}
	return countErrorLines(string(out)), true
}

func countErrorLines(out string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// isUndecodable reports whether err is a file check that failed because the
// picture could not be decoded.
func isUndecodable(err error) bool {
	var m *mediaCheckError
	return errors.As(err, &m) && m.Reason == undecodableReason
}

// undecodable reports whether a video file's picture cannot be decoded.
func (a *app) undecodable(ctx context.Context, path string, info mediaInfo) bool {
	if !info.HasVideo {
		return false
	}
	n, ok := a.countDecodeErrors(ctx, path)
	return ok && n >= decodeErrorThreshold
}
