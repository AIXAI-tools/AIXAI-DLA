//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestEpisodeNamingJoin(t *testing.T) {
	cases := []struct {
		n    episodeNaming
		want string
	}{
		{episodeNaming{}, "Show_E003"},
		{episodeNaming{Series: "ABC"}, "ABC_E003"},
		{episodeNaming{Series: "ABC", Format: "zh", Sep: "space"}, "ABC 第003集"},
		{episodeNaming{Series: "ABC", Format: "zh", Sep: "none"}, "ABC第003集"},
		{episodeNaming{Series: "ABC", Format: "E", Sep: "dash"}, "ABC-E003"},
		{episodeNaming{Series: "ABC", Format: "EP", Sep: "space_dash"}, "ABC - EP003"},
		{episodeNaming{Series: "ABC", Format: "Episode", Sep: "underscore"}, "ABC_Episode 003"},
		{episodeNaming{Series: "ABC", Format: "EP", Sep: "space_underscore"}, "ABC _ EP003"},
		{episodeNaming{Format: "zh"}, "Show 第003集"}, // separator defaults to a space
		{episodeNaming{Series: "A:B", Format: "E", Sep: "space"}, "A_B E003"},
	}
	for _, c := range cases {
		if got := c.n.join("Show", 3); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.n, got, c.want)
		}
	}
}

func TestCapturedOutputBaseNamedDefaultUnchanged(t *testing.T) {
	for _, u := range []string{"https://x/play/16465/3", "https://x/detail/watch/some-show-3/3", "https://x/en/film/some-title-123/watch?ep=2", "https://x/v/abc"} {
		if a, b := capturedOutputBase(u, "Title"), capturedOutputBaseNamed(u, "Title", episodeNaming{}, 7); a != b {
			t.Errorf("%s: default naming changed %q -> %q", u, a, b)
		}
	}
	n := episodeNaming{Series: "ABC", Format: "zh", Sep: "space"}
	if got := capturedOutputBaseNamed("https://x/play/16465/3", "Long Title - Site", n, 9); got != "ABC 第003集" {
		t.Errorf("address number = %q", got)
	}
	// No number in the address: the batch's own number.
	if got := capturedOutputBaseNamed("https://x/v/abc", "Title", n, 9); got != "ABC 第009集" {
		t.Errorf("sequence number = %q", got)
	}
	if got := capturedOutputBaseNamed("https://x/play/16465/3", "Long Title - Site", episodeNaming{Format: "EP", Sep: "dash"}, 3); got != "Long Title - Site-EP003" {
		t.Errorf("title kept = %q", got)
	}
}

func TestCustomSequenceTemplate(t *testing.T) {
	cfg := settings{Sequence: true, SequenceStart: 5, SequenceEnd: 7}
	a := &app{cfg: cfg, itemOffset: 1, curItem: 1} // third number: 7
	if got := a.customSequenceTemplate(cfg, `C:\Videos`); got != "" {
		t.Errorf("no naming chosen: %q", got)
	}
	cfg.SeriesName, cfg.EpisodeFormat, cfg.EpisodeSep = "50%劇", "EP", "space"
	a.cfg = cfg
	if got := a.customSequenceTemplate(cfg, `C:\Videos`); got != "50%%劇 EP007.%(ext)s" {
		t.Errorf("series template = %q", got)
	}
	cfg.SeriesName = ""
	a.cfg = cfg
	if got := a.customSequenceTemplate(cfg, `C:\Videos`); !strings.HasPrefix(got, "%(title).") || !strings.HasSuffix(got, "B EP007.%(ext)s") {
		t.Errorf("title template = %q", got)
	}
	// Two addresses: numbers start over for the second one.
	a.itemOffset, a.curItem = 3, 1
	if ep := a.sequenceEpisode(); ep != 6 {
		t.Errorf("second address episode = %d", ep)
	}
	cfg.Sequence = false
	a.cfg = cfg
	if got := a.customSequenceTemplate(cfg, `C:\Videos`); got != "" {
		t.Errorf("not a sequence: %q", got)
	}
}

func TestJobNameFromFile(t *testing.T) {
	cases := map[string]string{
		`C:\d\Show - Page - Site_E042.mp4`: "Show - Page - Site",
		`C:\d\ABC 第001集.mp4`:               "ABC",
		`C:\d\ABC - EP012.mp4`:             "ABC",
		`C:\d\ABC_Episode 003.mkv`:         "ABC",
		`C:\d\Some Video [abc123].mp4`:     "Some Video",
		`C:\d\Office123.mp4`:               "Office123",
		`C:\d\E001.mp4`:                    "E001",
	}
	for in, want := range cases {
		if got := jobNameFromFile(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func testJob(id int, state, url string) *job {
	return &job{ID: id, State: state, URLs: []string{url}, Items: make([]jobItem, 1)}
}

func TestMoveAndYield(t *testing.T) {
	// 1, 2, 3 running; 4 queued on the same site as 1. Moving 4 above 1
	// makes 1 step aside.
	j1 := testJob(1, jobRunning, "https://www.site-a.com/1")
	j2 := testJob(2, jobRunning, "https://www.site-b.com/1")
	j3 := testJob(3, jobRunning, "https://www.site-c.com/1")
	j4 := testJob(4, jobQueued, "https://m.site-a.com/2")
	jobs := moveJobInList([]*job{j1, j2, j3, j4}, j4, j1, false)
	if ids := jobIDs(jobs); ids != "4123" {
		t.Fatalf("order = %s", ids)
	}
	y := yieldingJobs(jobs, 8)
	if len(y) != 1 || y[0] != j1 {
		t.Fatalf("yield = %v", y)
	}
	// Card order without the move: nobody yields.
	if y := yieldingJobs([]*job{j1, j2, j3, j4}, 8); len(y) != 0 {
		t.Fatalf("unexpected yield %v", y)
	}
	// Slot limit: 4 (another site) moved up takes the last running one's slot.
	j4.URLs = []string{"https://www.site-d.com/1"}
	if y := yieldingJobs(moveJobInList([]*job{j1, j2, j3, j4}, j4, j1, false), 3); len(y) != 1 || y[0] != j3 {
		t.Fatalf("slot yield = %v", y)
	}
	// Moved after the target.
	if ids := jobIDs(moveJobInList([]*job{j1, j2, j3, j4}, j1, j3, true)); ids != "2314" {
		t.Fatalf("after order = %s", ids)
	}
	// A task already being paused keeps its site; a yielding task moved back
	// up keeps going.
	j1.stopAs = jobPaused
	j4.URLs = []string{"https://m.site-a.com/2"}
	if y := yieldingJobs([]*job{j4, j1}, 8); len(y) != 0 {
		t.Fatalf("paused task yielded: %v", y)
	}
	j1.stopAs = jobYield
	yieldingJobs([]*job{j1, j4}, 8)
	if j1.stopAs != "" {
		t.Fatalf("yield not cancelled")
	}
}

func jobIDs(jobs []*job) string {
	var b strings.Builder
	for _, j := range jobs {
		b.WriteString(string(rune('0' + j.ID)))
	}
	return b.String()
}
