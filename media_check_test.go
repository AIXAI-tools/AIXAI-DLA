package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const probeVideoAudio = `{"streams":[
 {"codec_type":"video","codec_name":"h264","width":1080,"height":1920,"duration":"92.4","disposition":{"attached_pic":0}},
 {"codec_type":"audio","codec_name":"aac","duration":"92.5"}],
 "format":{"duration":"92.500000","size":"12345678"}}`

func TestParseProbeJSON(t *testing.T) {
	info, err := parseProbeJSON([]byte(probeVideoAudio))
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasVideo || !info.HasAudio || info.Width != 1080 || info.Height != 1920 || info.Duration < 92 || info.Size != 12345678 {
		t.Fatalf("unexpected %+v", info)
	}
	// Cover art of an MP3 is not a picture track.
	mp3 := `{"streams":[{"codec_type":"audio","codec_name":"mp3"},{"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}}],"format":{"duration":"180"}}`
	info, err = parseProbeJSON([]byte(mp3))
	if err != nil || info.HasVideo || !info.HasAudio {
		t.Fatalf("mp3 with cover: %+v %v", info, err)
	}
	if _, err := parseProbeJSON([]byte(`{"streams":[],"format":{}}`)); err == nil {
		t.Fatal("a file without streams must be an error")
	}
	// Duration from the streams when the container has none.
	info, _ = parseProbeJSON([]byte(`{"streams":[{"codec_type":"video","codec_name":"vp9","width":1,"height":1,"duration":"7.5"}],"format":{"duration":"N/A"}}`))
	if info.Duration != 7.5 {
		t.Fatalf("stream duration fallback: %v", info.Duration)
	}
}

func blocking(ps []mediaProblem) int {
	n := 0
	for _, p := range ps {
		if p.Blocking {
			n++
		}
	}
	return n
}

func TestJudgeMedia(t *testing.T) {
	good := mediaInfo{HasVideo: true, HasAudio: true, Duration: 90}
	audioOnly := mediaInfo{HasAudio: true, Duration: 90}
	silent := mediaInfo{HasVideo: true, Duration: 90}
	short := mediaInfo{HasVideo: true, HasAudio: true, Duration: 4}
	video := checkPolicy{needVideo: true}
	strictVideo := checkPolicy{needVideo: true, strict: true}

	if ps := judgeMedia(good, strictVideo); len(ps) != 0 {
		t.Fatalf("good file: %v", ps)
	}
	if blocking(judgeMedia(audioOnly, video)) != 1 {
		t.Fatal("audio-only file in a video download must block, even when not strict")
	}
	if ps := judgeMedia(silent, video); len(ps) != 1 || blocking(ps) != 0 {
		t.Fatalf("silent video, not strict: warn only: %v", ps)
	}
	if blocking(judgeMedia(silent, strictVideo)) != 1 {
		t.Fatal("silent video, strict: block")
	}
	if ps := judgeMedia(short, video); len(ps) != 1 || blocking(ps) != 0 {
		t.Fatalf("short clip, not strict: warn only: %v", ps)
	}
	if blocking(judgeMedia(short, strictVideo)) != 1 {
		t.Fatal("short clip, strict: block")
	}
	if blocking(judgeMedia(audioOnly, checkPolicy{needVideo: true, strict: true, warnOnly: true})) != 0 {
		t.Fatal("playlist downloads are never failed")
	}
	if blocking(judgeMedia(mediaInfo{HasVideo: true, Duration: 60}, checkPolicy{needAudio: true})) != 1 {
		t.Fatal("audio download without sound must block")
	}
}

func TestPolicyFor(t *testing.T) {
	cfg := settings{}
	if p := policyFor(`C:\x\a.mp4`, 0, cfg, true); !p.needVideo || p.needAudio || !p.strict {
		t.Fatalf("mp4 best: %+v", p)
	}
	if p := policyFor(`C:\x\a.mp3`, 1, cfg, false); !p.needAudio || p.needVideo {
		t.Fatalf("mp3: %+v", p)
	}
	if p := policyFor(`C:\x\a.mp4`, 2, cfg, false); !p.warnOnly {
		t.Fatalf("playlist: %+v", p)
	}
	if p := policyFor(`C:\x\a.mp4`, 4, cfg, false); p.needVideo || p.needAudio {
		t.Fatalf("format ID mode: %+v", p)
	}
	for _, extra := range []string{"-f ba", "-fbest", "--format=bv", "-x --audio-format m4a", "--extract-audio"} {
		if p := policyFor(`C:\x\a.mp4`, 0, settings{ExtraArgs: extra}, false); p.needVideo {
			t.Fatalf("user args %q must be respected: %+v", extra, p)
		}
	}
	if p := policyFor(`C:\x\a.mp4`, 0, settings{ExtraArgs: "--embed-subs --sub-langs zh-TW"}, false); !p.needVideo {
		t.Fatalf("unrelated user args: %+v", p)
	}
}

func TestPendingOutputs(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	final := write("Title.mp4")
	part := write("Title.f137.mp4")
	sub := write("Title.zh.vtt")
	a := &app{}
	a.noteOutput(part)
	a.noteOutput(final)
	a.noteOutput(final)
	a.noteOutput(sub)
	a.noteOutput(filepath.Join(dir, "gone.mp4"))
	got := a.pendingOutputs()
	if len(got) != 1 || got[0] != final {
		t.Fatalf("pending outputs: %v", got)
	}
	if other := a.nonMediaOutputs(); len(other) != 0 {
		t.Fatalf("subtitles are side files, not wrong results: %v", other)
	}
	page := write("watch.html")
	b := &app{}
	b.noteOutput(page)
	if other := b.nonMediaOutputs(); len(other) != 1 || other[0] != page {
		t.Fatalf("a saved web page must be reported: %v", other)
	}
}

func TestMarkFailedCheck(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Show_E001.mp4")
	_ = os.WriteFile(p, []byte("x"), 0644)
	m1 := markFailedCheck(p)
	if m1 != filepath.Join(dir, failedCheckDir, "Show_E001"+failedCheckSuffix+".mp4") || !validFile(m1, 1) || validFile(p, 1) {
		t.Fatalf("first rename: %q", m1)
	}
	_ = os.WriteFile(p, []byte("y"), 0644)
	m2 := markFailedCheck(p)
	if m2 == m1 || !strings.HasSuffix(m2, "_2.mp4") {
		t.Fatalf("second rename must not overwrite the first: %q", m2)
	}
}

func TestCaptureAlternates(t *testing.T) {
	now := time.Now()
	seen := []browserMediaCandidate{
		{URL: "https://a.example/ad.mp4", Score: 125, SeenAt: now},
		{URL: "https://cdn.example/v/7/index.m3u8", Score: 150, EpisodeMatch: true, SeenAt: now},
		{URL: "https://cdn.example/v/8/index.m3u8", Score: 150, SeenAt: now},
		{URL: "https://cdn.example/clip.mp4", Score: 133, SeenAt: now},
	}
	isAd := func(u string) bool { return strings.Contains(u, "/ad.") }
	alts := captureAlternates(seen, "https://cdn.example/v/7/index.m3u8", isAd)
	if len(alts) != 2 || alts[0].URL != "https://cdn.example/v/8/index.m3u8" || alts[1].URL != "https://cdn.example/clip.mp4" {
		t.Fatalf("alternates: %+v", alts)
	}
}

func TestMediaCheckErrorIs(t *testing.T) {
	var err error = &mediaCheckError{File: `C:\x\a.mp4`, Reason: "只有聲音、沒有影像"}
	if !isMediaCheckFailure(err) || isMediaCheckFailure(errors.New("x")) {
		t.Fatal("isMediaCheckFailure")
	}
	if !strings.Contains(err.Error(), "a.mp4") {
		t.Fatal(err.Error())
	}
}
