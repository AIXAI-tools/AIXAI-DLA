//go:build windows

package main

import (
	"os"
	"testing"
)

// TestMain points the default data folder at a temporary folder: tests that
// set appDir would otherwise log into the real Documents data folder.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aixai-test-data-")
	if err != nil {
		panic(err)
	}
	testDataRoot = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
