//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func killStandaloneProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	// Ask the dedicated browser to close cleanly first so its capture profile
	// (including a one-time login) is persisted. Force-kill only as a fallback.
	softCtx, softCancel := context.WithTimeout(context.Background(), 2*time.Second)
	soft := exec.CommandContext(softCtx, "taskkill.exe", "/PID", pid, "/T")
	soft.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	_ = soft.Run()
	softCancel()
	time.Sleep(250 * time.Millisecond)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return
	}
	hardCtx, hardCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer hardCancel()
	hard := exec.CommandContext(hardCtx, "taskkill.exe", "/PID", pid, "/T", "/F")
	hard.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
	_ = hard.Run()
	_ = cmd.Process.Kill()
}

func (a *app) captureMediaWithBrowser(ctx context.Context, rawURL, cookieFile string) (browserMediaCandidate, error) {
	// Primary path: Chrome DevTools Protocol. Network.enable is active before
	// navigation, so immediate HLS/MP4 requests cannot race past the listener.
	candidate, cdpErr := a.captureMediaWithCDP(ctx, rawURL, cookieFile)
	if cdpErr == nil {
		return candidate, nil
	}
	// There is deliberately no second capture path: the DevTools listener is
	// the only supported way to observe the capture browser.
	return browserMediaCandidate{}, cdpErr
}

func sanitizeCapturedHeader(v string) string {
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, "\n", "")
	return strings.TrimSpace(v)
}

func replaceOutputTemplate(args []string, tmpl string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-o" || out[i] == "--output" {
			out[i+1] = tmpl
			return out
		}
	}
	return out
}

// capturedFileBase names a captured video: as before, except that a video
// that would otherwise be "Captured_Video_<time>" takes the page's title,
// shortened so the full path stays playable (path_length.go).
func (a *app) capturedFileBase(rawURL string, c browserMediaCandidate, dir string) string {
	if a.cfg.FileName != "" {
		return a.cfg.FileName
	}
	n, ep := namingFor(a.cfg), a.sequenceEpisode()
	if n.custom() && ep > 0 && strings.TrimSpace(c.Title) == "" {
		return capturedOutputBaseNamed(rawURL, c.PageTitle, n, ep)
	}
	base := capturedOutputBaseNamed(rawURL, c.Title, n, ep)
	if strings.TrimSpace(c.Title) != "" || !strings.HasPrefix(base, "Captured_Video_") {
		return base
	}
	name := []rune(safeWindowsBaseName(c.PageTitle))
	if len(name) == 0 {
		return base
	}
	if limit := titleBudget(dir, 0) + pathIDReserve; len(name) > limit {
		name = []rune(strings.TrimRight(string(name[:limit]), " ."))
	}
	return string(name)
}

// capturedOutputBaseNamed names a captured download with 連續集數 naming
// (episode_naming.go); seqEp numbers an item whose address has no episode.
func capturedOutputBaseNamed(rawURL, title string, n episodeNaming, seqEp int) string {
	if dramaID, ep, ok := parseTikTokShortDramaURL(rawURL); ok {
		if clean := safeWindowsBaseName(title); clean != "" {
			return n.join(clean, ep)
		}
		return n.join("Video_"+dramaID, ep)
	}
	if u, err := url.Parse(rawURL); err == nil {
		parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
		if len(parts) >= 2 {
			last := parts[len(parts)-1]
			if m := captureEpisodeRE.FindStringSubmatch(strings.ToLower(last)); len(m) > 1 {
				ep, _ := strconv.Atoi(m[1])
				return n.join(parts[len(parts)-2], ep)
			}
			// e.g. /play/123/3: the number a sequence download replaces. Episode
			// pages often share one title, so without the number every episode
			// gets the same name and yt-dlp skips the rest as already downloaded.
			if allDigits(last) && len(last) <= 4 {
				ep, _ := strconv.Atoi(last)
				name := safeWindowsBaseName(title)
				if name == "" {
					name = safeWindowsBaseName(parts[len(parts)-2])
				}
				return n.join(name, ep)
			}
		}
	}
	// One address for every episode (?ep=N): the page title is the same for
	// all of them, so the episode number must be part of the name.
	if ep := episodeFromQuery(rawURL); ep > 0 {
		name := safeWindowsBaseName(title)
		if name == "" {
			name = seriesNameFromPath(rawURL)
		}
		if name == "" {
			name = "Video"
		}
		return n.join(name, ep)
	}
	if n.custom() && seqEp > 0 {
		name := safeWindowsBaseName(title)
		if name == "" {
			name = "Video"
		}
		return n.join(name, seqEp)
	}
	if strings.TrimSpace(title) != "" {
		return safeWindowsBaseName(title)
	}
	return "Captured_Video_" + time.Now().Format("20060102_150405")
}

func (a *app) downloadBrowserCapturedMedia(ctx context.Context, rawURL string, candidate browserMediaCandidate, mode int, formatID, output string, cfg settings) error {
	if mode == 3 {
		a.postLog(fmt.Sprintf("✓ 瀏覽器嗅探到 %s 媒體來源（主機：%s）。此模式無法可靠列出原網站的 yt-dlp 格式 ID。\r\n", candidate.Kind, hostOnly(candidate.URL)))
		return nil
	}
	if mode == 4 {
		return errors.New("瀏覽器嗅探得到的是實際媒體串流，不適用原網站的 yt-dlp 格式 ID；請改用最佳畫質或最高 720p")
	}
	localCfg := cfg
	// Browser capture already carries the exact request context. Avoid a second
	// attempt to read a possibly locked Chrome/Edge cookie database.
	localCfg.CookieBrowser = "none"
	localCfg.CookieFile = ""
	args, err := a.buildArgs(candidate.URL, mode, formatID, output, localCfg)
	if err != nil {
		return err
	}
	base := a.capturedFileBase(rawURL, candidate, output)
	args = replaceOutputTemplate(args, base+".%(ext)s")

	headers := candidate.RequestHeaders
	args = a.applyHLSTracks(ctx, args, mode, localCfg, rawURL, candidate.URL, headers)
	// Preserve the request context observed by the browser. Never log these
	// values; cookies and authorization tokens may be sensitive.
	if referer := sanitizeCapturedHeader(captureHeader(headers, "referer")); referer != "" {
		args = insertBeforeLast(args, "--referer", referer)
	} else {
		args = insertBeforeLast(args, "--referer", rawURL)
	}
	if ua := sanitizeCapturedHeader(captureHeader(headers, "user-agent")); ua != "" {
		args = insertBeforeLast(args, "--user-agent", ua)
	}
	for _, name := range []string{"cookie", "authorization", "origin", "accept", "accept-language"} {
		if value := sanitizeCapturedHeader(captureHeader(headers, name)); value != "" {
			canonical := map[string]string{"cookie": "Cookie", "authorization": "Authorization", "origin": "Origin", "accept": "Accept", "accept-language": "Accept-Language"}[name]
			args = insertBeforeLast(args, "--add-header", canonical+":"+value)
		}
	}
	a.postLog(fmt.Sprintf("✓ 瀏覽器偵測到 %s 媒體串流（%s），正在交由 yt-dlp 下載。\r\n", candidate.Kind, hostOnly(candidate.URL)))
	a.postStatus("狀態：已捕捉實際媒體串流，正在下載…")
	// A large plain file is fetched over several connections when the server
	// allows it (parallel_download.go); otherwise yt-dlp fetches it as before.
	if handled, perr := a.tryParallelCapture(ctx, rawURL, candidate, mode, output); handled {
		if perr == nil {
			a.lastCaptureStreams = []string{candidate.URL, candidate.PlayedURL}
		}
		return perr
	}
	err = a.runCommand(ctx, args)
	if err != nil && isAntiBotChallenge(err) && !a.stopRequested.Load() && ctx.Err() == nil {
		// The media server sits behind the same anti-bot check as the page.
		a.postLog("⚠ 媒體伺服器拒絕一般連線（防機器人檢查），改用瀏覽器連線方式重試。\r\n")
		err = a.runCommand(ctx, insertBeforeLast(args, "--impersonate", "chrome"))
	}
	if err != nil && (isHTTPForbidden(err) || isAntiBotChallenge(err)) && !a.stopRequested.Load() && ctx.Err() == nil {
		// Some media servers refuse requests that name the page that embeds
		// them (Referer/Origin) while serving the same address without them.
		a.postLog("⚠ 媒體伺服器拒絕存取（403），改用不帶來源網頁資訊的連線重試。\r\n")
		plain := withoutRefererArgs(args)
		err = a.runCommand(ctx, plain)
		if err != nil && isAntiBotChallenge(err) && !a.stopRequested.Load() && ctx.Err() == nil {
			err = a.runCommand(ctx, insertBeforeLast(plain, "--impersonate", "chrome"))
		}
	}
	if err != nil && a.isUnusualExtensionError(err) && !a.stopRequested.Load() && ctx.Err() == nil {
		// Some HLS subtitle URLs use signed paths without a standard extension;
		// yt-dlp aborts when --write-subs is active, while the app handles HLS
		// subtitles separately via applyHLSTracks.
		a.postLog("⚠ 字幕網址的副檔名無法辨識，yt-dlp 停止下載；改成不讓 yt-dlp 寫字幕重試（字幕由程式另外處理）。\r\n")
		err = a.runCommand(ctx, insertBeforeLast(args, "--no-write-subs", "--no-write-auto-subs"))
	}
	if err == nil {
		for _, f := range a.lastYtFiles {
			if validFile(f, 1) {
				a.lastCaptureFile = f
			}
		}
		a.lastCaptureStreams = []string{candidate.URL, candidate.PlayedURL}
		return nil
	} else if errors.Is(err, errStopped) {
		return err
	} else if candidate.PlayedURL != "" && ctx.Err() == nil {
		// The larger rendition could not be downloaded: fall back to the stream
		// the page actually played, exactly as before.
		a.postLog("⚠ 較高畫質下載失敗，改用頁面預設播放的畫質。\r\n")
		candidate.URL = candidate.PlayedURL
		candidate.PlayedURL = ""
		return a.downloadBrowserCapturedMedia(ctx, rawURL, candidate, mode, formatID, output, cfg)
	} else {
		a.postLog("⚠ 已捕捉到真實媒體網址，但 yt-dlp 下載仍失敗；改用 FFmpeg 並重放瀏覽器原始 Request Headers。\r\n")
		if ffErr := a.downloadCapturedWithFFmpeg(ctx, rawURL, candidate, mode, output); ffErr == nil {
			return nil
		} else if errors.Is(ffErr, errIncompleteStream) {
			// The reason the stream cannot be saved whole says more than yt-dlp's error.
			return fmt.Errorf("%w（yt-dlp：%v）", ffErr, err)
		} else {
			return fmt.Errorf("已捕捉媒體，但 yt-dlp 與 FFmpeg 都下載失敗：%v；FFmpeg：%w", err, ffErr)
		}
	}
}

func (a *app) downloadCapturedWithFFmpeg(ctx context.Context, rawURL string, candidate browserMediaCandidate, mode int, output string) error {
	if mode == 3 || mode == 4 {
		return errors.New("此模式不適用 FFmpeg 媒體捕捉備援")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	base := a.capturedFileBase(rawURL, candidate, output)
	ext := ".mp4"
	if mode == 1 {
		ext = ".mp3"
	}
	dest := filepath.Join(output, base+ext)
	headers := candidate.RequestHeaders
	var lines []string
	for _, item := range []struct{ key, name string }{
		{"referer", "Referer"}, {"origin", "Origin"}, {"cookie", "Cookie"},
		{"authorization", "Authorization"}, {"accept", "Accept"}, {"accept-language", "Accept-Language"},
	} {
		if v := sanitizeCapturedHeader(captureHeader(headers, item.key)); v != "" {
			lines = append(lines, item.name+": "+v)
		}
	}
	if captureHeader(headers, "referer") == "" {
		lines = append(lines, "Referer: "+sanitizeCapturedHeader(rawURL))
	}
	ffArgs := []string{"-y"}
	if ua := sanitizeCapturedHeader(captureHeader(headers, "user-agent")); ua != "" {
		ffArgs = append(ffArgs, "-user_agent", ua)
	}
	if len(lines) > 0 {
		ffArgs = append(ffArgs, "-headers", strings.Join(lines, "\r\n")+"\r\n")
	}
	input := candidate.URL
	keyed := false
	if local, cleanup, kerr := a.prepareKeyedPlaylist(ctx, candidate); kerr != nil {
		a.postLog("⚠ " + kerr.Error() + "\r\n")
	} else if local != "" {
		// The stream's key comes from the player (capture_hls_key.go). The
		// input is a local file now, which takes no HTTP options.
		defer cleanup()
		input = local
		keyed = true
		ffArgs = []string{"-y", "-allowed_extensions", "ALL", "-protocol_whitelist", "file,http,https,tcp,tls,crypto"}
	}
	ffArgs = append(ffArgs, capturedFFmpegInputArgs(candidate, input, keyed)...)
	if mode == 1 {
		ffArgs = append(ffArgs, "-vn", "-q:a", "0", dest)
	} else {
		ffArgs = append(ffArgs, "-map", "0:v?", "-map", "0:a?", "-c", "copy", "-movflags", "+faststart", dest)
	}
	removeYtDlpLeftovers(dest)
	if err := a.runExternalCommand(ctx, a.ffmpegPath, ffArgs, "FFmpeg Capture"); err != nil {
		if errors.Is(err, errStopped) || ctx.Err() != nil || !isHTTPForbidden(errors.New(a.lastCmdOutput)) {
			return err
		}
		// Same as the yt-dlp path: try once more without Referer/Origin.
		a.postLog("⚠ 媒體伺服器拒絕存取（403），改用不帶來源網頁資訊的連線重試。\r\n")
		if err := a.runExternalCommand(ctx, a.ffmpegPath, withoutRefererFFmpeg(ffArgs), "FFmpeg Capture"); err != nil {
			return err
		}
	}
	if !validFile(dest, 1024) {
		return errors.New("FFmpeg 執行完成但沒有產生有效媒體檔")
	}
	if reason := ffmpegIncompleteReason(a.lastCmdOutput); reason != "" {
		if moved := markFailedCheck(dest); moved != "" {
			a.postLog("⚠ 不完整的檔案已移到：" + moved + "\r\n")
		}
		return fmt.Errorf("%w：%s", errIncompleteStream, reason)
	}
	a.postLog("✓ FFmpeg 已使用瀏覽器實際播放請求完成下載：" + dest + "\r\n")
	a.lastCaptureFile = dest
	a.lastCaptureStreams = []string{candidate.URL, candidate.PlayedURL}
	a.noteOutput(dest)
	return nil
}

func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "未知來源"
	}
	return u.Hostname()
}

func (a *app) runBrowserCaptureFallback(ctx context.Context, rawURL string, mode int, formatID, output string, cfg settings) error {
	a.captureMaxShortSide = maxShortSideFor(mode)
	candidate, err := a.captureMediaWithBrowser(ctx, rawURL, cfg.CookieFile)
	if err != nil {
		return err
	}
	alternates := candidate.Alternates
	candidate.Alternates = nil
	a.diagNote(fmt.Sprintf("擷取到 %d 個串流，選用：%s", len(alternates)+1, describeCaptureCandidate(candidate)))
	for k, alt := range alternates {
		if k >= 5 {
			a.diagNote(fmt.Sprintf("其他串流：…（另有 %d 個）", len(alternates)-k))
			break
		}
		a.diagNote("其他串流：" + describeCaptureCandidate(alt))
	}
	pageTitle := candidate.PageTitle
	subs := candidate.Subtitles
	if len(subs) > 0 {
		a.diagNote(fmt.Sprintf("頁面字幕：%d 個", len(subs)))
	}
	if cfg.SubtitlesOnly {
		return a.captureSubtitlesOnly(ctx, rawURL, candidate, pageTitle, output, cfg)
	}
	candidate = a.upgradeToBestVariant(candidate, mode)
	// download fetches one captured stream and checks the file.
	download := func(c browserMediaCandidate) error {
		c.PageTitle = pageTitle
		a.resetOutputs()
		a.lastCaptureFile = ""
		err := a.downloadBrowserCapturedMedia(ctx, rawURL, c, mode, formatID, output, cfg)
		if err == nil && !a.stopRequested.Load() {
			err = a.finishStep(ctx, mode, cfg, true)
		}
		if err == nil && a.episodeGuard != nil && mode != 3 && a.lastCaptureFile != "" {
			if err = a.checkSavedEpisode(a.episodeGuard, a.lastCaptureFile); err != nil {
				a.lastCaptureFile = ""
			}
		}
		if err == nil && !a.stopRequested.Load() && a.lastCaptureFile != "" {
			a.saveSubtitles(ctx, subs, rawURL, []string{a.lastCaptureFile}, cfg)
		}
		return err
	}
	err = download(candidate)
	if err == nil || !(isMediaCheckFailure(err) || isEpisodeRepeated(err)) || a.stopRequested.Load() {
		return err
	}
	// The chosen stream was a preview, an ad, a lone audio/video track or a
	// repeat of an earlier episode: try the other streams the page played.
	for k, alt := range alternates {
		if k >= maxCaptureAlternates || a.stopRequested.Load() {
			break
		}
		a.postLog(fmt.Sprintf("→ 改試這次擷取到的其他串流 %d/%d（%s，%s）…\r\n", k+1, min(len(alternates), maxCaptureAlternates), alt.Kind, hostOnly(alt.URL)))
		altErr := download(alt)
		if altErr == nil {
			a.postLog("✓ 改用其他串流後，檔案已通過檢查。\r\n")
			a.diagNote("改用其他串流 ✓ " + describeCaptureCandidate(alt))
			return nil
		}
		a.diagNote("改用其他串流 ✗ " + describeCaptureCandidate(alt) + " — " + diagErrorText(altErr))
		if errors.Is(altErr, errStopped) {
			return altErr
		}
	}
	return err
}

func (a *app) isUnusualExtensionError(err error) bool {
	if err == nil {
		return false
	}
	const needle = "is unusual and will be skipped for safety reasons"
	if strings.Contains(err.Error(), needle) {
		return true
	}
	return a != nil && strings.Contains(a.lastCmdOutput, needle)
}

func capturedFFmpegInputArgs(candidate browserMediaCandidate, input string, keyed bool) []string {
	if !keyed && (strings.EqualFold(candidate.Kind, "HLS") || isManifestURL(input)) {
		return []string{"-allowed_segment_extensions", "ALL", "-extension_picky", "0", "-i", input}
	}
	return []string{"-i", input}
}
