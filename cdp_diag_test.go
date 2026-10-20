//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestTikTokCaptureDiagSummary(t *testing.T) {
	d := newTikTokCaptureDiag()
	d.api("https://www.tiktok.com/api/drama/episode/item_list/?a=1&sig=secret", 200)
	d.api("https://www.tiktok.com/api/drama/episode/item_list/?a=2", 200)
	d.knownAPI()
	d.knownAPI()
	_, res, eps := tiktokCandidateFromAPIBodyResult([]byte(`{"statusCode":10201,"statusMsg":"too many requests","itemList":[]}`), "https://www.tiktok.com/api/drama/episode/item_list/", 23, nil)
	d.read(true, res, eps)
	d.apiStatus([]byte(`{"statusCode":10201,"statusMsg":"too many requests","itemList":[]}`))
	body := `{"itemList":[{"id":"1","dramaInfo":{"dramaVideoData":{"episodeNumber":1}}},{"id":"2","dramaInfo":{"dramaVideoData":{"episodeNumber":2}}},{"id":"5","dramaInfo":{"dramaVideoData":{"episodeNumber":5}}}]}`
	_, res, eps = tiktokCandidateFromAPIBodyResult([]byte(body), "https://www.tiktok.com/api/drama/episode/item_list/", 23, nil)
	if res != tiktokAPINoEpisode {
		t.Fatalf("result = %v, want no episode", res)
	}
	d.read(true, res, eps)
	d.media()
	got := d.summary(23)
	for _, want := range []string{"分集資料回應 2 次", "格式無法解析 1", "沒有第 23 集 1", "回應中的集數：1-2, 5", "/api/drama/episode/item_list/ 200×2", "10201 too many requests", "略過的其他影片請求 1 個"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret") {
		t.Errorf("summary leaks the query string: %s", got)
	}
	if d.noKnownAPI() {
		t.Error("noKnownAPI after two responses")
	}
	if !newTikTokCaptureDiag().noKnownAPI() {
		t.Error("empty diag should report no known API")
	}
}
