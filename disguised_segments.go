package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Some HLS hosts put a small image header (a 1×1 PNG) in front of every
// segment, so the segments look like pictures to the network in between. The
// data after the picture is ordinary, unencrypted MPEG-TS; the site's player
// drops the picture before playing. yt-dlp joins the segments as they come,
// so the saved file starts with a PNG and is taken for an image. Such a file
// is unwrapped here: the picture in front of each segment is dropped and the
// rest is remuxed into the MP4 it was meant to be (nothing is re-encoded).

var (
	disguisePNGSig = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	disguisePNGEnd = []byte("IEND")
)

const (
	tsPacketSize        = 188
	disguiseMaxHeader   = 4096 // a disguise picture is tiny; real images are not looked for
	unwrapTempTSSuffix  = ".unwrap-ts.ts"
	unwrapTempMP4Suffix = ".unwrap-ts.mp4"
)

// disguiseHeaderAt returns the length of a disguise picture starting at
// b[i] (a PNG whose end is directly followed by TS packets), or 0. need is
// true when b ends before the picture could be judged.
func disguiseHeaderAt(b []byte, i int) (n int, need bool) {
	if !bytes.HasPrefix(b[i:], disguisePNGSig) {
		if len(b)-i < len(disguisePNGSig) && bytes.HasPrefix(disguisePNGSig, b[i:]) {
			return 0, true
		}
		return 0, false
	}
	limit := i + disguiseMaxHeader
	if limit > len(b) {
		limit = len(b)
	}
	e := bytes.Index(b[i:limit], disguisePNGEnd)
	if e < 0 {
		return 0, limit == len(b) && limit-i < disguiseMaxHeader
	}
	end := i + e + len(disguisePNGEnd) + 4 // IEND chunk type + CRC
	if end+tsPacketSize >= len(b) {
		return 0, true
	}
	if b[end] != 0x47 || b[end+tsPacketSize] != 0x47 {
		return 0, false
	}
	return end - i, false
}

// isDisguisedTS reports a file that starts with a disguise picture.
func isDisguisedTS(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, disguiseMaxHeader+2*tsPacketSize)
	n, _ := io.ReadFull(f, head)
	h, _ := disguiseHeaderAt(head[:n], 0)
	return h > 0
}

// stripDisguiseHeaders copies r to w without the disguise pictures and
// returns how many it dropped.
func stripDisguiseHeaders(r io.Reader, w io.Writer) (int, error) {
	const chunk = 4 << 20
	br := bufio.NewReaderSize(r, chunk)
	buf := make([]byte, 0, 2*chunk)
	dropped := 0
	eof := false
	for {
		if !eof {
			tmp := make([]byte, chunk)
			n, err := io.ReadFull(br, tmp)
			buf = append(buf, tmp[:n]...)
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				eof = true
			} else if err != nil {
				return dropped, err
			}
		}
		start, i := 0, 0
		keep := len(buf)
		for {
			j := bytes.IndexByte(buf[i:], 0x89)
			if j < 0 {
				break
			}
			j += i
			n, need := disguiseHeaderAt(buf, j)
			if need && !eof {
				keep = j // judge it with the next block
				break
			}
			if n > 0 {
				if _, err := w.Write(buf[start:j]); err != nil {
					return dropped, err
				}
				dropped++
				start = j + n
				i = start
				continue
			}
			i = j + 1
		}
		if _, err := w.Write(buf[start:keep]); err != nil {
			return dropped, err
		}
		buf = append(buf[:0], buf[keep:]...)
		if eof {
			if len(buf) > 0 {
				if _, err := w.Write(buf); err != nil {
					return dropped, err
				}
			}
			return dropped, nil
		}
	}
}

// unwrapDisguisedSegments repairs a downloaded file whose segments were
// disguised as pictures. It reports whether the file was replaced; a file
// that is not such a download is left untouched.
func (a *app) unwrapDisguisedSegments(ctx context.Context, path string) bool {
	if !isDisguisedTS(path) {
		return false
	}
	if !validFile(a.ffmpegPath, 1<<20) {
		a.postLog("⚠ 下載的片段前面附有圖片檔頭，但找不到 FFmpeg，無法還原：" + filepath.Base(path) + "\r\n")
		return false
	}
	a.postLog("→ 下載的影片片段前面都附有一小張圖片（網站的包裝方式），正在去掉並還原成 MP4（不重新編碼）…\r\n")
	a.postStatus("狀態：正在還原影片片段…")
	base := strings.TrimSuffix(path, filepath.Ext(path))
	tsPath, mp4Path := base+unwrapTempTSSuffix, base+unwrapTempMP4Suffix
	defer os.Remove(tsPath) // our own temp files
	err := func() error {
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(tsPath)
		if err != nil {
			return err
		}
		bw := bufio.NewWriterSize(out, 4<<20)
		n, err := stripDisguiseHeaders(in, bw)
		if err == nil {
			err = bw.Flush()
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil && n == 0 {
			err = errors.New("沒有找到圖片檔頭")
		}
		return err
	}()
	if err == nil {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "mpegts", "-i", tsPath,
			"-map", "0:v?", "-map", "0:a?", "-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart", mp4Path}
		err = a.runExternalCommand(ctx, a.ffmpegPath, args, "FFmpeg")
	}
	if err == nil {
		var info mediaInfo
		info, err = a.probeMedia(ctx, mp4Path)
		if err == nil && !info.HasVideo && !info.HasAudio {
			err = errors.New("還原後沒有影音內容")
		}
	}
	if err == nil {
		if rerr := os.Rename(mp4Path, path); rerr != nil {
			err = fmt.Errorf("無法取代原檔：%w", rerr)
		}
	}
	if err != nil {
		_ = os.Remove(mp4Path)
		a.postLog("⚠ 還原影片片段未成功：" + firstLine(err.Error()) + "\r\n")
		a.diagNote("還原圖片包裝片段 ✗ " + firstLine(err.Error()))
		return false
	}
	a.postLog("✓ 已還原影片片段：" + filepath.Base(path) + "\r\n")
	a.diagNote("還原圖片包裝片段 ✓")
	return true
}
