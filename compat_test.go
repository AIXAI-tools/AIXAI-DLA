package main

import (
	"strings"
	"testing"
)

func TestNeedsCompatConvert(t *testing.T) {
	av1 := mediaInfo{HasVideo: true, HasAudio: true, VideoCodec: "av1", AudioCodec: "aac"}
	h264 := mediaInfo{HasVideo: true, HasAudio: true, VideoCodec: "h264", AudioCodec: "aac"}
	on := settings{ConvertH264: true}
	cases := []struct {
		name string
		path string
		info mediaInfo
		mode int
		cfg  settings
		want bool
	}{
		{"av1 best", "a.mp4", av1, 0, on, true},
		{"av1 best, setting off", "a.mp4", av1, 0, settings{}, false},
		{"vp9 720p", "a.mp4", mediaInfo{HasVideo: true, VideoCodec: "vp9"}, 5, on, true},
		{"already h264", "a.mp4", h264, 0, on, false},
		{"format id mode", "a.mp4", av1, 4, on, false},
		{"user -f", "a.mp4", av1, 0, settings{ConvertH264: true, ExtraArgs: "-f bv+ba"}, false},
		{"webm container", "a.webm", av1, 0, on, false},
		{"mp3", "a.mp3", mediaInfo{HasAudio: true, AudioCodec: "mp3"}, 1, on, false},
	}
	for _, c := range cases {
		if got := needsCompatConvert(c.path, c.info, c.mode, c.cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCompatFFmpegArgsAudio(t *testing.T) {
	keep := strings.Join(compatFFmpegArgs("i.mp4", "o.mp4", mediaInfo{HasVideo: true, HasAudio: true, AudioCodec: "aac"}), " ")
	if !strings.Contains(keep, "-c:a copy") || !strings.Contains(keep, "libx264") {
		t.Errorf("aac should be copied: %s", keep)
	}
	opus := strings.Join(compatFFmpegArgs("i.mp4", "o.mp4", mediaInfo{HasVideo: true, HasAudio: true, AudioCodec: "opus"}), " ")
	if !strings.Contains(opus, "-c:a aac") {
		t.Errorf("opus should become aac: %s", opus)
	}
}

func TestH264FormatSort(t *testing.T) {
	cases := []struct {
		mode  int
		extra string
		want  string
	}{
		{0, "", "-S " + h264SortFields},
		{2, "", "-S " + h264SortFields},
		{5, "", "-S res:720,hasvid,ie_pref,lang,quality,fps,hdr:12,vcodec:h264"},
		{1, "", ""},
		{3, "", ""},
		{4, "", ""},
		{0, "-f bv+ba", ""},
		{0, "-S res:1080", ""},
		{0, "--format-sort=vcodec:vp9", ""},
		{5, "-Sres", ""},
		{0, "--embed-subs", "-S " + h264SortFields},
	}
	for _, c := range cases {
		if got := strings.Join(h264FormatSort(c.mode, c.extra), " "); got != c.want {
			t.Errorf("mode %d extra %q: got %q, want %q", c.mode, c.extra, got, c.want)
		}
	}
}

func TestBuildArgsFormatSort(t *testing.T) {
	a := &app{appShared: &appShared{binDir: t.TempDir()}}
	out := t.TempDir()
	sortOf := func(args []string) string {
		for i, v := range args {
			if v == "-S" && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	for _, c := range []struct {
		mode  int
		extra string
		want  string
	}{
		{0, "", h264SortFields},
		{5, "", "res:720,hasvid,ie_pref,lang,quality,fps,hdr:12,vcodec:h264"},
		{5, "-S vcodec:vp9", "vcodec:vp9"}, // the user's own -S, plus the 720p limit as before
		{0, "-S vcodec:vp9", "vcodec:vp9"},
		{1, "", ""},
	} {
		args, err := a.buildArgs("https://example.com/v", c.mode, "", out, settings{ExtraArgs: c.extra})
		if err != nil {
			t.Fatal(err)
		}
		if got := sortOf(args); got != c.want {
			t.Errorf("mode %d extra %q: first -S %q, want %q (%q)", c.mode, c.extra, got, c.want, args)
		}
		if c.mode == 5 && c.extra != "" && !strings.Contains(strings.Join(args, " "), "-S res:720") {
			t.Errorf("mode 5 lost its 720p limit: %q", args)
		}
	}
}
