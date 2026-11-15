//go:build windows

package main

import (
	"io/fs"
	"strings"
	"testing"
)

// The page is assembled from ui/ parts (webui.go). The split was checked once
// against the original single file (2026-11-15, byte-identical); this test
// keeps the assembly honest as the parts change: every part is present, in
// order, and no marker is left.
func TestAssembleUI(t *testing.T) {
	read := func(p string) string {
		b, err := uiFS.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if len(b) == 0 {
			t.Fatalf("%s is empty", p)
		}
		return string(b)
	}
	parts := []string{read("ui/style.css"), read("ui/dialogs.html")}
	entries, err := fs.ReadDir(uiFS, "ui/js")
	if err != nil || len(entries) == 0 {
		t.Fatalf("ui/js: %d files, %v", len(entries), err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			parts = append(parts, read("ui/js/"+e.Name()))
		}
	}

	got := assembleUI()
	if got != uiHTML {
		t.Fatalf("uiHTML differs from assembleUI()")
	}
	if strings.Contains(got, "<!--@") {
		t.Errorf("assembled HTML still contains a marker")
	}
	pos := 0
	for i, p := range parts {
		at := strings.Index(got[pos:], p)
		if at < 0 {
			t.Fatalf("part %d is missing or out of order", i)
		}
		pos += at + len(p)
	}
	for _, want := range []string{"<style>", "</style>", "<dialog id=\"dlgTerms\">", "<script>", "</script>", "TERMS_VERSION", "</html>"} {
		if !strings.Contains(got, want) {
			t.Errorf("assembled HTML lacks %q", want)
		}
	}
}
