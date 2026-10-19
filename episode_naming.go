//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// File names of a 連續集數 batch. By default an episode is saved as
// "<title>_E003", as before. The user may give the series name, the episode
// label (第003集, E003, EP003, Episode 003) and what joins the two; the
// batch's own number (SequenceStart + item) is used when the address does
// not carry one.

// episodeFormats are the 集數格式 choices; "" is the default "_E003".
var episodeFormats = map[string]string{
	"zh":      "第%03d集",
	"E":       "E%03d",
	"EP":      "EP%03d",
	"Episode": "Episode %03d",
}

// episodeSeps are the 連接符號 choices.
var episodeSeps = map[string]string{
	"none":             "",
	"space":            " ",
	"dash":             "-",
	"underscore":       "_",
	"space_dash":       " - ",
	"space_underscore": " _ ",
}

const defaultEpisodeSep = "space"

type episodeNaming struct {
	Series string // 劇名; empty = the title the download would use anyway
	Format string // key of episodeFormats; empty = default "_E003"
	Sep    string // key of episodeSeps (ignored for the default format)
}

func (n episodeNaming) custom() bool { return n.Series != "" || episodeFormats[n.Format] != "" }

// label is the episode part with what joins it to the name, e.g. " 第003集".
func (n episodeNaming) label(ep int) string {
	f, ok := episodeFormats[n.Format]
	if !ok {
		return fmt.Sprintf("_E%03d", ep)
	}
	sep, ok := episodeSeps[n.Sep]
	if !ok {
		sep = episodeSeps[defaultEpisodeSep]
	}
	return sep + fmt.Sprintf(f, ep)
}

// join names episode ep of name; the series name, when given, replaces name.
func (n episodeNaming) join(name string, ep int) string {
	if s := safeWindowsBaseName(n.Series); s != "" {
		name = s
	}
	return safeWindowsBaseName(name + n.label(ep))
}

// namingFor is the naming of a batch; only 連續集數 batches have one.
func namingFor(cfg settings) episodeNaming {
	if !cfg.Sequence {
		return episodeNaming{}
	}
	return episodeNaming{Series: strings.TrimSpace(cfg.SeriesName), Format: cfg.EpisodeFormat, Sep: cfg.EpisodeSep}
}

// sequenceEpisode is the number 連續集數 gave the current item (0 outside a
// sequence batch). Several addresses expand to one run of numbers each.
func (a *app) sequenceEpisode() int {
	cfg := a.cfg
	if !cfg.Sequence || cfg.SequenceStart <= 0 || cfg.SequenceEnd < cfg.SequenceStart {
		return 0
	}
	count := cfg.SequenceEnd - cfg.SequenceStart + 1
	return cfg.SequenceStart + (a.itemOffset+a.curItem)%count
}

// outputBase names a page or capture download of rawURL with the batch's
// naming (capturedOutputBase when there is none).
func (a *app) outputBase(rawURL, title string) string {
	return capturedOutputBaseNamed(rawURL, title, namingFor(a.cfg), a.sequenceEpisode())
}

// customSequenceTemplate is the yt-dlp output template of a 連續集數 item
// when the user chose a naming, or "" to keep the usual template.
func (a *app) customSequenceTemplate(cfg settings, dir string) string {
	n := namingFor(cfg)
	ep := a.sequenceEpisode()
	if !n.custom() || ep <= 0 {
		return ""
	}
	label := strings.ReplaceAll(n.label(ep), "%", "%%")
	if s := safeWindowsBaseName(n.Series); s != "" {
		return strings.ReplaceAll(s, "%", "%%") + label + ".%(ext)s"
	}
	t := titleBudget(dir, len(label))
	if t > 200 {
		t = 200
	}
	return fmt.Sprintf("%%(title).%dB", t) + label + ".%(ext)s"
}

var (
	jobNameIDRE = regexp.MustCompile(`\s*\[[^\[\]]*\]$`)
	jobNameEpRE = regexp.MustCompile(`(?i)(\s*[_\-]?\s*第\s*\d+\s*集|[\s_\-]+(?:episode|ep|e)\s*\d+)$`)
)

// jobNameFromFile is the name a task card shows, taken from the first saved
// file: without extension, yt-dlp's " [id]" and the episode number.
func jobNameFromFile(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = jobNameIDRE.ReplaceAllString(base, "")
	if s := strings.TrimSpace(jobNameEpRE.ReplaceAllString(base, "")); s != "" {
		base = s
	}
	return strings.TrimSpace(base)
}
