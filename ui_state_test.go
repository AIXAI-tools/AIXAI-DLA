//go:build windows

package main

import "testing"

func TestValidUIState(t *testing.T) {
	for _, s := range []string{`{}`, `{"layout":{"ratio":0.4,"h":{"log":320}},"foldJobs":[1,2],"theme":"dark"}`} {
		if !validUIState([]byte(s)) {
			t.Errorf("rejected %s", s)
		}
	}
	for _, s := range []string{``, `null`, `[]`, `"x"`, `{bad`} {
		if validUIState([]byte(s)) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestUIStateRoundTrip(t *testing.T) {
	a := &app{appShared: &appShared{appDir: t.TempDir()}}
	if got := a.loadUIState(); got != "{}" {
		t.Fatalf("empty = %q", got)
	}
	a.saveUIState(`{"theme":"light"}`)
	a.saveUIState(`not json`)
	if got := a.loadUIState(); got != `{"theme":"light"}` {
		t.Fatalf("loaded %q", got)
	}
}
