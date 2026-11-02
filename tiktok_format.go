package main

import (
	"net/url"
	"strings"
)

// TikTok 的高畫質 bytevc1（HEVC）格式只有影像，yt-dlp 會替它配上 format_id 為
// "audio" 的聲音；但那是影片使用的配樂原曲（music.playUrl），不是影片本身的聲音，
// 合併後講話的人聲會消失（只剩背景音樂）。所以 TikTok 先選影音在同一個檔的格式，
// 沒有才排除配樂原曲再合併，最後才退回一般做法（例如只有圖片＋音樂的投影片貼文）。
const (
	tiktokVideoFormat = "b[acodec!=none][vcodec!=none]/bv*+ba[format_id!=audio]/bv*+ba/b"
	tiktokAudioFormat = "b[acodec!=none][vcodec!=none]/ba[format_id!=audio]/ba/b"
)

// isTikTokPage reports whether rawURL is a tiktok.com page (including vm./vt. short links).
func isTikTokPage(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com")
}

// applyTikTokFormat swaps buildArgs' format choice for the TikTok one. args must
// end with rawURL (as buildArgs builds them); other modes are left unchanged.
func applyTikTokFormat(args []string, mode int) []string {
	switch mode {
	case 0, 2, 5:
		return replaceBestFormat(args, tiktokVideoFormat)
	case 1:
		if len(args) == 0 {
			return args
		}
		out := make([]string, 0, len(args)+2)
		out = append(out, args[:len(args)-1]...)
		out = append(out, "-f", tiktokAudioFormat, args[len(args)-1])
		return out
	}
	return args
}
