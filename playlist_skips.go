package main

import (
	"fmt"
	"regexp"
	"strings"
)

// A playlist runs with --no-abort-on-error, so yt-dlp goes on past videos
// that cannot be downloaded (private, removed by the uploader) but still exits with
// an error at the end. When the playlist finished and those were its only
// errors, every video that could be downloaded was: the item is done, and the
// skipped videos are listed instead of starting the retry / sign-in flow
// (which runs the whole playlist again and ends with an unrelated cookie error).

var ytItemErrorRE = regexp.MustCompile(`^ERROR: \[[\w:]+\] ([\w-]+): (.+)$`)

// unavailableItemReasons are per-video errors no retry or sign-in fixes.
// "Video unavailable" alone is not one: yt-dlp also says it when the site
// is limiting requests ("This content isn't available, try again later").
var unavailableItemReasons = []string{
	"private video",
	"removed by the uploader",
	"account associated with this video has been terminated",
	"video has been removed for violating",
	"has not made this video available in your country",
}

type ytRunErrors struct {
	unavailable  []string // "<id>：<reason>"
	other        bool
	playlistDone bool
}

func (r *ytRunErrors) observe(line string) {
	for _, part := range strings.Split(line, "\r") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "[download] Finished downloading playlist") {
			r.playlistDone = true
			continue
		}
		if !strings.HasPrefix(part, "ERROR:") {
			continue
		}
		if m := ytItemErrorRE.FindStringSubmatch(part); m != nil && isUnavailableItemReason(m[2]) {
			r.unavailable = append(r.unavailable, m[1]+"："+unavailableItemText(m[2]))
			continue
		}
		r.other = true
	}
}

func isUnavailableItemReason(reason string) bool {
	lower := strings.ToLower(reason)
	for _, n := range unavailableItemReasons {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func unavailableItemText(reason string) string {
	if strings.Contains(strings.ToLower(reason), "private video") {
		return "私人影片"
	}
	return "已移除或無法觀看"
}

// onlyUnavailable: the playlist ran to the end and its only errors were
// videos that cannot be downloaded (a private video only with the account it
// was shared with, which the sign-in hint in the log explains).
func (r *ytRunErrors) onlyUnavailable() bool {
	return r.playlistDone && !r.other && len(r.unavailable) > 0
}

// acceptPlaylistSkips turns a yt-dlp error into success when the latest run
// was a finished playlist whose only errors were unavailable videos and that
// saved (or already had) at least one video.
func (a *app) acceptPlaylistSkips(err error) error {
	// A playlist with nothing saved at all still fails.
	if err == nil || a.stopRequested.Load() || !a.lastYtErrors.onlyUnavailable() || len(a.lastYtFiles) == 0 {
		return err
	}
	skipped := a.lastYtErrors.unavailable
	a.postLog(fmt.Sprintf("ℹ 播放清單已全部處理完；其中 %d 部影片是私人或已移除，已略過：\r\n", len(skipped)))
	private := false
	for _, s := range skipped {
		a.postLog("   · " + s + "\r\n")
		private = private || strings.HasSuffix(s, "私人影片")
	}
	if private {
		a.postLog("ℹ 若影片擁有者有分享給你的帳號，可在『網站登入狀態』選擇已登入的瀏覽器，或按『選擇…』指定 cookies.txt 後重試。\r\n")
	}
	a.diagNote(fmt.Sprintf("播放清單略過 %d 部私人／已移除影片", len(skipped)))
	return nil
}
