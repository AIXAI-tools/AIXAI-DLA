package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Episode pages that keep one address for every episode select the episode
// with a query parameter (?ep=N). The page itself usually embeds only the
// first episode, so yt-dlp's generic extractor would save episode 1 for every
// N without any error. Such addresses are therefore always played in the
// capture browser, which loads exactly the episode the page shows.
//
// Safeguards:
//   - a stream already saved for another episode of the batch is chosen last:
//     such pages often load episode 1 next to the requested one;
//   - every saved file is compared with the earlier episodes of the batch;
//     an identical file is set aside and the other streams of the capture are
//     tried; when none differs, the page ignored the parameter → stop;
//   - two episodes in a row that do not play (sign-in or unlock required,
//     or the page has no such episode) stop the batch instead of opening
//     the browser for every remaining episode. Nothing tries to get around
//     such a restriction.

var episodeQueryKeys = []string{"ep", "episode", "episode_id"}

// episodeFromQuery returns the episode number carried in the query, or 0.
func episodeFromQuery(raw string) int {
	return episodeFromQueryKeys(raw, episodeQueryKeys)
}

// episodeNumberFromQuery is episodeFromQuery without "episode_id", which on
// many sites is an id rather than the episode's number: it decides whether a
// single address (not a numbered sequence) is a shared-address episode page.
func episodeNumberFromQuery(raw string) int {
	return episodeFromQueryKeys(raw, []string{"ep", "episode"})
}

func episodeFromQueryKeys(raw string, keys []string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	for _, key := range keys {
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

// episodeBatch remembers the files and streams saved by one batch.
type episodeBatch struct {
	byHash   map[string]int
	byStream map[string]int // streamPathKey → episode
	failures int            // consecutive episodes that did not play
}

func newEpisodeBatch() *episodeBatch {
	return &episodeBatch{byHash: map[string]int{}, byStream: map[string]int{}}
}

// episodeGuard is the batch and episode being captured (app.episodeGuard).
type episodeGuard struct {
	batch *episodeBatch
	ep    int
}

// streamPathKey identifies a stream by host and path: signed query values
// change between requests of the same file.
func streamPathKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.ToLower(u.Host) + u.EscapedPath()
}

// streamUsedBy returns the other episode that already saved this stream, or 0.
func (g *episodeGuard) streamUsedBy(stream string) int {
	if g == nil || stream == "" {
		return 0
	}
	if other := g.batch.byStream[streamPathKey(stream)]; other != g.ep {
		return other
	}
	return 0
}

// checkSavedEpisode compares a saved file with the earlier episodes. A repeat is
// kept (nothing is deleted) but renamed so it is not taken for this episode.
func (a *app) checkSavedEpisode(g *episodeGuard, path string) error {
	sum, err := fileSHA256(path)
	if err != nil {
		return nil
	}
	first, ok := g.batch.byHash[sum]
	if !ok || first == g.ep {
		return nil
	}
	ext := filepath.Ext(path)
	marked := strings.TrimSuffix(path, ext) + fmt.Sprintf("（與第%d集相同）", first) + ext
	if os.Rename(path, marked) == nil {
		a.postLog("ℹ 重複的檔案已改名為：" + filepath.Base(marked) + "\r\n")
	}
	a.diagNote(fmt.Sprintf("檔案與第 %d 集完全相同", first))
	return &errEpisodeRepeated{ep: g.ep, sameAs: first}
}

// runEpisodeCapture downloads one ?ep=N episode through the capture browser.
func (a *app) runEpisodeCapture(ctx context.Context, batch *episodeBatch, rawURL string, mode int, formatID, output string, cfg settings) error {
	ep := episodeFromQuery(rawURL)
	a.postLog(fmt.Sprintf("→ 這個網頁的所有集數共用同一個網址，改以瀏覽器播放第 %d 集並擷取影片。\r\n", ep))
	a.lastCaptureFile, a.lastCaptureStreams = "", nil
	g := &episodeGuard{batch: batch, ep: ep}
	a.episodeGuard = g
	err := a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg)
	a.episodeGuard = nil
	if isEpisodeRepeated(err) {
		return err
	}
	if err != nil {
		batch.failures++
		return err
	}
	batch.failures = 0
	if mode == 3 || a.lastCaptureFile == "" {
		return nil
	}
	sum, hashErr := fileSHA256(a.lastCaptureFile)
	if hashErr == nil {
		batch.byHash[sum] = ep
	} else {
		sum = ""
	}
	var keys []string
	for _, s := range a.lastCaptureStreams {
		if s != "" {
			batch.byStream[streamPathKey(s)] = ep
			keys = append(keys, streamPathKey(s))
		}
	}
	a.rememberEpisode(ep, sum, keys)
	return nil
}

// rememberEpisode stores a saved ?ep=N episode in the task as well, so a task
// paused, moved aside or reopened still knows the episodes it saved.
func (a *app) rememberEpisode(ep int, sum string, streams []string) {
	if a.job == nil {
		return
	}
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	if sum != "" {
		if a.job.EpisodeHashes == nil {
			a.job.EpisodeHashes = map[string]int{}
		}
		a.job.EpisodeHashes[sum] = ep
	}
	for _, k := range streams {
		if a.job.EpisodeStreams == nil {
			a.job.EpisodeStreams = map[string]int{}
		}
		a.job.EpisodeStreams[k] = ep
	}
}

// savedEpisodeBatch is the ?ep=N comparison a run starts from: the episodes
// the task saved in its earlier runs.
func (a *app) savedEpisodeBatch() *episodeBatch {
	b := newEpisodeBatch()
	if a.job == nil {
		return b
	}
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for k, ep := range a.job.EpisodeHashes {
		b.byHash[k] = ep
	}
	for k, ep := range a.job.EpisodeStreams {
		b.byStream[k] = ep
	}
	return b
}

func isEpisodeRepeated(err error) bool {
	var rep *errEpisodeRepeated
	return errors.As(err, &rep)
}

// withEpisodeQuery returns raw with its episode query parameter set to n, or
// "" when the address carries none.
func withEpisodeQuery(raw string, n int) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, key := range episodeQueryKeys {
		if allDigits(q.Get(key)) {
			q.Set(key, strconv.Itoa(n))
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return ""
}

// pageVideoKeys returns the media addresses written in a page's HTML (as
// adURLKey keys), read without a browser.
func pageVideoKeys(ctx context.Context, raw string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, c := range extractWebMediaCandidates(string(b), raw) {
		keys[adURLKey(c.URL)] = true
	}
	return keys, nil
}

// builtInVideoIsEpisode decides whether the video written into the page's
// HTML (sources: its addresses there) belongs to episode ep. Pages that share
// one address for every episode usually write the same video into every
// episode's HTML; a server that writes each episode's own video gives another
// episode's page a different one. Unknown (the other page cannot be read)
// counts as "not this episode", so a wrong episode is never saved.
func builtInVideoIsEpisode(ctx context.Context, pageURL string, ep int, sources []string) bool {
	if ep <= 0 || len(sources) == 0 {
		return false
	}
	other := withEpisodeQuery(pageURL, ep+1)
	if ep > 1 {
		other = withEpisodeQuery(pageURL, ep-1)
	}
	if other == "" {
		return false
	}
	keys, err := pageVideoKeys(ctx, other)
	if err != nil || len(keys) == 0 {
		return false
	}
	for _, s := range sources {
		if keys[s] {
			return false
		}
	}
	return true
}
