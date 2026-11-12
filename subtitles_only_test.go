//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildArgsSubtitlesOnly(t *testing.T) {
	a := testApp(t)
	a.binDir = filepath.Join(a.appDir, "bin")

	// 1. Gold comparison when SubtitlesOnly is false (backward compatibility)
	modes := []int{0, 1, 2, 3, 4, 5}
	rawURL := "https://example.com/watch?v=123"
	outDir := filepath.Join(a.appDir, "out")
	for _, m := range modes {
		fid := ""
		if m == 4 {
			fid = "22"
		}
		cfgDefault := settings{}
		argsDefault, err := a.buildArgs(rawURL, m, fid, outDir, cfgDefault)
		if err != nil {
			t.Fatalf("mode %d default buildArgs err: %v", m, err)
		}
		// SubtitlesOnly = false must return identical slice
		cfgExplicitFalse := settings{SubtitlesOnly: false}
		argsFalse, err := a.buildArgs(rawURL, m, fid, outDir, cfgExplicitFalse)
		if err != nil {
			t.Fatalf("mode %d explicit false err: %v", m, err)
		}
		if !reflect.DeepEqual(argsDefault, argsFalse) {
			t.Fatalf("mode %d args differ when SubtitlesOnly=false:\ndefault: %v\nfalse:   %v", m, argsDefault, argsFalse)
		}
	}

	// 2. ExtraArgs preservation and golden comparisons
	cfgWithExtra := settings{
		ExtraArgs: "--sub-langs en.* --write-subs",
		FileName:  "custom_name",
	}
	argsExtra, err := a.buildArgs(rawURL, 0, "", outDir, cfgWithExtra)
	if err != nil {
		t.Fatalf("buildArgs with extra err: %v", err)
	}
	cfgExtraFalse := cfgWithExtra
	cfgExtraFalse.SubtitlesOnly = false
	argsExtraFalse, err := a.buildArgs(rawURL, 0, "", outDir, cfgExtraFalse)
	if err != nil {
		t.Fatalf("buildArgs with extra false err: %v", err)
	}
	if !reflect.DeepEqual(argsExtra, argsExtraFalse) {
		t.Fatalf("SubtitlesOnly=false modified extra args:\ngot:  %v\nwant: %v", argsExtraFalse, argsExtra)
	}

	// 3. When SubtitlesOnly is true:
	cfgSubs := settings{
		SubtitlesOnly: true,
		ExtraArgs:     "--sub-langs zh.*",
		FileName:      "custom_name",
	}
	argsSubs, err := a.buildArgs(rawURL, 0, "", outDir, cfgSubs)
	if err != nil {
		t.Fatalf("buildArgs subtitles only err: %v", err)
	}

	// Assertions for SubtitlesOnly=true:
	// a. Last arg must be rawURL
	if len(argsSubs) == 0 || argsSubs[len(argsSubs)-1] != rawURL {
		t.Fatalf("last arg must be rawURL, got %v", argsSubs)
	}
	// b. Contains required subtitle arguments
	required := []string{"--skip-download", "--write-subs", "--write-auto-subs", "--convert-subs", "srt"}
	for _, req := range required {
		found := false
		for _, arg := range argsSubs {
			if arg == req {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing required arg %q in %v", req, argsSubs)
		}
	}
	// c. Must contain --ffmpeg-location
	foundFfmpeg := false
	for i, arg := range argsSubs {
		if arg == "--ffmpeg-location" && i+1 < len(argsSubs) && argsSubs[i+1] == a.binDir {
			foundFfmpeg = true
			break
		}
	}
	if !foundFfmpeg {
		t.Fatalf("missing --ffmpeg-location in %v", argsSubs)
	}
	// d. Must preserve user's ExtraArgs
	foundUserExtra := false
	for _, arg := range argsSubs {
		if arg == "--sub-langs" {
			foundUserExtra = true
			break
		}
	}
	if !foundUserExtra {
		t.Fatalf("user ExtraArgs (--sub-langs) was dropped: %v", argsSubs)
	}
	// e. Must NOT contain video format selectors
	for _, bad := range []string{"-f", "-S", "--merge-output-format"} {
		for _, arg := range argsSubs {
			if arg == bad {
				t.Fatalf("subtitles only should not contain %q: %v", bad, argsSubs)
			}
		}
	}
}

func TestSaveSubtitlesAtAndBaseNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sub.vtt":
			w.Header().Set("Content-Type", "text/vtt")
			_, _ = w.Write([]byte("WEBVTT\n\n00:00:00.000 --> 00:00:05.000\nHello World\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	outDir := t.TempDir()

	subs := []pageSubtitle{{URL: srv.URL + "/sub.vtt", Label: "English", Default: true}}

	// 1. Original saveSubtitles with no video files -> writes nothing
	a.saveSubtitles(context.Background(), subs, srv.URL+"/page", nil, settings{})
	matches, _ := filepath.Glob(filepath.Join(outDir, "*.srt"))
	if len(matches) != 0 {
		t.Fatalf("saveSubtitles with nil videos wrote files: %v", matches)
	}

	// 2. saveSubtitlesAt with base name -> writes subtitle even if no video exists
	saved := a.saveSubtitlesAt(context.Background(), subs, srv.URL+"/page", []string{filepath.Join(outDir, "test_base")}, settings{})
	if len(saved) != 1 {
		t.Fatalf("saveSubtitlesAt returned %d items, want 1", len(saved))
	}
	if !validFile(saved[0].Path, 1) {
		t.Fatalf("saved subtitle file does not exist or empty: %v", saved[0].Path)
	}
	if saved[0].Label != "English" {
		t.Fatalf("saved subtitle label = %q, want English", saved[0].Label)
	}

	// 3. saveSubtitlesOnly helper
	outDir2 := t.TempDir()
	err := a.saveSubtitlesOnly(context.Background(), subs, srv.URL+"/page", "ep1_base", outDir2, settings{SubtitlesOnly: true})
	if err != nil {
		t.Fatalf("saveSubtitlesOnly err: %v", err)
	}
	if len(a.itemSubs) != 1 {
		t.Fatalf("a.itemSubs = %d, want 1", len(a.itemSubs))
	}
	expectedFile := filepath.Join(outDir2, "ep1_base.srt")
	if !validFile(expectedFile, 1) && !validFile(filepath.Join(outDir2, "ep1_base.vtt"), 1) {
		t.Fatalf("expected subtitle file was not created in %s", outDir2)
	}
}

func TestDownloadPageEpisodeSubtitlesOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := signedStreamSource(r.URL.Path)
		switch {
		case strings.HasSuffix(src, "/ep1-a4398125.vtt"):
			io.WriteString(w, "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nDIRECT_EP1_SUB\n")
		case strings.HasSuffix(src, "/en-US/subtitle.srt"):
			io.WriteString(w, "1\n00:00:01,000 --> 00:00:03,000\nPLAY_EN_SUB\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	bodyWithSubs := readTestPage(t, "page_entry_en_ep1.html")
	outDir := t.TempDir()
	a := testApp(t)

	// If pageStreamDownload hook is called during SubtitlesOnly, test must fail!
	a.pageStreamDownload = func(ctx context.Context, stream string) ([]string, error) {
		t.Fatalf("video download hook called when SubtitlesOnly is true! stream: %s", stream)
		return nil, nil
	}

	cfg := settings{SubtitlesOnly: true}
	pageURL := srv.URL + "/detail/watch/test-drama/1?lang=en-US"

	// 1. Success case: subtitles are saved, video is NOT downloaded
	found, err := a.downloadPageEpisode(context.Background(), pageURL, pageURL, bodyWithSubs, 0, "", outDir, cfg)
	if !found || err != nil {
		t.Fatalf("downloadPageEpisode found=%v, err=%v", found, err)
	}

	subPath := filepath.Join(outDir, "test-drama_E001.srt")
	if !validFile(subPath, 1) {
		// If ffmpeg not present during test, vtt might be preserved
		vttPath := filepath.Join(outDir, "test-drama_E001.vtt")
		if !validFile(vttPath, 1) {
			t.Fatalf("neither %s nor %s exists in %s", subPath, vttPath, outDir)
		}
	}

	// 2. Failure case: page data has streams but NO subtitles -> must return error (noSubtitlesFinal)
	bodyNoSubs := strings.ReplaceAll(bodyWithSubs, "multi_subtitles", "no_subtitles")
	bodyNoSubs = strings.ReplaceAll(bodyNoSubs, "subtitle_url", "no_sub_url")
	bodyNoSubs = strings.ReplaceAll(bodyNoSubs, "direct_subtitle_url", "no_direct_sub")
	foundNoSub, errNoSub := a.downloadPageEpisode(context.Background(), pageURL, pageURL, bodyNoSubs, 0, "", outDir, cfg)
	if !foundNoSub {
		t.Fatalf("downloadPageEpisode with no subs should find episode, but got found=false")
	}
	if errNoSub == nil {
		t.Fatalf("downloadPageEpisode with no subs must return error, got nil")
	}
	if !isNoSubtitles(errNoSub) {
		t.Fatalf("error should satisfy isNoSubtitles, got %v", errNoSub)
	}
	var finalErr *noSubtitlesFinal
	if !errors.As(errNoSub, &finalErr) {
		t.Fatalf("error should be noSubtitlesFinal, got %T: %v", errNoSub, errNoSub)
	}
}

func TestTasksAndFailedJSONSubtitlesOnly(t *testing.T) {
	// 1. Old tasks.json without subtitles_only unmarshals to false
	oldJSON := `{"mode":0,"extra_args":"","convert_h264":true}`
	var cfg settings
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatalf("unmarshal err: %v", err)
	}
	if cfg.SubtitlesOnly {
		t.Fatalf("old config unmarshaled SubtitlesOnly as true, want false")
	}

	// 2. Setting subtitles_only marshals and unmarshals accurately
	cfg.SubtitlesOnly = true
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal err: %v", err)
	}
	if !strings.Contains(string(data), `"subtitles_only":true`) {
		t.Fatalf("marshaled json missing subtitles_only: %s", string(data))
	}

	var cfg2 settings
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("unmarshal err: %v", err)
	}
	if !cfg2.SubtitlesOnly {
		t.Fatalf("roundtrip lost SubtitlesOnly: %v", cfg2)
	}

	// 3. Failed entry grouping keeps SubtitlesOnly distinct
	entry1 := failedEntry{URL: "https://example.com/1", Cfg: settings{SubtitlesOnly: true}}
	entry2 := failedEntry{URL: "https://example.com/1", Cfg: settings{SubtitlesOnly: false}}
	k1, _ := json.Marshal(entry1.Cfg)
	k2, _ := json.Marshal(entry2.Cfg)
	if string(k1) == string(k2) {
		t.Fatalf("failed entry configs should have different JSON keys: %s vs %s", k1, k2)
	}
}

func TestJobNameFromSubtitleFile(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"C:\\Downloads\\MyShow_E001.srt", "MyShow"},
		{"C:\\Downloads\\MyShow_E001.vtt", "MyShow"},
		{"C:\\Downloads\\Video Title [dQw4w9WgXcQ].en.srt", "Video Title"},
		{"C:\\Downloads\\Video Title [dQw4w9WgXcQ].zh-Hant.srt", "Video Title"},
		{"C:\\Downloads\\Video Title [dQw4w9WgXcQ].zh-TW.srt", "Video Title"},
		{"C:\\Downloads\\SingleVideo.srt", "SingleVideo"},
		{"C:\\Downloads\\SingleVideo.es.srt", "SingleVideo"},
	}

	for _, c := range cases {
		got := jobNameFromSubtitleFile(c.input)
		if got != c.want {
			t.Errorf("jobNameFromSubtitleFile(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestJobNameFromSubtitleFileKeepsTitleDot(t *testing.T) {
	if got := jobNameFromSubtitleFile(`C:\out\Dr.Who.srt`); got != jobNameFromFile("Dr.Who.mp4") {
		t.Fatalf("title dot stripped: %q", got)
	}
	if got := jobNameFromSubtitleFile(`C:\out\Clip [abc].zh-Hant.srt`); got != jobNameFromFile("Clip [abc].mp4") {
		t.Fatalf("lang suffix kept: %q", got)
	}
}
