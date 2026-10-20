//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file implements a small Chrome DevTools Protocol (CDP) client using
// only Go's standard library.  Unlike the unpacked-extension fallback, CDP
// enables Network monitoring BEFORE the target page is navigated.  This is
// important for players that request their MP4/HLS manifest immediately on
// page load (an always-on browser extension would also see those).

type cdpWireMessage struct {
	ID        int                    `json:"id,omitempty"`
	Method    string                 `json:"method,omitempty"`
	SessionID string                 `json:"sessionId,omitempty"`
	Params    map[string]interface{} `json:"params,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     map[string]interface{} `json:"error,omitempty"`
}

type cdpRequestState struct {
	URL             string
	RequestType     string
	Initiator       string
	RequestHeaders  map[string]string
	ResponseHeaders map[string]string
	MimeType        string
	Status          int
	SeenResponse    bool
}

// cdpPipe is a private DevTools connection over two anonymous pipes
// (--remote-debugging-pipe). Unlike --remote-debugging-port no TCP port is
// opened, so no other local program can attach to the capture browser while
// it runs and read the TikTok login cookies kept in its profile.
// Messages are JSON objects terminated by a NUL byte.
type cdpPipe struct {
	w   *os.File
	rf  *os.File
	r   *bufio.Reader
	wmu sync.Mutex
}

func (p *cdpPipe) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.w.Write(append(data, 0))
	return err
}

func (p *cdpPipe) ReadMessage() ([]byte, error) {
	msg, err := p.r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	if len(msg) > 64<<20 {
		return nil, errors.New("DevTools 訊息異常過大")
	}
	return msg[:len(msg)-1], nil
}

func (p *cdpPipe) Close() {
	_ = p.w.Close()
	_ = p.rf.Close()
}

// startBrowserWithPipe launches the browser with DevTools bound to private
// pipes. On Windows the inheritable pipe handles are announced through
// --remote-debugging-io-pipes=<read>,<write> (verified with Edge 154).
func startBrowserWithPipe(path string, args []string, hidden bool) (*exec.Cmd, *cdpPipe, error) {
	toChildR, toChildW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	fromChildR, fromChildW, err := os.Pipe()
	if err != nil {
		toChildR.Close()
		toChildW.Close()
		return nil, nil, err
	}
	pipeArgs := []string{
		"--remote-debugging-pipe",
		fmt.Sprintf("--remote-debugging-io-pipes=%d,%d", toChildR.Fd(), fromChildW.Fd()),
		// Edge relaunches itself when it sees a compatibility layer; the
		// relaunched copy does not get our pipe handles.
		"--edge-skip-compat-layer-relaunch",
	}
	cmd := exec.Command(path, append(pipeArgs, args...)...)
	cmd.Env = browserEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:              CREATE_NEW_PROCESS_GROUP,
		HideWindow:                 hidden,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(toChildR.Fd()), syscall.Handle(fromChildW.Fd())},
	}
	startErr := cmd.Start()
	if startErr == nil {
		bindToAppLifetime(cmd.Process.Pid) // the browser can never outlive the app
	}
	// The child owns its ends now; keep only ours.
	toChildR.Close()
	fromChildW.Close()
	if startErr != nil {
		toChildW.Close()
		fromChildR.Close()
		return nil, nil, startErr
	}
	return cmd, &cdpPipe{w: toChildW, rf: fromChildR, r: bufio.NewReaderSize(fromChildR, 1<<20)}, nil
}

// browserEnv is the app environment without __COMPAT_LAYER, which Windows adds
// when the app was started from Explorer with a compatibility record.
func browserEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "__COMPAT_LAYER=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func cdpMapString(v interface{}) map[string]string {
	out := map[string]string{}
	m, ok := v.(map[string]interface{})
	if !ok {
		return out
	}
	for k, raw := range m {
		if raw == nil {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = fmt.Sprint(raw)
	}
	return out
}

func mergeStringMaps(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		if strings.TrimSpace(v) != "" {
			dst[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	return dst
}

func cdpString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func cdpFloat(m map[string]interface{}, key string) float64 {
	if m == nil {
		return 0
	}
	v, _ := m[key].(float64)
	return v
}

// cdpAutoplayScript wakes up players that only load their stream after a Play
// button is clicked. v3.5 clicked each button exactly once, which was often
// before React/Next.js hydration attached the click handler, so the
// click was lost and never retried. Buttons are now re-clicked every 2.5 s until
// a <video> is actually playing.
func cdpAutoplayScript() string {
	return `(() => {
  const lastClick = new WeakMap();
  const selectors = [
    'button[aria-label*="play" i]', '[role="button"][aria-label*="play" i]',
    '.vjs-big-play-button', '.jw-icon-playback', '.plyr__control[data-plyr="play"]',
    '[data-e2e*="play"]', '[class*="play-button" i]', '[class*="playButton"]'
  ];
  function playing() {
    try { return Array.from(document.querySelectorAll('video')).some(v => !v.paused && v.readyState > 1); } catch (_) { return false; }
  }
  function wake() {
    try {
      document.querySelectorAll('video').forEach(v => {
        try { v.muted = true; v.defaultMuted = true; v.autoplay = true; v.playsInline = true; if (v.paused) { const p = v.play(); if (p && p.catch) p.catch(()=>{}); } } catch (_) {}
      });
      if (document.readyState === 'loading' || playing()) return;
      const now = Date.now();
      selectors.forEach(sel => {
        try { document.querySelectorAll(sel).forEach(el => {
          const label = (el.getAttribute('aria-label') || '').toLowerCase();
          if (label.includes('pause') || label.includes('playlist') || label.includes('display')) return;
          const r = el.getBoundingClientRect();
          if (r.width < 8 || r.height < 8) return;
          if (now - (lastClick.get(el) || 0) < 2500) return;
          lastClick.set(el, now); el.click();
        }); } catch (_) {}
      });
    } catch (_) {}
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wake, {once:true});
  wake(); setInterval(wake, 900);
})();`
}

// cdpAdVideoScript lists, as a JSON array, what the page's ad <video> elements
// play: a video counts as an ad when it or one of its parents has "ad", "ads",
// "vast", "ima", "preroll" or "advert" as a word of its id or class.
const cdpAdVideoScript = `(() => {
  const re = /(^|[^a-z])(ad|ads|vast|ima|preroll|advert|advertisement)([^a-z]|$)/i;
  const out = [];
  try {
    document.querySelectorAll('video').forEach(v => {
      let el = v, ad = false;
      for (let i = 0; i < 6 && el; i++, el = el.parentElement) {
        const cls = typeof el.className === 'string' ? el.className : '';
        if (re.test((el.id || '') + ' ' + cls)) { ad = true; break; }
      }
      if (!ad) return;
      if (v.currentSrc && /^https?:/.test(v.currentSrc)) out.push(v.currentSrc);
      if (v.src && /^https?:/.test(v.src)) out.push(v.src);
      v.querySelectorAll('source').forEach(s => { if (s.src && /^https?:/.test(s.src)) out.push(s.src); });
    });
  } catch (_) {}
  return JSON.stringify(out);
})()`

// isTikTokDramaAPI reports whether a captured request is one of the TikTok web
// API calls whose JSON body carries the episode's playAddr once logged in.
func isTikTokDramaAPI(raw string) bool {
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "tiktok.com/") {
		return false
	}
	return strings.Contains(lower, "/api/drama/episode/item_list") || strings.Contains(lower, "/api/item/detail")
}

// tiktokCandidateFromAPIBody picks the requested episode out of a TikTok API
// response body and turns its best stream into a capture candidate.
func tiktokCandidateFromAPIBody(body []byte, apiURL string, episode int, reqHeaders map[string]string) (browserMediaCandidate, bool) {
	c, res, _ := tiktokCandidateFromAPIBodyResult(body, apiURL, episode, 0, reqHeaders)
	return c, res == tiktokAPIMatched
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

// tiktokCandidateFromAPIBodyResult is tiktokCandidateFromAPIBody with the
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
		}, tiktokAPIMatched, listed
	}
	return browserMediaCandidate{}, res, listed
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
}

func newTikTokCaptureDiag() *tiktokCaptureDiag {
	return &tiktokCaptureDiag{apis: map[string]int{}, results: map[tiktokAPIResult]int{}, episodes: map[int]bool{}}
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

// cdpCookiesFromFile converts a Netscape cookies.txt (exported by the user from
// the browser they are already logged in with) into Network.setCookies params.
// Only cookies for the target site's domain are used; values are never logged.
func cdpCookiesFromFile(path, rawURL string) []map[string]interface{} {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	labels := strings.Split(strings.ToLower(u.Hostname()), ".")
	if len(labels) < 2 {
		return nil
	}
	site := strings.Join(labels[len(labels)-2:], ".")
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		httpOnly := false
		if strings.HasPrefix(line, "#HttpOnly_") {
			httpOnly = true
			line = strings.TrimPrefix(line, "#HttpOnly_")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(f[0]))
		bare := strings.TrimPrefix(domain, ".")
		if bare != site && !strings.HasSuffix(bare, "."+site) {
			continue
		}
		c := map[string]interface{}{
			"name":     f[5],
			"value":    f[6],
			"domain":   domain,
			"path":     f[2],
			"secure":   strings.EqualFold(f[3], "TRUE"),
			"httpOnly": httpOnly,
		}
		if exp, err := strconv.ParseFloat(f[4], 64); err == nil && exp > 0 {
			c["expires"] = exp
		}
		if strings.EqualFold(f[3], "TRUE") {
			c["sameSite"] = "None"
		}
		out = append(out, c)
	}
	return out
}

// prepareCaptureProfile stops the persistent capture profile from restoring the
// previous run's tabs: startup is set to "open a new tab" and the last exit is
// marked clean so no crash-restore is offered. Must run before the browser starts.
func prepareCaptureProfile(profileDir string) {
	defaultDir := filepath.Join(profileDir, "Default")
	_ = os.MkdirAll(defaultDir, 0755)
	prefPath := filepath.Join(defaultDir, "Preferences")
	prefs := map[string]interface{}{}
	if data, err := os.ReadFile(prefPath); err == nil {
		if err := json.Unmarshal(data, &prefs); err != nil {
			return // never overwrite a Preferences file we cannot parse
		}
	}
	section := func(name string) map[string]interface{} {
		m, ok := prefs[name].(map[string]interface{})
		if !ok {
			m = map[string]interface{}{}
			prefs[name] = m
		}
		return m
	}
	section("session")["restore_on_startup"] = 5
	profile := section("profile")
	profile["exit_type"] = "Normal"
	profile["exited_cleanly"] = true
	if data, err := json.Marshal(prefs); err == nil {
		_ = os.WriteFile(prefPath, data, 0644)
	}
}

func cdpCommand(id int, method string, params map[string]interface{}, sessionID string) map[string]interface{} {
	m := map[string]interface{}{"id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	if sessionID != "" {
		m["sessionId"] = sessionID
	}
	return m
}
