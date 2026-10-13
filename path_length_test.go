package main

import (
	"strings"
	"testing"
)

func TestOutputTemplatesKeepPathPlayable(t *testing.T) {
	// A long post text saved under a 53-character folder.
	dir := `C:\Users\User\OneDrive\Documents\Video Downloads\abcd`
	single, _ := outputTemplates(dir)
	if single != "%(title).129B [%(id)s].%(ext)s" {
		t.Fatalf("single = %q", single)
	}
	title := strings.Repeat("😈 long post text ｜ ", 40)
	b := []byte(title)[:129]
	full := dir + `\` + strings.ToValidUTF8(string(b), "") + " [" + strings.Repeat("9", 32) + "].f1234567890123456v.mp4.part"
	if n := pathUnits(full); n > maxPlayablePath {
		t.Fatalf("path is %d units, want <= %d", n, maxPlayablePath)
	}
}

func TestOutputTemplatesShortFolderCap(t *testing.T) {
	single, playlist := outputTemplates(`D:\V`)
	if single != "%(title).178B [%(id)s].%(ext)s" {
		t.Fatalf("single = %q", single)
	}
	if !strings.HasPrefix(playlist, "%(playlist_title).57B/%(playlist_index)03d - %(title).114B ") {
		t.Fatalf("playlist = %q", playlist)
	}
}

func TestOutputTemplatesVeryLongFolder(t *testing.T) {
	single, playlist := outputTemplates(`C:\` + strings.Repeat("a", 240))
	if single != "%(title).40B [%(id)s].%(ext)s" {
		t.Fatalf("single = %q", single)
	}
	if !strings.Contains(playlist, "%(playlist_title).20B/") || !strings.Contains(playlist, "%(title).40B ") {
		t.Fatalf("playlist = %q", playlist)
	}
}

func TestPathUnitsCountsSurrogates(t *testing.T) {
	if n := pathUnits("a😈文"); n != 4 {
		t.Fatalf("pathUnits = %d, want 4", n)
	}
}
