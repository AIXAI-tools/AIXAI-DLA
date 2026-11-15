//go:build windows

// yt-dlp arguments, URL normalization, site classification, and file names.
package main

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type parameterPreset struct {
	Label string
	Args  string
}

var parameterPresets = []parameterPreset{
	{Label: "▼ 點選常用參數，會自動加入上方欄位", Args: ""},
	{Label: "-x｜只擷取音訊（通常搭配「將音訊轉成 MP3」）", Args: "-x"},
	{Label: "--audio-format mp3｜將音訊轉成 MP3（需搭配「只擷取音訊」）", Args: "--audio-format mp3"},
	{Label: "--audio-quality 0｜MP3 使用最佳可變位元率品質", Args: "--audio-quality 0"},
	{Label: "--write-subs｜下載影片提供的人工字幕", Args: "--write-subs"},
	{Label: "--write-auto-subs｜下載平台自動產生的字幕", Args: "--write-auto-subs"},
	{Label: "--sub-langs zh-Hant,zh-Hans,en｜下載繁中、簡中與英文字幕", Args: "--sub-langs zh-Hant,zh-Hans,en"},
	{Label: "--embed-subs｜將字幕嵌入影片檔（需 FFmpeg）", Args: "--embed-subs"},
	{Label: "--write-thumbnail｜另外下載影片縮圖", Args: "--write-thumbnail"},
	{Label: "--embed-thumbnail｜將縮圖嵌入媒體檔", Args: "--embed-thumbnail"},
	{Label: "--embed-metadata｜嵌入標題、作者等媒體資訊", Args: "--embed-metadata"},
	{Label: "--embed-chapters｜將影片章節嵌入媒體檔", Args: "--embed-chapters"},
	{Label: "--no-overwrites｜已有同名檔案時不要覆蓋", Args: "--no-overwrites"},
	{Label: "--download-archive downloaded.txt｜記錄已下載項目，避免重複", Args: "--download-archive downloaded.txt"},
	{Label: "--retries 10｜一般下載失敗時最多重試 10 次", Args: "--retries 10"},
	{Label: "--fragment-retries 10｜片段下載失敗時最多重試 10 次", Args: "--fragment-retries 10"},
	{Label: "--concurrent-fragments 4｜同時下載 4 個影音片段", Args: "--concurrent-fragments 4"},
	{Label: "--limit-rate 5M｜將下載速度限制為每秒約 5 MiB", Args: "--limit-rate 5M"},
	{Label: "--sleep-interval 2｜每個下載開始前等待 2 秒", Args: "--sleep-interval 2"},
	{Label: "--restrict-filenames｜檔名只使用較安全的英數字元", Args: "--restrict-filenames"},
	{Label: "--keep-video｜轉成音訊後保留原始影片檔", Args: "--keep-video"},
	{Label: "--write-info-json｜另存影片完整資訊 JSON 檔", Args: "--write-info-json"},
	{Label: "--force-ipv4｜只使用 IPv4，部分網站連線異常時可嘗試", Args: "--force-ipv4"},
}

func extractHTMLTitle(body string) string {
	re := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[1], "")))
	}
	return ""
}

func safeWindowsBaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	invalid := regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)
	name = invalid.ReplaceAllString(name, "_")
	name = strings.Trim(name, " .")
	if len([]rune(name)) > maxBaseNameRunes {
		r := []rune(name)
		name = string(r[:maxBaseNameRunes])
	}
	return name
}

// maxBaseNameRunes is the longest name safeWindowsBaseName keeps.
const maxBaseNameRunes = 160

// withEpisodeLabel joins name and an episode label such as "_E003" within
// maxBaseNameRunes, shortening the name so the episode number is never cut
// off (two episodes of a long title would otherwise get the same file).
func withEpisodeLabel(name, label string) string {
	r := []rune(safeWindowsBaseName(name))
	if room := maxBaseNameRunes - len([]rune(label)); len(r) > room {
		if room < 0 {
			room = 0
		}
		r = []rune(strings.TrimRight(string(r[:room]), " ."))
	}
	return safeWindowsBaseName(string(r) + label)
}

func (a *app) buildArgs(rawURL string, mode int, formatID, output string, cfg settings) ([]string, error) {
	collection := mode == 2 || isCollectionURL(rawURL)
	args := []string{
		"--ignore-config",
		"--newline", "--no-colors", "--encoding", "utf-8",
		"--windows-filenames",
		"--continue", "--part",
		"--retries", "10",
		"--fragment-retries", "20",
		"--retry-sleep", "http:exp=1:20",
		"--retry-sleep", "fragment:exp=1:20",
		"--ffmpeg-location", a.binDir,
		"--js-runtimes", "deno:" + a.denoPath,
	}
	args = append(args, a.ytDlpPluginArgs(rawURL)...)
	if cookieFile := strings.TrimSpace(cfg.CookieFile); cookieFile != "" && validFile(cookieFile, 1) {
		args = append(args, "--cookies", cookieFile)
	} else if browser := strings.TrimSpace(cfg.CookieBrowser); browser != "" && browser != "auto" && browser != "none" {
		args = append(args, "--cookies-from-browser", browser)
	}
	if mode != 3 {
		args = append(args, "-P", output)
		single, playlist := outputTemplates(output)
		if collection && cfg.PlaylistFolder {
			args = append(args, "-o", playlist)
		} else if cfg.FileName != "" && !collection {
			args = append(args, "-o", customTemplate(cfg.FileName))
		} else if custom := a.sequenceTemplate(rawURL, cfg, output); custom != "" && !collection {
			args = append(args, "-o", custom)
		} else {
			args = append(args, "-o", single)
		}
	}
	if collection {
		args = append(args, "--yes-playlist", "--no-abort-on-error")
	} else if cfg.NoPlaylist && mode != 2 {
		args = append(args, "--no-playlist")
	}
	extra, err := splitArgs(cfg.ExtraArgs)
	if err != nil {
		return nil, fmt.Errorf("進階參數格式錯誤：%w", err)
	}
	args = append(args, extra...)

	if cfg.SubtitlesOnly {
		return append(args, "--skip-download", "--write-subs", "--write-auto-subs", "--convert-subs", "srt", rawURL), nil
	}

	sorting := h264FormatSort(mode, cfg.ExtraArgs)
	switch mode {
	case 0:
		args = append(args, sorting...)
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", rawURL)
	case 1:
		args = append(args, "-x", "--audio-format", "mp3", "--audio-quality", "0", rawURL)
	case 2:
		args = append(args, sorting...)
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", rawURL)
	case 3:
		args = append(args, "-F", rawURL)
	case 4:
		if strings.TrimSpace(formatID) == "" {
			return nil, errors.New("格式 ID 不可空白")
		}
		args = append(args, "-f", strings.TrimSpace(formatID), rawURL)
	case 5:
		// yt-dlp's "res" is the short side: a portrait 720×1280 video is 720p.
		if len(sorting) == 0 {
			sorting = []string{"-S", "res:720"}
		}
		args = append(args, sorting...)
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", rawURL)
	default:
		args = append(args, rawURL)
	}
	if isTikTokPage(rawURL) && !userChoosesFormat(cfg.ExtraArgs) {
		args = applyTikTokFormat(args, mode)
	}
	return args, nil
}

func siteDisplayName(site string) string {
	switch site {
	case "youtube":
		return "YouTube"
	case "bilibili":
		return "Bilibili"
	case "vimeo":
		return "Vimeo"
	case "haokan":
		return "此網站"
	case "tiktok":
		return "TikTok"
	case "dramatip":
		return "網頁播放器"
	default:
		return site
	}
}

func addSiteCompatibilityArgs(args []string, site string) []string {
	if len(args) == 0 {
		return args
	}
	extras := []string{"--impersonate", "chrome"}
	switch site {
	case "bilibili":
		extras = append(extras, "--force-ipv4")
	case "vimeo":
		extras = append(extras, "--extractor-args", "vimeo:client=web")
	}
	return insertBeforeLast(args, extras...)
}

func insertBeforeLast(args []string, extras ...string) []string {
	if len(args) == 0 || len(extras) == 0 {
		return append([]string(nil), args...)
	}
	out := make([]string, 0, len(args)+len(extras))
	out = append(out, args[:len(args)-1]...)
	out = append(out, extras...)
	out = append(out, args[len(args)-1])
	return out
}

func expandSequenceURL(raw string, startN, endN int) ([]string, error) {
	if startN <= 0 || endN < startN {
		return nil, errors.New("連續序號範圍無效")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("無法解析連續序號網址：%s", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return nil, fmt.Errorf("網址中找不到可替換的集數：%s", raw)
	}

	kind := ""
	index := len(parts) - 1
	prefix := ""
	if len(parts) >= 4 && strings.EqualFold(parts[0], "shortdrama") && strings.EqualFold(parts[1], "episode") && allDigits(parts[2]) && allDigits(parts[3]) {
		kind = "segment"
		index = 3
	} else if m := regexp.MustCompile(`(?i)^(episode[-_]?)(\d+)$`).FindStringSubmatch(parts[index]); len(m) == 3 {
		kind = "episode-prefix"
		prefix = m[1]
	} else if allDigits(parts[index]) {
		kind = "segment"
	} else {
		for _, key := range []string{"episode", "ep", "episode_id"} {
			if value := u.Query().Get(key); allDigits(value) {
				kind = "query:" + key
				break
			}
		}
	}
	if kind == "" {
		// One-page players keep the same address for every episode. Many of
		// them open a given episode from an "ep" query parameter (the form
		// their share links use); such addresses are played in the capture
		// browser one by one, and a repeated video stops the batch, so a
		// page that ignores the parameter cannot save episode 1 over and over.
		if u.Query().Get("ep") != "" { // an "ep" value we cannot read; do not overwrite it
			return nil, fmt.Errorf("無法判斷集數位置：%s", raw)
		}
		kind = "query:ep"
	}

	result := make([]string, 0, endN-startN+1)
	for n := startN; n <= endN; n++ {
		clone := *u
		if strings.HasPrefix(kind, "query:") {
			q := clone.Query()
			q.Set(strings.TrimPrefix(kind, "query:"), strconv.Itoa(n))
			clone.RawQuery = q.Encode()
		} else {
			newParts := append([]string(nil), parts...)
			if kind == "episode-prefix" {
				newParts[index] = prefix + strconv.Itoa(n)
			} else {
				newParts[index] = strconv.Itoa(n)
			}
			clone.Path = "/" + strings.Join(newParts, "/")
		}
		result = append(result, clone.String())
	}
	return result, nil
}

func normalizeInputURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("網址不可空白")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("無法辨識網址：%s", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("目前只接受 http 或 https 網址：%s", raw)
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if host == "space.bilibili.com" {
		parts := strings.FieldsFunc(strings.Trim(u.Path, "/"), func(r rune) bool { return r == '/' })
		if len(parts) == 0 || !allDigits(parts[0]) {
			return "", errors.New("Bilibili 空間網址需要包含使用者 UID，例如：https://space.bilibili.com/123456/video")
		}
		if len(parts) == 1 {
			u.Path = "/" + parts[0] + "/video"
		}
	}
	return u.String(), nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func classifySiteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	switch {
	case host == "youtube.com" || host == "youtu.be" || strings.HasSuffix(host, ".youtube.com"):
		return "youtube"
	case host == "b23.tv" || strings.HasSuffix(host, ".bilibili.com") || host == "bilibili.com" || host == "bilibili.tv" || strings.HasSuffix(host, ".bilibili.tv"):
		return "bilibili"
	case host == "vimeo.com" || strings.HasSuffix(host, ".vimeo.com"):
		return "vimeo"
	case host == "haokan.baidu.com":
		return "haokan"
	case host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com"):
		return "tiktok"
	case host == "dramatip.net" || strings.HasSuffix(host, ".dramatip.net"):
		return "dramatip"
	default:
		return ""
	}
}

func isCollectionURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	path := strings.ToLower(strings.Trim(u.Path, "/"))
	if host == "space.bilibili.com" {
		return true
	}
	if host == "vimeo.com" || strings.HasSuffix(host, ".vimeo.com") {
		prefixes := []string{"user", "channels/", "showcase/", "album/", "groups/", "ondemand/"}
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
	}
	return false
}
