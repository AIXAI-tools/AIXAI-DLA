package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const dubbedMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subtitles",NAME="English",LANGUAGE="en-US",URI="text-srt-en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="English",LANGUAGE="en-US",DEFAULT=YES,AUTOSELECT=YES,URI="audio-aac-en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Español",LANGUAGE="es-ES",URI="audio-aac-es.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Português (Brasil)",LANGUAGE="pt-BR",URI="audio-aac-pt-br.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1321370,RESOLUTION=1080x1920,AUDIO="audio",CODECS="hvc1.1.4.L123.90,mp4a.40.2"
hevc_1080x1920.m3u8
`

func TestChooseAudioTrack(t *testing.T) {
	tracks := hlsTracks(dubbedMaster, "AUDIO")
	if len(tracks) != 3 || tracks[2].Name != "Português (Brasil)" || !tracks[0].Default {
		t.Fatalf("tracks = %+v", tracks)
	}
	for _, c := range []struct {
		page, want string
		byPage     bool
	}{
		{"https://example.com/watch/show/1?lang=en-US", "en-US", true},
		{"https://example.com/watch/show/1?lang=es", "es-ES", true},
		{"https://example.com/watch/show/1?lang=pt_br", "pt-BR", true},
		{"https://example.com/watch/show/1", "en-US", false},
		{"https://example.com/watch/show/1?lang=ja-JP", "en-US", false},
	} {
		got, byPage, ok := chooseAudioTrack(tracks, pageLanguage(c.page))
		if !ok || got.Lang != c.want || byPage != c.byPage {
			t.Errorf("%s: got %+v byPage=%v ok=%v, want %s byPage=%v", c.page, got, byPage, ok, c.want, c.byPage)
		}
	}
}

func TestChooseAudioTrackLeavesSingleLanguage(t *testing.T) {
	one := strings.Replace(strings.Replace(dubbedMaster, `"es-ES"`, `"en-US"`, 1), `"pt-BR"`, `"en-us"`, 1)
	if _, _, ok := chooseAudioTrack(hlsTracks(one, "AUDIO"), "es"); ok {
		t.Error("one language: the format choice should stay as it was")
	}
	noDefault := strings.Replace(dubbedMaster, "DEFAULT=YES,", "", 1)
	if _, _, ok := chooseAudioTrack(hlsTracks(noDefault, "AUDIO"), ""); ok {
		t.Error("no page language and no DEFAULT: the format choice should stay as it was")
	}
}

func TestPageLanguageRejectsOddValues(t *testing.T) {
	for _, raw := range []string{"https://e.com/1?lang=en]/b", "https://e.com/1?lang=", "https://e.com/1?lang=english-language-x"} {
		if got := pageLanguage(raw); got != "" && !langTagRE.MatchString(got) {
			t.Errorf("pageLanguage(%q) = %q", raw, got)
		}
	}
	if got := pageLanguage("https://e.com/1?lang=en]/b"); got != "" {
		t.Errorf("pageLanguage with ] = %q, want empty", got)
	}
}

func TestReplaceBestFormat(t *testing.T) {
	args := []string{"-S", h264SortFields, "-f", "bv*+ba/b", "--merge-output-format", "mp4", "URL"}
	got := replaceBestFormat(args, "bv*+ba[language=en-US]/bv*+ba/b")
	if got[3] != "bv*+ba[language=en-US]/bv*+ba/b" || args[3] != "bv*+ba/b" {
		t.Errorf("got %v (original %v)", got, args)
	}
}

func TestHLSSubtitlesPrimaryLanguage(t *testing.T) {
	master := strings.Replace(dubbedMaster, `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subtitles",NAME="English",LANGUAGE="en-US",URI="text-srt-en.m3u8"`+"\n", "", 1) + `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en-US",URI="text-en.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Español",LANGUAGE="es-ES",URI="text-es.m3u8"
`
	tracks := hlsTracks(master, "SUBTITLES")
	for lang, want := range map[string]string{"es": "es-ES", "en-US": "en-US", "": "en-US", "ja": "en-US"} {
		subs := hlsSubtitles(tracks, "https://cdn.example.com/v/master.m3u8?t=1", lang, nil)
		if len(subs) != 2 || subs[1].URL != "https://cdn.example.com/v/text-es.m3u8" || !subs[0].HLS {
			t.Fatalf("subs = %+v", subs)
		}
		names := subtitleNames("", subs)
		for i, s := range subs {
			if (names[i] == "") != (s.Lang == want) {
				t.Errorf("lang %q: %s named %q, want the video's own name only for %s", lang, s.Lang, names[i], want)
			}
		}
	}
}

func TestFetchHLSSubtitleJoinsSegments(t *testing.T) {
	segs := map[string]string{
		"/subs.m3u8": "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000,\ns0.vtt\n#EXTINF:6.000,\ns1.vtt\n#EXT-X-ENDLIST\n",
		"/s0.vtt":    "WEBVTT\r\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\r\n\r\n00:00:01.000 --> 00:00:03.000\r\nFirst line\r\n\r\n00:00:05.000 --> 00:00:07.000\r\nAcross the cut\r\n",
		"/s1.vtt":    "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n\n00:00:05.000 --> 00:00:07.000\nAcross the cut\n\ncue-9\n00:00:08.000 --> 00:00:09.000\nLast one\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://site.example/watch/1" {
			http.Error(w, "no referer", http.StatusForbidden)
			return
		}
		body, ok := segs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()
	got, err := fetchHLSSubtitle(context.Background(), srv.URL+"/subs.m3u8", map[string]string{"referer": "https://site.example/watch/1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "Across the cut") != 1 || !strings.Contains(got, "First line") || !strings.Contains(got, "cue-9\n00:00:08.000") || strings.Contains(got, "X-TIMESTAMP-MAP") || !strings.HasPrefix(got, "WEBVTT\n\n") {
		t.Errorf("joined subtitle:\n%s", got)
	}
}

func TestPageTracksIgnoreScriptText(t *testing.T) {
	body := `<html><body><video><track kind="subtitles" src="/real.vtt" srclang="en"></video>
<script>// <track src=blob> loads instantly; fall back to <track src> otherwise</script></body></html>`
	subs := pageEpisodeSubtitles(body, "https://site.example/watch/show/1", 1)
	if len(subs) != 1 || subs[0].URL != "https://site.example/real.vtt" {
		t.Errorf("subs = %+v", subs)
	}
}
