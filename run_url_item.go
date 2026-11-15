//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type urlRun struct {
	a                *app
	ctx              context.Context
	urls             []string
	mode             int
	formatID         string
	output           string
	cfg              settings
	safe             bool
	pacer            *safePacer
	episodes         *episodeBatch
	envRefreshed     bool
	unsupportedHosts map[string]bool
	order            []int
	deferred         map[int]bool
	visited          []bool
	batchFailures    []string
	i                int
	rawURL           string
	site             string
	host             string
	chunked          bool
	notServed        bool
	antiBot          bool
	pageTimeout      bool
}

func (r *urlRun) runItem(i int, rawURL string) (bool, bool, error) {
	a, urls := r.a, r.urls
	a.postLog(fmt.Sprintf("\r\n--- 任務 %d/%d ---\r\n網址：%s\r\n", i+1, len(urls), rawURL))
	a.emitTask(i, "running", "")
	a.postStatus(fmt.Sprintf("狀態：正在處理第 %d/%d 個網址…", i+1, len(urls)))
	site := classifySiteURL(rawURL)
	a.lastYtArgs, a.lastYtFiles = nil, nil
	a.curItem = i
	a.resetOutputs()
	a.itemFiles = nil
	a.itemSubs = nil
	a.diagStartItem(i, len(urls), rawURL)

	r.i, r.rawURL, r.site = i, rawURL, site
	if cont, done, err := r.stepDirectPipeline(); cont || done {
		return cont, done, err
	}
	err, done := r.stepYtDlp()
	if done {
		return false, true, err
	}
	err, done = r.stepBlockSignal(err)
	if done {
		return false, true, err
	}
	err, done = r.stepEnvRefreshRetry(err)
	if done {
		return false, true, err
	}
	err = r.stepAuthRetry(err)
	err = r.stepBilibiliBackup(err)
	err = r.stepSiteCompat(err)
	err = r.stepAntiBotRetry(err)
	err = r.stepTikTokCrawler(err)
	err = r.stepCaptureLoginRetry(err)
	err = r.stepPageTimeoutRetry(err)
	err = r.stepExtractorFallback(err)
	err = r.stepUnsupportedFallback(err)
	err = r.stepFinishSuccess(err)
	return false, false, err
}

func (r *urlRun) stepDirectPipeline() (bool, bool, error) {
	a, ctx, urls, mode, formatID, output, cfg, safe, episodes, batchFailures := r.a, r.ctx, r.urls, r.mode, r.formatID, r.output, r.cfg, r.safe, r.episodes, r.batchFailures
	i, rawURL, site := r.i, r.rawURL, r.site
	deferItem := r.deferItem
	// These URL families are currently known to fall through yt-dlp's generic
	// extractor. Route them directly to AIXAI's dedicated pipeline so a 50-episode
	// batch does not waste time failing and checking for updates on every episode.
	// Episodes chosen by a query parameter on a page that keeps one address
	// for every episode: only the capture browser loads the right one
	// (see episode_capture.go).
	// A single address with ?ep=N is such a page too: the page's HTML
	// names one video for every episode, which yt-dlp would take.
	episodeByQuery := site == "" && (cfg.Sequence && episodeFromQuery(rawURL) > 0 || episodeNumberFromQuery(rawURL) > 0)
	if (site == "tiktok" && func() bool { _, _, ok := parseTikTokShortDramaURL(rawURL); return ok }()) || site == "dramatip" || episodeByQuery {
		var err error
		if episodeByQuery {
			// runEpisodeCapture compares the episodes itself and stops the
			// batch on a repeat; the generic sequence comparison stays off.
			seq := a.seqHashes
			a.seqHashes = nil
			a.diagBegin("瀏覽器擷取（同網址分集）")
			err = a.runEpisodeCapture(ctx, episodes, rawURL, mode, formatID, output, cfg)
			a.diagEnd(err)
			a.seqHashes = seq
			if err != nil && !a.stopRequested.Load() && isEpisodeRepeated(err) {
				a.diagFinishItem(err)
				a.emitTask(i, "failed", firstLine(err.Error()))
				return false, true, &commandRunError{Cause: err, Summary: err.Error()}
			}
			if err != nil && !a.stopRequested.Load() && episodes.failures >= 2 {
				a.diagFinishItem(err)
				a.emitTask(i, "failed", firstLine(err.Error()))
				return false, true, &commandRunError{Cause: err, Summary: "連續兩集無法在瀏覽器播放，已停止整批。\n\n常見原因：後面的集數需要登入或解鎖（本工具不會嘗試繞過），或這部作品沒有這麼多集。\n\n最後一次錯誤：" + firstLine(err.Error())}
			}
		} else {
			err = a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg)
		}
		if err == nil && !a.stopRequested.Load() {
			if cfg.SubtitlesOnly {
				err = a.finishSubtitlesOnly()
			} else {
				err = a.finishStep(ctx, mode, cfg, true)
			}
		}
		if a.stopRequested.Load() {
			return false, true, nil
		}
		if deferItem(i, err) {
			return true, false, nil
		}
		a.diagFinishItem(err)
		if err == nil {
			a.postLog("✓ 本項任務完成。\r\n")
			doneMsg := ""
			if cfg.SubtitlesOnly {
				doneMsg = subtitleDoneMessage(a.itemSubs)
			}
			a.emitTask(i, "done", doneMsg)
			return true, false, nil
		}
		if safe && isBlockSignal(err) {
			return false, true, blockStopError(err)
		}
		if a.captureStartFailed.Load() {
			return false, true, captureStartStopError(err)
		}
		if a.loginCancelled.Load() {
			return false, true, &commandRunError{Cause: err, Summary: "已關閉登入視窗、取消登入，因此停止整批下載。需要時重新按「開始下載」即可再次登入。"}
		}
		if len(urls) == 1 {
			a.emitTask(i, "failed", firstLine(err.Error()))
			return false, true, fmt.Errorf("第 %d 個任務失敗：%w", i+1, err)
		}
		msg := fmt.Sprintf("第 %d/%d 項失敗：%s", i+1, len(urls), firstLine(err.Error()))
		batchFailures = append(batchFailures, msg)
		r.batchFailures = batchFailures
		a.postLog("⚠ " + msg + "；跳過此項，繼續下一項。\r\n")
		a.emitTask(i, "failed", firstLine(err.Error()))
		return true, false, nil
	}
	return false, false, nil
}

func (r *urlRun) stepYtDlp() (error, bool) {
	a, ctx, mode, formatID, output, cfg, unsupportedHosts := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg, r.unsupportedHosts
	rawURL, site := r.rawURL, r.site
	// yt-dlp already reported this site unsupported earlier in the run
	// (after the environment check): asking again for every episode only
	// costs time and log lines, so go straight to the fallback chain.
	host := urlHostKey(rawURL)
	var err error
	chunked := false
	r.host, r.chunked = host, chunked
	if site == "" && unsupportedHosts[host] {
		a.postLog("ℹ 本批前面已確認 yt-dlp 不支援此網站，直接使用備援流程。\r\n")
		err = errors.New("ERROR: Unsupported URL（本批已確認）")
		a.diagRecord("yt-dlp（本批已確認不支援，略過）", err)
	} else {
		args, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg)
		if buildErr != nil {
			return buildErr, true
		}
		err = a.acceptPlaylistSkips(a.runCommand(ctx, args))
		a.diagRecord("yt-dlp", err)
	}
	return err, false
}

func (r *urlRun) stepBlockSignal(err error) (error, bool) {
	a, safe := r.a, r.safe
	if err != nil && safe && isBlockSignal(err) && !a.stopRequested.Load() {
		return blockStopError(err), true
	}
	return err, false
}

func (r *urlRun) stepEnvRefreshRetry(err error) (error, bool) {
	a, ctx, urls, mode, formatID, output, cfg, envRefreshed, unsupportedHosts := r.a, r.ctx, r.urls, r.mode, r.formatID, r.output, r.cfg, r.envRefreshed, r.unsupportedHosts
	i, rawURL, site, host, chunked := r.i, r.rawURL, r.site, r.host, r.chunked
	// Any real yt-dlp execution error first triggers the same environment
	// check/update flow as the manual button. This specifically covers cases
	// where a site changes and an older yt-dlp starts returning HTTP 403/
	// extractor errors. The original task is rebuilt and retried exactly once
	// after the environment refresh; only if that still fails do we enter
	// authentication or site-specific compatibility recovery below.
	// The environment is checked at most once per run.
	if err != nil && !a.stopRequested.Load() && !(site == "" && unsupportedHosts[host]) {
		if !envRefreshed && a.otherJobsRunning() {
			// Another task may be running the tools right now; replacing
			// them mid-run would break it, so only retry here.
			envRefreshed = true
			r.envRefreshed = envRefreshed
			a.postLog("⚠ 下載錯誤；其他任務正在執行，先不更新元件，直接重試一次。\r\n")
		} else if !envRefreshed {
			envRefreshed = true
			r.envRefreshed = envRefreshed
			a.postLog("⚠ 偵測到下載錯誤，正在自動執行「檢查／更新環境」後重試原任務。\r\n")
			a.postStatus("狀態：下載失敗，正在自動檢查／更新環境…")
			refreshErr := a.ensureEnvironment(ctx, true)
			if refreshErr != nil {
				a.postLog("⚠ 自動檢查／更新環境未完整完成，將先以現有元件重試一次：" + refreshErr.Error() + "\r\n")
			} else {
				a.postLog("✓ 自動檢查／更新環境完成，正在重新執行原任務。\r\n")
			}
		} else {
			a.postLog("⚠ 下載錯誤，本批已檢查過環境，直接重試一次。\r\n")
		}

		retryArgs, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg)
		if buildErr != nil {
			return buildErr, true
		}
		// The connection kept breaking mid-transfer: short ranged requests
		// survive a connection that is cut or slowed after a while.
		if isTransferDrop(err) && !userSetChunkSize(cfg.ExtraArgs) {
			chunked = true
			r.chunked = chunked
			a.postLog("→ 下載中連線多次中斷，重試時改用分段下載（每段 " + transferDropChunk + "）。\r\n")
			a.diagNote("連線多次中斷，改用分段下載")
		}
		if chunked {
			retryArgs = insertBeforeLast(retryArgs, "--http-chunk-size", transferDropChunk)
		}
		a.postStatus(fmt.Sprintf("狀態：環境檢查完成，正在重試第 %d/%d 個網址…", i+1, len(urls)))
		retryErr := a.acceptPlaylistSkips(a.runCommand(ctx, retryArgs))
		a.diagRecord("yt-dlp 重試（檢查環境後）", retryErr)
		if retryErr == nil {
			a.postLog("✓ 更新／檢查環境後，自動重試成功。\r\n")
			err = nil
		} else {
			err = retryErr
			a.postLog("⚠ 重試仍失敗，正在進一步判斷登入驗證或網站相容性。\r\n")
		}
	}
	return err, false
}

func (r *urlRun) stepAuthRetry(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	// Authentication retry is intentionally deferred until a public/no-cookie
	// attempt fails with an authentication-specific message. This avoids
	// reading browser cookies for ordinary public downloads.
	if err != nil && !a.stopRequested.Load() && (isAuthenticationRequired(err) || isCookieAccessFailure(err)) {
		if site == "youtube" || site == "vimeo" || site == "bilibili" || site == "tiktok" {
			a.postLog("⚠ 網站要求登入或年齡驗證，進入登入重試流程。\r\n")
			a.postStatus("狀態：正在嘗試可用的網站登入狀態…")
			err = a.retryWithAuthentication(ctx, rawURL, mode, formatID, output, cfg, err)
			a.diagRecord("登入狀態重試", err)
		}
	}
	return err
}

func (r *urlRun) stepBilibiliBackup(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	// The main media server refused but the site lists backup servers for
	// the same streams (backup_streams.go); only for a normally served page.
	notServed := false
	if err != nil && !a.stopRequested.Load() && !cfg.SubtitlesOnly && site == "bilibili" && isHTTPForbidden(err) {
		if _, _, ok := backupStreamsIDs(rawURL); ok {
			a.postLog("⚠ 主要影音伺服器拒絕存取（403），改用網站提供的備用伺服器下載。\r\n")
			a.postStatus("狀態：正在改用網站提供的備用伺服器…")
			backupErr := a.downloadFromBackupStreams(ctx, rawURL, mode, formatID, output, cfg)
			a.diagRecord("備用伺服器", backupErr)
			switch {
			case backupErr == nil:
				a.postLog("✓ 已從備用伺服器下載完成。\r\n")
				err = nil
			case errors.Is(backupErr, errBackupNotServed):
				notServed = true
				a.postLog("⚠ " + backupErr.Error() + "。\r\n")
				err = &commandRunError{Cause: err, Summary: backupErr.Error() + "。\n\n" + errorLine(err.Error())}
			case a.stopRequested.Load():
			default:
				a.postLog("⚠ 備用伺服器也無法下載：" + firstLine(backupErr.Error()) + "\r\n")
			}
		}
	}
	r.notServed = notServed
	return err
}

func (r *urlRun) stepSiteCompat(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site, chunked, notServed := r.rawURL, r.site, r.chunked, r.notServed
	// Vimeo and Bilibili may also need transport/extractor compatibility
	// options that are unrelated to authentication.
	if err != nil && !a.stopRequested.Load() && (site == "bilibili" || site == "vimeo") && !isAuthenticationRequired(err) && !notServed {
		a.postLog("⚠ 一般自動重試仍失敗，正在啟用網站相容模式再重試一次。\r\n")
		a.postStatus("狀態：正在啟用 " + siteDisplayName(site) + " 相容模式重試…")
		retryArgs, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg)
		if buildErr == nil {
			retryArgs = addSiteCompatibilityArgs(retryArgs, site)
			if chunked || (isTransferDrop(err) && !userSetChunkSize(cfg.ExtraArgs)) {
				retryArgs = insertBeforeLast(retryArgs, "--http-chunk-size", transferDropChunk)
			}
			err = a.runCommand(ctx, retryArgs)
			a.diagRecord("網站相容模式", err)
			if err == nil {
				a.postLog("✓ 網站相容模式重試成功。\r\n")
			}
		}
	}
	return err
}

func (r *urlRun) stepAntiBotRetry(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	// A page behind an anti-bot check refuses yt-dlp's plain request but
	// opens for a normal browser connection: retry with a browser-like
	// connection; a page that then opens but has no known player goes on
	// to the fallback chain below (Unsupported URL).
	if err != nil && !a.stopRequested.Load() && site == "" && isAntiBotChallenge(err) {
		a.postLog("⚠ 網站拒絕一般連線（防機器人檢查），改用瀏覽器連線方式重試。\r\n")
		a.postStatus("狀態：正在改用瀏覽器連線方式重試…")
		retryArgs, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg)
		if buildErr == nil {
			err = a.runCommand(ctx, insertBeforeLast(retryArgs, "--impersonate", "chrome"))
			a.diagRecord("瀏覽器連線方式", err)
			if err == nil {
				a.postLog("✓ 改用瀏覽器連線方式後下載成功。\r\n")
			}
		}
	}
	// Still refused: only a real browser gets in, which the fallback chain's
	// browser capture is.
	antiBot := err != nil && !a.stopRequested.Load() && site == "" && isAntiBotChallenge(err)
	r.antiBot = antiBot
	return err
}

func (r *urlRun) stepTikTokCrawler(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	// TikTok has several URL families that change independently of yt-dlp.
	// For non-Unsupported errors, give the TikTok-specific crawler one chance
	// before surfacing the failure. Unsupported URLs are handled by the common
	// fallback chain below so Lux remains a final fallback.
	if err != nil && !a.stopRequested.Load() && !cfg.SubtitlesOnly && site == "tiktok" && !isUnsupportedURL(err) {
		a.postLog("⚠ TikTok 一般下載仍失敗，正在改用 TikTok 專用備援引擎。\r\n")
		a.postStatus("狀態：正在使用 TikTok 專用備援引擎重試…")
		crawlerErr := a.runTikTokCrawlerFallback(ctx, rawURL, mode, formatID, output, cfg)
		a.diagRecord("TikTok Crawler", crawlerErr)
		if crawlerErr == nil {
			err = nil
			a.postLog("✓ TikTok 專用備援引擎處理成功。\r\n")
		} else {
			err = &commandRunError{Cause: crawlerErr, Summary: "yt-dlp 與 TikTok Crawler 都未能完成此 TikTok 任務。\n\nTikTok Crawler：" + firstLine(crawlerErr.Error())}
		}
	}
	return err
}

func (r *urlRun) stepCaptureLoginRetry(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	// yt-dlp intentionally does not support every website. When it reports
	// Unsupported URL after the normal update/retry flow, automatically
	// switch to AIXAI's fallback chain instead of failing immediately.
	// Some posts of a site only open for a signed-in account, which yt-dlp
	// reports as a page it cannot read: retry once with the sign-in kept in
	// the dedicated capture browser (site_login.go).
	if !a.stopRequested.Load() && site == "" && wantsCaptureLogin(rawURL, err, cfg) {
		loginErr := a.retryWithCaptureLogin(ctx, rawURL, mode, formatID, output, cfg)
		a.diagRecord("專用瀏覽器登入重試", loginErr)
		switch {
		case loginErr == nil:
			a.postLog("✓ 使用登入狀態下載成功。\r\n")
			err = nil
		case errors.Is(loginErr, errLoginCancelled):
			err = loginErr
		case a.stopRequested.Load():
		default:
			a.postLog("⚠ 使用登入狀態仍無法下載：" + firstLine(loginErr.Error()) + "\r\n")
		}
	}
	return err
}

func (r *urlRun) stepPageTimeoutRetry(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	// The site did not answer in time while serving the page (some sites
	// are slow at times): wait a moment and ask once more; if it still
	// does not answer, the fallback chain reads the page its own way.
	pageTimeout := false
	if err != nil && !a.stopRequested.Load() && site == "" && isPageTimeout(err) {
		a.postLog("⚠ 網站回應逾時，等 5 秒後再試一次。\r\n")
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		if retryArgs, buildErr := a.buildArgs(rawURL, mode, formatID, output, cfg); buildErr == nil && !a.stopRequested.Load() {
			err = a.runCommand(ctx, retryArgs)
			a.diagRecord("逾時後重試", err)
		}
		pageTimeout = isPageTimeout(err)
	}
	r.pageTimeout = pageTimeout
	return err
}

func (r *urlRun) stepExtractorFallback(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site, antiBot, pageTimeout := r.rawURL, r.site, r.antiBot, r.pageTimeout
	// A site yt-dlp knows but whose page it could not read this time (one
	// video of the site, or after a site change) gets the same fallback
	// chain; the site itself stays marked as supported for later items.
	// The generic extractor may also find a media address in the page
	// that has since been refused (an expired signed address).
	genericRefused := a.lastYtGeneric && isHTTPForbidden(err)
	if err != nil && !a.stopRequested.Load() && site == "" && !isUnsupportedURL(err) && (isExtractorFailure(err) || antiBot || isGenericForbidden(err) || genericRefused || pageTimeout) {
		a.postLog("⚠ yt-dlp 無法解析此頁面，改用 AIXAI 多引擎備援流程。\r\n")
		a.postStatus("狀態：yt-dlp 無法解析此頁面，正在嘗試備援解析…")
		if fallbackErr := a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg); fallbackErr == nil {
			err = nil
			a.postLog("✓ 備援流程處理成功。\r\n")
		} else {
			err = &commandRunError{Cause: fallbackErr, Summary: "yt-dlp 無法解析此頁面，備援流程也未成功。\n\nyt-dlp：" + firstLine(err.Error()) + "\n備援：" + firstLine(fallbackErr.Error()) + "\n\n常見原因：影片已刪除或設為不公開、需要登入才能觀看，或網站改版。"}
		}
	}
	return err
}

func (r *urlRun) stepUnsupportedFallback(err error) error {
	a, ctx, mode, formatID, output, cfg, unsupportedHosts := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg, r.unsupportedHosts
	rawURL, site, host := r.rawURL, r.site, r.host
	if err != nil && !a.stopRequested.Load() && isUnsupportedURL(err) {
		if site == "" && host != "" && unsupportedHosts[host] {
			// Already explained for an earlier item of this run.
		} else {
			if site == "" && host != "" {
				unsupportedHosts[host] = true
			}
			a.postLog("⚠ yt-dlp 回報 Unsupported URL，啟動 AIXAI 多引擎備援流程。\r\n")
		}
		a.postStatus("狀態：yt-dlp 不支援此網址，正在嘗試備援解析…")
		fallbackErr := a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg)
		if fallbackErr == nil {
			err = nil
			a.postLog("✓ 備援流程處理成功。\r\n")
		} else {
			err = fallbackErr
		}
	}
	return err
}

func (r *urlRun) stepFinishSuccess(err error) error {
	a, ctx, mode, formatID, output, cfg := r.a, r.ctx, r.mode, r.formatID, r.output, r.cfg
	rawURL, site := r.rawURL, r.site
	if err == nil && !a.stopRequested.Load() {
		if cfg.SubtitlesOnly {
			err = a.finishSubtitlesOnly()
			if err != nil && isNoSubtitles(err) && site == "" && a.lastYtGeneric && !a.stopRequested.Load() {
				a.postLog("⚠ yt-dlp 未取得字幕，改用其他方式重新取得。\r\n")
				a.postStatus("狀態：未取得字幕，正在嘗試備援解析…")
				if fallbackErr := a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg); fallbackErr == nil {
					err = nil
					a.postLog("✓ 改用其他方式後已取得字幕。\r\n")
				} else {
					err = fallbackErr
				}
			}
			if err != nil {
				a.diagFail(err)
			}
		} else {
			// A result of yt-dlp's generic extractor is a guess from the page
			// and is checked strictly; a site's own extractor is trusted more.
			strict := site == "" && a.lastYtGeneric
			stepFiles, ytArgs := len(a.itemFiles), a.lastYtArgs // FFmpeg runs in finishStep clear lastYtArgs
			err = a.finishStep(ctx, mode, cfg, strict)
			if err == nil && !a.stopRequested.Load() {
				a.renameIDOnlyFiles(ctx, rawURL, ytArgs, stepFiles) // title_fallback.go
			}
			if err != nil && isMediaCheckFailure(err) && site == "" && mode != 3 && mode != 4 && !a.stopRequested.Load() {
				a.diagFail(err)
				a.postLog("⚠ yt-dlp 取得的檔案未通過檢查，改用其他方式重新取得。\r\n")
				a.postStatus("狀態：檔案未通過檢查，正在改用其他方式…")
				if fallbackErr := a.runUnsupportedFallback(ctx, rawURL, mode, formatID, output, cfg); fallbackErr == nil {
					err = nil
					a.postLog("✓ 改用其他方式後取得的檔案已通過檢查。\r\n")
				} else {
					err = fmt.Errorf("%v；其他方式也未成功：%s", err, firstLine(fallbackErr.Error()))
				}
			} else if err != nil {
				a.diagFail(err)
			}
		}
	}
	return err
}
