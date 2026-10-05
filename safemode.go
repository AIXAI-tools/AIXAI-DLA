//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"time"
)

// 帳號安全模式 (account safety mode)
//
// Sites act against the *account* whose login is used for heavy automated
// fetching (yt-dlp's own docs warn YouTube accounts can be banned; Meta and
// TikTok terms prohibit automated collection). Safety mode therefore:
//   - waits a random, site-dependent pause between items (fixed intervals look
//     robotic), and takes long breaks during big batches on login-bound sites;
//   - stops the whole batch on the first rate-limit / bot-check signal instead
//     of retrying, because hammering on is what escalates a block into a ban;
//   - never silently falls back to the cookies of the user's everyday browsers;
//   - does not chain extra engines for episode pages that require sign-in.

type paceTier struct {
	key        string
	label      string
	minDelay   time.Duration
	maxDelay   time.Duration
	batchSize  int // 0 = no long break
	breakMin   time.Duration
	breakMax   time.Duration
	loginBound bool
}

var (
	tierLogin = paceTier{key: "login", label: "需登入網站", minDelay: 15 * time.Second, maxDelay: 45 * time.Second,
		batchSize: 25, breakMin: 10 * time.Minute, breakMax: 15 * time.Minute, loginBound: true}
	tierGuarded = paceTier{key: "guarded", label: "限制較嚴格的網站", minDelay: 5 * time.Second, maxDelay: 10 * time.Second,
		batchSize: 100, breakMin: 10 * time.Minute, breakMax: 15 * time.Minute}
	tierNormal = paceTier{key: "normal", label: "一般網站", minDelay: 2 * time.Second, maxDelay: 5 * time.Second}
)

func hostMatches(host string, domains ...string) bool {
	for _, d := range domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// paceTierFor decides how carefully a URL must be fetched.
func paceTierFor(rawURL string, cfg settings) paceTier {
	if _, _, ok := parseTikTokShortDramaURL(rawURL); ok {
		return tierLogin // these episode pages only play when signed in
	}
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	}
	if hostMatches(host, "facebook.com", "fb.watch", "instagram.com", "threads.net", "threads.com", "x.com", "twitter.com") {
		return tierLogin
	}
	// The user explicitly chose a login (cookie file or a specific browser).
	if strings.TrimSpace(cfg.CookieFile) != "" {
		return tierLogin
	}
	if b := strings.TrimSpace(cfg.CookieBrowser); b != "" && b != "auto" && b != "none" {
		return tierLogin
	}
	switch classifySiteURL(rawURL) {
	case "youtube", "bilibili", "tiktok":
		return tierGuarded
	}
	return tierNormal
}

// blockSignals are messages sites/engines emit when they start rate limiting
// or challenging the session. Matching is case-insensitive.
var blockSignals = []string{
	"http error 429", "too many requests", "rate-limit", "rate limit", "ratelimit",
	"sign in to confirm", "not a bot", "confirm you're not a bot", "confirm you’re not a bot",
	"captcha", "verify you are human", "unusual traffic",
	"http error 412", "precondition failed", "code -352", "\"code\":-352",
	"checkpoint", "temporarily blocked", "action blocked", "try again later",
	"ip address is blocked", "your ip address is blocked",
}

func isBlockSignal(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	var cre *commandRunError
	if errors.As(err, &cre) {
		text += "\n" + strings.ToLower(cre.Summary)
	}
	for _, s := range blockSignals {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func randomBetween(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(rand.Int63n(int64(hi-lo)))
}

// safeWait sleeps with a visible countdown; it returns false if the user
// pressed 停止 or the task was cancelled.
func (a *app) safeWait(ctx context.Context, d time.Duration, reason string) bool {
	end := time.Now().Add(d)
	for {
		left := time.Until(end)
		if left <= 0 {
			return true
		}
		if a.stopRequested.Load() {
			return false
		}
		secs := int(left.Round(time.Second) / time.Second)
		if secs >= 120 {
			a.postStatus(fmt.Sprintf("狀態：帳號安全模式｜%s，約 %d 分 %02d 秒後繼續（可按「停止」中斷）", reason, secs/60, secs%60))
		} else {
			a.postStatus(fmt.Sprintf("狀態：帳號安全模式｜%s，%d 秒後繼續（可按「停止」中斷）", reason, secs))
		}
		step := time.Second
		if left < step {
			step = left
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(step):
		}
	}
}

// safePacer tracks a download task's progress per tier.
type safePacer struct {
	done map[string]int
}

func newSafePacer() *safePacer { return &safePacer{done: map[string]int{}} }

// beforeItem waits before every item except the first: a random pause, or a
// long break after each full batch on login-bound / guarded sites.
func (p *safePacer) beforeItem(ctx context.Context, a *app, index int, rawURL string, cfg settings) bool {
	tier := paceTierFor(rawURL, cfg)
	count := p.done[tier.key]
	p.done[tier.key] = count + 1
	if index == 0 {
		return true
	}
	if tier.batchSize > 0 && count > 0 && count%tier.batchSize == 0 {
		d := randomBetween(tier.breakMin, tier.breakMax)
		a.postLog(fmt.Sprintf("ℹ 帳號安全模式：%s已連續處理 %d 個，休息約 %d 分鐘再繼續，降低被判定為機器人的風險。\r\n", tier.label, count, int(d.Minutes()+0.5)))
		return a.safeWait(ctx, d, fmt.Sprintf("%s分批休息中", tier.label))
	}
	d := randomBetween(tier.minDelay, tier.maxDelay)
	return a.safeWait(ctx, d, fmt.Sprintf("%s隨機間隔 %d 秒", tier.label, int(d.Seconds()+0.5)))
}

func safeModeIntro(urls []string, cfg settings) string {
	counts := map[string]int{}
	for _, u := range urls {
		counts[paceTierFor(u, cfg).key]++
	}
	var parts []string
	for _, t := range []paceTier{tierLogin, tierGuarded, tierNormal} {
		if n := counts[t.key]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 個（每個間隔 %d–%d 秒隨機%s）", t.label, n, int(t.minDelay.Seconds()), int(t.maxDelay.Seconds()),
				map[bool]string{true: fmt.Sprintf("，每 %d 個休息 %d–%d 分鐘", t.batchSize, int(t.breakMin.Minutes()), int(t.breakMax.Minutes())), false: ""}[t.batchSize > 0]))
		}
	}
	return "🛡 帳號安全模式已開啟：" + strings.Join(parts, "；") + "。偵測到限流或驗證時會立即停止整批，不自動重試。\r\n"
}

func blockStopError(err error) error {
	return &commandRunError{Cause: err, Summary: "🛡 帳號安全模式：網站回應了「請求過多／需要驗證／存取限制」訊號，已立即停止整批下載，避免帳號被進一步限制或封鎖。\n\n" +
		"網站訊息：" + firstLine(err.Error()) + "\n\n建議：先暫停數小時再繼續；需要登入的網站請使用「下載專用帳號」，不要用主帳號。"}
}
