//go:build windows

package main

import (
	"os"
	"testing"
)

// Go has 2000 callback slots and never frees them: a new callback per call
// would end the process here with "too many callback functions".
func TestBrowserTopWindowsReusesCallback(t *testing.T) {
	for i := 0; i < 2500; i++ {
		browserTopWindows(uint32(os.Getpid()))
	}
}
