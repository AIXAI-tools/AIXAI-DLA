package main

import (
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"testing"
)

const keyedPlaylist = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:5\n#EXT-X-MEDIA-SEQUENCE:0\n" +
	"#EXTINF:5.000000,\nv_000000.ts\n#EXTINF:5.000000,\nv_000001.ts\n#EXTINF:5.000000,\n" +
	"#EXT-X-KEY:METHOD=AES-128,URI=\"local://offline-key/epoch=1&prefix=abc\",IV=0x00000000000000000000000000000002\n" +
	"v_000002.ts\n#EXTINF:5.000000,\nv_000003.ts\n#EXT-X-ENDLIST\n"

func TestPlayerOnlyKey(t *testing.T) {
	if uri, ok := playerOnlyKey(keyedPlaylist); !ok || !strings.HasPrefix(uri, "local://offline-key/") {
		t.Fatalf("got %q %v", uri, ok)
	}
	for _, p := range []string{
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://k.example/key\"\na.ts\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\na.ts\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://drm\"\na.ts\n",
		"#EXTM3U\na.ts\n",
	} {
		if _, ok := playerOnlyKey(p); ok {
			t.Fatalf("fetchable or DRM key taken as player-only: %q", p)
		}
	}
}

func TestFirstPlayerKeySegmentAndLocalPlaylist(t *testing.T) {
	base := "https://cdn.example/books/1/720p/v_720p.m3u8"
	seg, iv, ok := firstPlayerKeySegment(keyedPlaylist, base)
	if !ok || seg != "https://cdn.example/books/1/720p/v_000002.ts" || iv[15] != 2 {
		t.Fatalf("got %q %x %v", seg, iv, ok)
	}
	// No IV attribute: the media sequence number is the IV.
	noIV := strings.Replace(keyedPlaylist, ",IV=0x00000000000000000000000000000002", "", 1)
	if _, iv, _ := firstPlayerKeySegment(noIV, base); iv[15] != 2 {
		t.Fatalf("sequence IV %x", iv)
	}
	out := localKeyPlaylist(keyedPlaylist, base, `C:\Temp\aixai-hls-1\key.bin`)
	if !strings.Contains(out, `URI="C:/Temp/aixai-hls-1/key.bin"`) || strings.Contains(out, "local://") {
		t.Fatalf("key not rewritten:\n%s", out)
	}
	if !strings.Contains(out, "\nhttps://cdn.example/books/1/720p/v_000000.ts\n") || !strings.Contains(out, "IV=0x00000000000000000000000000000002") {
		t.Fatalf("segments/IV not kept:\n%s", out)
	}
}

func TestBestHLSVariant(t *testing.T) {
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=480x854\n480p/a.m3u8\n" +
		"#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=1,BANDWIDTH=2500000,RESOLUTION=720x1280\n720p/a.m3u8\n"
	if got := bestHLSVariant(master, "https://cdn.example/x/master.m3u8"); got != "https://cdn.example/x/720p/a.m3u8" {
		t.Fatalf("got %q", got)
	}
	if bestHLSVariant(keyedPlaylist, "https://cdn.example/x/v.m3u8") != "" {
		t.Fatal("media playlist taken as master")
	}
}

func TestKeyOpensSegment(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, 16)
	iv[15] = 2
	plain := make([]byte, 384) // whole AES blocks
	plain[0], plain[188] = 0x47, 0x47
	block, _ := aes.NewCipher(key)
	enc := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, plain)
	if !keyOpensSegment(key, iv, enc) {
		t.Fatal("right key rejected")
	}
	if keyOpensSegment([]byte("fedcba9876543210"), iv, enc) {
		t.Fatal("wrong key accepted")
	}
	if keyOpensSegment(key, iv, enc[:100]) {
		t.Fatal("short data accepted")
	}
}

func TestMayBeKeyResponse(t *testing.T) {
	for _, m := range []string{"application/octet-stream", "binary/octet-stream", ""} {
		if !mayBeKeyResponse(m) {
			t.Fatalf("%q rejected", m)
		}
	}
	for _, m := range []string{"application/json", "text/html", "image/gif", "application/vnd.apple.mpegurl"} {
		if mayBeKeyResponse(m) {
			t.Fatalf("%q accepted", m)
		}
	}
}
