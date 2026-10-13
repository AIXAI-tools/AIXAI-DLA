//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"
)

// Feedback attachments: a partial screenshot taken with Windows' own
// snipping overlay (the user picks the area, so nothing private is captured
// by accident), and run logs attached as scrubbed copies.
//
// The snip is started with the standard ms-screenclip: URI. Windows puts the
// picture on the clipboard; the app waits for the clipboard to change and
// reads the bitmap from it. Nothing else on the screen is read.

const (
	CF_DIB         = 8
	snipWait       = 90 * time.Second
	maxFeedbackLog = 5 << 20
)

var (
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
)

// snipRunning: one snip at a time; snipCancel asks the waiting one to stop.
var snipRunning, snipCancel atomic.Bool

// startSnip opens the snipping overlay and reports the result as a "snip"
// event: {image} or {error} ({cancelled} when the user gave up waiting).
func (a *app) startSnip() string {
	if !snipRunning.CompareAndSwap(false, true) {
		return "已經在等待截圖。"
	}
	snipCancel.Store(false)
	seq, _, _ := procGetClipboardSequenceNumber.Call()
	openInBrowser("ms-screenclip:")
	go func() {
		defer snipRunning.Store(false)
		deadline := time.Now().Add(snipWait)
		for time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
			if snipCancel.Load() || a.closing.Load() {
				a.emit("snip", map[string]interface{}{"cancelled": true})
				return
			}
			now, _, _ := procGetClipboardSequenceNumber.Call()
			if now == seq {
				continue
			}
			seq = now
			if r, _, _ := procIsClipboardFormatAvailable.Call(CF_DIB); r == 0 {
				continue // text or something else was copied meanwhile
			}
			img, err := a.saveClipboardImage()
			if err != nil {
				a.emit("snip", map[string]interface{}{"error": err.Error()})
			} else {
				a.emit("snip", map[string]interface{}{"image": img})
			}
			return
		}
		a.emit("snip", map[string]interface{}{"error": "90 秒內沒有收到截圖（可能按了 Esc 取消）。可以再按一次「部分截圖」，或用「加入圖片…」選擇已存的截圖。"})
	}()
	return ""
}

// saveClipboardImage writes the clipboard bitmap as a PNG in the feedback folder.
func (a *app) saveClipboardImage() (feedbackImage, error) {
	var data []byte
	var err error
	for try := 0; try < 10; try++ { // the snipping tool may still hold the clipboard
		if data, err = readClipboardDIB(); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return feedbackImage{}, err
	}
	img, err := dibToImage(data)
	if err != nil {
		return feedbackImage{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return feedbackImage{}, err
	}
	name := "AIXAI_回報截圖_" + time.Now().Format("20060102_150405") + ".png"
	path := filepath.Join(a.feedbackDir(), name)
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return feedbackImage{}, err
	}
	out := feedbackImage{Path: path, Name: name}
	if buf.Len() <= 6<<20 {
		out.DataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	return out, nil
}

func readClipboardDIB() ([]byte, error) {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return nil, errors.New("剪貼簿正被其他程式使用，請稍後再試")
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(CF_DIB)
	if h == 0 {
		return nil, errors.New("剪貼簿中沒有圖片")
	}
	size, _, _ := procGlobalSize.Call(h)
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 || size == 0 {
		return nil, errors.New("無法讀取剪貼簿中的圖片")
	}
	defer procGlobalUnlock.Call(h)
	if size > 512<<20 {
		return nil, errors.New("剪貼簿中的圖片太大")
	}
	data := make([]byte, size)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&data[0])), ptr, size)
	return data, nil
}

// dibToImage decodes a CF_DIB block (BITMAPINFOHEADER or a later version
// followed by the pixels); 24- and 32-bit pictures, as screenshots are.
func dibToImage(data []byte) (image.Image, error) {
	bad := errors.New("無法辨識剪貼簿中的圖片格式")
	if len(data) < 40 {
		return nil, bad
	}
	le := binary.LittleEndian
	hdrSize := int(le.Uint32(data[0:]))
	w := int(int32(le.Uint32(data[4:])))
	h := int(int32(le.Uint32(data[8:])))
	bpp := int(le.Uint16(data[14:]))
	compression := le.Uint32(data[16:])
	if hdrSize < 40 || hdrSize > len(data) || w <= 0 || h == 0 || w > 20000 || (bpp != 24 && bpp != 32) {
		return nil, bad
	}
	if compression != 0 && compression != 3 { // BI_RGB, BI_BITFIELDS
		return nil, bad
	}
	offset := hdrSize
	if compression == 3 && hdrSize == 40 {
		offset += 12 // the three colour masks follow a plain header
	}
	if clrUsed := int(le.Uint32(data[32:])); clrUsed > 0 && clrUsed < 1<<16 {
		offset += clrUsed * 4
	}
	bottomUp := h > 0
	if h < 0 {
		h = -h
	}
	if h > 20000 {
		return nil, bad
	}
	stride := (w*bpp + 31) / 32 * 4
	if offset+stride*h > len(data) {
		return nil, bad
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	step := bpp / 8
	for y := 0; y < h; y++ {
		src := y
		if bottomUp {
			src = h - 1 - y
		}
		row := data[offset+src*stride:]
		for x := 0; x < w; x++ {
			p := row[x*step:]
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = p[2], p[1], p[0], 255
		}
	}
	return img, nil
}

// feedbackLog is a run log attached to a report (a scrubbed copy).
type feedbackLog struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// runLogFiles lists the run logs in the data folder, newest first.
func (a *app) runLogFiles() []string {
	base := a.dataFolder()
	if base == "" {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(filepath.Join(base, runLogsSubdir), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), runLogPrefix) && strings.EqualFold(filepath.Ext(p), ".txt") {
			files = append(files, p)
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		if fi == nil || fj == nil {
			return files[i] > files[j]
		}
		return fi.ModTime().After(fj.ModTime())
	})
	return files
}

// attachFeedbackLog makes the scrubbed copy of one log in the feedback folder.
func (a *app) attachFeedbackLog(src string) (feedbackLog, error) {
	info, err := os.Stat(src)
	if err != nil {
		return feedbackLog{}, err
	}
	if info.Size() > maxFeedbackLog {
		return feedbackLog{}, errors.New(filepath.Base(src) + " 超過 5 MB，請改附較短的紀錄")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return feedbackLog{}, err
	}
	text := privacyScrub(strings.TrimPrefix(string(data), "\uFEFF"))
	name := filepath.Base(src)
	dest := filepath.Join(a.feedbackDir(), name)
	if err := os.WriteFile(dest, []byte("\uFEFF"+text), 0644); err != nil {
		return feedbackLog{}, err
	}
	return feedbackLog{Path: dest, Name: name}, nil
}

// feedbackFailMarkers are the lines a run log gets when an item or the whole
// task failed (main.go 項失敗／diagnostics.go 結果：失敗／jobs.go finishJob).
var feedbackFailMarkers = []string{"項失敗：", "結果：失敗", "部分項目失敗", "] 錯誤："}

// feedbackFailScan is how many of the newest run logs are searched for a
// failed one.
const feedbackFailScan = 10

func runLogHasFailure(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxFeedbackLog {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(data)
	for _, m := range feedbackFailMarkers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// latestFeedbackLogs attaches the newest run log and, when that one has no
// failure, also the newest run log that does: a report is usually about a
// failure, while the newest log may belong to another task still running.
func (a *app) latestFeedbackLogs() []feedbackLog {
	files := a.runLogFiles()
	var out []feedbackLog
	newest := ""
	for _, f := range files {
		if l, err := a.attachFeedbackLog(f); err == nil {
			out, newest = append(out, l), f
			break
		}
	}
	if newest == "" || runLogHasFailure(newest) {
		return out
	}
	for i, f := range files {
		if i >= feedbackFailScan {
			break
		}
		if f == newest || !runLogHasFailure(f) {
			continue
		}
		if l, err := a.attachFeedbackLog(f); err == nil {
			out = append(out, l)
		}
		break
	}
	return out
}

// pickFeedbackLogs is 加入紀錄檔…: the file dialog starts in the run-log folder.
func (a *app) pickFeedbackLogs() ([]feedbackLog, string) {
	dir := ""
	if files := a.runLogFiles(); len(files) > 0 {
		dir = filepath.Dir(files[0])
	} else if base := a.dataFolder(); base != "" {
		dir = filepath.Join(base, runLogsSubdir)
	}
	var out []feedbackLog
	var problems []string
	for _, src := range a.openFileDialog("選擇要附上的紀錄檔", dir, true, "紀錄檔 (*.txt;*.log)", "*.txt;*.log") {
		l, err := a.attachFeedbackLog(src)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, l)
	}
	return out, strings.Join(problems, "；")
}
