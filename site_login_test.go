//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"
)

func TestWantsCaptureLogin(t *testing.T) {
	parse := errors.New("ERROR: [facebook] 1: Cannot parse data; please report this issue")
	net := errors.New("ERROR: unable to download video data: HTTP Error 403: Forbidden")
	reel := "https://www.facebook.com/reel/123"
	cases := []struct {
		name string
		url  string
		err  error
		cfg  settings
		want bool
	}{
		{"auto, parse error", reel, parse, settings{CookieBrowser: "auto"}, true},
		{"empty setting", "https://fb.watch/abc/", parse, settings{}, true},
		{"network error", reel, net, settings{CookieBrowser: "auto"}, false},
		{"user chose no login", reel, parse, settings{CookieBrowser: "none"}, false},
		{"user chose a browser", reel, parse, settings{CookieBrowser: "edge"}, false},
		{"user cookie file", reel, parse, settings{CookieBrowser: "auto", CookieFile: `C:\c.txt`}, false},
		{"other site", "https://example.com/v/1", parse, settings{}, false},
	}
	for _, c := range cases {
		if got := wantsCaptureLogin(c.url, c.err, c.cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSiteLoginCookies(t *testing.T) {
	s := siteLoginFor("https://m.facebook.com/watch/?v=1")
	if s == nil {
		t.Fatal("no sign-in for the site")
	}
	cookies := []map[string]interface{}{
		{"name": "c_user", "value": "1", "domain": ".facebook.com", "path": "/", "secure": true, "expires": 1.9e9},
		{"name": "xs", "value": "v", "domain": ".facebook.com", "path": "/", "secure": true, "httpOnly": true, "expires": -1.0},
	}
	if !signedIn(cookies, s) {
		t.Error("both session cookies present: should count as signed in")
	}
	if signedIn(cookies[:1], s) {
		t.Error("one session cookie missing: should not count as signed in")
	}
	txt := netscapeCookies(cookies)
	for _, want := range []string{
		".facebook.com\tTRUE\t/\tTRUE\t1900000000\tc_user\t1\n",
		"#HttpOnly_.facebook.com\tTRUE\t/\tTRUE\t0\txs\tv\n",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("cookies file missing %q in:\n%s", want, txt)
		}
	}
}
