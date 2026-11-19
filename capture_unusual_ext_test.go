package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestIsUnusualExtensionError(t *testing.T) {
	a := &app{}
	realMsg := "ERROR: The extracted extension ('en.UVPRsig') is unusual and will be skipped for safety reasons."
	if !a.isUnusualExtensionError(errors.New(realMsg)) {
		t.Fatalf("expected true for unusual extension error")
	}
	a2 := &app{lastCmdOutput: realMsg}
	if !a2.isUnusualExtensionError(errors.New("exit status 1")) {
		t.Fatalf("expected true when message is in lastCmdOutput")
	}
	if a.isUnusualExtensionError(errors.New("HTTP Error 403: Forbidden")) {
		t.Fatalf("expected false for 403 error")
	}
	if a.isUnusualExtensionError(nil) {
		t.Fatalf("expected false for nil")
	}
}

func TestCapturedFFmpegInputArgs(t *testing.T) {
	hlsURL := "https://example.com/master.m3u8"
	gotHLS := capturedFFmpegInputArgs(browserMediaCandidate{Kind: "HLS", URL: hlsURL}, hlsURL, false)
	wantHLS := []string{"-allowed_segment_extensions", "ALL", "-extension_picky", "0", "-i", hlsURL}
	if !reflect.DeepEqual(gotHLS, wantHLS) {
		t.Fatalf("HLS args mismatch: got %v want %v", gotHLS, wantHLS)
	}

	mp4URL := "https://example.com/video.mp4"
	gotMP4 := capturedFFmpegInputArgs(browserMediaCandidate{Kind: "MP4", URL: mp4URL}, mp4URL, false)
	wantMP4 := []string{"-i", mp4URL}
	if !reflect.DeepEqual(gotMP4, wantMP4) {
		t.Fatalf("MP4 args mismatch: got %v want %v", gotMP4, wantMP4)
	}

	localKeyed := "C:/tmp/keyed.m3u8"
	gotKeyed := capturedFFmpegInputArgs(browserMediaCandidate{Kind: "HLS", URL: hlsURL}, localKeyed, true)
	wantKeyed := []string{"-i", localKeyed}
	if !reflect.DeepEqual(gotKeyed, wantKeyed) {
		t.Fatalf("keyed args mismatch: got %v want %v", gotKeyed, wantKeyed)
	}
}

func TestWithoutRefererFFmpegKeepsExtensionOptions(t *testing.T) {
	in := []string{
		"-y",
		"-headers", "Referer: https://example.com/\r\nOrigin: https://example.com\r\n",
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-i", "https://example.com/master.m3u8",
	}
	got := withoutRefererFFmpeg(in)
	want := []string{
		"-y",
		"-allowed_segment_extensions", "ALL",
		"-extension_picky", "0",
		"-i", "https://example.com/master.m3u8",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withoutRefererFFmpeg mismatch: got %v want %v", got, want)
	}
}
