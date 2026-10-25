package main

import "testing"

func TestYtRunErrorsOnlyUnavailable(t *testing.T) {
	private := "ERROR: [youtube] priv1234567: Private video. If the owner of this video has granted you access, please sign in. Use --cookies-from-browser or --cookies for the authentication.\n"
	removed := "ERROR: [youtube] gone1234567: Video unavailable. This video has been removed by the uploader\n"
	finished := "[download] Finished downloading playlist: Example\n"
	cases := []struct {
		name  string
		lines []string
		want  bool
		n     int
	}{
		{"private and removed, finished", []string{"[download] Downloading item 3 of 201\n", private, removed, finished}, true, 2},
		{"not finished", []string{private}, false, 1},
		{"no errors", []string{finished}, false, 0},
		{"rate limited", []string{private, "ERROR: [youtube] abc-_12: Video unavailable. This content isn't available, try again later.\n", finished}, false, 1},
		{"members only", []string{"ERROR: [youtube] abc: Join this channel to get access to members-only content like this video\n", finished}, false, 0},
		{"other error", []string{private, "ERROR: unable to download video data: HTTP Error 403: Forbidden\n", finished}, false, 1},
		{"progress before error", []string{"[download]  50.0% of 1MiB\r" + private, finished}, true, 1},
	}
	for _, c := range cases {
		var r ytRunErrors
		for _, l := range c.lines {
			r.observe(l)
		}
		if got := r.onlyUnavailable(); got != c.want {
			t.Errorf("%s: onlyUnavailable = %v, want %v", c.name, got, c.want)
		}
		if len(r.unavailable) != c.n {
			t.Errorf("%s: %d unavailable, want %d (%v)", c.name, len(r.unavailable), c.n, r.unavailable)
		}
	}
}

func TestUnavailableItemText(t *testing.T) {
	var r ytRunErrors
	r.observe("ERROR: [youtube] priv1234567: Private video. Sign in if you've been granted access\n")
	r.observe("ERROR: [youtube] gone1234567: Video unavailable. This video has been removed by the uploader\n")
	want := []string{"priv1234567：私人影片", "gone1234567：已移除或無法觀看"}
	for i, w := range want {
		if r.unavailable[i] != w {
			t.Errorf("unavailable[%d] = %q, want %q", i, r.unavailable[i], w)
		}
	}
}
