package main

import "testing"

func TestClassifySiteURLBilibili(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.bilibili.com/video/BV0000000000": "bilibili",
		"https://b23.tv/abc":                          "bilibili",
		"https://www.bilibili.tv/en/video/1000000000": "bilibili",
		"https://m.bilibili.tv/video/1":               "bilibili",
		"https://notbilibili.tv/video/1":              "",
	} {
		if got := classifySiteURL(raw); got != want {
			t.Errorf("classifySiteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
