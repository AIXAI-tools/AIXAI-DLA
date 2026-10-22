package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Some file hosts limit the speed of every single connection, not of the
// whole download (measured: 1 connection 1.2 MiB/s, 8 connections 9.5 MiB/s
// on the same file). yt-dlp fetches a plain file over one connection, so a
// large file captured from the browser is fetched here in fixed-size pieces
// over several connections at once, the way download managers do. The pieces
// that are done are remembered next to the file, so a paused or stopped task
// continues where it was, also with a fresh stream address of the same file.

const (
	parallelConnections = 8
	parallelChunkSize   = 8 << 20
	parallelMinSize     = 64 << 20 // smaller files gain little; yt-dlp keeps them
	parallelChunkTries  = 6
	parallelStateSuffix = ".aixai-parts.json"
)

// errNotParallel: the server cannot serve pieces (no ranges) or the file is
// small; the caller downloads it the usual way.
var errNotParallel = errors.New("伺服器不支援分段下載")

var contentRangeTotalRE = regexp.MustCompile(`^bytes \d+-\d+/(\d+)$`)

type parallelState struct {
	Size      int64  `json:"size"`
	ChunkSize int64  `json:"chunk_size"`
	Done      []bool `json:"done"`
}

func (s *parallelState) chunkRange(i int) (int64, int64) {
	start := int64(i) * s.ChunkSize
	end := start + s.ChunkSize - 1
	if end >= s.Size {
		end = s.Size - 1
	}
	return start, end
}

func newParallelState(size int64) *parallelState {
	n := int((size + parallelChunkSize - 1) / parallelChunkSize)
	return &parallelState{Size: size, ChunkSize: parallelChunkSize, Done: make([]bool, n)}
}

// loadParallelState returns the saved progress for a file of this size. A
// ".part" left by yt-dlp (no state file) counts as its leading pieces.
func loadParallelState(dest string, size int64) *parallelState {
	if b, err := os.ReadFile(dest + parallelStateSuffix); err == nil {
		var s parallelState
		if json.Unmarshal(b, &s) == nil && s.Size == size && s.ChunkSize > 0 &&
			len(s.Done) == int((size+s.ChunkSize-1)/s.ChunkSize) {
			return &s
		}
	}
	s := newParallelState(size)
	if st, err := os.Stat(dest + ".part"); err == nil && st.Size() > 0 && st.Size() <= size {
		for i := range s.Done {
			if _, end := s.chunkRange(i); end < st.Size() {
				s.Done[i] = true
			}
		}
	}
	return s
}

func saveParallelState(dest string, s *parallelState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(dest+parallelStateSuffix, b)
}

// rangeRequest builds a GET for bytes [start, end] with the browser's headers.
func rangeRequest(ctx context.Context, rawURL string, headers map[string]string, start, end int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	return req, nil
}

// probeRangeSize asks for the first byte and returns the full size when the
// server answers with a part (206) and a known total.
func probeRangeSize(ctx context.Context, client *http.Client, rawURL string, headers map[string]string) (int64, error) {
	req, err := rangeRequest(ctx, rawURL, headers, 0, 0)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode >= 400 {
			return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return 0, errNotParallel
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "html") || strings.Contains(ct, "mpegurl") {
		return 0, errNotParallel
	}
	m := contentRangeTotalRE.FindStringSubmatch(strings.TrimSpace(resp.Header.Get("Content-Range")))
	if m == nil {
		return 0, errNotParallel
	}
	return strconv.ParseInt(m[1], 10, 64)
}

// fetchChunk writes bytes [start, end] of the file at their place in f,
// counting them in got as they arrive. It returns how many it wrote.
func fetchChunk(ctx context.Context, client *http.Client, rawURL string, headers map[string]string, f *os.File, start, end int64, got *atomic.Int64) (int64, error) {
	req, err := rangeRequest(ctx, rawURL, headers, start, end)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !strings.HasPrefix(strings.TrimSpace(resp.Header.Get("Content-Range")), fmt.Sprintf("bytes %d-%d/", start, end)) {
		return 0, errors.New("伺服器回傳的範圍不符")
	}
	buf := make([]byte, 256<<10)
	off := start
	for off <= end {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if off+int64(n)-1 > end {
				return off - start, errors.New("伺服器回傳的資料超出範圍")
			}
			if _, werr := f.WriteAt(buf[:n], off); werr != nil {
				return off - start, werr
			}
			off += int64(n)
			got.Add(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return off - start, rerr
		}
	}
	if off != end+1 {
		return off - start, fmt.Errorf("連線中斷（只收到 %d／%d bytes）", off-start, end-start+1)
	}
	return off - start, nil
}

func formatMiB(n float64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.2fGiB", n/(1<<30))
	}
	return fmt.Sprintf("%.2fMiB", n/(1<<20))
}

// parallelDownload fetches rawURL into dest over several connections. It
// returns errNotParallel (nothing written) when the file should be fetched
// the usual way instead.
func (a *app) parallelDownload(ctx context.Context, rawURL string, headers map[string]string, dest string) error {
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxConnsPerHost:       parallelConnections,
		MaxIdleConnsPerHost:   parallelConnections,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}}
	defer client.CloseIdleConnections()
	pctx, cancelProbe := context.WithTimeout(ctx, 30*time.Second)
	size, err := probeRangeSize(pctx, client, rawURL, headers)
	cancelProbe()
	if err != nil {
		if errors.Is(err, errNotParallel) {
			return err
		}
		return fmt.Errorf("%w：%v", errNotParallel, err)
	}
	if size < parallelMinSize {
		return errNotParallel
	}
	state := loadParallelState(dest, size)
	// Recorded before the file is sized, so a ".part" without a record is
	// always yt-dlp's (a plain prefix), never one of ours.
	if err := saveParallelState(dest, state); err != nil {
		return err
	}
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	var doneBytes int64
	var todo []int
	for i, d := range state.Done {
		if d {
			s, e := state.chunkRange(i)
			doneBytes += e - s + 1
		} else {
			todo = append(todo, i)
		}
	}
	if doneBytes > 0 {
		a.postLog(fmt.Sprintf("[download] Resuming download at byte %d\r\n", doneBytes))
	}
	a.postLog(fmt.Sprintf("[download] Destination: %s\r\n", dest))
	a.postLog(fmt.Sprintf("→ 伺服器限制每條連線的速度，改用 %d 條連線分段下載（%s）。\r\n", parallelConnections, formatMiB(float64(size))))

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var stateMu sync.Mutex
	var got atomic.Int64
	var firstErr error
	var errOnce sync.Once
	fail := func(err error) {
		errOnce.Do(func() { firstErr = err; cancel() })
	}
	var wg sync.WaitGroup
	for w := 0; w < parallelConnections; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				start, end := state.chunkRange(i)
				var err error
				for try := 0; try < parallelChunkTries; try++ {
					if try > 0 {
						select {
						case <-wctx.Done():
							return
						case <-time.After(time.Duration(1<<uint(try-1)) * time.Second):
						}
					}
					var n int64
					n, err = fetchChunk(wctx, client, rawURL, headers, f, start, end, &got)
					if err == nil {
						break
					}
					got.Add(-n) // a failed piece is fetched again from its start
					if wctx.Err() != nil {
						return
					}
				}
				if err != nil {
					fail(fmt.Errorf("第 %d 段下載失敗：%w", i+1, err))
					return
				}
				stateMu.Lock()
				state.Done[i] = true
				_ = saveParallelState(dest, state)
				stateMu.Unlock()
			}
		}()
	}

	// Progress in yt-dlp's format, which the window turns into the task's bar.
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		last, lastAt := int64(0), time.Now()
		for {
			select {
			case <-stopProgress:
				return
			case now := <-tick.C:
				cur := got.Load()
				speed := float64(cur-last) / now.Sub(lastAt).Seconds()
				last, lastAt = cur, now
				have := doneBytes + cur
				eta := "--:--"
				if speed > 0 {
					secs := int64(float64(size-have) / speed)
					eta = fmt.Sprintf("%02d:%02d", secs/60, secs%60)
					if secs >= 3600 {
						eta = fmt.Sprintf("%d:%02d:%02d", secs/3600, secs%3600/60, secs%60)
					}
				}
				a.postLog(fmt.Sprintf("[download] %5.1f%% of %s at %s/s ETA %s\r\n", float64(have)*100/float64(size), formatMiB(float64(size)), formatMiB(speed), eta))
			}
		}
	}()

feed:
	for _, i := range todo {
		select {
		case jobs <- i:
		case <-wctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(stopProgress)
	<-progressDone
	cerr := f.Close()

	if ctx.Err() != nil || a.stopRequested.Load() {
		return errStopped // progress is kept for the next run
	}
	if firstErr != nil {
		return firstErr
	}
	if cerr != nil {
		return cerr
	}
	for _, d := range state.Done {
		if !d {
			return errors.New("仍有未完成的分段")
		}
	}
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("無法完成檔案：%w", err)
	}
	_ = os.Remove(dest + parallelStateSuffix) // our own progress record
	a.postLog(fmt.Sprintf("[download] 100%% of %s\r\n", formatMiB(float64(size))))
	return nil
}

// capturedDirectFile reports a captured stream that is one plain file (not a
// playlist), for which pieces can be fetched over several connections.
func capturedDirectFile(c browserMediaCandidate, mode int) bool {
	if mode != 0 && mode != 2 && mode != 5 {
		return false
	}
	switch c.Kind {
	case "MP4", "Video", "WebM":
	default:
		return false
	}
	ct := strings.ToLower(c.ContentType)
	return !strings.Contains(ct, "mpegurl") && !strings.Contains(ct, "dash")
}

// capturedFileExt is the extension for a captured plain file.
func capturedFileExt(c browserMediaCandidate) string {
	if strings.Contains(strings.ToLower(c.ContentType), "webm") || c.Kind == "WebM" {
		return ".webm"
	}
	return ".mp4"
}

// capturedRequestHeaders is the browser's request context for a captured
// stream, as sent with each piece.
func capturedRequestHeaders(rawURL string, c browserMediaCandidate) map[string]string {
	h := map[string]string{}
	for key, name := range map[string]string{
		"referer": "Referer", "user-agent": "User-Agent", "cookie": "Cookie", "authorization": "Authorization",
		"origin": "Origin", "accept-language": "Accept-Language",
	} {
		if v := sanitizeCapturedHeader(captureHeader(c.RequestHeaders, key)); v != "" {
			h[name] = v
		}
	}
	if h["Referer"] == "" {
		h["Referer"] = sanitizeCapturedHeader(rawURL)
	}
	return h
}

// tryParallelCapture downloads a captured plain file over several
// connections. handled is false when the file is left to yt-dlp.
func (a *app) tryParallelCapture(ctx context.Context, rawURL string, c browserMediaCandidate, mode int, output string) (handled bool, err error) {
	if !capturedDirectFile(c, mode) {
		return false, nil
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return false, nil
	}
	dest := filepath.Join(output, a.capturedFileBase(rawURL, c, output)+capturedFileExt(c))
	if _, err := os.Stat(dest); err == nil {
		return false, nil // yt-dlp reports it as already downloaded
	}
	err = a.parallelDownload(ctx, c.URL, capturedRequestHeaders(rawURL, c), dest)
	switch {
	case err == nil:
		a.noteOutput(dest)
		a.lastCaptureFile = dest
		return true, nil
	case errors.Is(err, errNotParallel):
		return false, nil
	case errors.Is(err, errStopped):
		return true, err
	default:
		// The file is already sized and partly filled, which yt-dlp would take
		// for a finished download: the pieces done are kept for the next try.
		a.postLog("⚠ 分段下載未完成：" + firstLine(err.Error()) + "。已下載的部分會保留，重試或按「繼續」會接著下載。\r\n")
		return true, err
	}
}
