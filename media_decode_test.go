//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCountErrorLines(t *testing.T) {
	if n := countErrorLines("a\r\nb\n\n  \nc"); n != 3 {
		t.Fatalf("got %d", n)
	}
	if n := countErrorLines(""); n != 0 {
		t.Fatalf("empty got %d", n)
	}
	wrapped := fmt.Errorf("capture: %w", &mediaCheckError{File: "x.mp4", Reason: undecodableReason})
	if !isUndecodable(wrapped) {
		t.Error("wrapped undecodable not recognised")
	}
	if isUndecodable(&mediaCheckError{File: "x.mp4", Reason: "只有聲音、沒有影像"}) {
		t.Error("other check failure taken for undecodable")
	}
}

// TestUndecodable makes a normal H.264 MP4 and a copy whose picture data is
// scrambled while the header stays intact (what the reported files look
// like): only the scrambled one must fail. Needs the program's FFmpeg.
func TestUndecodable(t *testing.T) {
	bin := filepath.Join(os.Getenv("LOCALAPPDATA"), "AIXAI-YTDLP-OneClick", "bin")
	ffmpeg := filepath.Join(bin, "ffmpeg.exe")
	if !validFile(ffmpeg, 1<<20) {
		t.Skip("FFmpeg not installed")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-t", "5",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", good)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot make sample: %v %s", err, out)
	}
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	// faststart puts moov first; scramble everything inside mdat.
	i := bytes.Index(data, []byte("mdat"))
	if i < 0 {
		t.Fatal("no mdat")
	}
	bad := append([]byte(nil), data...)
	for k := i + 4; k < len(bad); k++ {
		bad[k] ^= byte(0x5A + k*7)
	}
	badPath := filepath.Join(dir, "bad.mp4")
	if err := os.WriteFile(badPath, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{appShared: &appShared{binDir: bin, ffmpegPath: ffmpeg}}
	info := mediaInfo{HasVideo: true}
	if a.undecodable(context.Background(), good, info) {
		t.Error("normal file judged undecodable")
	}
	if !a.undecodable(context.Background(), badPath, info) {
		t.Error("scrambled file passed")
	}
	if a.undecodable(context.Background(), badPath, mediaInfo{}) {
		t.Error("audio-only file must not be decoded as video")
	}
}
