package main

import (
	"strings"
	"testing"
)

func TestNeedsCompatConvert(t *testing.T) {
	av1 := mediaInfo{HasVideo: true, HasAudio: true, VideoCodec: "av1", AudioCodec: "aac"}
	h264 := mediaInfo{HasVideo: true, HasAudio: true, VideoCodec: "h264", AudioCodec: "aac"}
	cases := []struct {
		name string
		path string
		info mediaInfo
		mode int
		cfg  settings
		want bool
	}{
		{"av1 best", "a.mp4", av1, 0, settings{}, true},
		{"vp9 720p", "a.mp4", mediaInfo{HasVideo: true, VideoCodec: "vp9"}, 5, settings{}, true},
		{"already h264", "a.mp4", h264, 0, settings{}, false},
		{"format id mode", "a.mp4", av1, 4, settings{}, false},
		{"user -f", "a.mp4", av1, 0, settings{ExtraArgs: "-f bv+ba"}, false},
		{"webm container", "a.webm", av1, 0, settings{}, false},
		{"mp3", "a.mp3", mediaInfo{HasAudio: true, AudioCodec: "mp3"}, 1, settings{}, false},
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
