//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The page's own view preferences (column widths, section heights, folded
// task cards, theme) are kept in ui_state.json next to settings.json: the
// page is loaded from a string, so the browser's localStorage is not
// available to it. The program only stores the page's JSON object as is.

const (
	uiStateFileName = "ui_state.json"
	uiStateMaxBytes = 64 * 1024
)

func (a *app) uiStatePath() string { return filepath.Join(a.appDir, uiStateFileName) }

// loadUIState returns the saved object, or "{}".
func (a *app) loadUIState() string {
	data, err := os.ReadFile(a.uiStatePath())
	if err != nil || !validUIState(data) {
		return "{}"
	}
	return string(data)
}

func (a *app) saveUIState(s string) {
	if validUIState([]byte(s)) {
		_ = writeFileAtomic(a.uiStatePath(), []byte(s))
	}
}

// validUIState: a JSON object of a sane size.
func validUIState(data []byte) bool {
	var m map[string]json.RawMessage
	return len(data) <= uiStateMaxBytes && json.Unmarshal(data, &m) == nil && m != nil
}
