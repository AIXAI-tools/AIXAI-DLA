package main

import "testing"

func TestSiteKey(t *testing.T) {
	cases := map[string]string{
		"https://www.example.com/watch/1":      "example.com",
		"https://media.cdn.example.com/a.m3u8": "example.com",
		"https://shop.example.com.tw/p/1":      "example.com.tw",
		"https://video.example.co.jp/v/1":      "example.co.jp",
		"https://example.org/x":                "example.org",
		"https://a.b.example.io/x?y=1":         "example.io",
	}
	for in, want := range cases {
		if got := siteKey(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func newTestJob(id int, state string, urls ...string) *job {
	return &job{ID: id, State: state, URLs: urls, Items: make([]jobItem, len(urls))}
}

func TestPickRunnableSameSiteWaits(t *testing.T) {
	jobs := []*job{
		newTestJob(1, jobRunning, "https://www.site-a.com/1"),
		newTestJob(2, jobQueued, "https://m.site-a.com/2"),                       // same site as task 1: waits
		newTestJob(3, jobQueued, "https://site-b.com/1"),                         // free
		newTestJob(4, jobQueued, "https://site-b.com/2"),                         // same site as task 3 (just chosen): waits
		newTestJob(5, jobQueued, "https://site-c.com/1", "https://site-a.com/9"), // touches site-a: waits
		newTestJob(6, jobQueued, "https://site-d.com/1"),
	}
	got := pickRunnable(jobs, 3)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 6 {
		ids := []int{}
		for _, j := range got {
			ids = append(ids, j.ID)
		}
		t.Fatalf("picked %v, want [3 6]", ids)
	}
}

func TestPickRunnableLimit(t *testing.T) {
	jobs := []*job{
		newTestJob(1, jobQueued, "https://a.com/1"),
		newTestJob(2, jobQueued, "https://b.com/1"),
		newTestJob(3, jobQueued, "https://c.com/1"),
		newTestJob(4, jobPaused, "https://d.com/1"),
	}
	if got := pickRunnable(jobs, 2); len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("limit not respected: %d picked", len(got))
	}
	jobs[0].State, jobs[1].State = jobRunning, jobRunning
	if got := pickRunnable(jobs, 2); len(got) != 0 {
		t.Fatal("no slot is free")
	}
}

func TestPickRunnableUsesRemainingItems(t *testing.T) {
	// A running task that is past its site-a items no longer blocks site-a.
	running := newTestJob(1, jobRunning, "https://site-a.com/1", "https://site-b.com/1")
	running.Pos = 1
	jobs := []*job{running, newTestJob(2, jobQueued, "https://site-a.com/2")}
	if got := pickRunnable(jobs, 3); len(got) != 1 || got[0].ID != 2 {
		t.Fatal("site-a should be free once task 1 moved past it")
	}
}

func TestTagLines(t *testing.T) {
	got := tagLines("a\r\n\r\nb\r\n", "[任務2] ")
	if got != "[任務2] a\r\n\r\n[任務2] b\r\n" {
		t.Fatalf("got %q", got)
	}
}
