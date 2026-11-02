package main

import (
	"slices"
	"testing"
)

func formatOf(args []string) string {
	if i := slices.Index(args, "-f"); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func TestIsTikTokPage(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://www.tiktok.com/@someone/video/123?_r=1": true,
		"https://vm.tiktok.com/ZSabc/":                   true,
		"https://tiktok.com/@x/video/1":                  true,
		"https://example.com/tiktok.com/video":           false,
		"https://nottiktok.com/v":                        false,
		"https://www.youtube.com/watch?v=x":              false,
	} {
		if got := isTikTokPage(raw); got != want {
			t.Errorf("isTikTokPage(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TikTok 的 "audio" 格式是配樂原曲：影片下載要先選影音同檔的格式。
func TestBuildArgsTikTokFormat(t *testing.T) {
	a := &app{appShared: &appShared{binDir: t.TempDir()}}
	out := t.TempDir()
	const tt = "https://www.tiktok.com/@someone/video/1234567890123456789?_r=1"
	for _, c := range []struct {
		url   string
		mode  int
		extra string
		want  string
	}{
		{tt, 0, "", tiktokVideoFormat},
		{tt, 2, "", tiktokVideoFormat},
		{tt, 5, "", tiktokVideoFormat},
		{tt, 1, "", tiktokAudioFormat},
		{tt, 0, "-f best", "bv*+ba/b"},       // 使用者自己選格式：不覆寫（我們加的那個不變）
		{tt, 1, "-x --audio-format m4a", ""}, // 使用者自己抽音訊：不加 -f
		{"https://www.youtube.com/watch?v=x", 0, "", "bv*+ba/b"},
		{"https://www.youtube.com/watch?v=x", 1, "", ""},
	} {
		args, err := a.buildArgs(c.url, c.mode, "", out, settings{ExtraArgs: c.extra})
		if err != nil {
			t.Fatal(err)
		}
		got := formatOf(args)
		if c.extra != "" && c.mode == 0 {
			// 使用者的 -f 在前面，我們的在後面；取最後一個
			for i := len(args) - 2; i >= 0; i-- {
				if args[i] == "-f" {
					got = args[i+1]
					break
				}
			}
		}
		if got != c.want {
			t.Errorf("%s mode %d extra %q: -f %q, want %q (%v)", c.url, c.mode, c.extra, got, c.want, args)
		}
		if args[len(args)-1] != c.url {
			t.Errorf("%s mode %d: url not last: %v", c.url, c.mode, args)
		}
	}
}
