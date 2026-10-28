package main

import (
	"net/url"
	"strings"
)

// isManifestURL reports an HLS or DASH manifest address (.m3u8 / .mpd).
func isManifestURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".m3u8") || strings.HasSuffix(p, ".mpd")
}

// withoutRefererArgs returns yt-dlp arguments without the --referer value and
// the Origin header taken from the browser, for a media server that refuses
// requests naming the embedding page.
func withoutRefererArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--referer" && i+1 < len(args) {
			i++
			continue
		}
		if args[i] == "--add-header" && i+1 < len(args) && isRefererHeader(args[i+1]) {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// withoutRefererFFmpeg drops the Referer and Origin lines from FFmpeg's
// -headers value (the option itself goes when nothing is left).
func withoutRefererFFmpeg(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-headers" && i+1 < len(args) {
			var keep []string
			for _, line := range strings.Split(args[i+1], "\r\n") {
				if strings.TrimSpace(line) != "" && !isRefererHeader(line) {
					keep = append(keep, line)
				}
			}
			i++
			if len(keep) > 0 {
				out = append(out, "-headers", strings.Join(keep, "\r\n")+"\r\n")
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func isRefererHeader(h string) bool {
	name, _, _ := strings.Cut(h, ":")
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "referer" || name == "origin"
}
