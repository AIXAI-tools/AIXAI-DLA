//go:build windows

// Web media candidates and generic or unsupported-site fallbacks.
package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type webMediaCandidate struct {
	URL  string
	Kind string
}

func fetchHTMLPage(ctx context.Context, rawURL string, cfg settings, referer string) (string, string, error) {
	client := &http.Client{Timeout: 35 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", rawURL, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,zh-TW;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if u, err := url.Parse(rawURL); err == nil {
		if cookie := cookieHeaderFromFile(cfg.CookieFile, u.Hostname()); cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", rawURL, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", rawURL, fmt.Errorf("網頁 HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12*1024*1024))
	if err != nil {
		return "", rawURL, err
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return string(body), finalURL, nil
}

func resolveCandidateURL(baseURL, value string) string {
	value = strings.TrimSpace(html.UnescapeString(value))
	value = strings.ReplaceAll(value, `\/`, `/`)
	value = strings.ReplaceAll(value, `\u0026`, `&`)
	value = strings.Trim(value, " \t\r\n\\\"'")
	if value == "" || strings.HasPrefix(value, "blob:") || strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "data:") {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if u.IsAbs() {
		if u.Scheme == "http" || u.Scheme == "https" {
			return u.String()
		}
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return base.ResolveReference(u).String()
}

func candidateKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	return u.String()
}

func looksLikeMediaProvider(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	providers := []string{"dailymotion.com", "dai.ly", "vimeo.com", "youtube.com", "youtu.be", "tiktok.com", "bilibili.com", "facebook.com", "instagram.com", "streamable.com", "loom.com"}
	for _, domain := range providers {
		if h == domain || strings.HasSuffix(h, "."+domain) {
			return true
		}
	}
	return false
}

// looksLikeProviderVideo accepts a media-site link only when it points at one
// video or player. Pages often link their own channel or profile on the same
// sites (footer, share buttons); handing one of those to yt-dlp downloads
// that whole channel instead of the episode on the page.
func looksLikeProviderVideo(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	p := strings.ToLower(u.Path)
	segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	is := func(domain string) bool { return h == domain || strings.HasSuffix(h, "."+domain) }
	has := func(prefixes ...string) bool {
		for _, prefix := range prefixes {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}
		return false
	}
	switch {
	case is("youtu.be"), is("dai.ly"):
		return len(segs) == 1
	case is("youtube.com"), is("youtube-nocookie.com"):
		return (p == "/watch" && u.Query().Get("v") != "") || has("/embed/", "/shorts/", "/live/", "/v/")
	case is("vimeo.com"):
		for _, s := range segs {
			if allDigits(s) {
				return true
			}
		}
		return false
	case is("dailymotion.com"):
		return has("/video/", "/embed/video/") || u.Query().Get("video") != ""
	case is("tiktok.com"):
		return strings.Contains(p, "/video/") || has("/embed/", "/player/")
	case is("bilibili.com"):
		return h == "player.bilibili.com" || has("/video/", "/bangumi/play/")
	case is("facebook.com"):
		return strings.Contains(p, "/videos/") || has("/watch", "/reel/", "/plugins/video.php", "/share/v/", "/share/r/")
	case is("instagram.com"):
		return has("/p/", "/reel/", "/reels/", "/tv/")
	case is("streamable.com"):
		return has("/e/") || len(segs) == 1
	case is("loom.com"):
		return has("/share/", "/embed/")
	}
	return false
}

func looksLikeDirectMedia(raw string) bool {
	return hasAnyMediaExt(raw, ".m3u8", ".mp4", ".m4v", ".webm", ".mpd")
}

func extractWebMediaCandidates(body, baseURL string) []webMediaCandidate {
	body = html.UnescapeString(body)
	body = strings.ReplaceAll(body, `\/`, `/`)
	seen := map[string]bool{}
	out := []webMediaCandidate{}
	add := func(value, kind string) {
		// Player JSON also holds plain words under the same keys (e.g. a
		// "source":"mp4" type field); those are not addresses.
		if kind == "json" && !strings.Contains(value, "/") && !looksLikeDirectMedia(value) {
			return
		}
		resolved := resolveCandidateURL(baseURL, value)
		if resolved == "" {
			return
		}
		if looksLikeMediaProvider(resolved) && !looksLikeProviderVideo(resolved) {
			return
		}
		key := candidateKey(resolved)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, webMediaCandidate{URL: resolved, Kind: kind})
	}
	patterns := []struct {
		re   *regexp.Regexp
		kind string
	}{
		{regexp.MustCompile(`(?is)<iframe[^>]+src\s*=\s*["']([^"']+)["']`), "iframe"},
		{regexp.MustCompile(`(?is)<(?:video|source)[^>]+src\s*=\s*["']([^"']+)["']`), "media"},
		{regexp.MustCompile(`(?is)<meta[^>]+(?:property|name)\s*=\s*["'](?:og:video(?::url)?|twitter:player(?::stream)?)["'][^>]+content\s*=\s*["']([^"']+)["']`), "meta"},
		{regexp.MustCompile(`(?is)["'](?:file|src|source|playbackUrl|playback_url|videoUrl|video_url|hls|hlsUrl|hls_url|contentUrl)["']\s*:\s*["']([^"']+)["']`), "json"},
	}
	for _, p := range patterns {
		for _, m := range p.re.FindAllStringSubmatch(body, 80) {
			if len(m) > 1 {
				add(m[1], p.kind)
			}
		}
	}
	// Catch absolute URLs hidden inside framework JSON. Only accept recognized
	// media extensions/providers so ordinary links, ads, and analytics URLs are ignored.
	urlRe := regexp.MustCompile(`https?://[^\s"'<>\\]+`)
	for _, match := range urlRe.FindAllString(body, 160) {
		candidate := strings.TrimRight(match, "),.;]")
		if looksLikeDirectMedia(candidate) {
			add(candidate, "direct")
		} else if looksLikeMediaProvider(candidate) {
			add(candidate, "provider")
		}
	}
	return out
}

// episodeVideoCandidates drops candidates that cannot be this page's video:
// subtitle files (saved separately, page_subtitles.go; downloaded as the video
// they would take its name) and, on an episode page whose data lists every
// episode, the streams listed for other episodes.
func episodeVideoCandidates(cands []webMediaCandidate, hint pageEpisodeHint, pageURL string) []webMediaCandidate {
	useHint := hint.Page == pageURL
	var out []webMediaCandidate
	for _, c := range cands {
		if u, err := url.Parse(c.URL); err == nil && subtitleExts[strings.ToLower(filepath.Ext(u.Path))] {
			continue
		}
		if useHint && hint.otherEpisode(c.URL) > 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

// tryWebCandidate downloads one media candidate of the page pageURL. On an
// episode page the file is named like the page-data and capture downloads
// (title_E003): a stream address named by yt-dlp would carry its whole query
// string into the file name, past what Windows accepts.
func (a *app) tryWebCandidate(ctx context.Context, candidate, referer, pageURL, title string, mode int, formatID, output string, cfg settings) error {
	candidateMode := mode
	if mode == 2 && looksLikeDirectMedia(candidate) {
		candidateMode = 0
	}
	args, err := a.buildArgs(candidate, candidateMode, formatID, output, cfg)
	if err != nil {
		return err
	}
	if pageEpisodeNumber(pageURL) > 0 {
		args = replaceOutputTemplate(args, a.outputBase(pageURL, title)+".%(ext)s")
	}
	// A candidate stands for the one video on the page: never let it expand
	// into a playlist or channel.
	extras := []string{"--no-playlist", "--playlist-items", "1", "--referer", referer, "--impersonate", "chrome"}
	headers := map[string]string{"referer": referer, "user-agent": chromeUserAgent}
	if base, err := url.Parse(referer); err == nil && base.Scheme != "" && base.Host != "" {
		extras = append(extras, "--add-header", "Origin:"+base.Scheme+"://"+base.Host)
		headers["origin"] = base.Scheme + "://" + base.Host
	}
	args = a.applyHLSTracks(ctx, args, candidateMode, cfg, pageURL, candidate, headers)
	args = insertBeforeLast(args, extras...)
	return a.runCommand(ctx, args)
}

func (a *app) runGenericWebPageFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	a.postLog("→ 正在掃描網頁中的 iframe、video/source、HLS/MP4 與播放器 JSON…\r\n")
	body, finalURL, err := fetchHTMLPage(ctx, rawURL, cfg, "")
	if err != nil && (strings.Contains(err.Error(), "網頁 HTTP 5") || isConnectionDropped(err)) {
		// A passing server error or dropped connection: read once more, so the
		// page's episode data is not lost to a browser capture that may pick a
		// preloaded neighbour (or stop at the site's ad gate).
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		body, finalURL, err = fetchHTMLPage(ctx, rawURL, cfg, "")
	}
	if err != nil {
		return fmt.Errorf("讀取網頁失敗：%w", err)
	}
	// try downloads one candidate and checks its file; a file that fails the
	// check sends the loop on to the next candidate. A video that passed gets
	// the page's separate subtitle files (page_subtitles.go).
	ep, epTitle := pageEpisodeNumber(rawURL), pageEpisodeTitle(rawURL, body)
	try := func(candidateURL string, run func() error) error {
		if cfg.SubtitlesOnly {
			title := epTitle
			if title == "" {
				title = pageTitle(body)
			}
			base := a.outputBase(rawURL, title)
			headers := map[string]string{"referer": finalURL, "user-agent": chromeUserAgent}
			if u, err := url.Parse(finalURL); err == nil && u.Scheme != "" && u.Host != "" {
				headers["origin"] = u.Scheme + "://" + u.Host
			}
			_ = a.applyHLSTracks(ctx, nil, 0, cfg, rawURL, candidateURL, headers)
			subs := pageEpisodeSubtitlesFor(body, finalURL, candidateURL, ep)
			return a.saveSubtitlesOnly(ctx, subs, finalURL, base, output, cfg)
		}
		a.resetOutputs()
		done := len(a.itemFiles)
		err := run()
		if err == nil && !a.stopRequested.Load() {
			err = a.finishStep(ctx, mode, cfg, true)
		}
		if err == nil && !a.stopRequested.Load() {
			a.savePageSubtitlesFor(ctx, body, finalURL, candidateURL, ep, a.itemFiles[done:], cfg)
		}
		return err
	}
	a.resetOutputs()
	if found, err := a.downloadPageEpisode(ctx, rawURL, finalURL, body, mode, formatID, output, cfg); found {
		if err == nil && !a.stopRequested.Load() {
			a.postLog("✓ 已從頁面播放資料下載本集。\r\n")
			a.diagNote("頁面分集資料 ✓")
			return nil
		}
		if err != nil {
			a.diagNote("頁面分集資料 ✗ " + diagErrorText(err))
		}
		if a.stopRequested.Load() {
			return errStopped
		}
		var noEp *errNoSuchEpisode
		var withheld *errEpisodeWithheld
		var deferred *errEpisodeDeferred
		var noSubFinal *noSubtitlesFinal
		if errors.As(err, &noEp) || errors.As(err, &withheld) || errors.As(err, &deferred) || errors.As(err, &noSubFinal) {
			// A withheld episode goes straight to the capture browser: the
			// page's other media addresses belong to other episodes.
			return err
		}
		a.postLog("⚠ 頁面播放資料中的串流下載失敗，改用其他方式：" + firstLine(err.Error()) + "\r\n")
	}
	candidates := episodeVideoCandidates(extractWebMediaCandidates(body, finalURL), a.pageEpisodeHint, rawURL)
	if len(candidates) == 0 {
		return errors.New("網頁中沒有找到可辨識的公開播放器或媒體 URL")
	}
	a.postLog(fmt.Sprintf("✓ 網頁解析找到 %d 個候選媒體／播放器，開始逐一驗證。\r\n", len(candidates)))
	var lastErr error
	limit := len(candidates)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		c := candidates[i]
		if a.stopRequested.Load() {
			return errStopped
		}
		a.postLog(fmt.Sprintf("→ 候選 %d/%d [%s]：%s\r\n", i+1, limit, c.Kind, c.URL))
		if err := try(c.URL, func() error {
			return a.tryWebCandidate(ctx, c.URL, finalURL, rawURL, epTitle, mode, formatID, output, cfg)
		}); err == nil {
			a.postLog("✓ 網頁嵌入媒體候選下載成功。\r\n")
			a.diagNote(fmt.Sprintf("候選 %d/%d [%s] ✓ %s", i+1, limit, c.Kind, diagMediaURL(c.URL)))
			return nil
		} else {
			lastErr = err
			a.diagNote(fmt.Sprintf("候選 %d/%d [%s] ✗ %s — %s", i+1, limit, c.Kind, diagMediaURL(c.URL), diagErrorText(err)))
			if errors.Is(err, errStopped) {
				return err
			}
		}
		// Some sites embed an intermediate player page. Scan one nested level for
		// direct HLS/MP4/provider links and hand those back to yt-dlp.
		if !looksLikeDirectMedia(c.URL) {
			nestedBody, nestedFinal, fetchErr := fetchHTMLPage(ctx, c.URL, cfg, finalURL)
			if fetchErr == nil {
				nested := extractWebMediaCandidates(nestedBody, nestedFinal)
				nestedLimit := len(nested)
				if nestedLimit > 12 {
					nestedLimit = 12
				}
				for j := 0; j < nestedLimit; j++ {
					n := nested[j]
					if candidateKey(n.URL) == candidateKey(c.URL) {
						continue
					}
					a.postLog(fmt.Sprintf("  ↳ 內嵌候選 %d/%d：%s\r\n", j+1, nestedLimit, n.URL))
					if err := try(n.URL, func() error {
						return a.tryWebCandidate(ctx, n.URL, nestedFinal, rawURL, epTitle, mode, formatID, output, cfg)
					}); err == nil {
						a.diagNote(fmt.Sprintf("內嵌候選 %d/%d ✓ %s", j+1, nestedLimit, diagMediaURL(n.URL)))
						return nil
					} else {
						lastErr = err
						a.diagNote(fmt.Sprintf("內嵌候選 %d/%d ✗ %s — %s", j+1, nestedLimit, diagMediaURL(n.URL), diagErrorText(err)))
						if errors.Is(err, errStopped) {
							return err
						}
					}
				}
			}
		}
	}
	if lastErr != nil {
		return fmt.Errorf("已找到播放器／媒體候選，但都無法下載：%w", lastErr)
	}
	return errors.New("網頁解析沒有找到可下載的公開媒體")
}

func (a *app) runUnsupportedFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	site := classifySiteURL(rawURL)
	var lastErr error
	// layer runs one way of getting the video. A way that finishes but whose
	// file does not pass the media check counts as failed, so the next way is
	// tried instead of keeping a preview, an ad or the wrong episode.
	layer := func(name string, run func() error) error {
		a.diagBegin(name)
		a.resetOutputs()
		err := run()
		if err == nil && !a.stopRequested.Load() && !cfg.SubtitlesOnly {
			err = a.finishStep(ctx, mode, cfg, true)
		}
		a.diagEnd(err)
		return err
	}

	if site == "tiktok" {
		if _, _, isShortDrama := parseTikTokShortDramaURL(rawURL); isShortDrama {
			if !cfg.SubtitlesOnly {
				a.postLog("→ 偵測到分集網址，先使用內建解析器…\r\n")
				if err := layer("內建解析", func() error { return a.runTikTokShortDramaDirect(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
					return nil
				} else {
					lastErr = err
					if a.stopRequested.Load() {
						return errStopped
					}
					a.postLog("⚠ 內建解析未成功，改以瀏覽器實際播放擷取：" + firstLine(err.Error()) + "\r\n")
				}
			}
			a.postLog("→ 啟動瀏覽器播放擷取；直接觀察頁面實際載入的 HLS／MP4。\r\n")
			if err := layer("瀏覽器擷取", func() error { return a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
				return nil
			} else {
				lastErr = err
				if a.stopRequested.Load() {
					return errStopped
				}
				if cfg.SubtitlesOnly || !cfg.UnsafeMode {
					// Extra engines would re-request the same login-bound episode
					// several more times without the login; that only adds risk.
					return err
				}
				a.postLog("⚠ 瀏覽器播放擷取未成功，再改用 TikTok Crawler：" + firstLine(err.Error()) + "\r\n")
			}
		}
		if !cfg.SubtitlesOnly {
			a.postLog("→ 啟動 TikTok Crawler 專用解析器…\r\n")
			if err := layer("TikTok Crawler", func() error { return a.runTikTokCrawlerFallback(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
				return nil
			} else {
				lastErr = err
				if a.stopRequested.Load() {
					return errStopped
				}
				a.postLog("⚠ TikTok Crawler 未成功，最後再嘗試 Lux：" + firstLine(err.Error()) + "\r\n")
			}
		}
	}

	// For a site yt-dlp does not support yet, AIXAI first tries a small
	// site-specific resolver based on the page's public PRELOADED_STATE data.
	if site == "haokan" && !cfg.SubtitlesOnly {
		a.postLog("→ 先使用內建解析器尋找公開媒體網址…\r\n")
		if err := layer("內建解析", func() error { return a.runHaokanFallback(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
			return nil
		} else {
			lastErr = err
			if a.stopRequested.Load() {
				return errStopped
			}
			a.postLog("⚠ 內建解析未成功，改用瀏覽器播放擷取：" + firstLine(err.Error()) + "\r\n")
		}
		if err := layer("瀏覽器擷取", func() error { return a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
			return nil
		} else {
			lastErr = err
			if a.stopRequested.Load() {
				return errStopped
			}
			a.postLog("⚠ 瀏覽器網路嗅探未成功，再嘗試其他備援引擎：" + firstLine(err.Error()) + "\r\n")
		}
	}

	if site == "dramatip" || site == "" {
		a.postLog("→ 啟動網頁播放器解析；適用於 yt-dlp 尚未支援的公開網頁。\r\n")
		if err := layer("網頁解析", func() error { return a.runGenericWebPageFallback(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
			return nil
		} else {
			lastErr = err
			if a.stopRequested.Load() {
				return errStopped
			}
			var noEp *errNoSuchEpisode
			if errors.As(err, &noEp) {
				return &commandRunError{Cause: err, Summary: err.Error()}
			}
			var deferred *errEpisodeDeferred
			if errors.As(err, &deferred) {
				return err
			}
			var noSubFinal *noSubtitlesFinal
			if errors.As(err, &noSubFinal) {
				return err
			}
			a.postLog("⚠ 靜態網頁解析未找到串流，改用瀏覽器網路嗅探：" + firstLine(err.Error()) + "\r\n")
		}
		if err := layer("瀏覽器擷取", func() error { return a.runBrowserCaptureFallback(ctx, rawURL, mode, formatID, output, cfg) }); err == nil {
			return nil
		} else {
			lastErr = err
			if a.stopRequested.Load() {
				return errStopped
			}
			if isUndecodable(err) { // media_decode.go：加密的影片換引擎也一樣，直接說明原因
				return &commandRunError{Cause: err, Summary: "下載到的影片" + undecodableReason + "，本工具不處理受保護的內容。"}
			}
			a.postLog("⚠ 瀏覽器網路嗅探未成功，繼續嘗試 Lux：" + firstLine(err.Error()) + "\r\n")
		}
	}

	if cfg.SubtitlesOnly {
		if isNoSubtitles(lastErr) {
			return lastErr
		}
		if lastErr != nil {
			return lastErr
		}
		return &errNoSubtitles{}
	}

	if mode == 5 {
		if site == "tiktok" && lastErr != nil {
			return &commandRunError{Cause: lastErr, Summary: "TikTok 專用備援引擎已嘗試 720p 下載但仍失敗：" + firstLine(lastErr.Error())}
		}
		return &commandRunError{Cause: lastErr, Summary: "yt-dlp 不支援此網址，而 Lux 備援引擎無法可靠保證『最高 720p』。可改選『下載最佳畫質影片』後重試。"}
	}

	if err := a.ensureLux(ctx); err != nil {
		if lastErr != nil {
			return &commandRunError{Cause: err, Summary: "網站專用解析失敗，且 Lux 備援引擎配置失敗：" + err.Error() + "\n\n原始解析錯誤：" + firstLine(lastErr.Error())}
		}
		return &commandRunError{Cause: err, Summary: "yt-dlp 不支援此網址，Lux 備援引擎配置也失敗：" + err.Error()}
	}

	a.postLog("→ 正在改用 Lux 備援引擎處理此網址。Lux 對部分中文影音網站有獨立解析器。\r\n")
	a.postStatus("狀態：正在使用 Lux 備援引擎…")
	if strings.TrimSpace(cfg.ExtraArgs) != "" {
		a.postLog("ℹ 進階參數是 yt-dlp 專用語法，切換 Lux 時不會直接套用，以避免參數不相容。\r\n")
	}
	if err := layer("Lux", func() error { return a.runLuxFallback(ctx, rawURL, mode, formatID, output, cfg) }); err != nil {
		previous := ""
		if lastErr != nil {
			previous = "\n\n前一層解析：" + firstLine(lastErr.Error())
		}
		return &commandRunError{Cause: err, Summary: "所有可用下載引擎都未能處理此網址。" + previous + "\nLux：" + firstLine(err.Error()) + "\n\n本工具會依序嘗試 yt-dlp → 內建解析 → 瀏覽器播放擷取 → 備援引擎。若仍失敗，常見原因是網站改版、需要登入，或內容受 DRM／加密保護（本工具不處理受保護的內容）。"}
	}
	return nil
}
