//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPageEpisodeSubtitlesByEpisode(t *testing.T) {
	body := `<script>var player = { episodes: [{"episode":"1","src":"https://cdn.example.com/m/9/001_aa.mp4"}],
 subtitles: {"1":[{"label":"字幕一","lang":"","src":"https://cdn.example.com/m/9/001_aa.vtt","isDefault":true}],` +
		`"2":[{"label":"字幕一","lang":"","src":"https://cdn.example.com/m/9/002_bb.vtt","isDefault":true},` +
		`{"label":"English {en}","lang":"en","src":"/m/9/002_bb.en.vtt"}]}, other: 1 };</script>
<track kind="subtitles" src="https://cdn.example.com/wrong.vtt">`
	got := pageEpisodeSubtitles(body, "https://www.example.com/play/9/2/", 2)
	want := []pageSubtitle{
		{URL: "https://cdn.example.com/m/9/002_bb.vtt", Label: "字幕一", Default: true},
		{URL: "https://www.example.com/m/9/002_bb.en.vtt", Label: "English {en}", Lang: "en"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("episode 2 = %#v, want %#v", got, want)
	}
	if got := pageEpisodeSubtitles(body, "https://www.example.com/play/9/1/", 1); len(got) != 1 || got[0].URL != "https://cdn.example.com/m/9/001_aa.vtt" {
		t.Fatalf("episode 1 = %#v", got)
	}
	// An episode the subtitle list does not name falls back to the page's
	// <track>, which belongs to the video the page plays.
	if got := pageEpisodeSubtitles(body, "https://www.example.com/play/9/3/", 3); len(got) != 1 || got[0].URL != "https://cdn.example.com/wrong.vtt" {
		t.Fatalf("episode 3 = %#v", got)
	}
}

func TestPageEpisodeSubtitlesTrack(t *testing.T) {
	body := `<video><source src="v.mp4"><track kind="chapters" src="ch.vtt">` +
		`<track kind="captions" srclang="zh" label="中文" src="/subs/zh.vtt" default>` +
		`<track src='/subs/en.srt' srclang=en></video>`
	got := pageEpisodeSubtitles(body, "https://example.com/watch/abc", 0)
	want := []pageSubtitle{
		{URL: "https://example.com/subs/zh.vtt", Label: "中文", Lang: "zh", Default: true},
		{URL: "https://example.com/subs/en.srt", Lang: "en"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tracks = %#v, want %#v", got, want)
	}
	if got := pageEpisodeSubtitles(`<video src="v.mp4"></video>`, "https://example.com/", 1); len(got) != 0 {
		t.Fatalf("no subtitles = %#v", got)
	}
}

func TestSubtitleNames(t *testing.T) {
	subs := []pageSubtitle{{Lang: "en"}, {Label: "中文", Default: true}, {Lang: "en"}, {}}
	got := subtitleNames("", subs)
	want := []string{".en", "", ".3", ".4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
	// The fallback number already taken by a language, and a tag that only
	// differs in case (one file on Windows).
	got = subtitleNames("v", []pageSubtitle{{Default: true}, {Lang: "4"}, {Lang: "en"}, {Lang: "en"}, {Lang: "EN"}})
	want = []string{"v", "v.4", "v.en", "v.5", "v.6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func TestSubtitleExt(t *testing.T) {
	cases := []struct{ url, text, want string }{
		{"https://x.example.com/a.vtt?sig=1", "", ".vtt"},
		{"https://x.example.com/sub?id=1", "WEBVTT\n\n00:00.000 --> 00:01.000\nhi", ".vtt"},
		{"https://x.example.com/sub?id=1", "[Script Info]\nTitle: x", ".ass"},
		{"https://x.example.com/sub?id=1", "1\n00:00:00,000 --> 00:00:01,000\nhi", ".srt"},
	}
	for _, c := range cases {
		if got := subtitleExt(c.url, c.text); got != c.want {
			t.Errorf("subtitleExt(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestBalancedObjectSkipsStrings(t *testing.T) {
	body := `x = {"a":"}{","b":{"c":'}'}} tail`
	if got := balancedObject(body, 4); got != `{"a":"}{","b":{"c":'}'}}` {
		t.Fatalf("balancedObject = %q", got)
	}
	if got := balancedObject(`{"a":1`, 0); got != "" {
		t.Fatalf("unterminated = %q", got)
	}
}

func TestIsThumbnailTrack(t *testing.T) {
	thumbs := "WEBVTT\n\n1\n00:00:00.000 --> 00:01:01.780\nhttps://img.example.com/s0000.jpg#xywh=0,0,200,112\n\n2\n00:01:01.780 --> 00:02:03.560\nhttps://img.example.com/s0000.jpg#xywh=200,0,200,112\n"
	if !isThumbnailTrack(thumbs) {
		t.Fatal("sprite thumbnail track not detected")
	}
	if !isThumbnailTrack("WEBVTT\n\n00:00.000 --> 00:05.000\nthumbs/001.png\n") {
		t.Fatal("image-file thumbnail track not detected")
	}
	for _, sub := range []string{
		"WEBVTT\n\n00:00.000 --> 00:01.000\nhi\n",
		"1\n00:00:00,000 --> 00:00:01,000\n看 https://x.example.com/a.jpg 這張\n\n2\n00:00:01,000 --> 00:00:02,000\nok\n",
		"WEBVTT\n\n00:00.000 --> 00:01.000\n42\n\n00:01.000 --> 00:02.000\nphoto.jpg is here\n",
	} {
		if isThumbnailTrack(sub) {
			t.Errorf("subtitle taken for thumbnails: %q", sub)
		}
	}
}

func TestSubtitleFromJSONSkipsThumbnailKind(t *testing.T) {
	if _, ok := subtitleFromJSON(map[string]any{"file": "https://x.example.com/t.vtt", "kind": "thumbnails"}, ""); ok {
		t.Fatal("thumbnails track accepted")
	}
	if _, ok := subtitleFromJSON(map[string]any{"file": "https://x.example.com/c.vtt", "kind": "captions", "label": "English"}, ""); !ok {
		t.Fatal("captions track rejected")
	}
}

func TestPageEntrySubtitles(t *testing.T) {
	body := `<script>var data = {"episodes":[` +
		`{"id":11,"route_episode_number":1,"number":1,"play_url":"https:\/\/cdn.example.com\/1.m3u8","multi_subtitles":[` +
		`{"language_code":"en-US","label":"English","subtitle_url":"\/e\/s\/ep1-en","is_default":true},` +
		`{"language_code":"zh-TW","label":"繁體中文","subtitle_url":"\/e\/s\/ep1-zh","is_default":false}],` +
		`"selected_subtitle_language":"zh-TW","multi_resolutions":[]},` +
		`{"id":12,"route_episode_number":2,"number":2,"play_url":"https:\/\/cdn.example.com\/2.m3u8","multi_subtitles":[` +
		`{"language_code":"en-US","label":"English","subtitle_url":"\/e\/s\/ep2-en","is_default":true},` +
		`{"language_code":"zh-TW","label":"繁體中文","subtitle_url":"\/e\/s\/ep2-zh","is_default":false}],` +
		`"selected_subtitle_language":"zh-TW"}]};</script>`
	got := pageEpisodeSubtitles(body, "https://www.example.com/watch/show/2?lang=zh-TW", 2)
	want := []pageSubtitle{{URL: "https://www.example.com/e/s/ep2-zh", Label: "繁體中文", Lang: "zh-TW", Default: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("episode 2 = %#v, want %#v", got, want)
	}
	// No selected language in the data: the address's lang, then the default.
	noSel := strings.ReplaceAll(body, `"selected_subtitle_language":"zh-TW"`, `"selected_subtitle_language":""`)
	if got := pageEpisodeSubtitles(noSel, "https://www.example.com/watch/show/1?lang=zh-TW", 1); len(got) != 1 || got[0].URL != "https://www.example.com/e/s/ep1-zh" {
		t.Fatalf("address lang = %#v", got)
	}
	if got := pageEpisodeSubtitles(noSel, "https://www.example.com/watch/show/1", 1); len(got) != 1 || got[0].URL != "https://www.example.com/e/s/ep1-en" {
		t.Fatalf("default = %#v", got)
	}
	if got := pageEpisodeSubtitles(body, "https://www.example.com/watch/show/3", 3); len(got) != 0 {
		t.Fatalf("unlisted episode = %#v", got)
	}
}

func readTestPage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func urlPathOnly(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Path
}

func TestPageEntryPlayableAndDirectSubtitles(t *testing.T) {
	t.Run("zh_ep1_prefers_play_url_and_selected_es", func(t *testing.T) {
		body := readTestPage(t, "page_entry_zh_ep1.html")
		streams, listed, maxEp := pageEpisodeStreams(body, 1)
		if !listed || maxEp != 10 || len(streams) != 2 {
			t.Fatalf("streams=%v listed=%v maxEp=%d", streams, listed, maxEp)
		}
		if src := signedStreamSource(urlPathOnly(t, streams[0])); !strings.HasSuffix(src, "/manifest_1080.m3u8") {
			t.Fatalf("ep1 first stream = %q (src=%q), want play_url manifest_1080.m3u8", streams[0], src)
		}
		subs := pageEpisodeSubtitles(body, "https://site.example/detail/watch/test-drama/1?lang=zh-TW", 1)
		if len(subs) != 1 || subs[0].Lang != "es-ES" || !subs[0].Default {
			t.Fatalf("zh ep1 subs = %#v, want selected es-ES", subs)
		}
	})

	t.Run("en_ep1_prefers_play_url_and_selected_en", func(t *testing.T) {
		body := readTestPage(t, "page_entry_en_ep1.html")
		streams, listed, _ := pageEpisodeStreams(body, 1)
		if !listed || len(streams) != 2 {
			t.Fatalf("streams=%v listed=%v", streams, listed)
		}
		if src := signedStreamSource(urlPathOnly(t, streams[0])); !strings.HasSuffix(src, "/manifest_1080.m3u8") {
			t.Fatalf("ep1 first stream = %q (src=%q), want play_url manifest_1080.m3u8", streams[0], src)
		}
		subs := pageEpisodeSubtitles(body, "https://site.example/detail/watch/test-drama/1?lang=en-US", 1)
		if len(subs) != 1 || subs[0].Lang != "en-US" || !subs[0].Default {
			t.Fatalf("en ep1 subs = %#v, want selected en-US", subs)
		}
		if src := signedStreamSource(urlPathOnly(t, subs[0].URL)); !strings.HasSuffix(src, "/en-US/subtitle.srt") {
			t.Fatalf("en ep1 sub src = %q, want .../en-US/subtitle.srt", src)
		}
	})

	t.Run("ep2_uses_direct_play_url_and_direct_subtitle_url", func(t *testing.T) {
		for _, file := range []string{"page_entry_zh_ep2.html", "page_entry_en_ep2.html"} {
			body := readTestPage(t, file)
			streams, listed, _ := pageEpisodeStreams(body, 2)
			if !listed || len(streams) != 1 || !strings.HasSuffix(streams[0], "/ep2-18779eeb.mp4") {
				t.Fatalf("%s ep2 streams = %v", file, streams)
			}
			subs := pageEpisodeSubtitles(body, "https://site.example/detail/watch/test-drama/2?lang=zh-TW", 2)
			if len(subs) != 1 || !subs[0].Default || subs[0].Lang != "" || subs[0].Label != "" {
				t.Fatalf("%s ep2 subs = %#v, want unnamed default direct_subtitle_url", file, subs)
			}
			if !strings.HasPrefix(subs[0].URL, "https://site.example/e/s/") {
				t.Fatalf("%s ep2 sub URL not resolved against baseURL: %q", file, subs[0].URL)
			}
			if src := signedStreamSource(urlPathOnly(t, subs[0].URL)); !strings.HasSuffix(src, "/ep2-18779eeb.vtt") {
				t.Fatalf("%s ep2 sub src = %q, want suffix /ep2-18779eeb.vtt", file, src)
			}
		}
	})

	t.Run("same_src_ep1_keeps_direct_play_url_and_uses_selected_language", func(t *testing.T) {
		body := readTestPage(t, "page_entry_same_src_ep1.html")
		streams, listed, _ := pageEpisodeStreams(body, 1)
		if !listed || len(streams) != 2 || !strings.HasSuffix(streams[0], "/ep1-a4398125.mp4") {
			t.Fatalf("same_src ep1 streams = %v, want direct_play_url first", streams)
		}
		subs := pageEpisodeSubtitles(body, "https://site.example/detail/watch/test-drama/1?lang=en-US", 1)
		if len(subs) != 1 || !subs[0].Default {
			t.Fatalf("same_src ep1 subs = %#v", subs)
		}
		if src := signedStreamSource(urlPathOnly(t, subs[0].URL)); subs[0].Lang != "en-US" || !strings.HasSuffix(src, "/en-US/subtitle.srt") {
			t.Fatalf("same_src ep1 sub lang=%q src=%q, want selected en-US", subs[0].Lang, src)
		}
	})
}

// play_url 與 direct_play_url 是同一支檔案：direct_subtitle_url 只是網站預設語言，
// 字幕要照頁面選定的語言；字幕清單是空的才用 direct_subtitle_url。
func TestPageEntrySameFileUsesSelectedLanguage(t *testing.T) {
	const stream = "https://cdn.example.com/drama/en/1_.m3u8"
	const direct = "https://sub.example.org/drama/pt/ep1.vtt"
	entry := func(selected, list string) string {
		return `<script>var d={"episodes":[{"route_episode_number":1,"number":1,"is_playable":true,` +
			`"play_url":"` + stream + `","direct_play_url":"` + stream + `","direct_subtitle_url":"` + direct + `",` +
			`"selected_subtitle_language":"` + selected + `","multi_subtitles":[` + list + `]}]};</script>`
	}
	const list = `{"language_code":"pt-PT","label":"português","subtitle_url":"` + direct + `","is_default":true},` +
		`{"language_code":"en-US","label":"English","subtitle_url":"https://sub.example.org/drama/en/ep1.vtt","is_default":false}`
	const page = "https://site.example/detail/watch/test-drama/1?lang=en-US"

	for _, tc := range []struct {
		name, body, stream, wantURL string
	}{
		{"selected_language", entry("en-US", list), stream, "https://sub.example.org/drama/en/ep1.vtt"},
		{"address_language", entry("", list), stream, "https://sub.example.org/drama/en/ep1.vtt"},
		{"no_stream_given", entry("en-US", list), "", "https://sub.example.org/drama/en/ep1.vtt"},
		{"empty_list_keeps_direct_subtitle", entry("en-US", ""), stream, direct},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subs := pageEpisodeSubtitlesFor(tc.body, page, tc.stream, 1)
			if len(subs) != 1 || !subs[0].Default || subs[0].URL != tc.wantURL {
				t.Fatalf("subs = %#v, want default %s", subs, tc.wantURL)
			}
		})
	}
}

func TestDownloadPageEpisodePlayFallbackAndLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := signedStreamSource(r.URL.Path)
		switch {
		case strings.HasSuffix(src, "/ep1-a4398125.vtt"):
			io.WriteString(w, "1\n00:00:01,000 --> 00:00:03,000\nDIRECT_EP1_SUB\n")
		case strings.HasSuffix(src, "/ep2-18779eeb.vtt"):
			io.WriteString(w, "1\n00:00:01,000 --> 00:00:03,000\nDIRECT_EP2_SUB\n")
		case strings.HasSuffix(src, "/en-US/subtitle.srt"):
			io.WriteString(w, "1\n00:00:01,000 --> 00:00:03,000\nPLAY_EN_SUB\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Run("play_url_failure_falls_back_to_direct_play_url_with_paired_subtitle", func(t *testing.T) {
		body := readTestPage(t, "page_entry_en_ep1.html")
		outDir := t.TempDir()
		videoPath := filepath.Join(outDir, "test-drama_E001.mp4")
		a := testApp(t)
		var tried []string
		a.pageStreamDownload = func(ctx context.Context, stream string) ([]string, error) {
			tried = append(tried, stream)
			if len(tried) == 1 {
				return nil, errors.New("HTTP Error 403: Forbidden")
			}
			if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
				return nil, err
			}
			return []string{videoPath}, nil
		}
		pageURL := srv.URL + "/detail/watch/test-drama/1?lang=en-US"
		found, err := a.downloadPageEpisode(context.Background(), pageURL, pageURL, body, 0, "", outDir, settings{})
		if !found || err != nil {
			t.Fatalf("found=%v err=%v", found, err)
		}
		if len(tried) != 2 || !strings.Contains(tried[0], "stream.example.com/e/m/") || !strings.HasSuffix(tried[1], "/ep1-a4398125.mp4") {
			t.Fatalf("tried = %v, want [play_url, direct_play_url]", tried)
		}
		logs := strings.Join(a.sessionLog, "")
		if !strings.Contains(logs, "→ 本集可直接播放，改下載網頁播放器使用的串流（stream.example.com）。") {
			t.Fatalf("missing player stream log in:\n%s", logs)
		}
		if !strings.Contains(logs, "ℹ 頁面播放器的串流無法下載（HTTP Error 403: Forbidden），改用頁面備用串流（cdn.example.net）。") {
			t.Fatalf("missing fallback info log in:\n%s", logs)
		}
		subBytes, err := os.ReadFile(filepath.Join(outDir, "test-drama_E001.srt"))
		if err != nil {
			subBytes, err = os.ReadFile(filepath.Join(outDir, "test-drama_E001.vtt"))
		}
		if err != nil {
			t.Fatalf("paired subtitle not saved: %v\nlogs:\n%s", err, logs)
		}
		if !strings.Contains(string(subBytes), "DIRECT_EP1_SUB") {
			t.Fatalf("saved subtitle = %q, want DIRECT_EP1_SUB paired with direct_play_url", string(subBytes))
		}
	})

	t.Run("non_B_episode_keeps_identical_log_and_error_types", func(t *testing.T) {
		body := readTestPage(t, "page_entry_zh_ep2.html")
		outDir := t.TempDir()
		videoPath := filepath.Join(outDir, "test-drama_E002.mp4")
		a := testApp(t)
		a.pageStreamDownload = func(ctx context.Context, stream string) ([]string, error) {
			if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
				return nil, err
			}
			return []string{videoPath}, nil
		}
		pageURL := srv.URL + "/detail/watch/test-drama/2?lang=zh-TW"
		found, err := a.downloadPageEpisode(context.Background(), pageURL, pageURL, body, 0, "", outDir, settings{})
		if !found || err != nil {
			t.Fatalf("found=%v err=%v", found, err)
		}
		logs := strings.Join(a.sessionLog, "")
		wantLine := "→ 頁面播放資料列出各集串流，直接取得第 2 集（cdn.example.net）。\r\n"
		if !strings.HasPrefix(logs, wantLine) {
			t.Fatalf("non-B log = %q, want prefix %q", logs, wantLine)
		}

		// Verify errNoSuchEpisode, errEpisodeWithheld, and errEpisodeDeferred remain unchanged.
		_, errNoEp := a.downloadPageEpisode(context.Background(), srv.URL+"/detail/watch/test-drama/99", srv.URL, body, 0, "", outDir, settings{})
		var noEp *errNoSuchEpisode
		if !errors.As(errNoEp, &noEp) || noEp.ep != 99 || noEp.max != 10 {
			t.Fatalf("errNoSuchEpisode = %#v", errNoEp)
		}

		withheldBody := `{"episodes":[{"number":1,"play_url":"","is_playable":false},{"number":2,"play_url":"https://cdn.example.net/2.mp4"},{"number":3,"play_url":"https://cdn.example.net/3.mp4"}]}`
		a2 := testApp(t)
		_, errWithheld := a2.downloadPageEpisode(context.Background(), srv.URL+"/watch/show/1", srv.URL, withheldBody, 0, "", outDir, settings{})
		var wh *errEpisodeWithheld
		if !errors.As(errWithheld, &wh) || wh.ep != 1 {
			t.Fatalf("errEpisodeWithheld = %#v", errWithheld)
		}
		a3 := testApp(t)
		a3.deferWithheld = true
		_, errDef := a3.downloadPageEpisode(context.Background(), srv.URL+"/watch/show/1", srv.URL, withheldBody, 0, "", outDir, settings{})
		var def *errEpisodeDeferred
		if !errors.As(errDef, &def) || def.ep != 1 {
			t.Fatalf("errEpisodeDeferred = %#v", errDef)
		}
	})
}

func TestCandidateURLSubtitleUnchangedWithoutEpisodeEntry(t *testing.T) {
	// Track-based page and by-episode subtitles map: passing any candidateURL to
	// pageEpisodeSubtitlesFor must behave identically to pageEpisodeSubtitles.
	trackPage := `<video><source src="v.mp4"><track kind="subtitles" srclang="zh" label="中文" src="/subs/zh.vtt" default></video>`
	gotTrack := pageEpisodeSubtitlesFor(trackPage, "https://example.com/watch/1", "https://cdn.example.com/v.mp4", 1)
	wantTrack := pageEpisodeSubtitles(trackPage, "https://example.com/watch/1", 1)
	if !reflect.DeepEqual(gotTrack, wantTrack) {
		t.Fatalf("track page: got %#v, want %#v", gotTrack, wantTrack)
	}

	byEpPage := `<script>var p = { subtitles: {"2":[{"label":"中文","lang":"zh","src":"/s/2.vtt","isDefault":true}]} };</script>`
	gotByEp := pageEpisodeSubtitlesFor(byEpPage, "https://example.com/play/9/2", "https://cdn.example.com/2.mp4", 2)
	wantByEp := pageEpisodeSubtitles(byEpPage, "https://example.com/play/9/2", 2)
	if !reflect.DeepEqual(gotByEp, wantByEp) {
		t.Fatalf("by-episode map page: got %#v, want %#v", gotByEp, wantByEp)
	}

	// Episode object without direct_play_url: candidateURL must not alter multi_subtitles selection.
	entryNoDirect := `<script>var d={"episodes":[{"number":1,"play_url":"https:\/\/cdn.example.com\/1.m3u8","multi_subtitles":[{"language_code":"zh-TW","label":"繁體中文","subtitle_url":"\/e\/s\/1-zh","is_default":true}]}]};</script>`
	gotNoDirect := pageEpisodeSubtitlesFor(entryNoDirect, "https://example.com/watch/show/1", "https://other.example.com/cand.m3u8", 1)
	wantNoDirect := pageEpisodeSubtitles(entryNoDirect, "https://example.com/watch/show/1", 1)
	if !reflect.DeepEqual(gotNoDirect, wantNoDirect) {
		t.Fatalf("entry without direct_play_url: got %#v, want %#v", gotNoDirect, wantNoDirect)
	}
}
