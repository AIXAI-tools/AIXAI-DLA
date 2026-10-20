//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCookieHeaderFromFile(t *testing.T) {
	// Synthetic cookies only.
	text := "# Netscape HTTP Cookie File\n" +
		"#HttpOnly_.example.com\tTRUE\t/\tTRUE\t0\tsession\tabc\n" +
		".example.com\tTRUE\t/\tFALSE\t0\tlang\tzh\n" +
		".example.com\tTRUE\t/\tFALSE\t1000\told\tgone\n" +
		"other.example.com\tFALSE\t/\tFALSE\t0\tnarrow\tx\n" +
		".another.org\tTRUE\t/\tFALSE\t0\tforeign\ty\n"
	path := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if got, want := cookieHeaderFromFile(path, "www.example.com"), "session=abc; lang=zh"; got != want {
		t.Errorf("www.example.com: got %q, want %q", got, want)
	}
	if got, want := cookieHeaderFromFile(path, "other.example.com"), "session=abc; lang=zh; narrow=x"; got != want {
		t.Errorf("other.example.com: got %q, want %q", got, want)
	}
	if got := cookieHeaderFromFile(path, "sub.other.example.com"); got != "session=abc; lang=zh" {
		t.Errorf("host-only cookie sent to a subdomain: %q", got)
	}
}
