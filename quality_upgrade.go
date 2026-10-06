package main

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Many players start with a compatible default rendition (for example H.264 at
// a medium resolution) even when the page's own player data also lists larger
// renditions in its quality menu. The capture browser only sees the request for
// the rendition that actually played, so the page's JSON responses are kept and
// searched for the list that contains the played URL; the best entry of that
// same list is downloaded instead.

// streamVariant is one entry of a player's rendition list.
type streamVariant struct {
	URL    string
	Width  int
	Height int
	Codec  string
}

// shortSide is the "p" value of a rendition (576×1024 portrait = 576p).
func (v streamVariant) shortSide() int {
	if v.Width > 0 && v.Height > 0 {
		if v.Width < v.Height {
			return v.Width
		}
		return v.Height
	}
	if v.Height > 0 {
		return v.Height
	}
	return v.Width
}

func (v streamVariant) pixels() int { return v.Width * v.Height }

func (v streamVariant) isH264() bool {
	c := strings.ToLower(v.Codec)
	return strings.Contains(c, "264") || strings.Contains(c, "avc")
}

var variantQualityRE = regexp.MustCompile(`(?i)^\s*(\d{3,4})\s*p\s*$`)

func jsonInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}

// variantFromObject reads a rendition from a JSON object, or reports false when
// the object has no media URL or no resolution information.
func variantFromObject(m map[string]interface{}) (streamVariant, bool) {
	var v streamVariant
	for _, key := range []string{"url", "src", "play_url", "playUrl", "file"} {
		if s, ok := m[key].(string); ok && (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
			v.URL = s
			break
		}
	}
	if v.URL == "" {
		return v, false
	}
	v.Width = jsonInt(m["width"])
	v.Height = jsonInt(m["height"])
	if v.Width == 0 && v.Height == 0 {
		for _, key := range []string{"quality", "label", "resolution", "definition"} {
			if s, ok := m[key].(string); ok {
				if mm := variantQualityRE.FindStringSubmatch(s); len(mm) == 2 {
					v.Height, _ = strconv.Atoi(mm[1])
					break
				}
			}
		}
	}
	if v.Width == 0 && v.Height == 0 {
		return v, false
	}
	for _, key := range []string{"codec", "codecs", "vcodec", "video_codec", "codec_type"} {
		if s, ok := m[key].(string); ok && s != "" {
			v.Codec = s
			break
		}
	}
	return v, true
}

// sameMediaURL compares two URLs; signed URLs may be re-issued with a different
// query string, so an identical host and path also counts.
func sameMediaURL(a, b string) bool {
	if a == b {
		return true
	}
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil || ua.Path == "" || ua.Path == "/" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host) && ua.Path == ub.Path
}

// variantListsContaining walks a JSON document and returns every array of
// renditions that includes playedURL.
func variantListsContaining(doc interface{}, playedURL string) [][]streamVariant {
	var out [][]streamVariant
	var walk func(n interface{})
	walk = func(n interface{}) {
		switch t := n.(type) {
		case map[string]interface{}:
			for _, child := range t {
				walk(child)
			}
		case []interface{}:
			var list []streamVariant
			found := false
			for _, item := range t {
				m, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				if v, ok := variantFromObject(m); ok {
					list = append(list, v)
					if sameMediaURL(v.URL, playedURL) {
						found = true
					}
				}
			}
			if found && len(list) >= 2 {
				out = append(out, list)
			}
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(doc)
	return out
}

// bestVariant picks the highest rendition allowed by maxShortSide (0 = no
// limit). At equal resolution H.264 is preferred because it plays everywhere.
func bestVariant(list []streamVariant, maxShortSide int) (streamVariant, bool) {
	var best streamVariant
	found := false
	for _, v := range list {
		if maxShortSide > 0 && v.shortSide() > maxShortSide {
			continue
		}
		switch {
		case !found:
		case v.pixels() > best.pixels():
		case v.pixels() == best.pixels() && v.isH264() && !best.isH264():
		default:
			continue
		}
		best, found = v, true
	}
	return best, found
}

// betterVariantFromPageJSON looks through the page's JSON responses for the
// rendition list that contains playedURL. It returns the best rendition and the
// played one when the best is strictly larger; otherwise ok is false.
func betterVariantFromPageJSON(bodies [][]byte, playedURL string, maxShortSide int) (best, played streamVariant, ok bool) {
	for i := len(bodies) - 1; i >= 0; i-- { // newest response first
		var doc interface{}
		if json.Unmarshal(bodies[i], &doc) != nil {
			continue
		}
		for _, list := range variantListsContaining(doc, playedURL) {
			var cur streamVariant
			for _, v := range list {
				if sameMediaURL(v.URL, playedURL) {
					cur = v
					break
				}
			}
			b, found := bestVariant(list, maxShortSide)
			if found && b.pixels() > cur.pixels() {
				return b, cur, true
			}
			return streamVariant{}, cur, false
		}
	}
	return streamVariant{}, streamVariant{}, false
}
