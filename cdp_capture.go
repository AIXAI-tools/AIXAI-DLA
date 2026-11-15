//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// This file implements a small Chrome DevTools Protocol (CDP) client using
// only Go's standard library.  Unlike the unpacked-extension fallback, CDP
// enables Network monitoring BEFORE the target page is navigated.  This is
// important for players that request their MP4/HLS manifest immediately on
// page load (an always-on browser extension would also see those).

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
