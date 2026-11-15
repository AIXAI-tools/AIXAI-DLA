//go:build windows

package main

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// browserCaptureEvent describes one media request seen by the DevTools
// listener of the dedicated capture browser. Request headers are kept in memory only and are never written to the log.
type browserCaptureEvent struct {
	Kind            string            `json:"kind"`
	URL             string            `json:"url"`
	PageURL         string            `json:"pageUrl"`
	Title           string            `json:"title"`
	RequestType     string            `json:"requestType"`
	Initiator       string            `json:"initiator"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
	// Refused: the server refused this manifest to the browser.
	Refused bool `json:"refused"`
}

type browserMediaCandidate struct {
	URL     string
	PageURL string
	Title   string
	// PageTitle is the capture tab's title, used for the file name only when
	// nothing else names the video.
	PageTitle      string
	Kind           string
	RequestType    string
	RequestHeaders map[string]string
	ContentType    string
	ContentLength  int64
	Score          int
	SeenAt         time.Time
	// PageJSON holds the page's JSON responses seen during this capture (memory
	// only); PlayedURL is the stream that actually played when URL was
	// replaced by a larger rendition from that data.
	PageJSON  [][]byte
	PlayedURL string
	// EpisodeMatch: the stream's path carries the episode number of the page.
	EpisodeMatch bool
	// UsedByEpisode: another episode of the same batch already saved this
	// stream (episode_capture.go), so it is chosen last.
	UsedByEpisode int
	// Alternates are the other streams seen during the same capture, best
	// first; they are tried when the chosen one fails the media check.
	Alternates []browserMediaCandidate
	// Subtitles are the subtitle files the page offered for this video
	// (page_subtitles.go); they are saved next to the downloaded file.
	Subtitles []pageSubtitle
}

// maxCaptureAlternates limits how many other captured streams are tried.
const maxCaptureAlternates = 3

// captureAlternates lists the seen candidates other than chosen, best first.
func captureAlternates(seen []browserMediaCandidate, chosen string, isAd func(string) bool) []browserMediaCandidate {
	var out []browserMediaCandidate
	for _, c := range seen {
		if candidateKey(c.URL) == candidateKey(chosen) || (isAd != nil && isAd(c.URL)) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return betterCaptureCandidate(out[i], out[j]) })
	return out
}

// pageEpisodeNumber is the episode a page address asks for: a trailing number
// (/title/7), episode-7, or ?ep=7. 0 when the address has none.
func pageEpisodeNumber(raw string) int {
	if ep := episodeFromQuery(raw); ep > 0 {
		return ep
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return 0
	}
	last := strings.ToLower(parts[len(parts)-1])
	if m := captureEpisodeRE.FindStringSubmatch(last); len(m) > 1 {
		last = m[1]
	}
	if allDigits(last) && len(last) <= 4 {
		n, _ := strconv.Atoi(last)
		return n
	}
	return 0
}

// streamHasNumberSegment reports whether a stream path has a plain number as
// a segment of its own (…/9/stream.m3u8), i.e. it may name an episode.
func streamHasNumberSegment(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, s := range strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' }) {
		if allDigits(s) && len(s) <= 4 {
			return true
		}
	}
	return false
}

// streamHasEpisode reports whether a stream path has the episode number as a
// segment of its own (…/7/stream.m3u8). Players often preload the next
// episodes, so on an episode page the matching stream is the one to keep.
func streamHasEpisode(raw string, ep int) bool {
	u, err := url.Parse(raw)
	if err != nil || ep <= 0 {
		return false
	}
	want := strconv.Itoa(ep)
	for _, s := range strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' }) {
		if strings.TrimLeft(s, "0") == want {
			return true
		}
	}
	return false
}

const (
	maxPageJSONBytes = 2 << 20
	maxPageJSONCount = 60
)

// upgradeToBestVariant swaps the played stream for the largest rendition the
// page itself lists next to it (respecting the 720p mode). Audio-only and
// format-listing modes keep the played stream.
func (a *app) upgradeToBestVariant(c browserMediaCandidate, mode int) browserMediaCandidate {
	if mode != 0 && mode != 2 && mode != 5 {
		return c
	}
	limit := 0
	if mode == 5 {
		limit = 720
	}
	best, played, ok := betterVariantFromPageJSON(c.PageJSON, c.URL, limit)
	if !ok {
		return c
	}
	a.postLog(fmt.Sprintf("✓ 頁面播放器提供更高畫質：改下載 %s（預設播放的是 %s）。\r\n", best.sizeText(), played.sizeText()))
	c.PlayedURL = c.URL
	c.URL = best.URL
	return c
}

var captureEpisodeRE = regexp.MustCompile(`(?i)(?:episode[-_/])(\d+)(?:/)?$`)

func captureHeader(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func captureContentLength(headers map[string]string) int64 {
	raw := captureHeader(headers, "content-length")
	if raw == "" {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

// isAdMediaURL recognises video-ad requests by the address alone (ad servers,
// VAST tags, pre-roll clips). Ads served from ordinary hosts are caught from
// the page's VAST responses and ad <video> elements instead (cdp_session.go).
func isAdMediaURL(raw string) bool {
	lower := strings.ToLower(raw)
	for _, needle := range []string{"doubleclick", "googleads", "googlesyndication", "imasdk", "/ads/", "vast", "preroll", "pre-roll", "adserver"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

var vastMediaFileRE = regexp.MustCompile(`(?is)<MediaFile\b[^>]*>\s*(?:<!\[CDATA\[)?\s*(.*?)\s*(?:\]\]>)?\s*</MediaFile>`)

// vastMediaFiles returns the ad video addresses listed in a VAST response
// (the standard format for video ads). Anything else returns nil.
func vastMediaFiles(body string) []string {
	if !strings.Contains(body, "<VAST") && !strings.Contains(body, "<vast") {
		return nil
	}
	var out []string
	for _, m := range vastMediaFileRE.FindAllStringSubmatch(body, 64) {
		if u := strings.TrimSpace(html.UnescapeString(m[1])); strings.HasPrefix(u, "http") {
			out = append(out, u)
		}
	}
	return out
}

// adURLKey compares ad addresses without their query string, since tracking
// parameters often differ between the VAST entry and the actual request.
func adURLKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	return strings.ToLower(u.Host) + u.Path
}

func mediaCandidateScore(ev browserCaptureEvent) (browserMediaCandidate, bool) {
	raw := strings.TrimSpace(ev.URL)
	if raw == "" {
		return browserMediaCandidate{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return browserMediaCandidate{}, false
	}
	lower := strings.ToLower(raw)
	pathLower := strings.ToLower(u.Path)
	ct := strings.ToLower(captureHeader(ev.ResponseHeaders, "content-type"))
	respHeaders := ev.ResponseHeaders
	if ev.Refused {
		// The headers describe the server's error page, not the stream.
		ct, respHeaders = "", nil
	}
	// Never mistake a single media segment for the complete stream.
	segmentExts := []string{".ts", ".m4s", ".cmfv", ".cmfa", ".aac", ".vtt", ".srt"}
	for _, ext := range segmentExts {
		if strings.HasSuffix(pathLower, ext) {
			return browserMediaCandidate{}, false
		}
	}
	if isAdMediaURL(raw) {
		return browserMediaCandidate{}, false
	}
	// A web page is never the stream, even when its address reads like a
	// video file (file-sharing pages such as ".../name.mp4.html").
	if strings.Contains(ct, "html") {
		return browserMediaCandidate{}, false
	}
	// Some sign-in pages play a decorative static clip from a static-asset host;
	// it must never be mistaken for the requested video.
	if host := strings.ToLower(u.Hostname()); host == "ttwstatic.com" || strings.HasSuffix(host, ".ttwstatic.com") {
		return browserMediaCandidate{}, false
	}

	score := 0
	kind := "media"
	switch {
	case hasMediaExt(lower, ".m3u8") || strings.Contains(ct, "mpegurl") || strings.Contains(ct, "application/vnd.apple.mpegurl"):
		score = 150
		kind = "HLS"
	case hasMediaExt(lower, ".mpd") || strings.Contains(ct, "dash+xml"):
		score = 145
		kind = "DASH"
	case strings.Contains(ct, "video/mp4") || hasMediaExt(lower, ".mp4"):
		score = 125
		kind = "MP4"
	case strings.HasPrefix(ct, "video/") || strings.EqualFold(ev.RequestType, "media"):
		score = 100
		kind = "Video"
	case hasMediaExt(lower, ".webm") || strings.Contains(ct, "video/webm"):
		score = 105
		kind = "WebM"
	default:
		return browserMediaCandidate{}, false
	}
	if ev.Refused {
		// Only a manifest is kept, below a manifest the page could load but
		// still at least 125, the score the capture loop settles on.
		if kind != "HLS" && kind != "DASH" {
			return browserMediaCandidate{}, false
		}
		score -= 20
	}

	length := captureContentLength(respHeaders)
	if length >= 1<<20 {
		score += 8
	}
	if length >= 5<<20 {
		score += 6
	}
	if strings.EqualFold(ev.RequestType, "media") {
		score += 8
	}
	return browserMediaCandidate{
		URL:            raw,
		PageURL:        strings.TrimSpace(ev.PageURL),
		Title:          strings.TrimSpace(ev.Title),
		Kind:           kind,
		RequestType:    ev.RequestType,
		RequestHeaders: ev.RequestHeaders,
		ContentType:    ct,
		ContentLength:  length,
		Score:          score,
		SeenAt:         time.Now(),
	}, true
}

func betterCaptureCandidate(newC, oldC browserMediaCandidate) bool {
	if oldC.URL == "" {
		return true
	}
	if newC.EpisodeMatch != oldC.EpisodeMatch {
		return newC.EpisodeMatch
	}
	if (newC.UsedByEpisode == 0) != (oldC.UsedByEpisode == 0) {
		return newC.UsedByEpisode == 0
	}
	if newC.Score != oldC.Score {
		return newC.Score > oldC.Score
	}
	if newC.ContentLength != oldC.ContentLength {
		return newC.ContentLength > oldC.ContentLength
	}
	return newC.SeenAt.After(oldC.SeenAt)
}
