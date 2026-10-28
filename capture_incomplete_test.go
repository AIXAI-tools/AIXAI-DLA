package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestFFmpegIncompleteReason(t *testing.T) {
	keyFail := "[in#0/hls @ 0x1] Unable to open key file local://offline-key/epoch=1&prefix=x, Invalid data found when processing input\n" +
		"[in#0/hls @ 0x1] Failed to open segment 2 of playlist 0\n" +
		"[in#0/hls @ 0x1] Segment 2 of playlist 0 failed too many times, skipping\n"
	if r := ffmpegIncompleteReason(keyFail); r == "" || r == ffmpegIncompleteReason("Segment 3 of playlist 0 failed too many times, skipping") {
		t.Fatalf("key failure not told apart: %q", r)
	}
	if ffmpegIncompleteReason("Segment 3 of playlist 0 failed too many times, skipping") == "" {
		t.Fatal("skipped segment not detected")
	}
	if r := ffmpegIncompleteReason("Opening 'https://x/a_000001.ts' for reading\nvideo:8000KiB audio:500KiB\n"); r != "" {
		t.Fatalf("complete run reported incomplete: %q", r)
	}
}

func TestRemoveYtDlpLeftovers(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "Show [x]_E012.mp4")
	for _, n := range []string{
		"Show [x]_E012.mp4.part", "Show [x]_E012.mp4.part-Frag3", "Show [x]_E012.mp4.part-Frag3.part", "Show [x]_E012.mp4.ytdl",
		"Show [x]_E012.mp4", "Show [x]_E013.mp4.part", "Show [x]_E012.srt",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	removeYtDlpLeftovers(dest)
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	want := []string{"Show [x]_E012.mp4", "Show [x]_E012.srt", "Show [x]_E013.mp4.part"}
	if len(left) != len(want) {
		t.Fatalf("left %v, want %v", left, want)
	}
	for i := range want {
		if left[i] != want[i] {
			t.Fatalf("left %v, want %v", left, want)
		}
	}
}
