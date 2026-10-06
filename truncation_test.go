package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func mp4Box(typ string, payload int) []byte {
	b := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(b, uint32(8+payload))
	copy(b[4:], typ)
	return b
}

func writeTemp(t *testing.T, name string, parts ...[]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	var all []byte
	for _, x := range parts {
		all = append(all, x...)
	}
	if err := os.WriteFile(p, all, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMP4Truncated(t *testing.T) {
	full := writeTemp(t, "a.mp4", mp4Box("ftyp", 16), mp4Box("moov", 100), mp4Box("mdat", 1000))
	if mp4Truncated(full) {
		t.Fatal("complete file reported as truncated")
	}
	mdat := mp4Box("mdat", 1000)
	cut := writeTemp(t, "b.mp4", mp4Box("ftyp", 16), mp4Box("moov", 100), mdat[:400])
	if !mp4Truncated(cut) {
		t.Fatal("truncated mdat not detected")
	}
	noMoov := writeTemp(t, "c.mp4", mp4Box("ftyp", 16), mp4Box("mdat", 1000))
	if !mp4Truncated(noMoov) {
		t.Fatal("missing moov (moov-at-end cut) not detected")
	}
	notMP4 := writeTemp(t, "d.mp4", []byte("<html>this is not a video file at all</html>"))
	if mp4Truncated(notMP4) {
		t.Fatal("non-MP4 content must not be judged")
	}
	if mp4Truncated(filepath.Join(t.TempDir(), "missing.mp4")) {
		t.Fatal("missing file must not be judged")
	}
}

func TestDownloadedFilesFromOutput(t *testing.T) {
	out := "[download] Destination: C:\\x\\a [1].mp4\r\n[download]  50.0% of 4MiB\r[download] 100% of 4MiB\n" +
		"[Merger] Merging formats into \"C:\\x\\b.mp4\"\n[download] C:\\x\\c.mp4 has already been downloaded\n"
	got := downloadedFilesFromOutput(out)
	want := []string{`C:\x\a [1].mp4`, `C:\x\b.mp4`, `C:\x\c.mp4`}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

// TestMP4TruncatedRealSample checks real downloads when AIXAI_TRUNC_SAMPLES
// points at a folder with full.mp4 / cut.mp4 (manual verification only).
func TestMP4TruncatedRealSample(t *testing.T) {
	dir := os.Getenv("AIXAI_TRUNC_SAMPLES")
	if dir == "" {
		t.Skip("AIXAI_TRUNC_SAMPLES not set")
	}
	if mp4Truncated(filepath.Join(dir, "full.mp4")) {
		t.Fatal("full.mp4 reported as truncated")
	}
	if !mp4Truncated(filepath.Join(dir, "cut.mp4")) {
		t.Fatal("cut.mp4 not detected")
	}
}

func TestBrowserKeyForProgID(t *testing.T) {
	cases := map[string]string{"MSEdgeHTM": "edge", "ChromeHTML": "chrome", "BraveHTML": "brave", "VivaldiHTM.ABC": "vivaldi", "FirefoxURL-308046B0AF4A39CB": "firefox", "": ""}
	for in, want := range cases {
		if got, _ := browserKeyForProgID(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
