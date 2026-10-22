package main

import (
	"errors"
	"strings"
	"testing"
)

func TestIDOnlyName(t *testing.T) {
	cases := map[string]string{
		`C:\v\3nggw [3nggw].mp4`:      "3nggw",
		`C:\v\Real title [3nggw].mp4`: "",
		`C:\v\001 - x [x].mp4`:        "",
		`C:\v\plain.mp4`:              "",
	}
	for path, want := range cases {
		got, ok := idOnlyName(path)
		if got != want || ok != (want != "") {
			t.Errorf("idOnlyName(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
}

func TestTitleFromPage(t *testing.T) {
	cases := []struct{ body, want string }{
		// A single <h1> that the page title starts with.
		{`<title>Clip-12 Night Walk: Tag A, Tag B &amp; C - Site</title><h1 class="x" data-testid=video-title>Clip-12 Night Walk</h1>`, "Clip-12 Night Walk"},
		// <h1> unrelated to the page title (site logo): the page title wins.
		{`<title>My Video | Site</title><h1>Site</h1>`, "My Video"},
		// Several <h1>: page title without the site part.
		{`<title>A - B - Site</title><h1>x</h1><h1>y</h1>`, "A - B"},
		{`<h1> Only <b>heading</b> </h1>`, "Only heading"},
		{`<p>nothing</p>`, ""},
	}
	for _, c := range cases {
		if got := titleFromPage(c.body); got != c.want {
			t.Errorf("titleFromPage(%q) = %q; want %q", c.body, got, c.want)
		}
	}
}

func TestIsGoStackLine(t *testing.T) {
	stack := []string{
		"github.com/iawia002/lux/request.Size",
		"\t/home/runner/work/lux/lux/request/request.go:200",
		"github.com/iawia002/lux/extractors/universal.(*extractor).Extract",
		"main.main",
		"runtime.goexit",
		"/opt/hostedtoolcache/go/1.22.2/x64/src/runtime/asm_amd64.s:1695",
	}
	for _, l := range stack {
		if !isGoStackLine(l) {
			t.Errorf("isGoStackLine(%q) = false", l)
		}
	}
	for _, l := range []string{"Downloading https://example.com/a.html error:", "Content-Length is not present", ""} {
		if isGoStackLine(l) {
			t.Errorf("isGoStackLine(%q) = true", l)
		}
	}
}

func TestSummarizeJoinsErrorReason(t *testing.T) {
	out := "Downloading https://example.com/a.html error:\nContent-Length is not present\n"
	got := summarizeCommandOutput(out, errors.New("exit status 1"))
	if !strings.HasPrefix(got, "Downloading https://example.com/a.html error: Content-Length is not present") {
		t.Errorf("summary = %q", got)
	}
}

func TestSummarizeFFmpegSkipsBanner(t *testing.T) {
	out := "ffmpeg version 8.1\nlibavformat    62. 12.102 / 62. 12.102\n[in#0 @ 0001] Error opening input: Invalid data found when processing input\nError opening input file https://example.com/a.mp4.html.\n"
	got := diagErrorText(&commandRunError{Cause: errors.New("exit status 1"), Summary: summarizeCommandOutput(out, nil)})
	if !strings.Contains(got, "Error opening input") {
		t.Errorf("diag = %q", got)
	}
}

func TestCaptureIgnoresHTMLPage(t *testing.T) {
	ev := browserCaptureEvent{Kind: "cdp", URL: "https://example.com/abc/name.mp4.html", RequestType: "Document",
		ResponseHeaders: map[string]string{"content-type": "text/html; charset=UTF-8"}}
	if _, ok := mediaCandidateScore(ev); ok {
		t.Error("an HTML page was taken for an MP4 stream")
	}
	ev.URL, ev.RequestType, ev.ResponseHeaders = "https://example.com/v/name.mp4", "Media", map[string]string{"content-type": "video/mp4"}
	if _, ok := mediaCandidateScore(ev); !ok {
		t.Error("a real MP4 stream was ignored")
	}
}
