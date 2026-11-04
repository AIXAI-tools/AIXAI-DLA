package main

import (
	"net/url"
	"strings"
)

// isAlphaNumeric reports whether b is an ASCII letter or digit.
func isAlphaNumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// hasMediaExt reports whether raw contains ext (e.g. ".webm", ".mp4", ".m3u8", ".mpd", ".m4v"),
// where the extension must be followed by either the end of string or a non-alphanumeric
// character (such as '?', '#', '/', '&', etc.).
// Both raw and ext are compared case-insensitively.
func hasMediaExt(raw, ext string) bool {
	if ext == "" {
		return false
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	lower := strings.ToLower(raw)
	ext = strings.ToLower(ext)
	start := 0
	for {
		idx := strings.Index(lower[start:], ext)
		if idx < 0 {
			return false
		}
		pos := start + idx + len(ext)
		if pos == len(lower) || !isAlphaNumeric(lower[pos]) {
			return true
		}
		start = start + idx + 1
	}
}

// hasAnyMediaExt reports whether raw matches any of the given extensions using hasMediaExt.
func hasAnyMediaExt(raw string, exts ...string) bool {
	for _, ext := range exts {
		if hasMediaExt(raw, ext) {
			return true
		}
	}
	return false
}

// isTurnstileURL reports whether raw has the hostname challenges.cloudflare.com.
func isTurnstileURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "challenges.cloudflare.com"
}
