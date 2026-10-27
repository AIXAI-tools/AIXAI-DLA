//go:build windows

package main

import (
	"slices"
	"testing"
)

// A 連續集數 item retried from 無法下載的網址 keeps the task's file naming and
// its own episode number (it used to fall back to Title_E001).
func TestRetryFailedKeepsSequenceNaming(t *testing.T) {
	a := testApp(t)
	a.closing.Store(true) // queue the retry task without starting it
	a.loadFailedOnce.Do(func() {})
	cfg := settings{Sequence: true, SequenceStart: 1, SequenceEnd: 50, SeriesName: "劇名", EpisodeFormat: "zh", EpisodeSep: "dash"}
	base := "https://example.com/play/123/"
	a.failed = []failedEntry{
		{ID: 1, URL: base + "1", Item: 1, Cfg: cfg},
		{ID: 2, URL: base + "9", Item: 9, Cfg: cfg},
	}
	if msg := a.retryFailed(nil); msg != "" {
		t.Fatal(msg)
	}
	if len(a.jobs) != 1 {
		t.Fatalf("jobs = %d", len(a.jobs))
	}
	j := a.jobs[0]
	if !j.Cfg.Sequence || !slices.Equal(j.Cfg.Episodes, []int{1, 9}) {
		t.Fatalf("retry cfg: sequence=%v episodes=%v", j.Cfg.Sequence, j.Cfg.Episodes)
	}
	w := &app{cfg: j.Cfg}
	for i, want := range []string{"劇名-第001集", "劇名-第009集"} {
		w.curItem = i
		if got := w.outputBase(j.URLs[i], "Title"); got != want {
			t.Errorf("item %d: %q, want %q", i, got, want)
		}
	}
	// The retry fails again: its entry still knows the episode.
	if ep := sequenceEpisodeAt(j.Cfg, 1); ep != 9 {
		t.Errorf("re-failed episode = %d", ep)
	}
}

// An address whose episode cannot be told is retried as before.
func TestRetryFailedUnknownEpisode(t *testing.T) {
	a := testApp(t)
	a.closing.Store(true)
	a.loadFailedOnce.Do(func() {})
	cfg := settings{Sequence: true, SequenceStart: 1, SequenceEnd: 0}
	a.failed = []failedEntry{{ID: 1, URL: "https://example.com/v/1", Item: 1, Cfg: cfg}}
	a.retryFailed(nil)
	if c := a.jobs[0].Cfg; c.Sequence || c.Episodes != nil {
		t.Errorf("sequence=%v episodes=%v", c.Sequence, c.Episodes)
	}
}
