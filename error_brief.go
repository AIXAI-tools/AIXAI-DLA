package main

import (
	"regexp"
	"strings"
	"unicode"
)

// errorBrief turns a download error into one short Traditional Chinese line
// for the task card, the 無法下載的網址 list and the download history. The
// original message is always kept and shown next to it; "" means the message
// is already a Chinese explanation from this program and needs no brief.

type briefRule struct {
	needles []string
	text    string
}

var http5xxRE = regexp.MustCompile(`(?i)http error 5\d\d`)

// briefRules are checked in order; the first match wins.
var briefRules = []briefRule{
	{[]string{"getaddrinfo failed", "failed to resolve", "name or service not known", "nodename nor servname", "no such host"},
		"找不到這個網站：網址可能打錯，或網路／DNS 有問題"},
	{[]string{"drm protected", "drm-protected", "[drm]", "uses drm", "has drm"}, "影片受 DRM 保護，本工具不處理受保護的內容"},
	{[]string{"private video", "this video is private", "video is private"}, "這是私人影片，只有獲得權限的帳號能觀看"},
	{[]string{"members-only", "members only", "join this channel"}, "這是會員限定內容"},
	{[]string{"not available in your country", "geo restrict", "geo-restrict", "not available in your region", "blocked it in your country"},
		"這部影片在你所在的地區無法觀看"},
	{[]string{"sign in to confirm your age", "age-restricted", "age restricted", "inappropriate for some users"},
		"影片有年齡限制，需要登入已成年的帳號"},
	{[]string{"sign in to confirm you", "confirm you're not a bot", "confirm you’re not a bot"},
		"網站要求驗證（例如確認不是機器人），請稍後再試或登入後重試"},
	{[]string{"login required", "sign in", "log in to", "requires authentication", "authentication required", "use --cookies"},
		"需要登入才能觀看"},
	{[]string{"http error 429", "too many requests"}, "請求太頻繁，被網站暫時限制，請稍後再試"},
	{[]string{"http error 404", "404: not found", "404 not found"}, "找不到影片（404）：可能已刪除，或網址錯誤"},
	{[]string{"http error 410"}, "影片已被移除（410）"},
	{[]string{"http error 403", "403: forbidden", "403 forbidden"}, "網站拒絕存取（403）：可能需要登入、有地區限制，或連結已過期"},
	{[]string{"video unavailable", "this video is not available", "has been removed", "no longer available", "content isn't available", "content is not available"},
		"影片已無法觀看：可能已刪除或下架"},
	{[]string{"premieres in", "this live event will begin", "is not live", "live stream recording is not available"},
		"直播尚未開始，或直播已結束且沒有保存"},
	{[]string{"unsupported url"}, "下載引擎不支援這個網址"},
	{[]string{"cannot parse data", "unable to extract", "no video formats found", "unable to parse"},
		"無法從頁面取得影片：可能需要登入、影片已刪除，或網站改版"},
	{[]string{"timed out", "timeout", "connection reset", "connection aborted", "connectionerror", "remote end closed", "network is unreachable"},
		"連線逾時或中斷，請檢查網路後重試"},
	{[]string{"no space left", "not enough space", "disk full"}, "儲存位置的磁碟空間不足"},
	{[]string{"permission denied", "access is denied", "being used by another process"},
		"無法寫入儲存位置：權限不足，或檔案正被其他程式使用"},
	{[]string{"postprocessing", "ffmpeg", "conversion failed"}, "影片合併或轉檔失敗"},
	{[]string{"unable to download webpage", "unable to download"}, "無法連到這個網頁"},
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func errorBrief(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if http5xxRE.MatchString(raw) {
		return "網站伺服器暫時出錯，請稍後再試"
	}
	for _, r := range briefRules {
		for _, n := range r.needles {
			if strings.Contains(lower, n) {
				return r.text
			}
		}
	}
	if hasHan(raw) {
		return "" // already one of this program's Chinese explanations
	}
	return "下載失敗（詳細原因見原始訊息）"
}
