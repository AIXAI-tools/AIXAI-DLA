package main

import (
	"strings"
	"testing"
)

func TestHasMediaExt(t *testing.T) {
	cases := []struct {
		url     string
		ext     string
		want    bool
		comment string
	}{
		{"site.webmanifest", ".webm", false, "webmanifest contains .webm followed by 'a'"},
		{"a.webm", ".webm", true, "exact webm at end"},
		{"a.webm?x=1", ".webm", true, "webm followed by query '?'"},
		{"a.mp4/index.m3u8", ".m3u8", true, "m3u8 at end"},
		{"a.mp4/index.m3u8", ".mp4", true, "mp4 followed by '/'"},
		{"x.m3u8?token=1", ".m3u8", true, "m3u8 followed by '?'"},
		{"a.mp4x", ".mp4", false, "mp4 followed by alphanumeric 'x'"},
		{"a.mpd", ".mpd", true, "exact mpd at end"},
		{"/hls/1/2/playlist.m3u8", ".m3u8", true, "m3u8 path"},
		// Additional edge cases
		{"https://example.com/site.webmanifest", ".webm", false, "full url webmanifest"},
		{"https://example.com/site.webmanifest#top", ".webm", false, "webmanifest with hash"},
		{"video.MP4", ".mp4", true, "uppercase extension"},
		{"video.mp4#frag", ".mp4", true, "mp4 followed by '#'"},
		{"video.mp4&param=1", ".mp4", true, "mp4 followed by '&'"},
		{"notmedia.mp4abc", ".mp4", false, "mp4 followed by 'abc'"},
	}

	for _, tc := range cases {
		got := hasMediaExt(tc.url, tc.ext)
		if got != tc.want {
			t.Errorf("hasMediaExt(%q, %q) = %v, want %v (%s)", tc.url, tc.ext, got, tc.want, tc.comment)
		}
	}
}

func TestLooksLikeDirectMedia(t *testing.T) {
	// Demonstrate before vs after on site.webmanifest:
	// Before fix: strings.Contains(strings.ToLower("site.webmanifest"), ".webm") returned true (bug).
	legacyContains := strings.Contains(strings.ToLower("site.webmanifest"), ".webm")
	if !legacyContains {
		t.Fatal("expected legacy strings.Contains to match .webm in site.webmanifest")
	}

	// After fix: looksLikeDirectMedia must return false.
	if looksLikeDirectMedia("site.webmanifest") {
		t.Errorf("looksLikeDirectMedia(\"site.webmanifest\") = true, want false")
	}
	if looksLikeDirectMedia("https://example.com/site.webmanifest") {
		t.Errorf("looksLikeDirectMedia(\"https://example.com/site.webmanifest\") = true, want false")
	}

	// Valid media should return true
	validCases := []string{
		"a.webm",
		"a.webm?x=1",
		"a.mp4/index.m3u8",
		"x.m3u8?token=1",
		"a.mpd",
		"/hls/1/2/playlist.m3u8",
		"https://example.com/video.mp4",
		"https://example.com/stream.m4v",
	}
	for _, u := range validCases {
		if !looksLikeDirectMedia(u) {
			t.Errorf("looksLikeDirectMedia(%q) = false, want true", u)
		}
	}

	// Invalid media should return false
	invalidCases := []string{
		"a.mp4x",
		"site.webmanifest",
		"https://example.com/site.webmanifest",
		"https://example.com/page.html",
	}
	for _, u := range invalidCases {
		if looksLikeDirectMedia(u) {
			t.Errorf("looksLikeDirectMedia(%q) = true, want false", u)
		}
	}
}

func TestIsTurnstileURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"challenges.cloudflare.com", true},
		{"https://challenges.cloudflare.com", true},
		{"https://challenges.cloudflare.com/turnstile/v0/api.js", true},
		{"http://challenges.cloudflare.com:8080/foo", true},
		{"example.com/challenges.cloudflare.com.js", false},
		{"https://example.com/challenges.cloudflare.com.js", false},
		{"notchallenges.cloudflare.com.evil", false},
		{"https://notchallenges.cloudflare.com.evil", false},
		{"https://challenges.cloudflare.com.evil", false},
		{"", false},
		{"   ", false},
	}

	for _, tc := range cases {
		got := isTurnstileURL(tc.url)
		if got != tc.want {
			t.Errorf("isTurnstileURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
