//go:build windows

// Tool installation, release lookup, downloads, checksums, and archive extraction.
package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

type ytDlpReleaseInfo struct {
	Version     string
	BinaryURL   string
	ChecksumURL string
	Digest      string
}

type luxReleaseInfo struct {
	Version     string
	ZipURL      string
	ChecksumURL string
	AssetName   string
}

type tiktokCrawlerReleaseInfo struct {
	Version   string
	BinaryURL string
	Digest    string
	AssetName string
}

func (a *app) ensureYtDlp(ctx context.Context, checkUpdate bool) error {
	exists := validFile(a.ytDlpPath, 1<<20)
	localVersion := ""
	if exists {
		localVersion = normalizeVersion(firstLine(runSmallContext(ctx, a.ytDlpPath, "--version")))
	}

	if exists && !checkUpdate {
		if localVersion != "" {
			a.postLog("✓ yt-dlp 已存在：" + localVersion + "。\r\n")
		} else {
			a.postLog("✓ yt-dlp 執行檔已存在。\r\n")
		}
		return nil
	}

	a.postStatus("狀態：正在比對 yt-dlp 本機與官方版本…")
	a.postLog("檢查 yt-dlp 官方 nightly 版本（只在版本不同時下載）…\r\n")
	release, releaseErr := fetchLatestYtDlpRelease(ctx)
	if releaseErr != nil {
		if exists && localVersion != "" {
			a.postLog("⚠ 無法取得官方最新版本資訊，暫時沿用本機 yt-dlp " + localVersion + "：" + releaseErr.Error() + "\r\n")
			return nil
		}
		release = ytDlpReleaseInfo{BinaryURL: ytDlpURL, ChecksumURL: ytDlpChecksumURL}
		a.postLog("⚠ 無法取得版本資訊，首次安裝改用官方 nightly latest 下載點。\r\n")
	}

	latestVersion := normalizeVersion(release.Version)
	if exists && localVersion != "" && latestVersion != "" && localVersion == latestVersion {
		a.postLog("✓ yt-dlp 已是最新版本：" + localVersion + "，不需重新下載。\r\n")
		return nil
	}

	if exists && localVersion != "" && latestVersion != "" {
		a.postLog("發現新版：本機 " + localVersion + " → 官方 nightly " + latestVersion + "。\r\n")
	} else if !exists {
		a.postLog("尚未安裝 yt-dlp，開始首次配置。\r\n")
	} else {
		a.postLog("本機版本無法辨識，將重新安裝官方版本。\r\n")
	}

	binaryURL := release.BinaryURL
	checksumURL := release.ChecksumURL
	if binaryURL == "" {
		binaryURL = ytDlpURL
	}
	if checksumURL == "" {
		checksumURL = ytDlpChecksumURL
	}

	a.postStatus("狀態：正在下載 yt-dlp…")
	newPath := a.ytDlpPath + ".new"
	_ = os.Remove(newPath)
	if err := downloadFile(ctx, binaryURL, newPath, "yt-dlp", a.postLog, a.postStatus); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("yt-dlp 下載失敗：%w", err)
	}

	if release.Digest != "" {
		if err := verifyDigest(newPath, release.Digest); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("yt-dlp 完整性校驗失敗：%w", err)
		}
	} else if err := verifyRemoteSHA256(ctx, newPath, checksumURL, "yt-dlp.exe"); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("yt-dlp 完整性校驗失敗：%w", err)
	}
	a.postLog("✓ yt-dlp SHA-256 校驗通過。\r\n")

	backup := a.ytDlpPath + ".old"
	_ = os.Remove(backup)
	if validFile(a.ytDlpPath, 1) {
		if err := os.Rename(a.ytDlpPath, backup); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("無法備份舊版 yt-dlp：%w", err)
		}
	}
	if err := os.Rename(newPath, a.ytDlpPath); err != nil {
		_ = os.Rename(backup, a.ytDlpPath)
		_ = os.Remove(newPath)
		return fmt.Errorf("無法安裝新版 yt-dlp：%w", err)
	}
	_ = os.Remove(backup)

	installed := normalizeVersion(firstLine(runSmallContext(ctx, a.ytDlpPath, "--version")))
	if installed != "" {
		a.postLog("✓ yt-dlp 已安裝／更新為 " + installed + "。\r\n")
	} else {
		a.postLog("✓ yt-dlp 已完成安裝／更新。\r\n")
	}
	return nil
}

func fetchLatestYtDlpRelease(ctx context.Context) (ytDlpReleaseInfo, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ytDlpLatestAPI, nil)
	if err != nil {
		return ytDlpReleaseInfo{}, err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return ytDlpReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ytDlpReleaseInfo{}, fmt.Errorf("GitHub API HTTP %s", resp.Status)
	}
	var release githubRelease
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024))
	if err := dec.Decode(&release); err != nil {
		return ytDlpReleaseInfo{}, err
	}
	info := ytDlpReleaseInfo{Version: release.TagName}
	for _, asset := range release.Assets {
		switch strings.ToLower(asset.Name) {
		case "yt-dlp.exe":
			info.BinaryURL = asset.BrowserDownloadURL
			info.Digest = asset.Digest
		case "sha2-256sums":
			info.ChecksumURL = asset.BrowserDownloadURL
		}
	}
	if info.BinaryURL == "" {
		return ytDlpReleaseInfo{}, errors.New("官方 nightly 最新版本中找不到 yt-dlp.exe")
	}
	return info, nil
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if fields := strings.Fields(v); len(fields) > 0 {
		v = fields[0]
	}
	if i := strings.LastIndex(v, "@"); i >= 0 && i+1 < len(v) {
		v = v[i+1:]
	}
	v = strings.TrimPrefix(v, "v")
	return strings.TrimSpace(v)
}

func verifyDigest(path, digest string) error {
	parts := strings.SplitN(strings.TrimSpace(digest), ":", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") {
		return fmt.Errorf("不支援的官方摘要格式：%s", digest)
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, strings.TrimSpace(parts[1])) {
		return fmt.Errorf("SHA-256 不符（預期 %s，實際 %s）", strings.TrimSpace(parts[1]), actual)
	}
	return nil
}

// ensureEnvironment checks (and if needed downloads or updates) the tools.
// Tasks running in parallel take turns, so no two of them write the same tool.
func (a *app) ensureEnvironment(ctx context.Context, checkYtUpdate bool) error {
	select {
	case a.envSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-a.envSem }()
	return a.ensureEnvironmentLocked(ctx, checkYtUpdate)
}

func (a *app) ensureEnvironmentLocked(ctx context.Context, checkYtUpdate bool) error {
	if err := os.MkdirAll(a.binDir, 0755); err != nil {
		return err
	}

	if err := a.ensureYtDlp(ctx, checkYtUpdate); err != nil {
		return err
	}

	if !validFile(a.denoPath, 1<<20) {
		a.postLog("下載 Deno JavaScript 執行環境…\r\n")
		a.postStatus("狀態：正在下載 Deno…")
		zipPath := filepath.Join(a.appDir, "deno.download.zip")
		if err := downloadFile(ctx, denoURL, zipPath, "Deno", a.postLog, a.postStatus); err != nil {
			return fmt.Errorf("Deno 下載失敗：%w", err)
		}
		if err := verifyRemoteSHA256(ctx, zipPath, denoChecksumURL, "deno-x86_64-pc-windows-msvc.zip"); err != nil {
			_ = os.Remove(zipPath)
			return fmt.Errorf("Deno 完整性校驗失敗：%w", err)
		}
		a.postLog("✓ Deno SHA-256 校驗通過（已支援 PowerShell Format-List 校驗格式）。\r\n")
		if err := extractSelected(zipPath, a.binDir, map[string]string{"deno.exe": "deno.exe"}); err != nil {
			return fmt.Errorf("Deno 解壓縮失敗：%w", err)
		}
		_ = os.Remove(zipPath)
		a.postLog("✓ Deno 已完成配置。\r\n")
	} else {
		a.postLog("✓ Deno 已存在。\r\n")
	}

	if !validFile(a.ffmpegPath, 1<<20) || !validFile(a.ffprobePath, 1<<20) {
		a.postLog("下載 FFmpeg／ffprobe（檔案較大，請保持網路連線）…\r\n")
		a.postStatus("狀態：正在下載 FFmpeg，首次使用可能需要較久…")
		zipPath := filepath.Join(a.appDir, "ffmpeg.download.zip")
		if err := downloadFile(ctx, ffmpegURL, zipPath, "FFmpeg", a.postLog, a.postStatus); err != nil {
			return fmt.Errorf("FFmpeg 下載失敗：%w", err)
		}
		if err := verifyRemoteSHA256(ctx, zipPath, ffmpegChecksumURL, ""); err != nil {
			_ = os.Remove(zipPath)
			return fmt.Errorf("FFmpeg 完整性校驗失敗：%w", err)
		}
		a.postLog("✓ FFmpeg SHA-256 校驗通過。\r\n")
		wanted := map[string]string{"ffmpeg.exe": "ffmpeg.exe", "ffprobe.exe": "ffprobe.exe"}
		if err := extractSelected(zipPath, a.binDir, wanted); err != nil {
			return fmt.Errorf("FFmpeg 解壓縮失敗：%w", err)
		}
		_ = os.Remove(zipPath)
		a.postLog("✓ FFmpeg 與 ffprobe 已完成配置。\r\n")
	} else {
		a.postLog("✓ FFmpeg 與 ffprobe 已存在。\r\n")
	}

	if !validFile(a.ytDlpPath, 1<<20) || !validFile(a.denoPath, 1<<20) || !validFile(a.ffmpegPath, 1<<20) || !validFile(a.ffprobePath, 1<<20) {
		return errors.New("必要元件檢查未通過")
	}

	if validFile(a.tiktokCrawlerPath, 1<<20) && checkYtUpdate {
		if err := a.ensureTikTokCrawler(ctx); err != nil {
			a.postLog("⚠ TikTok Crawler 更新檢查失敗，暫時沿用現有版本：" + err.Error() + "\r\n")
		}
	}

	impersonationTargets := runSmallContext(ctx, a.ytDlpPath, "--list-impersonate-targets")
	if strings.Contains(strings.ToLower(impersonationTargets), "chrome") {
		a.postLog("✓ yt-dlp 內建 curl_cffi 瀏覽器模擬支援，可用於 Vimeo／Bilibili 相容模式。\r\n")
	} else {
		a.postLog("⚠ 目前 yt-dlp 未列出 Chrome 模擬目標；網站相容重試仍會執行，但可能需要更新 yt-dlp。\r\n")
	}

	notices := `AIXAI YT-DLP OneClick - Third-party components\r\n\r\nyt-dlp: https://github.com/yt-dlp/yt-dlp\r\nTikTok Crawler (on-demand TikTok fallback, MIT): https://github.com/hatienl0i2612/tiktok-crawler\r\nLux (on-demand fallback engine): https://github.com/iawia002/lux\r\nDeno: https://github.com/denoland/deno\r\nFFmpeg Windows build: https://www.gyan.dev/ffmpeg/builds/\r\nFFmpeg project: https://ffmpeg.org/\r\n\r\nPlease refer to each upstream project for its current license terms.\r\n`
	_ = os.WriteFile(filepath.Join(a.appDir, "THIRD_PARTY_NOTICES.txt"), []byte(notices), 0644)

	ytVer := firstLine(runSmallContext(ctx, a.ytDlpPath, "--version"))
	denoVer := firstLine(runSmallContext(ctx, a.denoPath, "--version"))
	ffVer := firstLine(runSmallContext(ctx, a.ffmpegPath, "-version"))
	envText := "環境狀態：已就緒"
	if ytVer != "" {
		envText += "｜yt-dlp " + ytVer
	}
	if denoVer != "" {
		envText += "｜" + denoVer
	}
	if ffVer != "" {
		fields := strings.Fields(ffVer)
		if len(fields) >= 3 {
			envText += "｜FFmpeg " + fields[2]
		}
	}
	if validFile(a.luxPath, 1<<20) {
		luxVer := strings.TrimSpace(firstLine(runSmallContext(ctx, a.luxPath, "-v")))
		if luxVer != "" {
			envText += "｜Lux 備援已就緒"
		}
	}
	if validFile(a.tiktokCrawlerPath, 1<<20) {
		tkVer := strings.TrimSpace(firstLine(runSmallContext(ctx, a.tiktokCrawlerPath, "-version")))
		if tkVer != "" {
			envText += "｜TikTok 備援已就緒"
		}
	}
	a.postEnv(envText)
	a.postLog("✓ 執行環境已就緒。\r\n")
	return nil
}

func (a *app) ensureTikTokCrawler(ctx context.Context) error {
	info, infoErr := fetchLatestTikTokCrawlerRelease(ctx)
	exists := validFile(a.tiktokCrawlerPath, 1<<20)
	if infoErr != nil {
		if exists {
			a.postLog("⚠ 無法取得 TikTok Crawler 最新版本資訊，暫時沿用本機版本：" + infoErr.Error() + "\r\n")
			return nil
		}
		return fmt.Errorf("TikTok Crawler 版本資訊取得失敗：%w", infoErr)
	}

	if exists {
		local := strings.TrimSpace(firstLine(runSmallContext(ctx, a.tiktokCrawlerPath, "-version")))
		if local != "" && info.Version != "" && strings.Contains(strings.ToLower(local), strings.ToLower(info.Version)) {
			a.postLog("✓ TikTok Crawler 已是最新版本：" + info.Version + "。\r\n")
			return nil
		}
		if local != "" {
			a.postLog("發現 TikTok Crawler 新版：" + local + " → " + info.Version + "。\r\n")
		}
	} else {
		a.postLog("首次需要 TikTok 專用備援：正在自動配置 TikTok Crawler…\r\n")
	}

	a.postStatus("狀態：正在配置 TikTok 專用備援引擎…")
	newPath := a.tiktokCrawlerPath + ".new"
	_ = os.Remove(newPath)
	if err := downloadFile(ctx, info.BinaryURL, newPath, "TikTok Crawler", a.postLog, a.postStatus); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("TikTok Crawler 下載失敗：%w", err)
	}
	if strings.TrimSpace(info.Digest) == "" {
		_ = os.Remove(newPath)
		return errors.New("TikTok Crawler 官方 Release 未提供 SHA-256 digest，為安全起見不執行")
	}
	if err := verifyDigest(newPath, info.Digest); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("TikTok Crawler 完整性校驗失敗：%w", err)
	}
	a.postLog("✓ TikTok Crawler SHA-256 校驗通過。\r\n")

	backup := a.tiktokCrawlerPath + ".old"
	_ = os.Remove(backup)
	if exists {
		if err := os.Rename(a.tiktokCrawlerPath, backup); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("TikTok Crawler 舊版備份失敗：%w", err)
		}
	}
	if err := os.Rename(newPath, a.tiktokCrawlerPath); err != nil {
		if exists {
			_ = os.Rename(backup, a.tiktokCrawlerPath)
		}
		_ = os.Remove(newPath)
		return fmt.Errorf("TikTok Crawler 更新替換失敗：%w", err)
	}
	_ = os.Remove(backup)
	a.postLog("✓ TikTok Crawler 已完成配置（" + info.Version + "）。\r\n")
	return nil
}

func fetchLatestTikTokCrawlerRelease(ctx context.Context) (tiktokCrawlerReleaseInfo, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tiktokCrawlerLatestAPI, nil)
	if err != nil {
		return tiktokCrawlerReleaseInfo{}, err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return tiktokCrawlerReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tiktokCrawlerReleaseInfo{}, fmt.Errorf("TikTok Crawler GitHub API HTTP %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&release); err != nil {
		return tiktokCrawlerReleaseInfo{}, err
	}
	info := tiktokCrawlerReleaseInfo{Version: release.TagName}
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if name == "tiktok_crawler-windows-amd64.exe" || (strings.Contains(name, "tiktok_crawler") && strings.Contains(name, "windows") && strings.Contains(name, "amd64") && strings.HasSuffix(name, ".exe")) {
			info.BinaryURL = asset.BrowserDownloadURL
			info.Digest = asset.Digest
			info.AssetName = asset.Name
			break
		}
	}
	if info.BinaryURL == "" {
		return tiktokCrawlerReleaseInfo{}, errors.New("TikTok Crawler 最新 Release 中找不到 Windows amd64 執行檔")
	}
	return info, nil
}

func (a *app) ensureLux(ctx context.Context) error {
	if validFile(a.luxPath, 1<<20) {
		ver := strings.TrimSpace(firstLine(runSmallContext(ctx, a.luxPath, "-v")))
		if ver != "" {
			a.postLog("✓ Lux 備援引擎已存在：" + ver + "。\r\n")
		} else {
			a.postLog("✓ Lux 備援引擎已存在。\r\n")
		}
		return nil
	}

	a.postLog("首次需要備援引擎：正在從 Lux 官方 GitHub Release 自動配置 Windows x64 版本…\r\n")
	a.postStatus("狀態：正在配置 Lux 備援下載引擎…")
	info, err := fetchLatestLuxRelease(ctx)
	if err != nil {
		return err
	}
	zipPath := filepath.Join(a.appDir, "lux.download.zip")
	_ = os.Remove(zipPath)
	if err := downloadFile(ctx, info.ZipURL, zipPath, "Lux", a.postLog, a.postStatus); err != nil {
		return fmt.Errorf("Lux 下載失敗：%w", err)
	}
	if info.ChecksumURL != "" && info.AssetName != "" {
		if err := verifyRemoteSHA256(ctx, zipPath, info.ChecksumURL, info.AssetName); err != nil {
			_ = os.Remove(zipPath)
			return fmt.Errorf("Lux 完整性校驗失敗：%w", err)
		}
		a.postLog("✓ Lux SHA-256 校驗通過。\r\n")
	}
	if err := extractSelected(zipPath, a.binDir, map[string]string{"lux.exe": "lux.exe"}); err != nil {
		_ = os.Remove(zipPath)
		return fmt.Errorf("Lux 解壓縮失敗：%w", err)
	}
	_ = os.Remove(zipPath)
	if !validFile(a.luxPath, 1<<20) {
		return errors.New("Lux 壓縮檔中找不到 lux.exe")
	}
	a.postLog("✓ Lux 備援引擎已完成配置" + func() string {
		if info.Version != "" {
			return "（" + info.Version + "）"
		}
		return ""
	}() + "。\r\n")
	return nil
}

func fetchLatestLuxRelease(ctx context.Context) (luxReleaseInfo, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, luxLatestAPI, nil)
	if err != nil {
		return luxReleaseInfo{}, err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return luxReleaseInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return luxReleaseInfo{}, fmt.Errorf("Lux GitHub API HTTP %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024)).Decode(&release); err != nil {
		return luxReleaseInfo{}, err
	}
	info := luxReleaseInfo{Version: release.TagName}
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "checksums") && strings.HasSuffix(name, ".txt") {
			info.ChecksumURL = asset.BrowserDownloadURL
		}
		if strings.Contains(name, "windows") && (strings.Contains(name, "x86_64") || strings.Contains(name, "amd64")) && strings.HasSuffix(name, ".zip") {
			info.ZipURL = asset.BrowserDownloadURL
			info.AssetName = asset.Name
		}
	}
	if info.ZipURL == "" {
		return luxReleaseInfo{}, errors.New("Lux 最新 Release 中找不到 Windows x64 ZIP")
	}
	return info, nil
}

func downloadFile(ctx context.Context, url, dest, label string, logFn func(string), statusFn func(string)) error {
	tmp := dest + ".part"
	_ = os.Remove(tmp)
	completed := false
	defer func() {
		if !completed {
			_ = os.Remove(tmp)
		}
	}()
	client := &http.Client{Timeout: 45 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 256*1024)
	var downloaded int64
	total := resp.ContentLength
	lastReport := time.Now().Add(-time.Hour)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			downloaded += int64(n)
			if time.Since(lastReport) >= 2*time.Second {
				if total > 0 {
					pct := float64(downloaded) * 100 / float64(total)
					text := fmt.Sprintf("%s：%.1f MB / %.1f MB（%.0f%%）", label, float64(downloaded)/(1024*1024), float64(total)/(1024*1024), pct)
					statusFn("狀態：正在下載 " + text)
				} else {
					statusFn(fmt.Sprintf("狀態：正在下載 %s：%.1f MB", label, float64(downloaded)/(1024*1024)))
				}
				lastReport = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && downloaded != total {
		return fmt.Errorf("下載不完整：預期 %d bytes，實際 %d bytes", total, downloaded)
	}
	_ = os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	completed = true
	logFn(fmt.Sprintf("✓ %s 下載完成（%.1f MB）。\r\n", label, float64(downloaded)/(1024*1024)))
	return nil
}

var sha256TokenPattern = regexp.MustCompile(`(?i)[0-9a-f]{64}`)
var powershellHashPattern = regexp.MustCompile(`(?im)^\s*Hash\s*:\s*([0-9a-f]{64})\s*$`)

func verifyRemoteSHA256(ctx context.Context, path, checksumURL, wantedName string) error {
	text, err := fetchText(ctx, checksumURL)
	if err != nil {
		return fmt.Errorf("無法取得官方校驗碼：%w", err)
	}
	expected, err := parseOfficialSHA256(text, wantedName)
	if err != nil {
		return err
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA-256 不符（預期 %s，實際 %s）", expected, actual)
	}
	return nil
}

// parseOfficialSHA256 supports the common sha256sum format used by yt-dlp
// and FFmpeg, plus Deno's Windows checksum files generated by PowerShell
// Get-FileHash | Format-List, whose hash appears on a separate "Hash :" line.
func parseOfficialSHA256(text, wantedName string) (string, error) {
	normalized := strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	wantedBase := strings.ToLower(filepath.Base(strings.TrimSpace(wantedName)))

	// 1) Standard format: <hash> [*]filename. This may be one entry among many.
	for _, rawLine := range strings.Split(normalized, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		matches := sha256TokenPattern.FindAllString(line, -1)
		if len(matches) == 0 {
			continue
		}
		if wantedBase == "" || strings.Contains(strings.ToLower(line), wantedBase) {
			return strings.ToLower(matches[0]), nil
		}
	}

	// 2) Windows Deno releases use PowerShell Format-List, for example:
	//    Algorithm : SHA256
	//    Hash      : <64 hex characters>
	//    Path      : ...\deno-x86_64-pc-windows-msvc.zip
	if wantedBase == "" || strings.Contains(strings.ToLower(normalized), wantedBase) {
		if m := powershellHashPattern.FindStringSubmatch(normalized); len(m) == 2 {
			return strings.ToLower(m[1]), nil
		}
	}

	// 3) Dedicated checksum assets should contain exactly one unique SHA-256.
	// This fallback remains strict: ambiguous files with multiple hashes fail.
	unique := make(map[string]struct{})
	for _, token := range sha256TokenPattern.FindAllString(normalized, -1) {
		token = strings.ToLower(token)
		if _, err := hex.DecodeString(token); err == nil {
			unique[token] = struct{}{}
		}
	}
	if len(unique) == 1 {
		for token := range unique {
			return token, nil
		}
	}

	return "", errors.New("官方校驗檔中找不到唯一且可辨識的 SHA-256")
}

func fetchText(ctx context.Context, url string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AIXAI-YTDLP-OneClick/"+appVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractSelected(zipPath, destDir string, wanted map[string]string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := make(map[string]bool)
	for _, zf := range zr.File {
		base := strings.ToLower(filepath.Base(filepath.FromSlash(zf.Name)))
		outName, ok := wanted[base]
		if !ok {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		tmp := filepath.Join(destDir, outName+".extracting")
		out, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr1 := out.Close()
		closeErr2 := rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr1 != nil {
			return closeErr1
		}
		if closeErr2 != nil {
			return closeErr2
		}
		final := filepath.Join(destDir, outName)
		_ = os.Remove(final)
		if err := os.Rename(tmp, final); err != nil {
			return err
		}
		found[base] = true
	}
	for name := range wanted {
		if !found[strings.ToLower(name)] {
			return fmt.Errorf("壓縮檔中找不到 %s", name)
		}
	}
	return nil
}
