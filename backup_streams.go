//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// For one video site the play data lists every stream twice: on a main
// server and on a backup server. yt-dlp only uses the main one, so when the
// main server refuses (HTTP 403) the download fails although the site's own
// player still plays the video from the backup server. This file does what
// the player does: it rebuilds the format list from the backup addresses and
// hands it to yt-dlp (--load-info-json), so format choice, file names, merging
// and the user's own options stay the same as a normal download.
//
// Only when the site serves the page normally: a page the site does not offer
// in the viewer's region comes back without a title, and is left alone.

var backupStreamsPageRE = regexp.MustCompile(`(?i)^https?://(?:www\.)?bili(?:bili\.tv|intl\.com)/(?:[a-z]{2}/)?(?:play/\d+/(\d+)|video/(\d+))`)

const backupStreamsAPI = "https://api.bilibili.tv/intl/gateway/web/playurl"

// errBackupNotServed: the site did not serve the page (no title), so the
// backup servers are not used.
var errBackupNotServed = errors.New("網站沒有在你目前的地區提供這個頁面（網頁是空白的），程式不會繞過地區限制")

// backupStreamsIDs returns the episode or video id of a page this applies to.
func backupStreamsIDs(raw string) (epID, aid string, ok bool) {
	m := backupStreamsPageRE.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// errorLine is the "ERROR:" line of yt-dlp's output (warnings come first).
func errorLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "ERROR:") {
			return l
		}
	}
	return firstLine(s)
}

func isHTTPForbidden(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "http error 403")
}

// pageServed reports whether yt-dlp read real page data: without it the
// extractor falls back to a title of the form "<extractor> video #<id>".
func pageServed(info map[string]interface{}) bool {
	title, _ := info["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	id, _ := info["id"].(string)
	extractor, _ := info["extractor"].(string)
	if id != "" && strings.EqualFold(title, extractor+" video #"+id) {
		return false
	}
	return !regexp.MustCompile(`(?i)^\S+ video #\d+$`).MatchString(title)
}

type backupPlayData struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Playurl struct {
			Video []struct {
				VideoResource backupResource `json:"video_resource"`
				StreamInfo    struct {
					DescWords string `json:"desc_words"`
				} `json:"stream_info"`
			} `json:"video"`
			AudioResource []backupResource `json:"audio_resource"`
		} `json:"playurl"`
	} `json:"data"`
}

type backupResource struct {
	URL       string   `json:"url"`
	BackupURL []string `json:"backup_url"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Bandwidth int      `json:"bandwidth"`
	Codecs    string   `json:"codecs"`
	Size      int64    `json:"size"`
}

// backupFormats builds yt-dlp formats from the backup addresses, in the same
// order and with the same fields as yt-dlp's own list (so format ids match
// what "列出格式" showed). Streams without a backup address are left out.
func backupFormats(pd backupPlayData) []map[string]interface{} {
	var out []map[string]interface{}
	for _, v := range pd.Data.Playurl.Video {
		r := v.VideoResource
		if r.URL == "" || len(r.BackupURL) == 0 || r.BackupURL[0] == "" {
			continue
		}
		out = append(out, map[string]interface{}{
			"url": r.BackupURL[0], "ext": "mp4", "format_note": v.StreamInfo.DescWords,
			"width": r.Width, "height": r.Height, "vbr": r.Bandwidth,
			"acodec": "none", "vcodec": r.Codecs, "filesize": r.Size,
		})
	}
	for _, r := range pd.Data.Playurl.AudioResource {
		if r.URL == "" || len(r.BackupURL) == 0 || r.BackupURL[0] == "" {
			continue
		}
		out = append(out, map[string]interface{}{
			"url": r.BackupURL[0], "ext": "mp4", "abr": r.Bandwidth,
			"acodec": r.Codecs, "vcodec": "none", "filesize": r.Size,
		})
	}
	return out
}

// backupInfoKeys are the page fields kept from yt-dlp's own result; the
// format choice it made there is dropped so it is made again.
var backupInfoKeys = []string{
	"id", "title", "description", "thumbnail", "thumbnails", "timestamp", "upload_date",
	"episode_number", "episode", "chapters", "subtitles", "duration", "display_id",
	"webpage_url", "original_url", "webpage_url_basename", "webpage_url_domain",
	"extractor", "extractor_key", "http_headers",
}

func backupInfo(page map[string]interface{}, formats []map[string]interface{}) map[string]interface{} {
	info := map[string]interface{}{"_type": "video"}
	for _, k := range backupInfoKeys {
		if v, ok := page[k]; ok && v != nil {
			info[k] = v
		}
	}
	info["formats"] = formats
	return info
}

// withInfoJSON swaps the page address (always the last argument of
// buildArgs) for the prepared info file.
func withInfoJSON(args []string, path string) []string {
	if len(args) == 0 {
		return nil
	}
	out := append([]string(nil), args[:len(args)-1]...)
	return append(out, "--load-info-json", path)
}

// readPageInfo asks yt-dlp for the page data only (no download).
func (a *app) readPageInfo(ctx context.Context, rawURL string, cfg settings) (map[string]interface{}, error) {
	args := []string{"--ignore-config", "--no-warnings", "--no-playlist", "-J", "--js-runtimes", "deno:" + a.denoPath}
	if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
		args = append(args, "--cookies", cookieFile)
	}
	args = append(args, rawURL)
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(pctx, a.ytDlpPath, args...)
	cmd.Dir = a.binDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("讀取頁面資訊失敗：%w", err)
	}
	var info map[string]interface{}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("頁面資訊格式不正確：%w", err)
	}
	return info, nil
}

func fetchBackupPlayData(ctx context.Context, epID, aid string, cfg settings) (backupPlayData, error) {
	q := url.Values{}
	q.Set("s_locale", "en_US")
	q.Set("platform", "web")
	if epID != "" {
		q.Set("ep_id", epID)
	} else {
		q.Set("aid", aid)
	}
	var pd backupPlayData
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, backupStreamsAPI+"?"+q.Encode(), nil)
	if err != nil {
		return pd, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://www.bilibili.tv/")
	req.Header.Set("Origin", "https://www.bilibili.tv")
	if cookie := cookieHeaderFromFile(cfg.CookieFile, "bilibili.tv"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		return pd, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return pd, err
	}
	if resp.StatusCode != http.StatusOK {
		return pd, fmt.Errorf("播放資料回應 HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &pd); err != nil {
		return pd, fmt.Errorf("播放資料格式不正確：%w", err)
	}
	if pd.Code != 0 {
		return pd, fmt.Errorf("網站回應錯誤 %d：%s", pd.Code, pd.Message)
	}
	return pd, nil
}

// clearOldInfoFiles removes the program's own info files older than maxAge
// (their stream addresses have expired by then).
func clearOldInfoFiles(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "backup-") || !strings.HasSuffix(e.Name(), ".info.json") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// downloadFromBackupStreams is the retry after the main server answered 403.
func (a *app) downloadFromBackupStreams(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	epID, aid, ok := backupStreamsIDs(rawURL)
	if !ok {
		return errors.New("不是可用備用伺服器的網址")
	}
	page, err := a.readPageInfo(ctx, rawURL, cfg)
	if err != nil {
		return err
	}
	if !pageServed(page) {
		return errBackupNotServed
	}
	pd, err := fetchBackupPlayData(ctx, epID, aid, cfg)
	if err != nil {
		return err
	}
	formats := backupFormats(pd)
	if len(formats) == 0 {
		return errors.New("網站沒有提供備用伺服器")
	}
	data, err := json.Marshal(backupInfo(page, formats))
	if err != nil {
		return err
	}
	// The file must outlive this run: the truncation repair (truncation.go)
	// runs the same yt-dlp command again. Old ones are cleared next time.
	dir := filepath.Join(a.appDir, "tmp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	clearOldInfoFiles(dir, 6*time.Hour)
	f, err := os.CreateTemp(dir, "backup-*.info.json")
	if err != nil {
		return err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	args, err := a.buildArgs(rawURL, mode, formatID, output, cfg)
	if err != nil {
		return err
	}
	return a.runCommand(ctx, withInfoJSON(args, path))
}
