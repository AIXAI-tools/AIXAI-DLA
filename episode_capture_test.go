package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestExpandSequenceAddsEpisodeQuery(t *testing.T) {
	got, err := expandSequenceURL("https://example.com/en/film/some-title-123/watch", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/en/film/some-title-123/watch?ep=1",
		"https://example.com/en/film/some-title-123/watch?ep=2",
		"https://example.com/en/film/some-title-123/watch?ep=3",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %q", got)
	}
	// Other query values are kept.
	got, err = expandSequenceURL("https://example.com/watch?lang=en", 2, 2)
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "lang=en") || episodeFromQuery(got[0]) != 2 {
		t.Fatalf("got %q, %v", got, err)
	}
	// An existing ep value is replaced, not duplicated.
	got, _ = expandSequenceURL("https://example.com/watch?ep=7", 4, 4)
	if len(got) != 1 || episodeFromQuery(got[0]) != 4 || strings.Count(got[0], "ep=") != 1 {
		t.Fatalf("got %q", got)
	}
	// A numeric last segment keeps the old behaviour.
	got, _ = expandSequenceURL("https://example.com/detail/watch/show/1", 5, 5)
	if len(got) != 1 || got[0] != "https://example.com/detail/watch/show/5" {
		t.Fatalf("got %q", got)
	}
	// An unreadable ep value is not overwritten.
	if _, err := expandSequenceURL("https://example.com/watch?ep=abc", 1, 2); err == nil {
		t.Fatal("want an error for a non-numeric ep value")
	}
}

func TestEpisodeFromQuery(t *testing.T) {
	cases := map[string]int{
		"https://x/watch?ep=9":          9,
		"https://x/watch?episode=12":    12,
		"https://x/watch?lang=en&ep=3":  3,
		"https://x/watch":               0,
		"https://x/watch?ep=abc":        0,
		"https://x/detail/watch/show/1": 0,
	}
	for in, want := range cases {
		if got := episodeFromQuery(in); got != want {
			t.Errorf("%s → %d, want %d", in, got, want)
		}
	}
}

func TestCapturedOutputBaseUsesNumericPathEpisode(t *testing.T) {
	title := "Some Title - Play - Site"
	a := capturedOutputBase("https://x/play/16465/1/", title)
	b := capturedOutputBase("https://x/play/16465/2", title)
	if a == b || !strings.HasSuffix(a, "_E001") || !strings.HasSuffix(b, "_E002") {
		t.Fatalf("names must differ by episode: %q %q", a, b)
	}
	if got := capturedOutputBase("https://x/play/16465/12", ""); got != "16465_E012" {
		t.Fatalf("without a page title the name comes from the path: %q", got)
	}
}

func TestCapturedOutputBaseUsesEpisodeQuery(t *testing.T) {
	a := capturedOutputBase("https://x/en/film/t-1/watch?ep=7", "Watch Some Title | Site")
	b := capturedOutputBase("https://x/en/film/t-1/watch?ep=8", "Watch Some Title | Site")
	if a == b || !strings.HasSuffix(a, "_E007") || !strings.HasSuffix(b, "_E008") {
		t.Fatalf("names must differ by episode: %q %q", a, b)
	}
	if got := capturedOutputBase("https://x/en/film/some-title-123/watch?ep=2", ""); got != "some-title-123_E002" {
		t.Fatalf("without a page title the series name comes from the path: %q", got)
	}
}

func TestPageEpisodeFromJSObjectList(t *testing.T) {
	// A JS object literal (not strict JSON) listing the season; subtitle .vtt
	// files next to it must not be taken as streams.
	body := `window.PlayData = {
  id: "100", count: 3,
  subtitles: {"1":[{"src":"https://cdn.example/m/100/001_a.vtt"}]},
  episodes: [{"episode":"1","title":"001","src":"https://cdn.example/m/100/001_a.mp4","type":"video/mp4"},{"episode":"2","title":"002","src":"https://cdn.example/m/100/002_b.mp4","type":"video/mp4"},{"episode":"3","title":"003","src":"https://cdn.example/m/100/003_c.mp4","type":"video/mp4"}]
};`
	for ep, want := range map[int]string{1: "001_a.mp4", 2: "002_b.mp4", 3: "003_c.mp4"} {
		got, ok := episodeStreamFromPage(body, pageEpisodeNumber(fmt.Sprintf("https://x/play/100/%d/", ep)))
		if !ok || !strings.HasSuffix(got, want) {
			t.Errorf("ep %d → %q, want …%s", ep, got, want)
		}
	}
}
