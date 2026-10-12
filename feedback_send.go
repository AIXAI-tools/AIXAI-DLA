package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// 直接送出: the report goes to the project's mail relay (a small Google Apps
// Script web app that mails it to the project mailbox). The relay holds the
// only permission (sending mail to that one mailbox); the app holds no
// secret. The pass value is public and only keeps casual scanners out.
// Nothing is sent until the user presses the button; the dialog lists what
// is included.

const (
	feedbackEndpoint  = "https://script.google.com/macros/s/AKfycbwwgrA7unx2l2cNDO9h8YLX74Nu4OX1099WJAD-0OPEhAz8llSV1PS3YYESMXEXecRJ7Q/exec"
	feedbackPass      = "aixai-dla-feedback-1"
	maxFeedbackUpload = 12 << 20 // before base64; the relay accepts 15 MB
)

type feedbackUploadFile struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

type feedbackUpload struct {
	Pass  string               `json:"pass"`
	Title string               `json:"title"`
	Body  string               `json:"body"`
	Files []feedbackUploadFile `json:"files"`
}

// feedbackSendErrors are the relay's error codes in plain words.
var feedbackSendErrors = map[string]string{
	"daily_limit":    "今天的回報數量已達上限，請明天再試，或改用「複製回報內容」貼給開發者。",
	"too_large":      "附件太大（上限約 12 MB），請移除部分截圖或紀錄檔後再送出。",
	"too_many_files": "附件太多（最多 10 個），請移除部分附件後再送出。",
	"bad_file_type":  "只能附上圖片（PNG、JPG、GIF、WebP）與紀錄檔（TXT、LOG）。",
	"busy":           "回報服務忙碌中，請稍後再試。",
}

func (a *app) sendFeedbackDirect(title, body string, paths []string) error {
	if feedbackEndpoint == "" {
		return errors.New("這個版本尚未設定直接送出，請改用 GitHub 回報或複製回報內容。")
	}
	up := feedbackUpload{Pass: feedbackPass, Title: title, Body: body}
	total := 0
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("讀取附件 %s 失敗：%v", filepath.Base(p), err)
		}
		total += len(data)
		if total > maxFeedbackUpload {
			return errors.New(feedbackSendErrors["too_large"])
		}
		up.Files = append(up.Files, feedbackUploadFile{Name: filepath.Base(p), Data: base64.StdEncoding.EncodeToString(data)})
	}
	payload, err := json.Marshal(up)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, feedbackEndpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", appTitle+"/"+appVersion)
	// The web app answers through a redirect; the client follows it with GET.
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("無法連到回報服務（請確認網路連線）：%v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &res) != nil {
		return fmt.Errorf("回報服務沒有正常回應（HTTP %d），請稍後再試或改用其他方式。", resp.StatusCode)
	}
	if !res.OK {
		if msg := feedbackSendErrors[res.Error]; msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("回報服務拒絕了這次送出（%s），請改用其他方式。", res.Error)
	}
	return nil
}
