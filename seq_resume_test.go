//go:build windows

package main

import (
	"encoding/json"
	"testing"
)

func TestSeqFilesSurviveResume(t *testing.T) {
	j := &job{ID: 1}
	a := &app{appShared: &appShared{}, job: j, itemOffset: 0, curItem: 0}
	a.rememberSeqFile("aaa", seqFile{item: 0, path: `C:\v\E001.mp4`})
	// Saved with the task and read back after a restart.
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var back job
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	// The next run starts at item 1 (paused after the first episode).
	w := &app{appShared: a.appShared, job: &back, itemOffset: 1}
	got := w.savedSeqFiles()
	if f, ok := got["aaa"]; !ok || f.item != 0 || f.path != `C:\v\E001.mp4` {
		t.Fatalf("seq files after resume = %#v", got)
	}
	if (&app{appShared: &appShared{}}).savedSeqFiles() == nil {
		t.Fatal("nil map without a task")
	}
}
