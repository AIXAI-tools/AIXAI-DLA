//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// 自訂檔名: a task with one address (not 連續集數) may be saved under a name
// the user typed instead of the video title. The name is made safe for
// Windows and short enough for the save folder; when a file of that name is
// already there, or another running task holds it, " (2)", " (3)"… is added.

// mediaExtRE matches an extension the user may have typed with the name.
var mediaExtRE = regexp.MustCompile(`(?i)\.(mp4|mkv|webm|mov|m4a|mp3|aac|flac|wav|opus|ts|flv|avi)$`)

// reservedDeviceRE matches names Windows keeps for devices (CON, NUL, COM1…).
var reservedDeviceRE = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])$`)

// customFileName is the typed name as a file name (without extension) that
// fits dir, or "" when nothing usable is left.
func customFileName(name, dir string) string {
	s := safeWindowsBaseName(mediaExtRE.ReplaceAllString(strings.TrimSpace(name), ""))
	if reservedDeviceRE.MatchString(s) {
		s += "_"
	}
	// The name takes the room a title and " [id]" would (as 連續集數 劇名).
	limit := titleBudget(dir, 0) + pathIDReserve
	if limit > 200 {
		limit = 200
	}
	s, _ = truncateUTF8(s, limit)
	return strings.TrimRight(s, " .")
}

var customNames = struct {
	sync.Mutex
	held map[string]int // lower-case full path without extension → task ID
}{held: map[string]int{}}

// reserveCustomName returns base, or base with " (N)", that no finished file
// in dir and no other running task uses, and holds it for task jobID.
func reserveCustomName(dir, base string, jobID int) string {
	taken := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			// "name.mp4" is taken; "name.mp4.part" or "name.f123.mp4" (an
			// unfinished download of the same name) is continued instead.
			n := e.Name()
			taken[strings.ToLower(strings.TrimSuffix(n, filepath.Ext(n)))] = true
		}
	}
	customNames.Lock()
	defer customNames.Unlock()
	name := base
	for i := 2; ; i++ {
		key := strings.ToLower(filepath.Join(dir, name))
		if owner, held := customNames.held[key]; !taken[strings.ToLower(name)] && (!held || owner == jobID) {
			customNames.held[key] = jobID
			return name
		}
		name = fmt.Sprintf("%s (%d)", base, i)
	}
}

// releaseCustomNames frees the names task jobID held.
func releaseCustomNames(jobID int) {
	customNames.Lock()
	defer customNames.Unlock()
	for k, id := range customNames.held {
		if id == jobID {
			delete(customNames.held, k)
		}
	}
}

// customTemplate is the yt-dlp output template of a custom name. An address
// that turns out to hold several videos numbers them name_1, name_2…
func customTemplate(name string) string {
	return strings.ReplaceAll(name, "%", "%%") + "%(playlist_index&_{}|)s.%(ext)s"
}
