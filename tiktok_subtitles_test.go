//go:build windows

package main

import "testing"

func TestTikTokCandidateSubtitles(t *testing.T) {
	body := `{"statusCode":0,"itemList":[{"id":"1","video":{"playAddr":"https://v.example/a.mp4",` +
		`"subtitleInfos":[{"Url":"https://s.example/en","LanguageCodeName":"eng-US","Source":"ASR","Format":"webvtt"},` +
		`{"Url":"https://s.example/en","LanguageCodeName":"eng-US","Source":"ASR","Format":"webvtt"},` +
		`{"Url":"https://s.example/zh","LanguageCodeName":"cmn-Hant-CN","Source":"MT","Format":"webvtt"},` +
		`{"Url":"https://s.example/x","LanguageCodeName":"eng-US","Source":"ASR","Format":"creator_caption"}]},` +
		`"dramaInfo":{"DramaVideoData":{"EpisodeNumber":1}}}]}`
	c, res, _ := tiktokCandidateFromAPIBodyResult([]byte(body), "https://www.tiktok.com/api/drama/episode/item_list/", 1, 0, nil)
	if res != tiktokAPIMatched {
		t.Fatalf("res = %v", res)
	}
	if len(c.Subtitles) != 2 {
		t.Fatalf("subtitles = %+v", c.Subtitles)
	}
	if s := c.Subtitles[0]; s.URL != "https://s.example/en" || !s.Default || s.Label != "英文自動字幕，語音辨識" {
		t.Fatalf("first = %+v", s)
	}
	if s := c.Subtitles[1]; s.Default || s.Lang != "cmn-Hant-CN" || s.Label != "繁體中文字幕，機器翻譯" {
		t.Fatalf("second = %+v", s)
	}
	if names := subtitleNames("v", c.Subtitles); names[0] != "v" || names[1] != "v.cmn-Hant-CN" {
		t.Fatalf("names = %v", names)
	}
}
