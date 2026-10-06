package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Some servers answer every request — even one without a Range header — with
// "206 Partial Content" capped at a few MiB, while Content-Range still states
// the full size. yt-dlp then treats the first piece as the whole file and
// reports 100%. The result plays only the first seconds.
//
// After each successful yt-dlp run the MP4 files it wrote are checked; a
// truncated file is downloaded again with --http-chunk-size so yt-dlp asks for
// the file piece by piece (verified: a 1 MiB chunk restores the whole file,
// while a chunk larger than the server's cap is truncated again).

// truncationRetryChunks are tried in order until the file is complete.
var truncationRetryChunks = []string{"1M", "256K"}

// downloadedFilesFromOutput returns the media files yt-dlp reported writing,
// final (merged) names last, without duplicates.
func downloadedFilesFromOutput(output string) []string {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`))
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		files = append(files, p)
	}
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n") {
		line := strings.TrimSpace(stripANSI(raw))
		switch {
		case strings.HasPrefix(line, "[download] Destination: "):
			add(strings.TrimPrefix(line, "[download] Destination: "))
		case strings.HasPrefix(line, "[Merger] Merging formats into "):
			add(strings.TrimPrefix(line, "[Merger] Merging formats into "))
		case strings.HasPrefix(line, "[download] ") && strings.HasSuffix(line, " has already been downloaded"):
			add(strings.TrimSuffix(strings.TrimPrefix(line, "[download] "), " has already been downloaded"))
		}
	}
	return files
}

// truncatedMP4Files returns the existing MP4-family files in the list whose
// box structure says data is missing at the end.
func truncatedMP4Files(files []string) []string {
	var bad []string
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".mp4", ".m4a", ".m4v", ".mov":
		default:
			continue
		}
		if mp4Truncated(f) {
			bad = append(bad, f)
		}
	}
	return bad
}

// mp4Truncated walks the top-level ISO-BMFF boxes. A file is truncated when a
// box claims more bytes than remain, or when it ends before any movie header
// ("moov"). Files that do not look like MP4, or cannot be read, are not
// reported (the check must never turn a good download into a failure).
func mp4Truncated(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 16 {
		return false
	}
	size := st.Size()
	var off int64
	hasMoov := false
	first := true
	hdr := make([]byte, 16)
	for off < size {
		if size-off < 8 {
			return true // a partial box header at the end
		}
		if _, err := f.ReadAt(hdr[:8], off); err != nil && err != io.EOF {
			return false
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		boxType := string(hdr[4:8])
		if !validBoxType(boxType) {
			// Not ISO-BMFF at all → not ours to judge. Garbage after valid boxes → damaged.
			return !first
		}
		if first {
			switch boxType {
			case "ftyp", "styp", "moov", "mdat", "free", "skip", "wide", "pnot":
			default:
				return false
			}
			first = false
		}
		headerLen := int64(8)
		switch boxSize {
		case 0: // box extends to end of file
			boxSize = size - off
		case 1: // 64-bit size follows
			if size-off < 16 {
				return true
			}
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil && err != io.EOF {
				return false
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			headerLen = 16
		}
		if boxSize < headerLen {
			return true
		}
		if boxType == "moov" {
			hasMoov = true
		}
		if off+boxSize > size {
			return true
		}
		off += boxSize
	}
	return !hasMoov
}

func validBoxType(t string) bool {
	if len(t) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := t[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// repairTruncatedDownload checks the files written by the last yt-dlp run and,
// when one is cut short, runs the same command again with smaller HTTP chunks.
// It returns an error only when the file is still incomplete afterwards.
func (a *app) repairTruncatedDownload(ctx context.Context, cfg settings) error {
	args := a.lastYtArgs
	bad := truncatedMP4Files(a.lastYtFiles)
	if len(bad) == 0 || len(args) == 0 {
		return nil
	}
	a.postLog(fmt.Sprintf("⚠ 下載結果不完整：%s 只收到影片開頭（伺服器每次只回傳一小段，但回報為完成）。\r\n", filepath.Base(bad[0])))
	if userSetChunkSize(cfg.ExtraArgs) {
		return errors.New("下載的影片不完整（只有開頭片段）。附加參數已自訂 --http-chunk-size，請調小該值或移除後再試。")
	}
	for _, chunk := range truncationRetryChunks {
		if a.stopRequested.Load() || ctx.Err() != nil {
			return nil
		}
		a.postLog("→ 改用分段下載重新取得完整檔案（每段 " + chunk + "）…\r\n")
		a.postStatus("狀態：偵測到檔案不完整，正在分段重新下載…")
		retry := append(append([]string(nil), args...), "--http-chunk-size", chunk, "--force-overwrites")
		if err := a.runCommand(ctx, retry); err != nil {
			return err
		}
		if a.stopRequested.Load() {
			return nil
		}
		if len(truncatedMP4Files(a.lastYtFiles)) == 0 {
			a.postLog("✓ 分段下載完成，檔案已完整。\r\n")
			return nil
		}
	}
	return errors.New("下載的影片不完整（只有開頭片段），分段重新下載後仍未取得完整檔案。")
}

// userSetChunkSize reports whether the user's own extra args already choose a
// chunk size; the automatic retry then leaves it alone.
func userSetChunkSize(extra string) bool {
	return strings.Contains(extra, "--http-chunk-size")
}
