//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestHistoryCSVKeepsFormulasAsText(t *testing.T) {
	out := string(historyCSV([]historyEntry{{Time: time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local), State: "failed",
		Title: "=HYPERLINK(\"x\")", URL: "https://x.example/1", Reason: "-1 error"}}))
	for _, want := range []string{`"'=HYPERLINK(""x"")"`, "'-1 error", ",https://x.example/1,"} {
		if !strings.Contains(out, want) {
			t.Errorf("csv missing %q:\n%s", want, out)
		}
	}
	if got := csvText("一般標題"); got != "一般標題" {
		t.Errorf("plain text changed: %q", got)
	}
}
