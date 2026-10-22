package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakePNG is a disguise picture like the ones seen in the wild (70 bytes).
func fakePNG() []byte {
	b := append([]byte{}, disguisePNGSig...)
	b = append(b, make([]byte, 13+4)...)                      // IHDR data + CRC
	b = append(b, 0, 0, 0, 10, 'I', 'D', 'A', 'T')            // IDAT
	b = append(b, make([]byte, 10+4)...)                      // data + CRC
	b = append(b, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 1, 2, 3, 4) // IEND + CRC
	return b
}

// tsSegment is n TS packets whose payload also holds a stray 0x89 byte.
func tsSegment(n int, fill byte) []byte {
	var b []byte
	for i := 0; i < n; i++ {
		p := make([]byte, tsPacketSize)
		p[0] = 0x47
		for k := 1; k < len(p); k++ {
			p[k] = fill
		}
		p[100] = 0x89
		b = append(b, p...)
	}
	return b
}

func TestStripDisguiseHeaders(t *testing.T) {
	var in, want []byte
	// Segment sizes chosen so that one picture straddles the 4 MiB read block.
	sizes := []int{3, 22306, 5, 40}
	for i, n := range sizes {
		seg := tsSegment(n, byte(i+1))
		in = append(in, fakePNG()...)
		in = append(in, seg...)
		want = append(want, seg...)
	}
	if !bytes.Contains(in[4<<20-200:4<<20], disguisePNGSig[:4]) {
		t.Fatal("test data: no picture near the end of the first read block")
	}
	var out bytes.Buffer
	n, err := stripDisguiseHeaders(context.Background(), bytes.NewReader(in), &out)
	if err != nil || n != len(sizes) {
		t.Fatalf("dropped %d, err %v; want %d", n, err, len(sizes))
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("output differs (got %d bytes, want %d)", out.Len(), len(want))
	}
}

func TestStripKeepsRealPNGInStream(t *testing.T) {
	// A PNG signature not followed by TS packets is data, not a disguise.
	in := append(tsSegment(2, 1), fakePNG()...)
	in = append(in, []byte("not ts at all, just bytes........")...)
	var out bytes.Buffer
	n, err := stripDisguiseHeaders(context.Background(), bytes.NewReader(in), &out)
	if err != nil || n != 0 || !bytes.Equal(out.Bytes(), in) {
		t.Fatalf("dropped %d, err %v, same %v", n, err, bytes.Equal(out.Bytes(), in))
	}
}

func TestStripDisguiseHeadersCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	n, err := stripDisguiseHeaders(ctx, bytes.NewReader(append(fakePNG(), tsSegment(3, 1)...)), &out)
	if !errors.Is(err, context.Canceled) || n != 0 || out.Len() != 0 {
		t.Fatalf("cancelled: dropped %d, wrote %d, err %v", n, out.Len(), err)
	}
}

type cancelStripWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelStripWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.cancel()
	return n, err
}

func TestStripDisguiseHeadersStopsBetweenBlocks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := bytes.Repeat([]byte{0x47}, 8<<20)
	out := &cancelStripWriter{cancel: cancel}
	_, err := stripDisguiseHeaders(ctx, bytes.NewReader(in), out)
	if !errors.Is(err, context.Canceled) || out.Len() != 4<<20 {
		t.Fatalf("wrote %d bytes after cancellation, err %v", out.Len(), err)
	}
}

func TestIsDisguisedTS(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.mp4")
	os.WriteFile(good, append(fakePNG(), tsSegment(5, 1)...), 0644)
	plain := filepath.Join(dir, "b.mp4")
	os.WriteFile(plain, tsSegment(5, 1), 0644)
	image := filepath.Join(dir, "c.png")
	os.WriteFile(image, append(fakePNG(), make([]byte, 1000)...), 0644)
	if !isDisguisedTS(good) || isDisguisedTS(plain) || isDisguisedTS(image) {
		t.Fatalf("isDisguisedTS: good %v plain %v image %v", isDisguisedTS(good), isDisguisedTS(plain), isDisguisedTS(image))
	}
}
