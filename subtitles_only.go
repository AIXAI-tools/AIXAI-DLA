//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errNoSubtitles represents the failure when no subtitles were found or saved.
type errNoSubtitles struct{}

func (e *errNoSubtitles) Error() string {
	return "這一集網頁沒有提供字幕檔（字幕可能燒在畫面上）"
}

// noSubtitlesFinal indicates a conclusive failure from page data (no fallback to browser).
type noSubtitlesFinal struct {
	err error
}

func (e *noSubtitlesFinal) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "這一集網頁沒有提供字幕檔（字幕可能燒在畫面上）"
}

func (e *noSubtitlesFinal) Unwrap() error {
	return e.err
}

// isNoSubtitles checks whether an error is due to missing subtitles.
func isNoSubtitles(err error) bool {
	if err == nil {
		return false
	}
	var target *errNoSubtitles
	var final *noSubtitlesFinal
	return errors.As(err, &target) || errors.As(err, &final) || strings.Contains(err.Error(), "沒有提供字幕檔")
}

// savedSubtitle records a saved subtitle file and its metadata.
type savedSubtitle struct {
	Path  string
	Label string
	Lang  string
}

// subtitlesOnlyMode forces effective mode to 0 (best quality) when subtitles-only is enabled.
func subtitlesOnlyMode(mode int, cfg settings) int {
	if cfg.SubtitlesOnly {
		return 0
	}
	return mode
}

var (
	ytSubLineRE = regexp.MustCompile(`(?i)Writing (?:video )?(?:automatic )?subtitles to:\s*(.*)`)
	// yt-dlp 的語言碼是小寫開頭（en、zh-Hant、pt-BR）；大寫開頭的「.Who」之類是標題的一部分，不當語言
	subLangTagRE = regexp.MustCompile(`\.([a-z]{2,3}(?:-[A-Za-z0-9]+)*)$`)
)

// ytSubtitleFilesFromOutput extracts subtitle destination paths from yt-dlp output lines.
func ytSubtitleFilesFromOutput(output string) []string {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`))
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		files = append(files, p)
	}
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r", "\n"), "\n") {
		line := strings.TrimSpace(stripANSI(raw))
		if m := ytSubLineRE.FindStringSubmatch(line); len(m) > 1 {
			add(m[1])
			continue
		}
		if strings.HasPrefix(line, "[download] Destination: ") {
			dest := strings.TrimPrefix(line, "[download] Destination: ")
			ext := strings.ToLower(filepath.Ext(dest))
			if ext == ".vtt" || ext == ".srt" || ext == ".ass" || ext == ".sub" {
				add(dest)
			}
			continue
		}
		if strings.HasPrefix(line, "[SubtitlesConvertor] Destination: ") {
			add(strings.TrimPrefix(line, "[SubtitlesConvertor] Destination: "))
			continue
		}
		if strings.HasPrefix(line, "[download] ") && strings.HasSuffix(line, " has already been downloaded") {
			candidate := strings.TrimSuffix(strings.TrimPrefix(line, "[download] "), " has already been downloaded")
			ext := strings.ToLower(filepath.Ext(candidate))
			if ext == ".vtt" || ext == ".srt" || ext == ".ass" || ext == ".sub" {
				add(candidate)
			}
			continue
		}
	}
	return files
}

// subtitleLangTag extracts a trailing language code from a subtitle file base name.
func subtitleLangTag(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if m := subLangTagRE.FindStringSubmatch(base); len(m) > 1 {
		return m[1]
	}
	return ""
}

// ytSavedSubtitles returns verified subtitle files saved by yt-dlp.
func (a *app) ytSavedSubtitles() []savedSubtitle {
	var out []savedSubtitle
	seen := map[string]bool{}
	for _, raw := range a.lastYtSubs {
		base := strings.TrimSuffix(raw, filepath.Ext(raw))
		srt := base + ".srt"
		chosen := ""
		if validFile(srt, 1) {
			chosen = srt
		} else if validFile(raw, 1) {
			chosen = raw
		}
		if chosen != "" && !seen[chosen] {
			seen[chosen] = true
			label := subtitleLangTag(chosen)
			out = append(out, savedSubtitle{Path: chosen, Label: label, Lang: label})
		}
	}
	return out
}

// finishSubtitlesOnly verifies that subtitles were saved, notes them, and returns an error if none were found.
func (a *app) finishSubtitlesOnly() error {
	if len(a.itemSubs) == 0 {
		a.itemSubs = a.ytSavedSubtitles()
		for _, s := range a.itemSubs {
			label := ""
			if s.Label != "" {
				label = "（" + s.Label + "）"
			}
			a.postLog("✓ 已下載字幕：" + filepath.Base(s.Path) + label + "\r\n")
		}
	}
	if len(a.itemSubs) == 0 {
		return &errNoSubtitles{}
	}
	for _, s := range a.itemSubs {
		a.itemFiles = append(a.itemFiles, s.Path)
	}
	return nil
}

// saveSubtitlesOnly saves subtitles under output folder with base name, returning an error if none were saved.
func (a *app) saveSubtitlesOnly(ctx context.Context, subs []pageSubtitle, pageURL, base, output string, cfg settings) error {
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	saved := a.saveSubtitlesAt(ctx, subs, pageURL, []string{filepath.Join(output, base)}, cfg)
	if len(saved) == 0 {
		return &errNoSubtitles{}
	}
	a.itemSubs = append(a.itemSubs, saved...)
	for _, s := range saved {
		a.itemFiles = append(a.itemFiles, s.Path)
	}
	return nil
}

// saveEpisodeSubtitlesOnly retrieves subtitles for a specific page episode and saves them.
func (a *app) saveEpisodeSubtitlesOnly(ctx context.Context, rawURL, finalURL, body, target string, ep int, output string, cfg settings) error {
	base := a.outputBase(rawURL, pageEpisodeTitle(rawURL, body))
	headers := map[string]string{"referer": finalURL, "user-agent": chromeUserAgent}
	if u, err := url.Parse(finalURL); err == nil && u.Scheme != "" && u.Host != "" {
		headers["origin"] = u.Scheme + "://" + u.Host
	}
	_ = a.applyHLSTracks(ctx, nil, 0, cfg, rawURL, target, headers)
	subs := pageEpisodeSubtitlesFor(body, finalURL, target, ep)
	err := a.saveSubtitlesOnly(ctx, subs, finalURL, base, output, cfg)
	if err != nil && isNoSubtitles(err) {
		return &noSubtitlesFinal{err: err}
	}
	return err
}

// captureSubtitlesOnly handles browser captured streams in subtitles-only mode.
func (a *app) captureSubtitlesOnly(ctx context.Context, rawURL string, candidate browserMediaCandidate, pageTitle, output string, cfg settings) error {
	if g := a.episodeGuard; g != nil {
		if other := g.streamUsedBy(candidate.URL); other != 0 {
			return &errEpisodeRepeated{ep: g.ep, sameAs: other}
		}
	}
	a.lastCaptureStreams = []string{candidate.URL, candidate.PlayedURL}
	base := a.capturedFileBase(rawURL, browserMediaCandidate{PageTitle: pageTitle, Title: candidate.Title}, output)
	return a.saveSubtitlesOnly(ctx, candidate.Subtitles, rawURL, base, output, cfg)
}

// subtitleDoneMessage formats the task card completion summary for subtitles.
func subtitleDoneMessage(subs []savedSubtitle) string {
	if len(subs) == 0 {
		return ""
	}
	var parts []string
	for _, s := range subs {
		name := filepath.Base(s.Path)
		if s.Label != "" {
			parts = append(parts, fmt.Sprintf("%s（%s）", name, s.Label))
		} else {
			parts = append(parts, name)
		}
	}
	return "已存字幕：" + strings.Join(parts, "、")
}

// jobNameFromSubtitleFile derives the show/video title from a saved subtitle file.
func jobNameFromSubtitleFile(path string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	if base == "" {
		return ""
	}
	base = subLangTagRE.ReplaceAllString(base, "")
	return jobNameFromFile(base + ".mp4")
}
