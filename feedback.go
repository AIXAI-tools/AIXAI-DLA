//go:build windows

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Feedback / bug reports.
//
// Channel: GitHub Issues on the project repository (public, trackable, tied
// to versions). The app pre-fills the issue (title, description, diagnostics)
// and opens it in the browser; screenshots and log copies are saved to one
// folder that is opened next to it so the user can drag them into the issue. Uploading
// images to GitHub requires the user's own login, so the app never uploads
// anything itself and holds no tokens.
// Optional e-mail channel: set feedbackEmail to a dedicated address.

// feedbackEmail receives reports from users without a GitHub account. The
// mail is sent from the user's own mail program/account (mailto:), so no
// GitHub login is needed; attachments must be added by hand.
const feedbackEmail = "aixai19861201@gmail.com"

type feedbackImage struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	DataURL string `json:"dataUrl,omitempty"`
}

type feedbackRequest struct {
	Kind        string   `json:"kind"` // bug | idea | other
	Text        string   `json:"text"`
	Contact     string   `json:"contact"`
	IncludeDiag bool     `json:"includeDiag"`
	Images      []string `json:"images"`
	Logs        []string `json:"logs"`    // scrubbed log copies (feedback_snip.go)
	Channel     string   `json:"channel"` // send | github | email | copy
}

type feedbackResult struct {
	Message string `json:"message"`
}

func (a *app) feedbackDir() string {
	dir := filepath.Join(a.appDir, "feedback")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// pickFeedbackImages lets the user add existing screenshots; copies go into
// the feedback folder so every attachment sits in one place for dragging.
func (a *app) pickFeedbackImages() []feedbackImage {
	files := a.openFileDialog("選擇要附上的截圖", "", true, "圖片 (*.png;*.jpg;*.jpeg;*.gif;*.webp)", "*.png;*.jpg;*.jpeg;*.gif;*.webp")
	var out []feedbackImage
	for _, src := range files {
		data, err := os.ReadFile(src)
		if err != nil || len(data) > 20<<20 {
			continue
		}
		dest := filepath.Join(a.feedbackDir(), time.Now().Format("150405_")+filepath.Base(src))
		if err := os.WriteFile(dest, data, 0644); err != nil {
			continue
		}
		img := feedbackImage{Path: dest, Name: filepath.Base(src)}
		if len(data) <= 6<<20 {
			mime := "image/png"
			switch strings.ToLower(filepath.Ext(src)) {
			case ".jpg", ".jpeg":
				mime = "image/jpeg"
			case ".gif":
				mime = "image/gif"
			case ".webp":
				mime = "image/webp"
			}
			img.DataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
		}
		out = append(out, img)
	}
	return out
}

func windowsVersionString() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer k.Close()
	product, _, _ := k.GetStringValue("ProductName")
	display, _, _ := k.GetStringValue("DisplayVersion")
	build, _, _ := k.GetStringValue("CurrentBuild")
	// Windows 11 still reports "Windows 10" in ProductName; the build tells.
	if strings.HasPrefix(product, "Windows 10") && build >= "22000" {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s (build %s)", product, display, build))
}

// privacyScrub removes secrets and the local user name from text that leaves
// the machine.
func privacyScrub(s string) string {
	s = redactSecrets(s)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "%USERPROFILE%")
		s = strings.ReplaceAll(s, strings.ReplaceAll(home, `\`, "/"), "%USERPROFILE%")
	}
	return s
}

func (a *app) buildFeedbackReport(fb feedbackRequest) (title, body string) {
	kind := map[string]string{"bug": "問題回報", "idea": "功能建議", "other": "其他意見"}[fb.Kind]
	if kind == "" {
		kind = "意見回報"
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(fb.Text), "\n", 2)[0])
	if r := []rune(first); len(r) > 50 {
		first = string(r[:50]) + "…"
	}
	if first == "" {
		first = "（未填寫摘要）"
	}
	title = fmt.Sprintf("[%s] %s", kind, first)

	var b strings.Builder
	b.WriteString("### 說明\n\n" + strings.TrimSpace(fb.Text) + "\n\n")
	if c := strings.TrimSpace(fb.Contact); c != "" {
		b.WriteString("### 聯絡方式\n\n" + c + "\n\n")
	}
	if len(fb.Images) > 0 {
		b.WriteString(fmt.Sprintf("### 截圖\n\n（%d 張，請將 AIXAI 開啟的資料夾中的圖片拖曳到這裡）\n\n", len(fb.Images)))
	}
	if len(fb.Logs) > 0 {
		var names []string
		for _, l := range fb.Logs {
			names = append(names, filepath.Base(l))
		}
		b.WriteString(fmt.Sprintf("### 紀錄檔\n\n%s（請將 AIXAI 開啟的資料夾中的這些檔案拖曳到這裡；已遮蔽敏感資訊）\n\n", strings.Join(names, "、")))
	}
	if fb.IncludeDiag {
		b.WriteString("### 診斷資訊\n\n")
		b.WriteString(fmt.Sprintf("- 版本：%s v%s\n- 系統：%s\n", appTitle, appVersion, windowsVersionString()))
		modeNames := []string{"最佳畫質", "MP3", "播放清單", "查看格式", "指定格式", "720p"}
		mode := "?"
		if a.cfg.Mode >= 0 && a.cfg.Mode < len(modeNames) {
			mode = modeNames[a.cfg.Mode]
		}
		login := a.cfg.CookieBrowser
		if strings.TrimSpace(a.cfg.CookieFile) != "" {
			login = "cookie 檔"
		}
		b.WriteString(fmt.Sprintf("- 處理方式：%s｜帳號安全模式：%v｜登入：%s\n", mode, !a.cfg.UnsafeMode, login))
		if strings.TrimSpace(a.cfg.ExtraArgs) != "" {
			b.WriteString("- 進階參數：`" + privacyScrub(a.cfg.ExtraArgs) + "`\n")
		}
		lines := strings.Split(strings.ReplaceAll(a.sessionLogText(), "\r\n", "\n"), "\n")
		var kept []string
		for _, l := range lines {
			if t := strings.TrimSpace(l); t != "" && !progressLineRE.MatchString(t) {
				kept = append(kept, t)
			}
		}
		if len(kept) > 60 {
			kept = kept[len(kept)-60:]
		}
		b.WriteString("\n<details><summary>最近的紀錄（已遮蔽敏感資訊）</summary>\n\n```\n" + privacyScrub(strings.Join(kept, "\n")) + "\n```\n</details>\n")
	}
	return title, b.String()
}

// openExplorerSelecting opens Explorer with the given file selected.
func openExplorerSelecting(path string) {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	_ = cmd.Start()
}

func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut], true
}

func (a *app) submitFeedback(fb feedbackRequest) (feedbackResult, error) {
	if strings.TrimSpace(fb.Text) == "" {
		return feedbackResult{}, errors.New("請先寫下想回報的內容。")
	}
	title, body := a.buildFeedbackReport(fb)
	full := "# " + title + "\n\n" + body
	attachments := append(append([]string(nil), fb.Images...), fb.Logs...)
	if fb.Channel == "send" {
		if err := a.sendFeedbackDirect(title, body, attachments); err != nil {
			if setClipboardText(a.hwnd, full) == nil {
				return feedbackResult{}, errors.New(err.Error() + "（回報內容已複製到剪貼簿，可改用其他方式貼上）")
			}
			return feedbackResult{}, err
		}
		return feedbackResult{Message: "已送出，謝謝你的回報！"}, nil
	}
	_ = setClipboardText(a.hwnd, full) // always available as a fallback

	showImages := func() {
		if len(attachments) > 0 {
			openExplorerSelecting(attachments[0])
		}
	}
	switch fb.Channel {
	case "copy":
		showImages()
		return feedbackResult{Message: "回報內容已複製到剪貼簿。"}, nil
	case "email":
		if feedbackEmail == "" {
			return feedbackResult{}, errors.New("尚未設定回報用 Email，請改用 GitHub 回報。")
		}
		short, cut := truncateUTF8(body, 1500)
		if cut {
			short += "\n…（完整內容已複製到剪貼簿，請貼上）"
		}
		mail := "mailto:" + feedbackEmail + "?subject=" + url.PathEscape(title) + "&body=" + url.PathEscape(short)
		openInBrowser(mail)
		showImages()
		msg := "已開啟郵件程式（收件人：" + feedbackEmail + "）。完整內容也已複製到剪貼簿；若沒有跳出郵件程式，可改用 Gmail 等網頁信箱，貼上內容寄到上述地址。"
		if len(attachments) > 0 {
			msg += "附件（截圖／紀錄檔）所在資料夾已開啟，請把檔案加到郵件附件。"
		}
		return feedbackResult{Message: msg}, nil
	default: // github
		encodedTitle := url.QueryEscape(title)
		short := body
		for {
			if len(url.QueryEscape(short))+len(encodedTitle) <= feedbackMaxIssueBodyURLBytes {
				break
			}
			var cut bool
			short, cut = truncateUTF8(short, len(short)*3/4)
			if !cut {
				break
			}
		}
		if len(short) < len(body) {
			short += "\n\n…（內容較長已截斷；完整內容已複製到剪貼簿，可直接貼上取代）"
		}
		issueURL := fmt.Sprintf("https://github.com/%s/%s/issues/new?title=%s&body=%s", updateRepoOwner, updateRepoName, encodedTitle, url.QueryEscape(short))
		openInBrowser(issueURL)
		showImages()
		msg := "已在瀏覽器開啟 GitHub 回報頁（需登入 GitHub）。確認內容後按「Submit new issue」即可送出。"
		if len(attachments) > 0 {
			msg += "附件（截圖／紀錄檔）所在資料夾已開啟，請把檔案拖曳到回報內容中。"
		}
		return feedbackResult{Message: msg}, nil
	}
}
