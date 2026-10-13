package main

import (
	"fmt"
	"path/filepath"
	"unicode/utf16"
)

// Many players and parts of File Explorer cannot open a file whose full path
// is longer than 260 characters (MAX_PATH): the file shows its size but no
// length, and will not play. yt-dlp and FFmpeg handle such paths, so the
// download and the media check still pass. Titles from social posts can be a
// whole paragraph, so the title part of the output template is shortened to
// keep the full path under the limit for the chosen save folder.

const (
	maxPlayablePath = 259 // MAX_PATH minus the terminating NUL
	// Room kept for " [id]" (ids up to 32 characters).
	pathIDReserve = 36
	// Room kept for the longest suffix used while downloading, e.g.
	// ".f1234567890123456v.mp4.part" or ".h264-converting.mp4".
	pathSuffixReserve = 40
	// Below this the title would become unreadable; a folder that long
	// cannot be helped by shortening the file name.
	minTitleBytes = 40
)

// pathUnits counts a path in UTF-16 code units, the unit of MAX_PATH.
func pathUnits(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// titleBudget is how many bytes of title fit after dir. yt-dlp's ".NB"
// truncation counts UTF-8 bytes, which are never fewer than UTF-16 units,
// so the result is a safe bound.
func titleBudget(dir string, fixed int) int {
	dir = filepath.Clean(dir)
	n := maxPlayablePath - pathUnits(dir) - 1 - fixed - pathIDReserve - pathSuffixReserve
	if n < minTitleBytes {
		return minTitleBytes
	}
	return n
}

// outputTemplates returns the single-video and playlist output templates
// for a save folder (title at most 200 bytes, as before).
func outputTemplates(dir string) (single, playlist string) {
	t := titleBudget(dir, 0)
	if t > 200 {
		t = 200
	}
	single = fmt.Sprintf("%%(title).%dB [%%(id)s].%%(ext)s", t)

	// Playlist: "<playlist title>/001 - <title> [id].ext"; the playlist
	// title takes at most a third of the room.
	total := titleBudget(dir, len("/001 - "))
	folder := total / 3
	if folder > 180 {
		folder = 180
	}
	if folder < 20 {
		folder = 20
	}
	title := total - folder
	if title > 170 {
		title = 170
	}
	if title < minTitleBytes {
		title = minTitleBytes
	}
	playlist = fmt.Sprintf("%%(playlist_title).%dB/%%(playlist_index)03d - %%(title).%dB [%%(id)s].%%(ext)s", folder, title)
	return single, playlist
}
