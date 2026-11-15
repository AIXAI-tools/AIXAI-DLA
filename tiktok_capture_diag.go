//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// isTikTokDramaAPI reports whether a captured request is one of the TikTok web
// API calls whose JSON body carries the episode's playAddr once logged in.
func isTikTokDramaAPI(raw string) bool {
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "tiktok.com/") {
		return false
	}
	return strings.Contains(lower, "/api/drama/episode/item_list") || strings.Contains(lower, "/api/item/detail")
}

// tiktokAPIResult is what one TikTok API response held for the episode; the
// capture counts them so a failed episode says why (never the addresses).
type tiktokAPIResult int

const (
	tiktokAPIMatched   tiktokAPIResult = iota
	tiktokAPIUnparsed                  // no item list in the body
	tiktokAPINoEpisode                 // items, but not this episode
	tiktokAPINoStream                  // this episode, without a playable address
)

// tiktokCandidateFromAPIBodyResult picks the requested episode and returns the
// result and the episode numbers the body listed; maxShortSide is the 最高
// 720p limit (0 = the best stream).
func tiktokCandidateFromAPIBodyResult(body []byte, apiURL string, episode, maxShortSide int, reqHeaders map[string]string) (browserMediaCandidate, tiktokAPIResult, []int) {
	isDetail := strings.Contains(strings.ToLower(apiURL), "/api/item/detail")
	items := tiktokDramaItemsFromJSON(body)
	if len(items) == 0 {
		return browserMediaCandidate{}, tiktokAPIUnparsed, nil
	}
	var listed []int
	res := tiktokAPINoEpisode
	for _, item := range items {
		epNo := item.DramaInfo.DramaVideoData.EpisodeNumber
		listed = append(listed, epNo)
		if epNo != episode && !(isDetail && epNo == 0) {
			continue
		}
		streams := tiktokDramaItemStreams(item)
		if len(streams) == 0 {
			res = tiktokAPINoStream
			continue
		}
		title := strings.TrimSpace(item.DramaInfo.DramaName)
		st, _ := chooseTikTokDramaStream(streams, maxShortSide)
		kind := "TikTok API MP4"
		if st.Width > 0 && st.Height > 0 {
			// The chosen size and how many the episode offered, for the log.
			which := "最高"
			if maxShortSide > 0 {
				which = fmt.Sprintf("%dp 以內最高", maxShortSide)
			}
			kind = fmt.Sprintf("TikTok API MP4 %d×%d，%d 種畫質中%s", st.Width, st.Height, len(streams), which)
		}
		return browserMediaCandidate{
			URL:            st.URLs[0],
			Title:          title,
			Kind:           kind,
			RequestType:    "api",
			RequestHeaders: reqHeaders,
			ContentType:    "video/mp4",
			Score:          200,
			SeenAt:         time.Now(),
			Subtitles:      tiktokItemSubtitles(item),
		}, tiktokAPIMatched, listed
	}
	return browserMediaCandidate{}, res, listed
}

// tiktokItemSubtitles lists the episode's separate subtitle files. The words
// shown in a short drama's picture are part of the video itself (each
// language is its own video file); what TikTok lists here is usually only an
// automatic transcript, saved next to the video as an extra.
func tiktokItemSubtitles(item tiktokDramaItem) []pageSubtitle {
	var out []pageSubtitle
	seen := map[string]bool{}
	for _, s := range item.Video.SubtitleInfos {
		u := strings.TrimSpace(s.URL)
		if u == "" || seen[u] || !strings.HasPrefix(u, "https://") {
			continue
		}
		if f := strings.ToLower(s.Format); f != "" && f != "webvtt" {
			continue
		}
		seen[u] = true
		out = append(out, pageSubtitle{URL: u, Lang: s.LanguageCodeName, Label: tiktokSubtitleLabel(s), Default: len(out) == 0})
	}
	return out
}

func tiktokSubtitleLabel(s tiktokSubtitleInfo) string {
	code := strings.ToLower(s.LanguageCodeName)
	lang := s.LanguageCodeName
	switch {
	case strings.HasPrefix(code, "eng"):
		lang = "英文"
	case strings.HasPrefix(code, "cmn-hant"):
		lang = "繁體中文"
	case strings.HasPrefix(code, "cmn-hans"):
		lang = "簡體中文"
	}
	switch strings.ToUpper(s.Source) {
	case "ASR":
		return lang + "自動字幕，語音辨識"
	case "MT":
		return lang + "字幕，機器翻譯"
	}
	return lang + "字幕"
}

// tiktokCaptureDiag counts what a TikTok episode capture saw: the API
// responses and why none gave the episode. Only paths, statuses and counts
// are kept: no query strings, cookies or stream addresses.
type tiktokCaptureDiag struct {
	mu         sync.Mutex
	apis       map[string]int // "path status" → count, any tiktok.com/api/ call
	order      []string
	known      int // finished known API responses (status 200)
	readFail   int
	results    map[tiktokAPIResult]int
	episodes   map[int]bool
	otherMedia int // media requests ignored because they are not the episode's
	statuses   []string
	// What the capture tab sent at all, to tell "the page made no API call"
	// from "the capture sees none of the page's traffic".
	requests  int
	reqTypes  map[string]int
	apiSent   int // requests sent to tiktok.com/api/
	docStatus int // status of the first page document
	fromSW    int // responses answered by the site's service worker
}

func newTikTokCaptureDiag() *tiktokCaptureDiag {
	return &tiktokCaptureDiag{apis: map[string]int{}, results: map[tiktokAPIResult]int{}, episodes: map[int]bool{}, reqTypes: map[string]int{}}
}

// reset forgets everything counted so far (the capture moved to a new tab).
func (d *tiktokCaptureDiag) reset() {
	fresh := newTikTokCaptureDiag()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.apis, d.order, d.known, d.readFail = fresh.apis, nil, 0, 0
	d.results, d.episodes, d.otherMedia, d.statuses = fresh.results, fresh.episodes, 0, nil
	d.requests, d.reqTypes, d.apiSent, d.docStatus, d.fromSW = 0, fresh.reqTypes, 0, 0, 0
}

func isTikTokAPIURL(raw string) bool {
	return strings.Contains(strings.ToLower(raw), "tiktok.com/api/")
}

// request counts one request the capture tab sent.
func (d *tiktokCaptureDiag) request(kind, raw string) {
	if kind == "" {
		kind = "Other"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests++
	d.reqTypes[kind]++
	if isTikTokAPIURL(raw) {
		d.apiSent++
	}
}

// response notes the first document's status and service-worker answers.
func (d *tiktokCaptureDiag) response(kind string, status int, fromSW bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if kind == "Document" && d.docStatus == 0 {
		d.docStatus = status
	}
	if fromSW {
		d.fromSW++
	}
}

// apiRequested reports whether the page has sent any TikTok API request.
func (d *tiktokCaptureDiag) apiRequested() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.apiSent > 0
}

func (d *tiktokCaptureDiag) api(raw string, status int) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	key := fmt.Sprintf("%s %d", u.Path, status)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.apis[key]; !ok {
		if len(d.order) >= 12 {
			return
		}
		d.order = append(d.order, key)
	}
	d.apis[key]++
}

func (d *tiktokCaptureDiag) knownAPI() {
	d.mu.Lock()
	d.known++
	d.mu.Unlock()
}

func (d *tiktokCaptureDiag) read(ok bool, res tiktokAPIResult, listed []int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !ok {
		d.readFail++
		return
	}
	d.results[res]++
	for _, n := range listed {
		d.episodes[n] = true
	}
}

// apiStatus keeps a non-zero status code (and short message) the API gave,
// e.g. a rate limit answered with HTTP 200.
func (d *tiktokCaptureDiag) apiStatus(body []byte) {
	var st tiktokDramaResponse
	if json.Unmarshal(body, &st) != nil {
		return
	}
	code, msg := st.StatusCode, st.StatusMessage
	if code == 0 {
		code = st.StatusCodeAlt
	}
	if msg == "" {
		msg = st.StatusMsgAlt
	}
	if code == 0 {
		return
	}
	note := strconv.Itoa(code)
	if msg = strings.TrimSpace(msg); msg != "" {
		if r := []rune(msg); len(r) > 40 {
			msg = string(r[:40])
		}
		note += " " + msg
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.statuses {
		if s == note {
			return
		}
	}
	if len(d.statuses) < 3 {
		d.statuses = append(d.statuses, note)
	}
}

func (d *tiktokCaptureDiag) media() {
	d.mu.Lock()
	d.otherMedia++
	d.mu.Unlock()
}

// noKnownAPI reports whether the page never answered with a known API call.
func (d *tiktokCaptureDiag) noKnownAPI() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.known == 0
}

// summary is one line for the log and the diagnostics.
func (d *tiktokCaptureDiag) summary(episode int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "分集資料回應 %d 次", d.known)
	if d.known > 0 {
		fmt.Fprintf(&b, "（讀取失敗 %d、格式無法解析 %d、沒有第 %d 集 %d、本集沒有影片網址 %d）",
			d.readFail, d.results[tiktokAPIUnparsed], episode, d.results[tiktokAPINoEpisode], d.results[tiktokAPINoStream])
	}
	if len(d.episodes) > 0 {
		eps := make([]int, 0, len(d.episodes))
		for n := range d.episodes {
			eps = append(eps, n)
		}
		sort.Ints(eps)
		fmt.Fprintf(&b, "；回應中的集數：%s", episodeRanges(eps))
	}
	if len(d.order) > 0 {
		parts := make([]string, 0, len(d.order))
		for _, k := range d.order {
			parts = append(parts, fmt.Sprintf("%s×%d", k, d.apis[k]))
		}
		b.WriteString("；頁面 API：" + strings.Join(parts, "、"))
	} else {
		b.WriteString("；頁面沒有呼叫任何 API")
	}
	if len(d.statuses) > 0 {
		b.WriteString("；API 狀態碼：" + strings.Join(d.statuses, "、"))
	}
	fmt.Fprintf(&b, "；略過的其他影片請求 %d 個", d.otherMedia)
	fmt.Fprintf(&b, "；擷取分頁送出的請求 %d 個", d.requests)
	if len(d.reqTypes) > 0 {
		kinds := make([]string, 0, len(d.reqTypes))
		for k := range d.reqTypes {
			kinds = append(kinds, k)
		}
		sort.Slice(kinds, func(i, j int) bool {
			if d.reqTypes[kinds[i]] != d.reqTypes[kinds[j]] {
				return d.reqTypes[kinds[i]] > d.reqTypes[kinds[j]]
			}
			return kinds[i] < kinds[j]
		})
		if len(kinds) > 6 {
			kinds = kinds[:6]
		}
		parts := make([]string, 0, len(kinds))
		for _, k := range kinds {
			parts = append(parts, fmt.Sprintf("%s %d", k, d.reqTypes[k]))
		}
		b.WriteString("（" + strings.Join(parts, "、") + "）")
	}
	if d.docStatus > 0 {
		fmt.Fprintf(&b, "；主頁面狀態 %d", d.docStatus)
	}
	if d.fromSW > 0 {
		fmt.Fprintf(&b, "；由網站背景程式（service worker）回應 %d 個", d.fromSW)
	}
	return b.String()
}

// episodeRanges writes sorted numbers as "1-24, 26".
func episodeRanges(nums []int) string {
	var parts []string
	for i := 0; i < len(nums); {
		j := i
		for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d-%d", nums[i], nums[j]))
		} else {
			parts = append(parts, strconv.Itoa(nums[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
