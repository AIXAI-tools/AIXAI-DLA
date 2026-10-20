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
	_, res, eps := tiktokCandidateFromAPIBodyResult([]byte(`{"statusCode":10201,"statusMsg":"too many requests","itemList":[]}`), "https://www.tiktok.com/api/drama/episode/item_list/", 23, 0, nil)
	d.read(true, res, eps)
	d.apiStatus([]byte(`{"statusCode":10201,"statusMsg":"too many requests","itemList":[]}`))
	body := `{"itemList":[{"id":"1","dramaInfo":{"dramaVideoData":{"episodeNumber":1}}},{"id":"2","dramaInfo":{"dramaVideoData":{"episodeNumber":2}}},{"id":"5","dramaInfo":{"dramaVideoData":{"episodeNumber":5}}}]}`
	_, res, eps = tiktokCandidateFromAPIBodyResult([]byte(body), "https://www.tiktok.com/api/drama/episode/item_list/", 23, 0, nil)
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

func TestTikTokAPI720IsShortSide(t *testing.T) {
	// The real episode offered these (portrait) sizes.
	body := `{"itemList":[{"id":"2","dramaInfo":{"dramaVideoData":{"episodeNumber":2}},"video":{"bitrateInfo":[
{"GearName":"a","Bitrate":623049,"PlayAddr":{"UrlList":["https://v.example/360"],"Width":360,"Height":626}},
{"GearName":"b","Bitrate":767408,"PlayAddr":{"UrlList":["https://v.example/540"],"Width":540,"Height":940}},
{"GearName":"c","Bitrate":1020737,"PlayAddr":{"UrlList":["https://v.example/720"],"Width":720,"Height":1252}},
{"GearName":"d","Bitrate":1619361,"PlayAddr":{"UrlList":["https://v.example/1080"],"Width":1080,"Height":1878}}]}}]}`
	c, res, _ := tiktokCandidateFromAPIBodyResult([]byte(body), "https://www.tiktok.com/api/drama/episode/item_list/", 2, 720, nil)
	if res != tiktokAPIMatched || c.URL != "https://v.example/720" {
		t.Fatalf("720p: %v %s", res, c.URL)
	}
	if !strings.Contains(c.Kind, "720×1252，4 種畫質中720p 以內最高") {
		t.Errorf("kind %q", c.Kind)
	}
	if c, _, _ := tiktokCandidateFromAPIBodyResult([]byte(body), "https://www.tiktok.com/api/drama/episode/item_list/", 2, 0, nil); c.URL != "https://v.example/1080" {
		t.Fatalf("best: %s", c.URL)
	}
	// Landscape: 1280×720 is 720p.
	st, _ := chooseTikTokDramaStream([]tiktokDramaStream{{URLs: []string{"a"}, Width: 1920, Height: 1080}, {URLs: []string{"b"}, Width: 1280, Height: 720}}, 720)
	if st.URLs[0] != "b" {
		t.Fatalf("landscape: %v", st.URLs)
	}
}
