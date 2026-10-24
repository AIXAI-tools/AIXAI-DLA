package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeedsYtDlpPlugin(t *testing.T) {
	cases := map[string]bool{
		"https://www.bilibili.com/video/BV0000000000":     true,
		"https://m.bilibili.com/video/BV0000000000":       true,
		"https://b23.tv/abc":                              true,
		"https://www.bilibili.tv/en/video/1000000000":     false,
		"https://www.youtube.com/watch?v=x":               false,
		"https://notbilibili.com/video/BV0000000000":      false,
		"https://example.com/?u=https://www.bilibili.com": false,
		"": false,
	}
	for raw, want := range cases {
		if got := needsYtDlpPlugin(raw); got != want {
			t.Errorf("needsYtDlpPlugin(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestEnsureYtDlpPluginWritesAndRepairs(t *testing.T) {
	a := &appShared{binDir: t.TempDir()}
	path := filepath.Join(a.ytDlpPluginDir(), "aixai", "yt_dlp_plugins", "extractor", "aixai_fixes.py")
	if err := a.ensureYtDlpPlugin(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, aixaiFixesPlugin) {
		t.Fatalf("plugin not written: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureYtDlpPlugin(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, aixaiFixesPlugin) {
		t.Fatal("stale plugin was not replaced")
	}
}

func TestEmbeddedPluginRetiresItself(t *testing.T) {
	src := string(aixaiFixesPlugin)
	for _, want := range []string{
		"if not hasattr(_BundledBiliBiliIE, '_extract_part'):",
		"plugin_name='aixai'",
		"return super()._real_extract(url)",
		"def _extract_part(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("plugin missing %q", want)
		}
	}
	if strings.Contains(src, "\r\n") {
		t.Error("plugin must keep LF line endings")
	}
}

func TestBuildArgsLoadsPluginOnlyForCoveredSites(t *testing.T) {
	a := &app{appShared: &appShared{binDir: t.TempDir()}}
	hasPlugin := func(args []string) bool {
		for i, v := range args {
			if v == "--plugin-dirs" && i+1 < len(args) && args[i+1] == a.ytDlpPluginDir() {
				return true
			}
		}
		return false
	}
	args, err := a.buildArgs("https://www.bilibili.com/video/BV0000000000", 3, "", "", settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasPlugin(args) || args[len(args)-1] != "https://www.bilibili.com/video/BV0000000000" {
		t.Fatalf("bilibili args = %q", args)
	}
	args, err = a.buildArgs("https://www.youtube.com/watch?v=x", 3, "", "", settings{})
	if err != nil {
		t.Fatal(err)
	}
	if hasPlugin(args) {
		t.Fatalf("plugin loaded for an unrelated site: %q", args)
	}
}
