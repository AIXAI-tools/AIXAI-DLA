package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Some HLS streams carry one video with several dubbed audio tracks
// (EXT-X-MEDIA TYPE=AUDIO, one LANGUAGE each). yt-dlp's generic extractor
// ignores DEFAULT=YES and, the tracks being equal in quality, takes the last
// by name: a page watched in English was saved with Portuguese audio. The
// language in the page address (?lang=en-US) wins; otherwise the track the
// playlist marks DEFAULT, which is what the player plays.
//
// The same master playlist may list subtitle tracks (TYPE=SUBTITLES), each a
// playlist of WebVTT segments the player loads itself: they are kept for
// saveSubtitles (page_subtitles.go), the one in the chosen language under the
// video's own name.

// chromeUserAgent is the desktop Chrome user agent the page fetches send.
const chromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// hlsTrack is one EXT-X-MEDIA entry of a master playlist.
type hlsTrack struct {
	Name, Lang, URI string
	Default         bool
}

var (
	hlsMediaAttrRE = regexp.MustCompile(`([A-Z0-9-]+)=("[^"]*"|[^,]*)`)
	langTagRE      = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
)

// progressiveExts are single-file streams: no master playlist to read.
var progressiveExts = map[string]bool{".mp4": true, ".m4v": true, ".webm": true, ".flv": true, ".mov": true, ".mkv": true, ".ts": true, ".m4a": true, ".mp3": true}

// hlsTracks lists the tracks of type typ (AUDIO, SUBTITLES) in a master
// playlist that carry a usable language tag.
func hlsTracks(playlist, typ string) []hlsTrack {
	var out []hlsTrack
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs := map[string]string{}
		for _, m := range hlsMediaAttrRE.FindAllStringSubmatch(strings.TrimPrefix(line, "#EXT-X-MEDIA:"), -1) {
			attrs[m[1]] = strings.Trim(m[2], `"`)
		}
		if attrs["TYPE"] != typ || !langTagRE.MatchString(attrs["LANGUAGE"]) {
			continue
		}
		out = append(out, hlsTrack{Name: attrs["NAME"], Lang: attrs["LANGUAGE"], URI: attrs["URI"], Default: attrs["DEFAULT"] == "YES"})
	}
	return out
}

// pageLanguage returns the language asked for in the page address
// (?lang= / ?language=), or "".
func pageLanguage(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	for _, key := range []string{"lang", "language"} {
		if v := strings.ReplaceAll(strings.TrimSpace(u.Query().Get(key)), "_", "-"); langTagRE.MatchString(v) {
			return v
		}
	}
	return ""
}

// trackForLanguage returns the index of the track in want (the exact tag,
// else the same primary language), or -1.
func trackForLanguage(tracks []hlsTrack, want string) int {
	if want == "" {
		return -1
	}
	for i, t := range tracks {
		if strings.EqualFold(t.Lang, want) {
			return i
		}
	}
	primary := strings.ToLower(strings.SplitN(want, "-", 2)[0])
	for i, t := range tracks {
		if strings.ToLower(strings.SplitN(t.Lang, "-", 2)[0]) == primary {
			return i
		}
	}
	return -1
}

// chooseAudioTrack picks the track for the page language, else the DEFAULT
// one. ok is false when the tracks do not differ in language or nothing fits.
func chooseAudioTrack(tracks []hlsTrack, want string) (track hlsTrack, byPage bool, ok bool) {
	langs := map[string]bool{}
	for _, t := range tracks {
		langs[strings.ToLower(t.Lang)] = true
	}
	if len(langs) < 2 {
		return hlsTrack{}, false, false
	}
	if i := trackForLanguage(tracks, want); i >= 0 {
		return tracks[i], true, true
	}
	for _, t := range tracks {
		if t.Default {
			return t, false, true
		}
	}
	return hlsTrack{}, false, false
}

// hlsSubtitles turns the subtitle tracks of the master playlist at base into
// subtitles to save: the one in lang (else DEFAULT, else the first) takes the
// video's own name.
func hlsSubtitles(tracks []hlsTrack, base, lang string, headers map[string]string) []pageSubtitle {
	var out []pageSubtitle
	primary := trackForLanguage(tracks, lang)
	for i, t := range tracks {
		if t.URI == "" {
			continue
		}
		if primary < 0 && t.Default {
			primary = i
		}
		out = append(out, pageSubtitle{URL: resolveHLSURI(base, t.URI), Label: t.Name, Lang: t.Lang, HLS: true, Headers: headers})
	}
	for i := range out {
		out[i].Default = out[i].Lang == tracks[max(primary, 0)].Lang
	}
	return out
}

// replaceBestFormat swaps the "-f bv*+ba/b" that buildArgs adds for format.
func replaceBestFormat(args []string, format string) []string {
	out := append([]string(nil), args...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "-f" && out[i+1] == "bv*+ba/b" {
			out[i+1] = format
			return out
		}
	}
	return out
}

// applyHLSTracks reads the stream's master playlist (with the request headers
// the download will use). When it offers dubbed audio tracks, yt-dlp is made
// to take the one for pageURL's language or the DEFAULT one; its subtitle
// tracks are kept in a.hlsSubs. Any failure leaves args as they were.
func (a *app) applyHLSTracks(ctx context.Context, args []string, mode int, cfg settings, pageURL, streamURL string, headers map[string]string) []string {
	a.hlsSubs = nil
	if mode != 0 && mode != 2 && mode != 5 {
		return args
	}
	if u, err := url.Parse(streamURL); err != nil || progressiveExts[strings.ToLower(filepath.Ext(u.Path))] {
		return args
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, err := fetchForKey(readCtx, streamURL, headers, maxPlaylistBytes)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(string(body), string(rune(0xFEFF)))), "#EXTM3U") {
		return args
	}
	playlist, lang := string(body), pageLanguage(pageURL)
	audio := hlsTracks(playlist, "AUDIO")
	track, byPage, ok := chooseAudioTrack(audio, lang)
	if ok && lang == "" {
		lang = track.Lang
	}
	a.hlsSubs = hlsSubtitles(hlsTracks(playlist, "SUBTITLES"), streamURL, lang, headers)
	if !ok || userChoosesFormat(cfg.ExtraArgs) {
		return args
	}
	why := "串流標示的預設音軌"
	if byPage {
		why = "網址指定的語言"
	}
	a.postLog(fmt.Sprintf("ℹ 串流有 %d 種配音，依%s選用 %s（%s）。\r\n", len(audio), why, track.Name, track.Lang))
	return replaceBestFormat(args, "bv*+ba[language="+track.Lang+"]/bv*+ba/b")
}

// maxSubtitleSegments caps the segments read for one HLS subtitle track.
const maxSubtitleSegments = 2000

var vttTimingRE = regexp.MustCompile(`^\S+\s+-->\s+\S+`)

// fetchHLSSubtitle reads a subtitle playlist of WebVTT segments and joins
// their cues into one WebVTT file. A cue that runs across a segment boundary
// is repeated in both segments and kept once. (Cue times are used as they
// are: the X-TIMESTAMP-MAP of these segments maps them to the stream start.)
func fetchHLSSubtitle(ctx context.Context, raw string, headers map[string]string) (string, error) {
	body, err := fetchForKey(ctx, raw, headers, maxPlaylistBytes)
	if err != nil {
		return "", err
	}
	text := strings.TrimPrefix(string(body), string(rune(0xFEFF)))
	if strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") {
		return checkSubtitleText(text)
	}
	if !strings.HasPrefix(strings.TrimSpace(text), "#EXTM3U") {
		return "", errors.New("內容不是字幕清單")
	}
	var segments []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			segments = append(segments, resolveHLSURI(raw, line))
		}
	}
	if len(segments) == 0 || len(segments) > maxSubtitleSegments {
		return "", fmt.Errorf("字幕清單有 %d 段，無法處理", len(segments))
	}
	var cues []string
	seen := map[string]bool{}
	for _, seg := range segments {
		b, err := fetchForKey(ctx, seg, headers, 5<<20)
		if err != nil {
			return "", err
		}
		for _, block := range strings.Split(strings.ReplaceAll(strings.TrimPrefix(string(b), string(rune(0xFEFF))), "\r\n", "\n"), "\n\n") {
			block = strings.Trim(block, "\n")
			lines := strings.Split(block, "\n")
			// A cue: its timing line first, or after a cue identifier.
			if !(vttTimingRE.MatchString(lines[0]) || (len(lines) > 1 && vttTimingRE.MatchString(lines[1]))) {
				continue
			}
			if !seen[block] {
				seen[block] = true
				cues = append(cues, block)
			}
		}
	}
	return checkSubtitleText("WEBVTT\n\n" + strings.Join(cues, "\n\n") + "\n")
}

// checkSubtitleText applies fetchSubtitle's content checks.
func checkSubtitleText(text string) (string, error) {
	if !strings.Contains(text, "-->") {
		return "", errors.New("內容不是字幕檔")
	}
	if isThumbnailTrack(text) {
		return "", errThumbnailTrack
	}
	return text, nil
}
