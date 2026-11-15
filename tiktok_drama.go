//go:build windows

// TikTok short-drama metadata, stream selection, direct downloads, and crawler fallback.
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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type tiktokDramaPlaybackAddress struct {
	URL          string   `json:"url"`
	URLList      []string `json:"urlList"`
	URLListAlt   []string `json:"url_list"`
	URLListUpper []string `json:"UrlList"`
	DataSize     int64    `json:"dataSize"`
	DataSizeAlt  int64    `json:"data_size"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
}

func (a *tiktokDramaPlaybackAddress) UnmarshalJSON(data []byte) error {
	data = bytesTrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &a.URL)
	}
	type alias tiktokDramaPlaybackAddress
	return json.Unmarshal(data, (*alias)(a))
}

func bytesTrimSpace(data []byte) []byte {
	start, end := 0, len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}

func (a tiktokDramaPlaybackAddress) urls() []string {
	all := []string{a.URL}
	all = append(all, a.URLList...)
	all = append(all, a.URLListAlt...)
	all = append(all, a.URLListUpper...)
	seen := map[string]bool{}
	out := make([]string, 0, len(all))
	for _, value := range all {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || (!strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://")) {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

type tiktokDramaBitrate struct {
	GearName       string                     `json:"GearName"`
	GearNameAlt    string                     `json:"gear_name"`
	Bitrate        int64                      `json:"Bitrate"`
	BitrateAlt     int64                      `json:"bit_rate"`
	PlayAddress    tiktokDramaPlaybackAddress `json:"PlayAddr"`
	PlayAddressAlt tiktokDramaPlaybackAddress `json:"playAddr"`
}

type tiktokDramaItem struct {
	ID    string `json:"id"`
	Video struct {
		Width         int                        `json:"width"`
		Height        int                        `json:"height"`
		Bitrate       int64                      `json:"bitrate"`
		PlayAddress   tiktokDramaPlaybackAddress `json:"playAddr"`
		BitrateInfo   []tiktokDramaBitrate       `json:"bitrateInfo"`
		SubtitleInfos []tiktokSubtitleInfo       `json:"subtitleInfos"`
	} `json:"video"`
	DramaInfo struct {
		DramaID        string `json:"dramaID"`
		DramaName      string `json:"dramaName"`
		NumVideos      int    `json:"numVideos"`
		DramaVideoData struct {
			EpisodeNumber int `json:"EpisodeNumber"`
		} `json:"DramaVideoData"`
	} `json:"dramaInfo"`
}

// tiktokSubtitleInfo is one subtitle file TikTok lists for a video (WebVTT,
// served without an extension and as video/mp4). Source is ASR (speech
// recognition) or MT (machine translation).
type tiktokSubtitleInfo struct {
	URL              string `json:"Url"`
	LanguageCodeName string `json:"LanguageCodeName"`
	Source           string `json:"Source"`
	Format           string `json:"Format"`
}

type tiktokItemDetailResponse struct {
	StatusCode int `json:"statusCode"`
	ItemInfo   struct {
		ItemStruct tiktokDramaItem `json:"itemStruct"`
	} `json:"itemInfo"`
}

// tiktokDramaItemsFromJSON accepts either an /api/drama/episode/item_list/ or an
// /api/item/detail/ response body, as observed by the logged-in capture browser.
func tiktokDramaItemsFromJSON(body []byte) []tiktokDramaItem {
	var list tiktokDramaResponse
	if err := json.Unmarshal(body, &list); err == nil {
		items := append([]tiktokDramaItem{}, list.ItemList...)
		items = append(items, list.ItemListAlt...)
		if len(items) > 0 {
			return items
		}
	}
	var detail tiktokItemDetailResponse
	if err := json.Unmarshal(body, &detail); err == nil && detail.ItemInfo.ItemStruct.ID != "" {
		return []tiktokDramaItem{detail.ItemInfo.ItemStruct}
	}
	return nil
}

func tiktokDramaItemStreams(item tiktokDramaItem) []tiktokDramaStream {
	streams := []tiktokDramaStream{}
	seen := map[string]bool{}
	for _, b := range item.Video.BitrateInfo {
		addr := b.PlayAddress
		if len(addr.urls()) == 0 {
			addr = b.PlayAddressAlt
		}
		urls := addr.urls()
		if len(urls) == 0 || seen[urls[0]] {
			continue
		}
		seen[urls[0]] = true
		label := strings.TrimSpace(b.GearName)
		if label == "" {
			label = strings.TrimSpace(b.GearNameAlt)
		}
		bitrate := b.Bitrate
		if bitrate == 0 {
			bitrate = b.BitrateAlt
		}
		streams = append(streams, tiktokDramaStream{Label: label, URLs: urls, Width: addr.Width, Height: addr.Height, Bitrate: bitrate})
	}
	if len(streams) == 0 {
		if urls := item.Video.PlayAddress.urls(); len(urls) > 0 {
			streams = append(streams, tiktokDramaStream{Label: "default", URLs: urls, Width: item.Video.Width, Height: item.Video.Height, Bitrate: item.Video.Bitrate})
		}
	}
	sortTikTokDramaStreams(streams)
	return streams
}

type tiktokDramaResponse struct {
	StatusCode    int               `json:"statusCode"`
	StatusCodeAlt int               `json:"status_code"`
	StatusMessage string            `json:"statusMsg"`
	StatusMsgAlt  string            `json:"status_msg"`
	ItemList      []tiktokDramaItem `json:"itemList"`
	ItemListAlt   []tiktokDramaItem `json:"item_list"`
}

type tiktokDramaStream struct {
	Label   string
	URLs    []string
	Width   int
	Height  int
	Bitrate int64
}

func parseTikTokShortDramaURL(rawURL string) (string, int, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if host != "tiktok.com" && !strings.HasSuffix(host, ".tiktok.com") {
		return "", 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0], "shortdrama") || !strings.EqualFold(parts[1], "episode") || !allDigits(parts[2]) || !allDigits(parts[3]) {
		return "", 0, false
	}
	ep, err := strconv.Atoi(parts[3])
	if err != nil || ep <= 0 {
		return "", 0, false
	}
	return parts[2], ep, true
}

func resolveTikTokShortDrama(ctx context.Context, rawURL string, cfg settings) (string, int, string, []tiktokDramaStream, error) {
	dramaID, episode, ok := parseTikTokShortDramaURL(rawURL)
	if !ok {
		return "", 0, "", nil, errors.New("不是支援的分集網址")
	}
	endpoint, _ := url.Parse("https://www.tiktok.com/api/drama/episode/item_list/")
	q := endpoint.Query()
	q.Set("dramaID", dramaID)
	q.Set("cursor", strconv.Itoa(episode-1))
	q.Set("count", "1")
	q.Set("aid", "1988")
	q.Set("language", "en")
	q.Set("region", "")
	q.Set("storeRegion", "")
	endpoint.RawQuery = q.Encode()

	client := &http.Client{Timeout: 40 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return dramaID, episode, "", nil, err
	}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", rawURL)
	req.Header.Set("Origin", "https://www.tiktok.com")
	if cookie := cookieHeaderFromFile(cfg.CookieFile, "tiktok.com"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return dramaID, episode, "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return dramaID, episode, "", nil, fmt.Errorf("分集資訊查詢 HTTP %s", resp.Status)
	}
	var payload tiktokDramaResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return dramaID, episode, "", nil, fmt.Errorf("分集資訊解析失敗：%w", err)
	}
	status := payload.StatusCode
	if status == 0 {
		status = payload.StatusCodeAlt
	}
	if status != 0 {
		msg := payload.StatusMessage
		if msg == "" {
			msg = payload.StatusMsgAlt
		}
		return dramaID, episode, "", nil, fmt.Errorf("分集資訊查詢狀態 %d：%s", status, msg)
	}
	items := payload.ItemList
	if len(items) == 0 {
		items = payload.ItemListAlt
	}
	if len(items) == 0 {
		return dramaID, episode, "", nil, errors.New("分集資訊沒有回傳本集資料")
	}
	item := items[0]
	title := strings.TrimSpace(item.DramaInfo.DramaName)
	return dramaID, episode, title, tiktokDramaItemStreams(item), nil
}

func sortTikTokDramaStreams(streams []tiktokDramaStream) {
	for i := 0; i < len(streams); i++ {
		for j := i + 1; j < len(streams); j++ {
			left, right := streams[i], streams[j]
			ls := left.Height*1000000 + int(left.Bitrate/1000)
			rs := right.Height*1000000 + int(right.Bitrate/1000)
			if rs > ls {
				streams[i], streams[j] = streams[j], streams[i]
			}
		}
	}
}

// chooseTikTokDramaStream picks the best stream whose short side is at most
// maxShortSide (0 = no limit): 720p means 720 lines on the short side, so a
// portrait 720×1252 video counts as 720p.
func chooseTikTokDramaStream(streams []tiktokDramaStream, maxShortSide int) (tiktokDramaStream, error) {
	if len(streams) == 0 {
		return tiktokDramaStream{}, errors.New("分集資訊中沒有可直接取得的影片網址")
	}
	if maxShortSide <= 0 {
		return streams[0], nil
	}
	for _, st := range streams {
		if s := shortSide(st.Width, st.Height); s > 0 && s <= maxShortSide {
			return st, nil
		}
	}
	return streams[len(streams)-1], nil
}

// shortSide is the smaller of width and height (height alone when the width
// is unknown).
func shortSide(width, height int) int {
	if width > 0 && height > 0 && width < height {
		return width
	}
	return height
}

// maxShortSideFor is the 最高 720p limit of a mode (0 = none).
func maxShortSideFor(mode int) int {
	if mode == 5 {
		return 720
	}
	return 0
}

func (a *app) downloadDirectMedia(ctx context.Context, urls []string, dest, referer, cookie string) error {
	if len(urls) == 0 {
		return errors.New("沒有可下載的媒體 URL")
	}
	if validFile(dest, 1024) {
		a.postLog("✓ 檔案已存在，跳過重複下載：" + dest + "\r\n")
		a.noteOutput(dest)
		return nil
	}
	var lastErr error
	for idx, mediaURL := range urls {
		part := dest + ".part"
		var offset int64
		if st, err := os.Stat(part); err == nil {
			offset = st.Size()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "*/*")
		if strings.Contains(strings.ToLower(referer), "tiktok.com") {
			req.Header.Set("Origin", "https://www.tiktok.com")
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := (&http.Client{Timeout: 90 * time.Minute}).Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			lastErr = fmt.Errorf("媒體來源 %d HTTP %s", idx+1, resp.Status)
			resp.Body.Close()
			continue
		}
		flags := os.O_CREATE | os.O_WRONLY
		if resp.StatusCode == http.StatusPartialContent && offset > 0 {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
			offset = 0
		}
		f, err := os.OpenFile(part, flags, 0644)
		if err != nil {
			resp.Body.Close()
			return err
		}
		total := resp.ContentLength
		if total > 0 {
			total += offset
		}
		downloaded := offset
		buf := make([]byte, 256*1024)
		lastReport := time.Now().Add(-time.Hour)
		copyErr := error(nil)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := f.Write(buf[:n]); werr != nil {
					copyErr = werr
					break
				}
				downloaded += int64(n)
				if time.Since(lastReport) > time.Second {
					if total > 0 {
						a.postStatus(fmt.Sprintf("狀態：直連下載 %.1f / %.1f MB（%.0f%%）", float64(downloaded)/(1024*1024), float64(total)/(1024*1024), float64(downloaded)*100/float64(total)))
					} else {
						a.postStatus(fmt.Sprintf("狀態：直連下載 %.1f MB", float64(downloaded)/(1024*1024)))
					}
					lastReport = time.Now()
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				copyErr = rerr
				break
			}
		}
		_ = f.Sync()
		_ = f.Close()
		_ = resp.Body.Close()
		if copyErr != nil {
			lastErr = copyErr
			continue
		}
		_ = os.Remove(dest)
		if err := os.Rename(part, dest); err != nil {
			return err
		}
		a.postLog("✓ 直連媒體下載完成：" + dest + "\r\n")
		a.noteOutput(dest)
		return nil
	}
	return lastErr
}

func (a *app) runTikTokShortDramaDirect(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	if _, _, ok := parseTikTokShortDramaURL(rawURL); !ok {
		return errors.New("不是支援的分集網址")
	}
	if mode == 4 {
		return errors.New("此類網址的內建解析不使用格式 ID；請改選最佳畫質或 720p")
	}
	dramaID, episode, title, streams, err := resolveTikTokShortDrama(ctx, rawURL, cfg)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return errors.New("此頁面需要登入才能取得影片，改用瀏覽器登入後擷取")
	}
	if mode == 3 {
		a.postLog(fmt.Sprintf("%s 第 %d 集可用格式：\r\n", dramaID, episode))
		for i, st := range streams {
			a.postLog(fmt.Sprintf("  %d｜%s｜%dx%d｜bitrate=%d\r\n", i+1, st.Label, st.Width, st.Height, st.Bitrate))
		}
		return nil
	}
	selected, err := chooseTikTokDramaStream(streams, maxShortSideFor(mode))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	base := "Video_" + dramaID + "_E" + fmt.Sprintf("%03d", episode)
	if clean := safeWindowsBaseName(title); clean != "" {
		base = withEpisodeLabel(clean, fmt.Sprintf("_E%03d", episode))
	}
	dest := filepath.Join(output, base+".mp4")
	cookie := cookieHeaderFromFile(cfg.CookieFile, "tiktok.com")
	a.postLog(fmt.Sprintf("→ 內建解析成功：第 %d 集，選擇 %dp。\r\n", episode, selected.Height))
	if err := a.downloadDirectMedia(ctx, selected.URLs, dest, rawURL, cookie); err != nil {
		return err
	}
	if mode == 1 {
		return a.convertMediaToMP3(ctx, dest)
	}
	return nil
}

func (a *app) runTikTokCrawlerFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	if err := a.ensureTikTokCrawler(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ExtraArgs) != "" {
		a.postLog("ℹ 進階參數是 yt-dlp 專用語法；切換 TikTok Crawler 時不會直接套用。\r\n")
	}
	if mode == 4 {
		return errors.New("TikTok Crawler 不使用 yt-dlp 格式 ID；請改選『下載最佳畫質影片』或『最高 720p MP4』")
	}

	buildArgs := func(browser string) ([]string, string, error) {
		args := []string{}
		if mode == 3 {
			args = append(args, "-json")
		} else {
			if err := os.MkdirAll(output, 0755); err != nil {
				return nil, "", err
			}
			quality := "best"
			if mode == 5 {
				quality = "720"
			}
			outputPath := filepath.Join(output, tiktokCrawlerOutputName(rawURL))
			if cfg.FileName != "" {
				outputPath = filepath.Join(output, cfg.FileName+".mp4")
			}
			args = append(args, "-output", outputPath, "-quality", quality)
			if mode == 2 {
				// Profiles/hashtags are batch outputs. The crawler interprets -output
				// as a directory for these URL types.
				args = []string{"-output", output, "-quality", quality}
			}
			if mode == 1 {
				// The crawler downloads the best available video; FFmpeg converts it
				// to MP3 after a successful download.
			}
		}
		if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
			args = append(args, "-cookies-file", cookieFile)
		} else if browser != "" {
			args = append(args, "-cookies-from-browser", browser)
		} else if selected := strings.TrimSpace(cfg.CookieBrowser); selected != "" && selected != "auto" && selected != "none" {
			args = append(args, "-cookies-from-browser", selected)
		}
		args = append(args, rawURL)
		return args, output, nil
	}

	runOnce := func(browser string) error {
		args, _, err := buildArgs(browser)
		if err != nil {
			return err
		}
		started := time.Now().Add(-2 * time.Second)
		if browser != "" {
			a.postLog("→ TikTok Crawler 正在嘗試 " + browserDisplayName(browser) + " 登入狀態…\r\n")
		}
		if err := a.runTikTokCrawlerCommand(ctx, args); err != nil {
			return err
		}
		if mode != 3 && mode != 2 {
			if f, err := findNewestMediaFile(output, started); err == nil {
				a.noteOutput(f)
			}
		}
		if mode == 1 {
			mediaPath, err := findNewestMediaFile(output, started)
			if err != nil {
				return fmt.Errorf("TikTok Crawler 已完成下載，但找不到可轉換的媒體檔：%w", err)
			}
			if strings.EqualFold(filepath.Ext(mediaPath), ".mp3") {
				return nil
			}
			return a.convertMediaToMP3(ctx, mediaPath)
		}
		return nil
	}

	a.postLog("→ 正在改用 TikTok Crawler 備援引擎。\r\n")
	a.postStatus("狀態：正在使用 TikTok 專用備援引擎…")
	err := runOnce("")
	if err == nil {
		return nil
	}

	if strings.TrimSpace(cfg.CookieFile) == "" && strings.TrimSpace(cfg.CookieBrowser) == "auto" {
		lastErr := err
		browsers := installedBrowserCandidates()
		if len(browsers) == 0 {
			browsers = []string{"firefox", "edge", "chrome"}
		}
		for _, browser := range browsers {
			if a.stopRequested.Load() {
				return errStopped
			}
			if retryErr := runOnce(browser); retryErr == nil {
				a.postLog("✓ TikTok Crawler 使用 " + browserDisplayName(browser) + " 登入狀態重試成功。\r\n")
				return nil
			} else {
				lastErr = retryErr
			}
		}
		return lastErr
	}
	return err
}

func tiktokCrawlerOutputName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
		if len(parts) >= 4 && strings.EqualFold(parts[0], "shortdrama") && strings.EqualFold(parts[1], "episode") {
			return safeWindowsBaseName("Video_"+parts[2]+"_E"+parts[3]) + ".mp4"
		}
		for i := len(parts) - 1; i >= 0; i-- {
			if allDigits(parts[i]) {
				return "TikTok_" + parts[i] + ".mp4"
			}
		}
	}
	return "TikTok_" + time.Now().Format("20060102_150405") + ".mp4"
}

func (a *app) runTikTokCrawlerCommand(ctx context.Context, args []string) error {
	return a.runExternalCommand(ctx, a.tiktokCrawlerPath, args, "TikTok Crawler")
}
