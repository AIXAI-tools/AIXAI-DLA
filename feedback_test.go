//go:build windows

package main

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestFeedbackReportPrivacy(t *testing.T) {
	home, _ := os.UserHomeDir()
	a := &app{statusQueue: make(chan string, 16)}
	a.cfg = settings{Mode: 0, CookieBrowser: "auto", CookieFile: home + `\secret\cookies.txt`, ExtraArgs: "--add-header Cookie:sessionid=LEAKME"}
	a.recordLog("✓ 已儲存：" + home + `\Videos\a.mp4` + "\r\n")
	a.recordLog("[debug] Authorization: Bearer TOPSECRET\r\n")
	a.recordLog("[download]  42.0% of 10MiB\r\n")

	title, body := a.buildFeedbackReport(feedbackRequest{Kind: "bug", Text: "第 3 集卡住\n詳細步驟…", IncludeDiag: true, Images: []string{"x.png"}})
	if !strings.HasPrefix(title, "[問題回報] 第 3 集卡住") {
		t.Errorf("title = %q", title)
	}
	for _, leak := range []string{"LEAKME", "TOPSECRET", home, `secret\cookies.txt`} {
		if strings.Contains(body, leak) {
			t.Errorf("report leaks %q", leak)
		}
	}
	for _, must := range []string{"%USERPROFILE%", "v" + appVersion, "帳號安全模式", "cookie 檔", "截圖"} {
		if !strings.Contains(body, must) {
			t.Errorf("report missing %q", must)
		}
	}
	if strings.Contains(body, "42.0%") {
		t.Error("progress lines should be dropped from diagnostics")
	}

	_, plain := a.buildFeedbackReport(feedbackRequest{Kind: "idea", Text: "建議", IncludeDiag: false})
	if strings.Contains(plain, "診斷資訊") {
		t.Error("diagnostics must be omitted when not requested")
	}
}

func TestTruncateUTF8KeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("萬能下載", 50)
	for n := 1; n < 40; n++ {
		out, cut := truncateUTF8(s, n)
		if !cut || !strings.HasPrefix(s, out) {
			t.Fatalf("n=%d bad cut", n)
		}
		if _, err := url.QueryUnescape(url.QueryEscape(out)); err != nil {
			t.Fatalf("n=%d produced invalid UTF-8", n)
		}
		for _, r := range out {
			if r == '�' {
				t.Fatalf("n=%d split a character", n)
			}
		}
	}
}
