package main

import (
	"reflect"
	"testing"
)

func TestWithoutRefererArgs(t *testing.T) {
	in := []string{"-f", "best", "--referer", "https://site.example/ep/4", "--user-agent", "UA",
		"--add-header", "Cookie:a=b", "--add-header", "Origin:https://site.example", "--add-header", "Accept:*/*", "-o", "x.%(ext)s", "https://cdn.example/v.m3u8"}
	want := []string{"-f", "best", "--user-agent", "UA", "--add-header", "Cookie:a=b", "--add-header", "Accept:*/*", "-o", "x.%(ext)s", "https://cdn.example/v.m3u8"}
	if got := withoutRefererArgs(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestRefusedManifestCandidate(t *testing.T) {
	// The refusal comes as the CDN's HTML error page.
	hls := browserCaptureEvent{URL: "https://cdn.example/playlet-hls/hls_1_4_2.m3u8?verify=x", Refused: true,
		ResponseHeaders: map[string]string{"content-type": "text/html; charset=UTF-8", "content-length": "5000000"}}
	c, ok := mediaCandidateScore(hls)
	if !ok || c.Kind != "HLS" {
		t.Fatalf("refused manifest dropped: %+v %v", c, ok)
	}
	loaded, _ := mediaCandidateScore(browserCaptureEvent{URL: "https://cdn.example/other.m3u8"})
	if c.Score >= loaded.Score || c.Score < 125 {
		t.Fatalf("refused manifest score %d: want below %d and at least 125", c.Score, loaded.Score)
	}
	if _, ok := mediaCandidateScore(browserCaptureEvent{URL: "https://cdn.example/v.mp4", Refused: true}); ok {
		t.Fatal("refused plain file kept")
	}
	if !isManifestURL("https://a.example/x/v.M3U8?verify=1") || isManifestURL("https://a.example/x/v.ts") {
		t.Fatal("isManifestURL")
	}
}

func TestWithoutRefererFFmpeg(t *testing.T) {
	in := []string{"-y", "-user_agent", "UA", "-headers", "Referer: https://site.example/\r\nOrigin: https://site.example\r\nCookie: a=b\r\n", "-i", "https://cdn.example/v.m3u8", "out.mp4"}
	want := []string{"-y", "-user_agent", "UA", "-headers", "Cookie: a=b\r\n", "-i", "https://cdn.example/v.m3u8", "out.mp4"}
	if got := withoutRefererFFmpeg(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	only := []string{"-y", "-headers", "Referer: https://site.example/\r\n", "-i", "u", "o.mp4"}
	if got := withoutRefererFFmpeg(only); !reflect.DeepEqual(got, []string{"-y", "-i", "u", "o.mp4"}) {
		t.Fatalf("got %q", got)
	}
}
