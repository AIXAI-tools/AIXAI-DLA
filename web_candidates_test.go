package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestWebCandidatesSkipChannelAndProfileLinks(t *testing.T) {
	page := `<html><body>
<script>var cfg = {"source":"mp4","type":"video"};</script>
<footer>
<a href="https://www.facebook.com/people/SomePage/123456/">fb</a>
<a href="https://www.youtube.com/@SomeChannel">yt</a>
<iframe src="https://www.facebook.com/plugins/page.php?href=x"></iframe>
</footer></body></html>`
	if got := extractWebMediaCandidates(page, "https://example.com/watch/title/7"); len(got) != 0 {
		t.Fatalf("expected no candidates, got %+v", got)
	}
}

func TestWebCandidatesKeepSingleVideos(t *testing.T) {
	page := `<iframe src="https://www.youtube.com/embed/abcdefghijk"></iframe>
<script>var p = {"file":"/media/ep7/index.m3u8"};</script>`
	got := extractWebMediaCandidates(page, "https://example.com/watch/title/7")
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %+v", got)
	}
	if got[1].URL != "https://example.com/media/ep7/index.m3u8" {
		t.Fatalf("unexpected json candidate %q", got[1].URL)
	}
}

func TestLooksLikeProviderVideo(t *testing.T) {
	cases := map[string]bool{
		"https://www.youtube.com/watch?v=abcdefghijk":      true,
		"https://youtu.be/abcdefghijk":                     true,
		"https://www.youtube.com/shorts/abcdefghijk":       true,
		"https://www.youtube.com/@SomeChannel":             false,
		"https://www.youtube.com/channel/UCxxxx":           false,
		"https://www.youtube.com/watch":                    false,
		"https://vimeo.com/123456":                         true,
		"https://vimeo.com/somebody":                       false,
		"https://www.facebook.com/people/SomePage/123456/": false,
		"https://www.facebook.com/somepage/videos/123/":    true,
		"https://www.instagram.com/somebody/":              false,
		"https://www.instagram.com/reel/abc/":              true,
		"https://www.tiktok.com/@somebody":                 false,
		"https://www.tiktok.com/@somebody/video/123":       true,
	}
	for in, want := range cases {
		if got := looksLikeProviderVideo(in); got != want {
			t.Errorf("%s: got %v, want %v", in, got, want)
		}
	}
}

func TestVastMediaFiles(t *testing.T) {
	body := `<?xml version="1.0"?><VAST version="3.0"><Ad><InLine><Creatives><Creative><Linear>
<MediaFiles><MediaFile type="video/mp4" width="640" height="360"><![CDATA[ https://cdn.example.net/clip/a.mp4?x=1 ]]></MediaFile>
<MediaFile type="video/webm">https://cdn.example.net/clip/a.webm</MediaFile></MediaFiles></Linear></Creative></Creatives></InLine></Ad></VAST>`
	got := vastMediaFiles(body)
	if len(got) != 2 || got[0] != "https://cdn.example.net/clip/a.mp4?x=1" || got[1] != "https://cdn.example.net/clip/a.webm" {
		t.Fatalf("got %q", got)
	}
	if adURLKey(got[0]) != adURLKey("https://cdn.example.net/clip/a.mp4?other=2") {
		t.Fatal("ad key must ignore the query string")
	}
	if vastMediaFiles(`{"file":"https://example.com/a.mp4"}`) != nil {
		t.Fatal("non-VAST body must give nothing")
	}
}

func TestIsAdMediaURL(t *testing.T) {
	if !isAdMediaURL("https://s.example.com/v1/vast.php?idzone=1") || !isAdMediaURL("https://example.com/ads/clip.mp4") {
		t.Fatal("ad addresses not recognised")
	}
	if isAdMediaURL("https://media.example.com/show_7/zh/7/media-3/stream.m3u8?s=abc") {
		t.Fatal("episode stream taken for an ad")
	}
}

func TestEpisodeMatching(t *testing.T) {
	if got := pageEpisodeNumber("https://example.com/detail/watch/some-title/7?lang=zh-TW&from=home"); got != 7 {
		t.Fatalf("page episode %d", got)
	}
	if got := pageEpisodeNumber("https://example.com/en/film/some-title/watch?ep=12"); got != 12 {
		t.Fatalf("query episode %d", got)
	}
	if !streamHasEpisode("https://media.example.com/show_719/zh-TW/7/media-3/stream.m3u8?s=x", 7) {
		t.Fatal("episode 7 stream not matched")
	}
	if streamHasEpisode("https://media.example.com/show_719/zh-TW/9/media-3/stream.m3u8?s=x", 7) {
		t.Fatal("episode 9 stream taken for episode 7")
	}
	ep9 := browserMediaCandidate{URL: "a", Score: 150}
	ep7 := browserMediaCandidate{URL: "b", Score: 150, EpisodeMatch: true}
	if betterCaptureCandidate(ep9, ep7) || !betterCaptureCandidate(ep7, ep9) {
		t.Fatal("matching episode must win")
	}
}

func TestEpisodeStreamFromPage(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<title>Some Title Episode 7</title><script>var data={"episodes":[`)
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, `{"id":%d,"route_episode_number":%d,"number":%d,"title":"Episode %d","play_url":"https:\/\/media.example.com\/show_9\/zh\/%d\/stream.m3u8?s=x%d","thumb_url":"\/p.jpg"},`, 100+i, i, i, i, i, i)
	}
	b.WriteString(`]};</script>`)
	got, ok := episodeStreamFromPage(b.String(), 3)
	if !ok || got != "https://media.example.com/show_9/zh/3/stream.m3u8?s=x3" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := episodeStreamFromPage(b.String(), 9); ok {
		t.Fatal("missing episode must not match")
	}
	if pageTitle(b.String()) != "Some Title Episode 7" {
		t.Fatal("title")
	}
	single := `{"number":3,"file":"https://media.example.com/a.mp4"}`
	if _, ok := episodeStreamFromPage(single, 3); ok {
		t.Fatal("a lone entry is not an episode list")
	}
}

func TestStreamHasNumberSegment(t *testing.T) {
	if !streamHasNumberSegment("https://media.example.com/show_9/zh/9/stream.m3u8") {
		t.Fatal("numbered stream not recognised")
	}
	if streamHasNumberSegment("https://cdn.example.com/video/tos/abc123def/obj/v0201?a=1") {
		t.Fatal("opaque CDN address taken as numbered")
	}
}

func TestLookupPageEpisodeMissing(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&b, `{"number":%d,"play_url":"https://media.example.com/s/%d/stream.m3u8"},`, i, i)
	}
	if s, listed, max := lookupPageEpisode(b.String(), 9); s != "" || !listed || max != 4 {
		t.Fatalf("got %q %v %d", s, listed, max)
	}
	if _, listed, _ := lookupPageEpisode(`{"number":1,"file":"https://a.example.com/x.mp4"}`, 9); listed {
		t.Fatal("a lone entry is not a list")
	}
}

// Page data written as JSON text keeps "&" as \u0026; the stream address must
// be decoded or the signed query string is lost (HTTP 403).
func TestPageEpisodeStreamUnescapesJSON(t *testing.T) {
	var b strings.Builder
	for ep := 1; ep <= 4; ep++ {
		fmt.Fprintf(&b, `{\"episode\":%d,\"play_url\":\"https:\/\/cdn.example\/h%d\/v.m3u8?ts=1\u0026secret=s%d\u0026usr=u\"},`, ep, ep, ep)
	}
	body := strings.ReplaceAll(b.String(), `\"`, `"`)
	got, ok := episodeStreamFromPage(body, 3)
	want := "https://cdn.example/h3/v.m3u8?ts=1&secret=s3&usr=u"
	if !ok || got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSameStreamPath(t *testing.T) {
	a := "https://cdn.example/h3/v.m3u8?ts=1&secret=a"
	if !sameStreamPath(a, "https://CDN.example/h3/v.m3u8?ts=2&secret=b") {
		t.Fatal("same file with different query should match")
	}
	if sameStreamPath(a, "https://cdn.example/h4/v.m3u8?ts=1&secret=a") {
		t.Fatal("different episode file must not match")
	}
}

func TestPageEpisodeOutputName(t *testing.T) {
	body := "<title>Some Show 第 3 集 - Site</title>"
	if got := capturedOutputBase("https://x/detail/watch/some-show-3/3", pageEpisodeTitle("https://x/detail/watch/some-show-3/3", body)); got != "some-show-3_E003" {
		t.Fatalf("got %q", got)
	}
	if got := capturedOutputBase("https://x/play/16465/3", pageEpisodeTitle("https://x/play/16465/3", body)); got != "Some Show 第 3 集 - Site_E003" {
		t.Fatalf("got %q", got)
	}
}
