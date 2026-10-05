//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// Feedback / bug reports.
//
// Channel: GitHub Issues on the project repository (public, trackable, tied
// to versions). The app pre-fills the issue (title, description, diagnostics)
// and opens it in the browser; screenshots are saved to one folder that is
// opened next to it so the user can drag them into the issue. Uploading
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
	Channel     string   `json:"channel"` // github | email | copy
}

type feedbackResult struct {
	Message string `json:"message"`
}

var (
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procPrintWindow            = user32.NewProc("PrintWindow")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func (a *app) feedbackDir() string {
	dir := filepath.Join(a.appDir, "feedback")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// captureWindowScreenshot renders the app window (including WebView2 content)
// into a PNG in the feedback folder.
func (a *app) captureWindowScreenshot() (feedbackImage, error) {
	var r rect
	procGetWindowRect.Call(a.hwnd, uintptr(unsafe.Pointer(&r)))
	w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
	if w <= 0 || h <= 0 {
		return feedbackImage{}, errors.New("視窗目前不可見，無法截圖")
	}
	screenDC, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screenDC)
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	defer procDeleteDC.Call(memDC)
	bmp, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return feedbackImage{}, errors.New("無法建立截圖緩衝區")
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(memDC, bmp)
	procPrintWindow.Call(a.hwnd, memDC, 2) // PW_RENDERFULLCONTENT
	procSelectObject.Call(memDC, old)

	hdr := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	pix := make([]byte, w*h*4)
	if n, _, _ := procGetDIBits.Call(memDC, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&hdr)), 0); n == 0 {
		return feedbackImage{}, errors.New("讀取截圖像素失敗")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ { // BGRA -> RGBA, opaque
		img.Pix[i*4+0] = pix[i*4+2]
		img.Pix[i*4+1] = pix[i*4+1]
		img.Pix[i*4+2] = pix[i*4+0]
		img.Pix[i*4+3] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return feedbackImage{}, err
	}
	name := "AIXAI_回報截圖_" + time.Now().Format("20060102_150405") + ".png"
	path := filepath.Join(a.feedbackDir(), name)
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return feedbackImage{}, err
	}
	return feedbackImage{Path: path, Name: name, DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
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
	_ = setClipboardText(a.hwnd, full) // always available as a fallback

	showImages := func() {
		if len(fb.Images) > 0 {
			openExplorerSelecting(fb.Images[0])
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
		if len(fb.Images) > 0 {
			msg += "截圖所在資料夾已開啟，請把圖片加到郵件附件。"
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
		if len(fb.Images) > 0 {
			msg += "截圖所在資料夾已開啟，請把圖片拖曳到回報內容中。"
		}
		return feedbackResult{Message: msg}, nil
	}
}
