//go:build windows

package main

import "testing"

func TestCaptureSubtitlesUsesListOfLoadedFile(t *testing.T) {
	other := []byte(`{"data":[{"url":"https://cdn.example/sub/ep9-en.vtt","language_code":"en"}]}`)
	list := []byte(`[{"url":"https://cdn.example/sub/ep3-zh.vtt","language_code":"zh-Hant","language_name":"中文"},` +
		`{"url":"https://cdn.example/sub/ep3-en.vtt","language_code":"en","language_name":"English"}]`)
	subs := captureSubtitles([][]byte{other, list}, []string{"https://cdn.example/sub/ep3-en.vtt?sig=1"}, "https://site.example/watch?ep=3")
	if len(subs) != 2 {
		t.Fatalf("got %d subtitles, want 2: %+v", len(subs), subs)
	}
	if subs[0].Lang != "zh-Hant" || subs[0].Label != "中文" || subs[0].Default {
		t.Errorf("first = %+v", subs[0])
	}
	if !subs[1].Default {
		t.Errorf("the loaded file should be the main one: %+v", subs[1])
	}
}

func TestCaptureSubtitlesFallbacks(t *testing.T) {
	// Only loaded files: they are used as they are.
	subs := captureSubtitles(nil, []string{"https://cdn.example/a.vtt"}, "https://site.example/")
	if len(subs) != 1 || !subs[0].Default {
		t.Fatalf("loaded only: %+v", subs)
	}
	// Many files are the segments of a streamed track, not subtitle files.
	var segs []string
	for _, n := range []string{"1", "2", "3", "4", "5", "6", "7"} {
		segs = append(segs, "https://cdn.example/seg"+n+".vtt")
	}
	if subs := captureSubtitles(nil, segs, "https://site.example/"); len(subs) != 0 {
		t.Fatalf("segments: %+v", subs)
	}
	// Nothing loaded: the page's only subtitle list.
	list := []byte(`{"subs":[{"src":"/s/zh.srt","lang":"zh"}],"poster":{"url":"https://cdn.example/p.jpg"}}`)
	subs = captureSubtitles([][]byte{list, []byte(`{"ok":true}`)}, nil, "https://site.example/watch")
	if len(subs) != 1 || subs[0].URL != "https://site.example/s/zh.srt" {
		t.Fatalf("single list: %+v", subs)
	}
	// Two lists and nothing loaded: unknown which episode, none used.
	other := []byte(`[{"url":"https://cdn.example/x.vtt"}]`)
	if subs := captureSubtitles([][]byte{list, other}, nil, "https://site.example/watch"); len(subs) != 0 {
		t.Fatalf("ambiguous: %+v", subs)
	}
}

func TestIsSubtitleResponse(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://cdn.example/a.vtt?x=1":  true,
		"https://cdn.example/a.SRT":      true,
		"https://cdn.example/a.mp4":      false,
		"https://cdn.example/vtt/a.json": false,
	} {
		if got := isSubtitleResponse(raw, ""); got != want {
			t.Errorf("%s = %v", raw, got)
		}
	}
	if !isSubtitleResponse("https://cdn.example/sub/123", "text/vtt; charset=utf-8") {
		t.Error("text/vtt type")
	}
}
