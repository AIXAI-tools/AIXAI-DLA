//go:build windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCustomFileName(t *testing.T) {
	dir := `C:\Users\x\Downloads`
	cases := map[string]string{
		"  我的影片  ":       "我的影片",
		"a/b:c?.mp4":     "a_b_c_",
		"旅行.MKV":         "旅行",
		"CON":            "CON_",
		"nul.mp4":        "nul_",
		"...":            "",
		"50% 折扣.":        "50% 折扣",
		"clip.part2.mp4": "clip.part2",
	}
	for in, want := range cases {
		if got := customFileName(in, dir); got != want {
			t.Errorf("customFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := customFileName(strings.Repeat("名", 300), dir)
	if len(long) > 200 || long == "" {
		t.Errorf("long name kept %d bytes", len(long))
	}
	deep := `C:\` + strings.Repeat(`很深的資料夾\`, 20)
	if n := pathUnits(filepath.Join(deep, customFileName(strings.Repeat("a", 300), deep))) + pathSuffixReserve; n > maxPlayablePath && !saveFolderTooLong(deep) {
		t.Errorf("deep folder path %d > %d", n, maxPlayablePath)
	}
}

func TestReserveCustomName(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"影片.mp4", "影片 (2).mkv", "續傳.mp4.part"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	defer releaseCustomNames(901)
	defer releaseCustomNames(902)
	if got := reserveCustomName(dir, "影片", 901); got != "影片 (3)" {
		t.Errorf("existing file: got %q", got)
	}
	// Another running task asking for the same name gets the next one.
	if got := reserveCustomName(dir, "影片", 902); got != "影片 (4)" {
		t.Errorf("held by other task: got %q", got)
	}
	// The same task (resumed) keeps its name.
	if got := reserveCustomName(dir, "影片 (3)", 901); got != "影片 (3)" {
		t.Errorf("own name: got %q", got)
	}
	// An unfinished download of the name is continued, not renamed.
	if got := reserveCustomName(dir, "續傳", 901); got != "續傳" {
		t.Errorf("partial download: got %q", got)
	}
	releaseCustomNames(902)
	if got := reserveCustomName(dir, "影片", 903); got != "影片 (4)" {
		t.Errorf("after release: got %q", got)
	}
	releaseCustomNames(903)
	if got := reserveCustomName(dir, "新名稱", 904); got != "新名稱" {
		t.Errorf("free name: got %q", got)
	}
	releaseCustomNames(904)
}

func TestBuildArgsCustomFileName(t *testing.T) {
	a := &app{appShared: &appShared{}}
	out := t.TempDir()
	cfg := settings{FileName: "我的 50% 影片"}
	args, err := a.buildArgs("https://video.example/watch/1", 0, "", out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "-o")
	if i < 0 || args[i+1] != "我的 50%% 影片%(playlist_index&_{}|)s.%(ext)s" {
		t.Fatalf("output template: %v", args)
	}
	// A collection address keeps the usual names.
	args, _ = a.buildArgs("https://video.example/watch/1", 2, "", out, cfg)
	if i := slices.Index(args, "-o"); strings.Contains(args[i+1], "我的") {
		t.Errorf("collection used the custom name: %s", args[i+1])
	}
}

func TestCustomFileNameOutputBase(t *testing.T) {
	a := &app{cfg: settings{FileName: "自訂"}}
	if got := a.outputBase("https://video.example/play/9/3", "標題"); got != "自訂" {
		t.Errorf("outputBase = %q", got)
	}
	if got := a.capturedFileBase("https://video.example/v", browserMediaCandidate{Title: "標題"}, `C:\d`); got != "自訂" {
		t.Errorf("capturedFileBase = %q", got)
	}
}
