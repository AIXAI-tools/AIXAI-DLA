//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"syscall"
)

type commandRunError struct {
	Cause   error
	Summary string
}

func (e *commandRunError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Summary) != "" {
		return strings.TrimSpace(e.Summary)
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "下載程序執行失敗"
}

func (e *commandRunError) Unwrap() error { return e.Cause }

type batchPartialError struct {
	Failures []string
}

func (e *batchPartialError) Error() string {
	if e == nil || len(e.Failures) == 0 {
		return ""
	}
	preview := e.Failures
	if len(preview) > 8 {
		preview = preview[:8]
	}
	extra := ""
	if len(e.Failures) > len(preview) {
		extra = fmt.Sprintf("\n…另有 %d 項", len(e.Failures)-len(preview))
	}
	return fmt.Sprintf("全部項目已處理完畢，但有 %d 項失敗。\n%s%s", len(e.Failures), strings.Join(preview, "\n"), extra)
}

func isAuthenticationRequired(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	needles := []string{
		"sign in to confirm your age", "sign in to confirm", "login required",
		"authentication required", "members-only", "members only", "private video",
		"age-restricted", "age restricted", "use --cookies-from-browser", "use --cookies",
		"confirm you're not a bot", "confirm you’re not a bot", "requires authentication",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func isCookieAccessFailure(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	needles := []string{
		"could not copy chrome cookie database", "permission denied", "failed to decrypt",
		"could not decrypt", "cookie database", "cookies could not be decrypted",
		"no such table: meta", "browser cookies",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// isExtractorFailure reports a yt-dlp error where the site's extractor ran
// but could not read the page, as opposed to a network, login or file error.
func isExtractorFailure(err error) bool {
	if err == nil || isAuthenticationRequired(err) || errors.Is(err, errStopped) {
		return false
	}
	lower := strings.ToLower(err.Error())
	needles := []string{
		"cannot parse data", "unable to extract", "no video formats found",
		"unable to download json metadata", "unable to parse",
	}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func urlHostKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
}

// errStopped is returned by a download step that ended because 停止 was
// pressed, so no caller mistakes the interrupted step for a finished one.
var errStopped = errors.New("已停止")

// isAntiBotChallenge reports a page refused by an anti-bot check in front of
// the site (not a login, region or rate limit).
func isAntiBotChallenge(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "anti-bot challenge")
}

// isGenericForbidden: yt-dlp's generic extractor found a media link in the
// page but the media server refused it (e.g. the page held an expired
// signed link); the page's own player may still get a working one.
// isPageTimeout: yt-dlp gave up waiting for the web page itself (not for the
// media transfer).
func isPageTimeout(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unable to download webpage") && (strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"))
}

func isGenericForbidden(err error) bool {
	return err != nil && strings.Contains(err.Error(), "[generic]") && isHTTPForbidden(err)
}

func isUnsupportedURL(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unsupported url")
}

// isConnectionDropped reports a connection the server closed or reset
// mid-request (e.g. "forcibly closed by the remote host"), which a second read
// usually gets past.
func isConnectionDropped(err error) bool {
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.Errno(10054)) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "forcibly closed") || strings.Contains(lower, "connection reset")
}
