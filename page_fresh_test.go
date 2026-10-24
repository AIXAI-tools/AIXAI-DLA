package main

import (
	"testing"
	"time"
)

func TestFreshStreamReplacesExpiredPageStream(t *testing.T) {
	a := &app{}
	expired := "https://cdn.example.com/vid/abc/f0.mp4?__token__=exp=1~hmac=old"
	fresh := "https://cdn.example.com/vid/abc/f0.mp4?__token__=exp=9~hmac=new"
	if got := a.freshStreamFor([]string{expired}); got != "" {
		t.Fatalf("nothing remembered yet, got %q", got)
	}
	if !a.rememberFreshStream(fresh, 4) {
		t.Fatal("first sighting should be new")
	}
	if a.rememberFreshStream(fresh, 4) {
		t.Fatal("second sighting should not be new")
	}
	if got := a.freshStreamFor([]string{"https://cdn.example.com/vid/other/f0.mp4", expired}); got != fresh {
		t.Fatalf("got %q, want the fresh address", got)
	}
	a.freshStreams[0].Seen = time.Now().Add(-freshStreamTTL - time.Minute)
	if got := a.freshStreamFor([]string{expired}); got != "" {
		t.Fatalf("stale stream used: %q", got)
	}
}

func TestListedEpisodeHintKnowsOtherEpisodes(t *testing.T) {
	body := `{"episodes":[` +
		`{"number":1,"title":"Episode 1","play_url":"https://cdn.example.com/vid/e1/f0.mp4?t=a"},` +
		`{"number":2,"title":"Episode 2","play_url":"https://cdn.example.com/vid/e2/f0.mp4?t=b"},` +
		`{"number":3,"title":"Episode 3","play_url":"https://cdn.example.com/vid/e3/f0.mp4?t=c"}]}`
	streams, listed, _ := pageEpisodeStreams(body, 1)
	if !listed || len(streams) != 1 {
		t.Fatalf("streams=%v listed=%v", streams, listed)
	}
	_, others := pageEpisodeListed(body, 1)
	h := pageEpisodeHint{Streams: streams, Others: others}
	if !h.matches("https://cdn.example.com/vid/e1/f0.mp4?t=new") {
		t.Fatal("own stream not matched")
	}
	if got := h.otherEpisode("https://cdn.example.com/vid/e3/f0.mp4?t=new"); got != 3 {
		t.Fatalf("preloaded episode 3 seen as %d", got)
	}
}
