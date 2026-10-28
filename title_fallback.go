package main

import (
	"context"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// When a site changes its page, yt-dlp's extractor for it can stop finding the
// title and falls back to the video id: the file is saved as "abc12 [abc12].mp4".
// The page itself still shows the title, so after such a download the page
// is read once more (through yt-dlp, which gets past the site's connection
// checks the same way as the download did) and the file is renamed to the
// title shown on the page.

var (
	idOnlyNameRE = regexp.MustCompile(`^(.+) \[([^\]]+)\]$`)
	pageH1RE     = regexp.MustCompile(`(?is)<h1\b[^>]*>(.*?)</h1>`)
	htmlTagRE    = regexp.MustCompile(`(?s)<[^>]*>`)
)

const titleFallbackTimeout = 90 * time.Second

// idOnlyName returns the id when a file is named "<id> [<id>].<ext>", the
// name yt-dlp gives a single video whose title it could not find.
func idOnlyName(path string) (string, bool) {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	m := idOnlyNameRE.FindStringSubmatch(stem)
	if len(m) != 3 || m[1] != m[2] {
		return "", false
	}
	return m[2], true
}

// cleanHTMLText turns an HTML fragment into one line of plain text.
func cleanHTMLText(s string) string {
	s = html.UnescapeString(htmlTagRE.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ")
}

// titleFromPage is the video title shown on a page. A single <h1> that the
// page title starts with is the title itself (the page title adds tags and
// the site name); otherwise the page title is used without a trailing
// " - Site" / " | Site" part.
func titleFromPage(body string) string {
	title := cleanHTMLText(pageTitle(body))
	var h1 string
	if all := pageH1RE.FindAllStringSubmatch(body, 2); len(all) == 1 {
		h1 = cleanHTMLText(all[0][1])
	}
	if h1 != "" && (title == "" || strings.HasPrefix(strings.ToLower(title), strings.ToLower(h1))) {
		return h1
	}
	for _, sep := range []string{" - ", " | ", " – ", " — "} {
		if i := strings.LastIndex(title, sep); i > 0 {
			title = strings.TrimSpace(title[:i])
			break
		}
	}
	return title
}

// renameIDOnlyFiles renames this step's files that are named after the video
// id to the page's title. Nothing happens when the page shows no title or
// the new name is taken; the download itself is never affected.
func (a *app) renameIDOnlyFiles(ctx context.Context, rawURL string, args []string, from int) {
	if from < 0 || from >= len(a.itemFiles) || a.seqHashes != nil || a.cfg.FileName != "" {
		return
	}
	type target struct {
		index int
		id    string
	}
	var targets []target
	for i := from; i < len(a.itemFiles); i++ {
		if id, ok := idOnlyName(a.itemFiles[i]); ok {
			targets = append(targets, target{i, id})
		}
	}
	if len(targets) == 0 || len(args) == 0 || args[len(args)-1] != rawURL {
		return
	}
	body := a.readPageWithYtDlp(ctx, args)
	title := safeWindowsBaseName(titleFromPage(body))
	if title == "" {
		a.postLog("ℹ 網站沒有提供影片標題，檔名維持影片 ID。\r\n")
		return
	}
	for _, t := range targets {
		old := a.itemFiles[t.index]
		dir := filepath.Dir(old)
		name, _ := truncateUTF8(title, min(titleBudget(dir, 0), 200))
		name = strings.TrimRight(name, " .")
		if name == "" || name == t.id {
			continue
		}
		newPath := filepath.Join(dir, name+" ["+t.id+"]"+filepath.Ext(old))
		if _, err := os.Stat(newPath); err == nil {
			a.postLog("ℹ 已有同名檔案，檔名維持影片 ID：" + filepath.Base(newPath) + "\r\n")
			continue
		}
		if err := os.Rename(old, newPath); err != nil {
			a.postLog("⚠ 無法改用網頁標題命名，檔名維持影片 ID：" + err.Error() + "\r\n")
			continue
		}
		a.itemFiles[t.index] = newPath
		a.postLog("✓ yt-dlp 沒有取得影片標題（以影片 ID 命名），已改用網頁上的標題：" + filepath.Base(newPath) + "\r\n")
		a.diagNote("檔名改用網頁標題")
	}
}

// readPageWithYtDlp runs yt-dlp again on the same address without
// downloading, with --write-pages, and returns the largest HTML page it read
// ("" when none). The run uses the same connection options as the download.
func (a *app) readPageWithYtDlp(ctx context.Context, downloadArgs []string) string {
	dir, err := os.MkdirTemp("", "aixai-pages-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, titleFallbackTimeout)
	defer cancel()
	args := insertBeforeLast(downloadArgs, "--skip-download", "--no-playlist", "--write-pages", "--quiet", "--no-warnings")
	cmd := exec.CommandContext(ctx, a.ytDlpPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+a.binDir+";"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP}
	if cmd.Run() != nil {
		// A refused or failed read may have saved a check page instead.
		return ""
	}
	dumps, _ := filepath.Glob(filepath.Join(dir, "*.dump"))
	var largestBody string
	var largestSize int64 = -1
	for _, d := range dumps {
		st, err := os.Stat(d)
		if err != nil || st.Size() > 8<<20 || st.Size() <= largestSize {
			continue
		}
		b, err := os.ReadFile(d)
		if err != nil {
			continue
		}
		if s := string(b); pageTitleRE.MatchString(s) || pageH1RE.MatchString(s) {
			largestSize, largestBody = st.Size(), s
		}
	}
	return largestBody
}
