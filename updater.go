//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// In-app update / rollback backed by GitHub Releases.
//
// Safety rules:
//   - only assets of this repository's releases are considered;
//   - every executable must be listed in the release's SHA256SUMS.txt and
//     match it, otherwise nothing is replaced;
//   - the running exe is renamed to .old (Windows allows renaming a running
//     executable) and the verified file takes its exact name, so shortcuts
//     keep working; on failure the original is put back.

const (
	updateRepoOwner = "AIXAI-tools"
	updateRepoName  = "AIXAI-DLA"
	checksumAsset   = "SHA256SUMS.txt"
	exeAssetSuffix  = "_Windows_x64.exe"
)

// updateAPIBase is a variable only so tests can point it at a local server.
var updateAPIBase = "https://api.github.com"

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	Published  time.Time `json:"published_at"`
	Body       string    `json:"body"`
	HTMLURL    string    `json:"html_url"`
	Assets     []ghAsset `json:"assets"`
}

type uiRelease struct {
	Tag         string `json:"tag"`
	Title       string `json:"title"`
	Channel     string `json:"channel"` // "stable" | "beta"
	Published   string `json:"published"`
	Notes       string `json:"notes"`
	Current     bool   `json:"current"`
	Newer       bool   `json:"newer"`
	Installable bool   `json:"installable"`
	URL         string `json:"url"`
}

type uiReleases struct {
	Current      string      `json:"current"`
	LatestStable string      `json:"latestStable"`
	Items        []uiRelease `json:"items"`
	Error        string      `json:"error,omitempty"`
	ReleasesURL  string      `json:"releasesUrl"`
}

func releasesPageURL() string {
	return fmt.Sprintf("https://github.com/%s/%s/releases", updateRepoOwner, updateRepoName)
}

func fetchReleases(ctx context.Context) ([]ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=30", updateAPIBase, updateRepoOwner, updateRepoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AIXAI-AllInOne-Downloader/"+appVersion)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("無法連線到 GitHub：%w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("找不到版本發佈頁。專案可能尚未公開，公開後即可在這裡更新")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("GitHub 暫時限制查詢次數，請稍後（約一小時內）再試")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub 回應 HTTP %d", resp.StatusCode)
	}
	var list []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("版本清單格式錯誤：%w", err)
	}
	out := list[:0]
	for _, r := range list {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, nil
}

// versionParts parses "v3.10.2-beta.1" into ([3 10 2], "beta.1").
func versionParts(v string) ([]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	var nums []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		nums = append(nums, n)
	}
	return nums, pre
}

// compareVersions returns -1, 0 or 1. A pre-release sorts before its release.
func compareVersions(a, b string) int {
	an, ap := versionParts(a)
	bn, bp := versionParts(b)
	for i := 0; i < len(an) || i < len(bn); i++ {
		var x, y int
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	case ap < bp:
		return -1
	default:
		return 1
	}
}

func findAsset(r ghRelease, match func(string) bool) (ghAsset, bool) {
	for _, as := range r.Assets {
		if match(as.Name) {
			return as, true
		}
	}
	return ghAsset{}, false
}

func buildUIReleases(list []ghRelease) uiReleases { return buildUIReleasesFor(list, appVersion) }

// buildUIReleasesFor marks releases relative to the given current version.
func buildUIReleasesFor(list []ghRelease, current string) uiReleases {
	out := uiReleases{Current: current, ReleasesURL: releasesPageURL()}
	for _, r := range list {
		_, hasExe := findAsset(r, func(n string) bool { return strings.HasSuffix(n, exeAssetSuffix) })
		_, hasSums := findAsset(r, func(n string) bool { return n == checksumAsset })
		channel := "stable"
		if r.Prerelease {
			channel = "beta"
		}
		notes := r.Body
		if len(notes) > 4000 {
			notes = notes[:4000] + "\n…"
		}
		title := r.Name
		if title == "" {
			title = r.Tag
		}
		item := uiRelease{
			Tag: r.Tag, Title: title, Channel: channel, Notes: notes, URL: r.HTMLURL,
			Published:   r.Published.Local().Format("2006-01-02"),
			Current:     compareVersions(r.Tag, current) == 0,
			Newer:       compareVersions(r.Tag, current) > 0,
			Installable: hasExe && hasSums,
		}
		if channel == "stable" && (out.LatestStable == "" || compareVersions(r.Tag, out.LatestStable) > 0) {
			out.LatestStable = r.Tag
		}
		out.Items = append(out.Items, item)
	}
	return out
}

func (a *app) checkUpdatesAsync() {
	ctx, cancel := context.WithTimeout(a.rootCtx, 30*time.Second)
	defer cancel()
	list, err := fetchReleases(ctx)
	if err != nil {
		a.emit("releases", uiReleases{Current: appVersion, Error: err.Error(), ReleasesURL: releasesPageURL()})
		return
	}
	a.emit("releases", buildUIReleases(list))
}

type updateProgress struct {
	Stage   string `json:"stage"` // download | verify | swap | restart | error
	Percent int    `json:"percent"`
	Message string `json:"message"`
}

func (a *app) updateStep(stage string, pct int, msg string) {
	a.emit("update", updateProgress{Stage: stage, Percent: pct, Message: msg})
}

func (a *app) installReleaseAsync(tag string) {
	fail := func(err error) {
		a.updateStep("error", 0, err.Error())
		a.postLog("⚠ 更新失敗：" + err.Error() + "\r\n")
	}
	if a.busy.Load() {
		fail(errors.New("請先等目前的下載任務完成或停止，再更新版本"))
		return
	}
	ctx, cancel := context.WithTimeout(a.rootCtx, 15*time.Minute)
	defer cancel()
	list, err := fetchReleases(ctx)
	if err != nil {
		fail(err)
		return
	}
	var rel *ghRelease
	for i := range list {
		if list[i].Tag == tag {
			rel = &list[i]
			break
		}
	}
	if rel == nil {
		fail(fmt.Errorf("找不到版本 %s", tag))
		return
	}
	exeAsset, ok := findAsset(*rel, func(n string) bool { return strings.HasSuffix(n, exeAssetSuffix) })
	if !ok {
		fail(fmt.Errorf("版本 %s 沒有 Windows 執行檔", tag))
		return
	}
	sumsAsset, ok := findAsset(*rel, func(n string) bool { return n == checksumAsset })
	if !ok {
		fail(fmt.Errorf("版本 %s 沒有 SHA256SUMS.txt，基於安全不安裝", tag))
		return
	}
	sums, err := fetchText(ctx, sumsAsset.URL)
	if err != nil {
		fail(fmt.Errorf("無法下載校驗檔：%w", err))
		return
	}
	want, err := parseOfficialSHA256(sums, exeAsset.Name)
	if err != nil {
		fail(fmt.Errorf("校驗檔中沒有 %s：%w", exeAsset.Name, err))
		return
	}

	self, err := os.Executable()
	if err != nil {
		fail(err)
		return
	}
	newPath := self + ".new"
	_ = os.Remove(newPath)
	a.updateStep("download", 0, "正在下載 "+tag+"…")
	if err := downloadWithProgress(ctx, exeAsset.URL, newPath, exeAsset.Size, func(p int) {
		a.updateStep("download", p, fmt.Sprintf("正在下載 %s（%d%%）", tag, p))
	}); err != nil {
		_ = os.Remove(newPath)
		fail(fmt.Errorf("下載失敗：%w", err))
		return
	}
	a.updateStep("verify", 100, "正在核對 SHA256…")
	got, err := fileSHA256(newPath)
	if err != nil || !strings.EqualFold(got, want) {
		_ = os.Remove(newPath)
		fail(errors.New("SHA256 不符，檔案可能損毀或遭竄改，已取消更新"))
		return
	}

	a.updateStep("swap", 100, "正在替換程式…")
	oldPath := self + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(self, oldPath); err != nil {
		_ = os.Remove(newPath)
		fail(fmt.Errorf("無法替換執行檔（資料夾可能需要系統管理員權限）：%w。可改到發佈頁手動下載", err))
		return
	}
	if err := os.Rename(newPath, self); err != nil {
		_ = os.Rename(oldPath, self)
		fail(fmt.Errorf("替換失敗，已還原原本版本：%w", err))
		return
	}
	a.postLog(fmt.Sprintf("✓ 已安裝 %s（SHA256 核對通過），正在重新啟動…\r\n", tag))
	a.updateStep("restart", 100, "已安裝 "+tag+"，正在重新啟動…")
	time.Sleep(1200 * time.Millisecond)
	if err := exec.Command(self, "--updated-from="+appVersion).Start(); err != nil {
		fail(fmt.Errorf("新版本已安裝，但無法自動啟動，請手動開啟：%w", err))
		return
	}
	a.ui.w.Dispatch(func() { procPostMessageW.Call(a.ui.hwnd, wmClose, 0, 0) })
}

func downloadWithProgress(ctx context.Context, url, dest string, size int64, progress func(int)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "AIXAI-AllInOne-Downloader/"+appVersion)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	var done int64
	last := -1
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if size > 0 {
				if p := int(done * 100 / size); p != last {
					last = p
					progress(p)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return f.Sync()
}

// cleanupAfterUpdate removes the previous version's .old file once the new
// version is running (retrying briefly while the old process exits).
func (a *app) cleanupAfterUpdate() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	old := self + ".old"
	if _, err := os.Stat(old); err != nil {
		return
	}
	go func() {
		for i := 0; i < 10; i++ {
			if os.Remove(old) == nil {
				return
			}
			time.Sleep(time.Second)
		}
	}()
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--updated-from=") {
			a.postLog(fmt.Sprintf("✓ 已從 v%s 切換到 v%s。\r\n", strings.TrimPrefix(arg, "--updated-from="), appVersion))
		}
	}
}
