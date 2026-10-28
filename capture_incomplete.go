package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// errIncompleteStream: FFmpeg finished but left segments out of the file.
var errIncompleteStream = errors.New("無法取得完整影片")

// FFmpeg skips HLS segments it cannot open and still exits 0, so a stream
// whose later segments are encrypted with a key only the page's player can
// supply ("local://…" key URI) used to be saved as its unencrypted first few
// seconds and reported as done.

// ffmpegIncompleteReason returns why FFmpeg's output is missing segments, or
// "" when every segment was read.
func ffmpegIncompleteReason(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "unable to open key file"):
		return "影片串流有加密，解密金鑰由網頁播放器另外提供，只取得未加密的開頭片段"
	case strings.Contains(lower, "failed too many times, skipping"):
		return "部分影片片段無法下載，影片不完整"
	}
	return ""
}

// removeYtDlpLeftovers removes yt-dlp's temporary files for dest (dest.part,
// dest.part-Frag*, dest.ytdl) after its run failed and another engine takes
// over the same file name. Only these exact temporary names are touched.
func removeYtDlpLeftovers(dest string) {
	dir, base := filepath.Dir(dest), filepath.Base(dest)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if name == base+".part" || name == base+".ytdl" || strings.HasPrefix(name, base+".part-Frag") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
