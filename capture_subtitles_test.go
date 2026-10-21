//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestEpisodeNumberFromQuery(t *testing.T) {
	for raw, want := range map[string]int{
		"https://site.example/film/x/watch?ep=9":        9,
		"https://site.example/play?episode=3&lang=en":   3,
		"https://site.example/play?episode_id=31284554": 0,
		"https://site.example/watch?v=abc":              0,
	} {
		if got := episodeNumberFromQuery(raw); got != want {
			t.Errorf("%s = %d, want %d", raw, got, want)
		}
	}
}

func TestWithEpisodeQuery(t *testing.T) {
	if got := withEpisodeQuery("https://site.example/watch?ep=9&lang=en", 8); got != "https://site.example/watch?ep=8&lang=en" {
		t.Errorf("got %s", got)
	}
	if got := withEpisodeQuery("https://site.example/watch", 8); got != "" {
		t.Errorf("no episode parameter: got %s", got)
	}
}

func TestBuiltInVideoIsEpisode(t *testing.T) {
	// /shared?ep=N names the same video on every episode; /own?ep=N names
	// each episode's own video; /broken fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ep := r.URL.Query().Get("ep")
		switch r.URL.Path {
		case "/shared":
			fmt.Fprint(w, `<video preload="none"><source src="/stream/series.mp4" type="video/mp4"></video>`)
		case "/own":
			fmt.Fprintf(w, `<video preload="none" src="/media/ep%s.mp4"></video>`, ep)
		default:
			http.Error(w, "no", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	key := func(p string) []string { return []string{adURLKey(srv.URL + p)} }
	if builtInVideoIsEpisode(ctx, srv.URL+"/shared?ep=3", 3, key("/stream/series.mp4")) {
		t.Error("shared video taken for the episode")
	}
	if !builtInVideoIsEpisode(ctx, srv.URL+"/own?ep=3", 3, key("/media/ep3.mp4")) {
		t.Error("the episode's own video refused")
	}
	if !builtInVideoIsEpisode(ctx, srv.URL+"/own?ep=1", 1, key("/media/ep1.mp4")) {
		t.Error("episode 1 compares with episode 2")
	}
	if builtInVideoIsEpisode(ctx, srv.URL+"/broken?ep=3", 3, key("/media/ep3.mp4")) {
		t.Error("unreadable other page must count as not this episode")
	}
}
