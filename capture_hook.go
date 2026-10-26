//go:build windows

package main

// captureNavigateHook runs right before a capture opens its page; only test
// builds set it (to simulate a capture tab whose traffic is never seen).
var captureNavigateHook func(*cdpBrowser)
