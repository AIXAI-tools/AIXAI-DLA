package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Some players encrypt HLS with standard AES-128 but write a key address that
// only the player itself understands (e.g. "local://…"); the player replaces it
// and fetches the 16-byte key from the site. yt-dlp and FFmpeg cannot open such
// an address, so only the unencrypted first segments were saved.
//
// The browser capture keeps the 16-byte responses the page fetched while it
// played. Before FFmpeg downloads such a stream, each kept key is tried on the
// first encrypted segment (the decrypted data must start like MPEG-TS) and the
// matching one is written next to a local copy of the playlist whose key
// address points at it. Only keys the page itself fetched to play the video
// are used; nothing is guessed or derived.

const (
	aesKeySize = 16
	// maxKeyResponseBytes bounds a key response as counted by DevTools
	// (headers included).
	maxKeyResponseBytes = 4096
	// hlsKeyWait is how long the capture page stays open for the player to
	// fetch the key once an encrypted manifest has been seen.
	hlsKeyWait = 15 * time.Second
	// maxCaptureKeys bounds the 16-byte responses kept per capture.
	maxCaptureKeys = 16
	// maxPlaylistBytes bounds a fetched playlist.
	maxPlaylistBytes = 4 << 20
)

type capturedKey struct {
	URL string
	Key []byte
}

func (a *app) resetCaptureKeys() {
	a.captureKeysMu.Lock()
	a.captureKeys = nil
	a.captureKeysMu.Unlock()
}

func (a *app) noteCaptureKey(keyURL string, key []byte) {
	a.captureKeysMu.Lock()
	defer a.captureKeysMu.Unlock()
	for _, k := range a.captureKeys {
		if bytes.Equal(k.Key, key) {
			return
		}
	}
	if len(a.captureKeys) < maxCaptureKeys {
		a.captureKeys = append(a.captureKeys, capturedKey{URL: keyURL, Key: append([]byte(nil), key...)})
	}
}

func (a *app) captureKeyCount() int {
	a.captureKeysMu.Lock()
	defer a.captureKeysMu.Unlock()
	return len(a.captureKeys)
}

func (a *app) captureKeysSnapshot() []capturedKey {
	a.captureKeysMu.Lock()
	defer a.captureKeysMu.Unlock()
	return append([]capturedKey(nil), a.captureKeys...)
}

// mayBeKeyResponse reports a response type an AES key can come as (binary or
// unlabelled), so text, pages, scripts and images are never read.
func mayBeKeyResponse(mime string) bool {
	m := strings.ToLower(mime)
	for _, no := range []string{"json", "html", "text", "javascript", "image", "css", "xml", "font", "mpegurl"} {
		if strings.Contains(m, no) {
			return false
		}
	}
	return true
}

var (
	hlsAttrRE    = regexp.MustCompile(`([A-Z0-9-]+)=("[^"]*"|[^,]*)`)
	hlsBandwidth = regexp.MustCompile(`(?:^|,)BANDWIDTH=(\d+)`)
)

func hlsAttrs(line string) map[string]string {
	_, rest, _ := strings.Cut(line, ":")
	out := map[string]string{}
	for _, m := range hlsAttrRE.FindAllStringSubmatch(rest, -1) {
		out[m[1]] = strings.Trim(m[2], `"`)
	}
	return out
}

// isPlayerOnlyKeyURI: an AES key address neither yt-dlp nor FFmpeg can fetch.
func isPlayerOnlyKeyURI(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme == "" {
		return false // relative: fetched next to the playlist
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "data", "skd":
		return false
	}
	return true
}

// playerOnlyKey returns the first AES-128 key address of a media playlist
// that only the player can fetch.
func playerOnlyKey(playlist string) (string, bool) {
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-KEY:") {
			continue
		}
		at := hlsAttrs(line)
		if strings.EqualFold(at["METHOD"], "AES-128") && isPlayerOnlyKeyURI(at["URI"]) {
			return at["URI"], true
		}
	}
	return "", false
}

// bestHLSVariant returns the highest-bandwidth variant of a master playlist ("" for
// a media playlist).
func bestHLSVariant(playlist, base string) string {
	best, bestBW, take := "", int64(-1), int64(-1)
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			take = 0
			if m := hlsBandwidth.FindStringSubmatch(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:")); m != nil {
				take, _ = strconv.ParseInt(m[1], 10, 64)
			}
			continue
		}
		if take >= 0 && line != "" && !strings.HasPrefix(line, "#") {
			if take > bestBW {
				best, bestBW = resolveHLSURI(base, line), take
			}
			take = -1
		}
	}
	return best
}

func resolveHLSURI(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// firstPlayerKeySegment returns the first segment encrypted with a
// player-only key and its IV (the IV attribute, else the media sequence number).
func firstPlayerKeySegment(playlist, base string) (string, []byte, bool) {
	seq := int64(0)
	var iv []byte
	keyed := false
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")), 10, 64)
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			at := hlsAttrs(line)
			keyed = strings.EqualFold(at["METHOD"], "AES-128") && isPlayerOnlyKeyURI(at["URI"])
			iv = nil
			if v := strings.TrimPrefix(strings.TrimPrefix(at["IV"], "0x"), "0X"); v != "" {
				if b, err := hex.DecodeString(v); err == nil && len(b) == aes.BlockSize {
					iv = b
				}
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			if keyed {
				if iv == nil {
					iv = make([]byte, aes.BlockSize)
					binary.BigEndian.PutUint64(iv[8:], uint64(seq))
				}
				return resolveHLSURI(base, line), iv, true
			}
			seq++
		}
	}
	return "", nil, false
}

// keyOpensSegment reports whether key decrypts the start of an encrypted
// segment into MPEG-TS (sync byte 0x47 at the first two 188-byte packets).
func keyOpensSegment(key, iv, head []byte) bool {
	const n = 192 // 12 AES blocks: covers the second TS sync byte at 188
	if len(key) != aesKeySize || len(iv) != aes.BlockSize || len(head) < n {
		return false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return false
	}
	out := make([]byte, n)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, head[:n])
	return out[0] == 0x47 && out[188] == 0x47
}

// localKeyPlaylist rewrites a media playlist for FFmpeg: every address becomes
// absolute and each player-only key address points at keyFile.
func localKeyPlaylist(playlist, base, keyFile string) string {
	keyRef := filepath.ToSlash(keyFile)
	uriRE := regexp.MustCompile(`URI="([^"]*)"`)
	lines := strings.Split(strings.ReplaceAll(playlist, "\r\n", "\n"), "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#EXT-X-KEY:") || strings.HasPrefix(t, "#EXT-X-MAP:"):
			lines[i] = uriRE.ReplaceAllStringFunc(t, func(m string) string {
				uri := uriRE.FindStringSubmatch(m)[1]
				if strings.HasPrefix(t, "#EXT-X-KEY:") && isPlayerOnlyKeyURI(uri) {
					return `URI="` + keyRef + `"`
				}
				return `URI="` + resolveHLSURI(base, uri) + `"`
			})
		case t != "" && !strings.HasPrefix(t, "#"):
			lines[i] = resolveHLSURI(base, t)
		}
	}
	return strings.Join(lines, "\n")
}

// fetchForKey reads up to limit bytes of a stream address with the request
// context the browser used; a server refusing the page's Referer/Origin is
// asked once more without them (as in downloadBrowserCapturedMedia).
func fetchForKey(ctx context.Context, raw string, headers map[string]string, limit int64) ([]byte, error) {
	get := func(withReferer bool) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, 0, err
		}
		for _, name := range []string{"user-agent", "cookie", "authorization", "accept-language", "referer", "origin"} {
			if !withReferer && (name == "referer" || name == "origin") {
				continue
			}
			if v := sanitizeCapturedHeader(captureHeader(headers, name)); v != "" {
				req.Header.Set(name, v)
			}
		}
		if limit < maxPlaylistBytes {
			req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		return body, resp.StatusCode, err
	}
	body, status, err := get(true)
	if status == http.StatusForbidden {
		body, _, err = get(false)
	}
	return body, err
}

// errNoPlayerKey: the stream needs a key the page never fetched (or none of
// the fetched ones opens it).
var errNoPlayerKey = errors.New("串流有加密，但沒有取得能解開它的播放器金鑰")

// prepareKeyedPlaylist returns a local playlist for a stream encrypted with a
// player-only key ("" when the stream needs none). cleanup removes the
// temporary playlist and key file.
func (a *app) prepareKeyedPlaylist(ctx context.Context, c browserMediaCandidate) (string, func(), error) {
	none := func() {}
	if c.Kind != "HLS" {
		return "", none, nil
	}
	base := c.URL
	text, err := fetchForKey(ctx, base, c.RequestHeaders, maxPlaylistBytes)
	if err != nil {
		return "", none, nil // FFmpeg reports the stream's own error
	}
	if v := bestHLSVariant(string(text), base); v != "" {
		base = v
		if text, err = fetchForKey(ctx, base, c.RequestHeaders, maxPlaylistBytes); err != nil {
			return "", none, nil
		}
	}
	playlist := string(text)
	if _, ok := playerOnlyKey(playlist); !ok {
		return "", none, nil
	}
	keys := a.captureKeysSnapshot()
	segURL, iv, ok := firstPlayerKeySegment(playlist, base)
	if len(keys) == 0 || !ok {
		return "", none, errNoPlayerKey
	}
	head, err := fetchForKey(ctx, segURL, c.RequestHeaders, 192)
	if err != nil {
		return "", none, fmt.Errorf("%w（讀取加密片段失敗：%v）", errNoPlayerKey, err)
	}
	var key *capturedKey
	for i := range keys {
		if keyOpensSegment(keys[i].Key, iv, head) {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return "", none, errNoPlayerKey
	}
	dir, err := os.MkdirTemp("", "aixai-hls-")
	if err != nil {
		return "", none, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	keyFile := filepath.Join(dir, "key.bin")
	local := filepath.Join(dir, "stream.m3u8")
	if err := os.WriteFile(keyFile, key.Key, 0600); err != nil {
		cleanup()
		return "", none, err
	}
	if err := os.WriteFile(local, []byte(localKeyPlaylist(playlist, base, keyFile)), 0600); err != nil {
		cleanup()
		return "", none, err
	}
	a.postLog(fmt.Sprintf("✓ 串流有加密，使用播放器取得的金鑰解密（金鑰來源：%s）。\r\n", hostOnly(key.URL)))
	return local, cleanup, nil
}
