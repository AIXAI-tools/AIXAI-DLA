package main

import (
	"strings"
	"testing"
)

func TestErrorBrief(t *testing.T) {
	cases := map[string]string{
		"ERROR: [generic] not-a-video: Unable to download webpage: HTTPSConnection(host='example.invalid', port=443): Failed to resolve 'example.invalid' ([Errno 11001] getaddrinfo failed)": "找不到這個網站",
		"ERROR: [facebook] 123: Cannot parse data; please report this issue on  https://github.com/yt-dlp/yt-dlp/issues":                                                                      "無法從頁面取得影片",
		"ERROR: unable to download video data: HTTP Error 403: Forbidden":                                                                                                                     "網站拒絕存取（403）",
		"ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video":                                                                                            "私人影片",
		"ERROR: [youtube] abc: Sign in to confirm your age. This video may be inappropriate for some users.":                                                                                  "年齡限制",
		"ERROR: [youtube] abc: Sign in to confirm you’re not a bot":                                                                                                                           "網站要求驗證",
		"ERROR: [youtube] abc: Video unavailable. This video has been removed by the uploader":                                                                                                "影片已無法觀看",
		"ERROR: Unsupported URL: https://example.com/":                                                                                                                                        "不支援這個網址",
		"ERROR: unable to download video data: HTTP Error 429: Too Many Requests":                                                                                                             "請求太頻繁",
		"ERROR: unable to download video data: HTTP Error 503: Service Unavailable":                                                                                                           "伺服器暫時出錯",
		"ERROR: [vimeo] 1: This video is DRM protected":                                                                                                                                       "DRM",
		"ERROR: something unexpected happened":                                                                                                                                                "詳細原因見原始訊息",
	}
	for raw, want := range cases {
		if got := errorBrief(raw); !strings.Contains(got, want) {
			t.Errorf("errorBrief(%q) = %q, want it to contain %q", raw, got, want)
		}
	}
	// The program's own Chinese explanations need no brief.
	for _, raw := range []string{"yt-dlp 無法解析此頁面，備援流程也未成功。", "連續兩集無法在瀏覽器播放，已停止整批。", ""} {
		if got := errorBrief(raw); got != "" {
			t.Errorf("errorBrief(%q) = %q, want empty", raw, got)
		}
	}
	// "drm" inside an address is not a DRM error.
	if got := errorBrief("ERROR: Unable to extract data from https://example.com/drmovies/1"); strings.Contains(got, "DRM") {
		t.Errorf("address containing drm misread as DRM: %q", got)
	}
}
