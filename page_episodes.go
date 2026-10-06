//go:build windows

package main

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Some episode pages carry the whole series in their player data: one object
// per episode with its number and its stream address. When the page address
// names an episode, that episode's entry is the exact stream to download, with
// no need to open the capture browser.

var (
	pageStreamRE  = regexp.MustCompile(`"(?:play_url|playUrl|video_url|videoUrl|stream_url|streamUrl|hls_url|hlsUrl|url|src|file)"\s*:\s*"(https?:[^"]+?\.(?:m3u8|mp4|mpd)[^"]*)"`)
	pageEpisodeRE = regexp.MustCompile(`"(route_episode_number|episode_number|episodeNumber|episode_no|episodeNo|episode|ep|number)"\s*:\s*"?(\d{1,4})"?[,}]`)
	pageTitleRE   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

type pageEpisodeEntry struct {
	Episode int
	Stream  string
}

// pageEpisodeEntries lists the (episode, stream) pairs found in the page data.
// Each stream is paired with an episode number from its own object: the text
// between the stream and the nearest "{" before it.
func pageEpisodeEntries(body string) []pageEpisodeEntry {
	body = strings.ReplaceAll(body, `\/`, `/`)
	var out []pageEpisodeEntry
	for _, m := range pageStreamRE.FindAllStringSubmatchIndex(body, 2000) {
		start := strings.LastIndex(body[:m[0]], "{")
		if start < 0 {
			continue
		}
		obj := body[start:m[0]]
		ep, rank := 0, 99
		for _, e := range pageEpisodeRE.FindAllStringSubmatch(obj, -1) {
			// Prefer explicit episode keys over a plain "number".
			r := map[string]int{"route_episode_number": 0, "episode_number": 1, "episodeNumber": 1, "episode_no": 1, "episodeNo": 1, "episode": 2, "ep": 2, "number": 3}[e[1]]
			if n, err := strconv.Atoi(e[2]); err == nil && n > 0 && r < rank {
				ep, rank = n, r
			}
		}
		if ep > 0 {
			out = append(out, pageEpisodeEntry{Episode: ep, Stream: html.UnescapeString(body[m[2]:m[3]])})
		}
	}
	return out
}

// episodeStreamFromPage returns the stream listed for episode ep. It only
// trusts a real episode list (at least three different episode numbers), so
// a lone number elsewhere in the data is never mistaken for an episode.
func episodeStreamFromPage(body string, ep int) (string, bool) {
	stream, _, _ := lookupPageEpisode(body, ep)
	return stream, stream != ""
}

// lookupPageEpisode also reports whether the page has an episode list at all
// and the highest episode in it.
func lookupPageEpisode(body string, ep int) (stream string, listed bool, maxEp int) {
	entries := pageEpisodeEntries(body)
	distinct := map[int]bool{}
	for _, e := range entries {
		distinct[e.Episode] = true
		if e.Episode > maxEp {
			maxEp = e.Episode
		}
	}
	if ep <= 0 || len(distinct) < 3 {
		return "", false, 0
	}
	for _, e := range entries {
		if e.Episode == ep {
			return e.Stream, true, maxEp
		}
	}
	return "", true, maxEp
}

// errNoSuchEpisode: the page lists the series' episodes and this one is not
// among them, so no other method (browser capture included) can find it.
type errNoSuchEpisode struct{ ep, max int }

func (e *errNoSuchEpisode) Error() string {
	return fmt.Sprintf("網頁的集數清單中沒有第 %d 集（清單最多到第 %d 集）", e.ep, e.max)
}

func pageTitle(body string) string {
	m := pageTitleRE.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

// downloadPageEpisode downloads the stream the page data lists for the page's
// episode, named after the page title (which includes the episode number).
func (a *app) downloadPageEpisode(ctx context.Context, rawURL, finalURL, body string, mode int, formatID, output string, cfg settings) (bool, error) {
	ep := pageEpisodeNumber(rawURL)
	stream, listed, maxEp := lookupPageEpisode(body, ep)
	if stream == "" {
		if listed {
			return true, &errNoSuchEpisode{ep: ep, max: maxEp}
		}
		return false, nil
	}
	a.postLog(fmt.Sprintf("→ 頁面播放資料列出各集串流，直接取得第 %d 集（%s）。\r\n", ep, hostOnly(stream)))
	streamMode := mode
	if mode == 2 {
		streamMode = 0
	}
	args, err := a.buildArgs(stream, streamMode, formatID, output, cfg)
	if err != nil {
		return true, err
	}
	args = replaceOutputTemplate(args, capturedOutputBase(rawURL, pageTitle(body))+".%(ext)s")
	extras := []string{"--no-playlist", "--referer", finalURL, "--impersonate", "chrome"}
	if base, err := url.Parse(finalURL); err == nil && base.Scheme != "" && base.Host != "" {
		extras = append(extras, "--add-header", "Origin:"+base.Scheme+"://"+base.Host)
	}
	args = insertBeforeLast(args, extras...)
	return true, a.runCommand(ctx, args)
}
