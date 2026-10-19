//go:build windows

package main

import (
	"reflect"
	"testing"
)

func TestPageEpisodeSubtitlesByEpisode(t *testing.T) {
	body := `<script>var player = { episodes: [{"episode":"1","src":"https://cdn.example.com/m/9/001_aa.mp4"}],
 subtitles: {"1":[{"label":"字幕一","lang":"","src":"https://cdn.example.com/m/9/001_aa.vtt","isDefault":true}],` +
		`"2":[{"label":"字幕一","lang":"","src":"https://cdn.example.com/m/9/002_bb.vtt","isDefault":true},` +
		`{"label":"English {en}","lang":"en","src":"/m/9/002_bb.en.vtt"}]}, other: 1 };</script>
<track kind="subtitles" src="https://cdn.example.com/wrong.vtt">`
	got := pageEpisodeSubtitles(body, "https://www.example.com/play/9/2/", 2)
	want := []pageSubtitle{
		{URL: "https://cdn.example.com/m/9/002_bb.vtt", Label: "字幕一", Default: true},
		{URL: "https://www.example.com/m/9/002_bb.en.vtt", Label: "English {en}", Lang: "en"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("episode 2 = %#v, want %#v", got, want)
	}
	if got := pageEpisodeSubtitles(body, "https://www.example.com/play/9/1/", 1); len(got) != 1 || got[0].URL != "https://cdn.example.com/m/9/001_aa.vtt" {
		t.Fatalf("episode 1 = %#v", got)
	}
	// An episode the subtitle list does not name falls back to the page's
	// <track>, which belongs to the video the page plays.
	if got := pageEpisodeSubtitles(body, "https://www.example.com/play/9/3/", 3); len(got) != 1 || got[0].URL != "https://cdn.example.com/wrong.vtt" {
		t.Fatalf("episode 3 = %#v", got)
	}
}

func TestPageEpisodeSubtitlesTrack(t *testing.T) {
	body := `<video><source src="v.mp4"><track kind="chapters" src="ch.vtt">` +
		`<track kind="captions" srclang="zh" label="中文" src="/subs/zh.vtt" default>` +
		`<track src='/subs/en.srt' srclang=en></video>`
	got := pageEpisodeSubtitles(body, "https://example.com/watch/abc", 0)
	want := []pageSubtitle{
		{URL: "https://example.com/subs/zh.vtt", Label: "中文", Lang: "zh", Default: true},
		{URL: "https://example.com/subs/en.srt", Lang: "en"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tracks = %#v, want %#v", got, want)
	}
	if got := pageEpisodeSubtitles(`<video src="v.mp4"></video>`, "https://example.com/", 1); len(got) != 0 {
		t.Fatalf("no subtitles = %#v", got)
	}
}

func TestSubtitleNames(t *testing.T) {
	subs := []pageSubtitle{{Lang: "en"}, {Label: "中文", Default: true}, {Lang: "en"}, {}}
	got := subtitleNames("", subs)
	want := []string{".en", "", ".3", ".4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func TestSubtitleExt(t *testing.T) {
	cases := []struct{ url, text, want string }{
		{"https://x.example.com/a.vtt?sig=1", "", ".vtt"},
		{"https://x.example.com/sub?id=1", "WEBVTT\n\n00:00.000 --> 00:01.000\nhi", ".vtt"},
		{"https://x.example.com/sub?id=1", "[Script Info]\nTitle: x", ".ass"},
		{"https://x.example.com/sub?id=1", "1\n00:00:00,000 --> 00:00:01,000\nhi", ".srt"},
	}
	for _, c := range cases {
		if got := subtitleExt(c.url, c.text); got != c.want {
			t.Errorf("subtitleExt(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestBalancedObjectSkipsStrings(t *testing.T) {
	body := `x = {"a":"}{","b":{"c":'}'}} tail`
	if got := balancedObject(body, 4); got != `{"a":"}{","b":{"c":'}'}}` {
		t.Fatalf("balancedObject = %q", got)
	}
	if got := balancedObject(`{"a":1`, 0); got != "" {
		t.Fatalf("unterminated = %q", got)
	}
}
