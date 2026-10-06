package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Episode pages that keep one address for every episode select the episode
// with a query parameter (?ep=N). The page itself usually embeds only the
// first episode, so yt-dlp's generic extractor would save episode 1 for every
// N without any error. Such addresses are therefore always played in the
// capture browser, which loads exactly the episode the page shows.
//
// Two safeguards:
//   - every saved file is compared with the earlier episodes of the batch;
//     an identical file means the page ignored the parameter → stop;
//   - two episodes in a row that do not play (sign-in or unlock required,
//     or the page has no such episode) stop the batch instead of opening
//     the browser for every remaining episode. Nothing tries to get around
//     such a restriction.

var episodeQueryKeys = []string{"ep", "episode", "episode_id"}

// episodeFromQuery returns the episode number carried in the query, or 0.
func episodeFromQuery(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	for _, key := range episodeQueryKeys {
		if v := u.Query().Get(key); allDigits(v) {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

// seriesNameFromPath names the series after the last meaningful path segment,
// e.g. /en/film/some-title-123/watch → "some-title-123".
func seriesNameFromPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	for i := len(parts) - 1; i >= 0; i-- {
		switch strings.ToLower(parts[i]) {
		case "watch", "play", "player", "video", "videos", "episode", "episodes":
			continue
		}
		if len(parts[i]) > 2 {
			if name, err := url.PathUnescape(parts[i]); err == nil {
				return name
			}
			return parts[i]
		}
	}
	return ""
}

// errEpisodeRepeated stops a batch whose page returned the same video again.
type errEpisodeRepeated struct{ ep, sameAs int }

func (e *errEpisodeRepeated) Error() string {
	return fmt.Sprintf("第 %d 集下載到的影片與第 %d 集完全相同：這個網頁可能不支援以網址參數指定集數，已停止以免存下重複的影片", e.ep, e.sameAs)
}

// episodeBatch remembers the files saved by one batch.
type episodeBatch struct {
	byHash   map[string]int
	failures int // consecutive episodes that did not play
}

func newEpisodeBatch() *episodeBatch { return &episodeBatch{byHash: map[string]int{}} }

// runEpisodeCapture downloads one ?ep=N episode through the capture browser.
func (a *app) runEpisodeCapture(ctx context.Context, batch *episodeBatch, rawURL string, mode int, formatID, output string, cfg settings) error {
	ep := episodeFromQuery(rawURL)
	a.postLog(fmt.Sprintf("→ 這個網頁的所有集數共用同一個網址，改以瀏覽器播放第 %d 集並擷取影片。\r\n", ep))
	a.lastCaptureFile = ""
	err := a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg)
	if err != nil {
		batch.failures++
		return err
	}
	batch.failures = 0
	if mode == 3 || a.lastCaptureFile == "" {
		return nil
	}
	sum, hashErr := fileSHA256(a.lastCaptureFile)
	if hashErr != nil {
		return nil
	}
	if first, ok := batch.byHash[sum]; ok && first != ep {
		// Keep the file (nothing is deleted) but mark it so it is not taken for episode N.
		ext := filepath.Ext(a.lastCaptureFile)
		marked := strings.TrimSuffix(a.lastCaptureFile, ext) + fmt.Sprintf("（與第%d集相同）", first) + ext
		if os.Rename(a.lastCaptureFile, marked) == nil {
			a.postLog("ℹ 重複的檔案已改名為：" + filepath.Base(marked) + "\r\n")
		}
		return &errEpisodeRepeated{ep: ep, sameAs: first}
	}
	batch.byHash[sum] = ep
	return nil
}

func isEpisodeRepeated(err error) bool {
	var rep *errEpisodeRepeated
	return errors.As(err, &rep)
}
