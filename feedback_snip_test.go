package main

import (
	"encoding/binary"
	"testing"
)

// makeDIB builds a 2x2 CF_DIB block; bottom-up when height > 0.
func makeDIB(bpp int, height int32, compression uint32) []byte {
	stride := (2*bpp + 31) / 32 * 4
	hdr := make([]byte, 40)
	le := binary.LittleEndian
	le.PutUint32(hdr[0:], 40)
	le.PutUint32(hdr[4:], 2)
	le.PutUint32(hdr[8:], uint32(height))
	le.PutUint16(hdr[12:], 1)
	le.PutUint16(hdr[14:], uint16(bpp))
	le.PutUint32(hdr[16:], compression)
	data := append([]byte(nil), hdr...)
	if compression == 3 {
		data = append(data, make([]byte, 12)...)
	}
	// Stored row 0 is red, stored row 1 is blue (BGR order).
	for row := 0; row < 2; row++ {
		line := make([]byte, stride)
		for x := 0; x < 2; x++ {
			p := line[x*bpp/8:]
			if row == 0 {
				p[2] = 255
			} else {
				p[0] = 255
			}
		}
		data = append(data, line...)
	}
	return data
}

func TestDIBToImage(t *testing.T) {
	for _, c := range []struct {
		bpp         int
		height      int32
		compression uint32
		topRed      bool
	}{
		{24, 2, 0, false}, // bottom-up: stored row 0 is the bottom line
		{32, 2, 0, false},
		{32, -2, 0, true}, // top-down
		{32, 2, 3, false}, // bit fields with masks after the header
	} {
		img, err := dibToImage(makeDIB(c.bpp, c.height, c.compression))
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		r, _, b, a := img.At(0, 0).RGBA()
		if a != 0xffff || (c.topRed && (r != 0xffff || b != 0)) || (!c.topRed && (b != 0xffff || r != 0)) {
			t.Fatalf("%+v: top-left pixel r=%x b=%x a=%x", c, r, b, a)
		}
	}
	if _, err := dibToImage([]byte{1, 2, 3}); err == nil {
		t.Fatal("short data must fail")
	}
	if _, err := dibToImage(makeDIB(8, 2, 0)); err == nil {
		t.Fatal("8-bit data must fail")
	}
}
