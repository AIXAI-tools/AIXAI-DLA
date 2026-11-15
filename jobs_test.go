package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestSaveJobsKeeps50NewestFinishedAndAllUnfinished(t *testing.T) {
	dir := t.TempDir()
	a := &app{appShared: &appShared{appDir: dir}}

	baseTime := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

	// Build 60 done + 1 paused + 1 failed:
	// 20 done (1..20)
	// 1 paused (21)
	// 25 done (22..46)
	// 1 failed (47)
	// 15 done (48..62)
	// Total done: 20 + 25 + 15 = 60.
	// 1 paused + 1 failed. Total = 62.
	// Oldest 10 done are 1..10.
	// 50 newest done are 11..20 (10), 22..46 (25), 48..62 (15) = 50.
	for i := 1; i <= 20; i++ {
		j := newTestJob(i, jobDone, "https://example.com/item")
		j.Created = baseTime.Add(time.Duration(i) * time.Minute)
		a.jobs = append(a.jobs, j)
	}
	paused := newTestJob(21, jobPaused, "https://example.com/paused")
	paused.Created = baseTime.Add(21 * time.Minute)
	a.jobs = append(a.jobs, paused)

	for i := 22; i <= 46; i++ {
		j := newTestJob(i, jobDone, "https://example.com/item")
		j.Created = baseTime.Add(time.Duration(i) * time.Minute)
		a.jobs = append(a.jobs, j)
	}
	failed := newTestJob(47, jobFailed, "https://example.com/failed")
	failed.Created = baseTime.Add(47 * time.Minute)
	a.jobs = append(a.jobs, failed)

	for i := 48; i <= 62; i++ {
		j := newTestJob(i, jobDone, "https://example.com/item")
		j.Created = baseTime.Add(time.Duration(i) * time.Minute)
		a.jobs = append(a.jobs, j)
	}

	a.saveJobsLocked()

	// In-memory list must not be changed.
	if len(a.jobs) != 62 {
		t.Fatalf("in-memory a.jobs length changed: got %d, want 62", len(a.jobs))
	}

	data, err := os.ReadFile(filepath.Join(dir, jobsFileName))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var saved []*job
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// 50 newest done + 1 paused + 1 failed = 52.
	if len(saved) != 52 {
		t.Fatalf("saved count = %d, want 52", len(saved))
	}

	// Verify IDs and original order.
	var expectedIDs []int
	for i := 11; i <= 20; i++ {
		expectedIDs = append(expectedIDs, i)
	}
	expectedIDs = append(expectedIDs, 21) // paused
	for i := 22; i <= 46; i++ {
		expectedIDs = append(expectedIDs, i)
	}
	expectedIDs = append(expectedIDs, 47) // failed
	for i := 48; i <= 62; i++ {
		expectedIDs = append(expectedIDs, i)
	}

	if len(saved) != len(expectedIDs) {
		t.Fatalf("mismatched expected count: %d vs %d", len(saved), len(expectedIDs))
	}
	for idx, wantID := range expectedIDs {
		if saved[idx].ID != wantID {
			t.Errorf("saved[%d].ID = %d, want %d", idx, saved[idx].ID, wantID)
		}
	}
}

func TestLoadJobsFinishedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := &app{appShared: &appShared{appDir: dir}}

	doneJob := &job{
		ID:         1,
		URLs:       []string{"https://example.com/1"},
		Items:      []jobItem{{State: "done", Msg: "OK", Brief: ""}},
		State:      jobDone,
		Message:    "全部完成。",
		Name:       "影片名稱A",
		LogFile:    `C:\logs\1.log`,
		Pos:        1,
		AutoResume: true, // Should be ignored and cleared for finished jobs
		Created:    time.Now(),
	}
	partialJob := &job{
		ID:      2,
		URLs:    []string{"https://example.com/2", "https://example.com/3"},
		Items:   []jobItem{{State: "done", Msg: "OK"}, {State: "failed", Msg: "404", Brief: "網址失效"}},
		State:   jobPartial,
		Message: "完成，1 項失敗；成功的項目已保留。",
		Name:    "影片名稱B",
		LogFile: `C:\logs\2.log`,
		Pos:     2,
		Created: time.Now(),
	}
	abortedJob := &job{
		ID:      3,
		URLs:    []string{"https://example.com/4"},
		Items:   []jobItem{{State: "pending"}},
		State:   jobAborted,
		Message: "已中止。",
		Name:    "影片名稱C",
		LogFile: `C:\logs\3.log`,
		Pos:     0,
		Created: time.Now(),
	}
	queuedJob := &job{
		ID:      4,
		URLs:    []string{"https://example.com/5"},
		Items:   []jobItem{{State: "running", Msg: "downloading"}},
		State:   jobQueued,
		Message: "",
		Name:    "影片名稱D",
		Created: time.Now(),
	}

	a.jobs = []*job{doneJob, partialJob, abortedJob, queuedJob}
	a.saveJobsLocked()

	// Load in a fresh app instance.
	a2 := &app{appShared: &appShared{appDir: dir}}
	resumed := a2.loadJobs()
	if resumed != 0 {
		t.Fatalf("loadJobs resumed = %d, want 0", resumed)
	}
	if len(a2.jobs) != 4 {
		t.Fatalf("loaded jobs count = %d, want 4", len(a2.jobs))
	}

	// Done job checks: state, message, items, Name, LogFile unchanged; AutoResume ignored.
	j1 := a2.jobs[0]
	if j1.State != jobDone || j1.Message != "全部完成。" || j1.Name != "影片名稱A" || j1.LogFile != `C:\logs\1.log` {
		t.Errorf("job 1 unexpected: state=%s, msg=%s, name=%s, log=%s", j1.State, j1.Message, j1.Name, j1.LogFile)
	}
	if j1.AutoResume {
		t.Errorf("job 1 AutoResume should be false")
	}
	if len(j1.Items) != 1 || j1.Items[0].State != "done" || j1.Items[0].Msg != "OK" {
		t.Errorf("job 1 items unexpected: %#v", j1.Items)
	}

	// Partial job checks
	j2 := a2.jobs[1]
	if j2.State != jobPartial || j2.Message != "完成，1 項失敗；成功的項目已保留。" || j2.Name != "影片名稱B" || j2.LogFile != `C:\logs\2.log` {
		t.Errorf("job 2 unexpected: state=%s, msg=%s, name=%s, log=%s", j2.State, j2.Message, j2.Name, j2.LogFile)
	}
	if len(j2.Items) != 2 || j2.Items[1].State != "failed" || j2.Items[1].Brief != "網址失效" {
		t.Errorf("job 2 items unexpected: %#v", j2.Items)
	}

	// Aborted job checks
	j3 := a2.jobs[2]
	if j3.State != jobAborted || j3.Message != "已中止。" || j3.Name != "影片名稱C" || j3.LogFile != `C:\logs\3.log` {
		t.Errorf("job 3 unexpected: state=%s, msg=%s, name=%s, log=%s", j3.State, j3.Message, j3.Name, j3.LogFile)
	}

	// Queued job checks: becomes paused as before.
	j4 := a2.jobs[3]
	if j4.State != jobPaused {
		t.Errorf("job 4 state = %s, want %s", j4.State, jobPaused)
	}
	if j4.Message != "上次關閉程式時尚未完成，按「繼續」接著下載。" {
		t.Errorf("job 4 msg = %s", j4.Message)
	}
	if len(j4.Items) != 1 || j4.Items[0].State != "pending" {
		t.Errorf("job 4 items not reset to pending: %#v", j4.Items)
	}
}

func TestLoadedDoneJobNotStartedByQueue(t *testing.T) {
	dir := t.TempDir()
	a := &app{appShared: &appShared{appDir: dir}}

	doneJob := &job{
		ID:      1,
		URLs:    []string{"https://example.com/done"},
		Items:   []jobItem{{State: "done"}},
		State:   jobDone,
		Message: "全部完成。",
		Created: time.Now(),
	}
	queuedJob := &job{
		ID:      2,
		URLs:    []string{"https://example.com/queued"},
		Items:   []jobItem{{State: "pending"}},
		State:   jobQueued,
		Created: time.Now(),
	}

	a.jobs = []*job{doneJob}
	a.saveJobsLocked()

	a2 := &app{appShared: &appShared{appDir: dir}}
	a2.loadJobs()

	// pickRunnable on only loaded done job should return nothing.
	if picked := pickRunnable(a2.jobs, 2); len(picked) != 0 {
		t.Fatalf("picked %d jobs from done only, want 0", len(picked))
	}

	// With a queued job added, pickRunnable should pick only the queued job.
	a2.jobs = append(a2.jobs, queuedJob)
	picked := pickRunnable(a2.jobs, 2)
	if len(picked) != 1 || picked[0].ID != 2 {
		t.Fatalf("picked %#v, want only queued job (ID 2)", picked)
	}
}

func TestClearFinishedJobsRemovesLoadedFinished(t *testing.T) {
	dir := t.TempDir()
	a := &app{appShared: &appShared{appDir: dir}}

	doneJob := &job{
		ID:      1,
		URLs:    []string{"https://example.com/done"},
		Items:   []jobItem{{State: "done"}},
		State:   jobDone,
		Message: "全部完成。",
		Created: time.Now(),
	}
	partialJob := &job{
		ID:      2,
		URLs:    []string{"https://example.com/part"},
		Items:   []jobItem{{State: "done"}},
		State:   jobPartial,
		Message: "部分完成",
		Created: time.Now(),
	}
	abortedJob := &job{
		ID:      3,
		URLs:    []string{"https://example.com/abort"},
		Items:   []jobItem{{State: "pending"}},
		State:   jobAborted,
		Message: "已中止",
		Created: time.Now(),
	}
	pausedJob := &job{
		ID:      4,
		URLs:    []string{"https://example.com/paused"},
		Items:   []jobItem{{State: "pending"}},
		State:   jobPaused,
		Created: time.Now(),
	}

	a.jobs = []*job{doneJob, partialJob, abortedJob, pausedJob}
	a.saveJobsLocked()

	// Load in a fresh app instance.
	a2 := &app{appShared: &appShared{appDir: dir}}
	a2.loadJobs()
	if len(a2.jobs) != 4 {
		t.Fatalf("loaded jobs = %d, want 4", len(a2.jobs))
	}

	// Clear finished.
	a2.clearFinishedJobs()

	// In memory: only paused remains.
	if len(a2.jobs) != 1 || a2.jobs[0].ID != 4 {
		t.Fatalf("after clearFinishedJobs in-memory count = %d, want 1 (paused)", len(a2.jobs))
	}

	// Check file: only paused remains, finished are gone.
	data, err := os.ReadFile(filepath.Join(dir, jobsFileName))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var fileJobs []*job
	if err := json.Unmarshal(data, &fileJobs); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(fileJobs) != 1 || fileJobs[0].ID != 4 {
		t.Fatalf("file jobs after clear = %d, want 1 (paused)", len(fileJobs))
	}

	// Test when only finished jobs exist and are cleared.
	a3 := &app{appShared: &appShared{appDir: dir}}
	a3.jobs = []*job{doneJob}
	a3.saveJobsLocked()

	a4 := &app{appShared: &appShared{appDir: dir}}
	a4.loadJobs()
	a4.clearFinishedJobs()

	if len(a4.jobs) != 0 {
		t.Fatalf("in-memory = %d, want 0", len(a4.jobs))
	}
	data, err = os.ReadFile(filepath.Join(dir, jobsFileName))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	fileJobs = nil
	if err := json.Unmarshal(data, &fileJobs); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(fileJobs) != 0 {
		t.Fatalf("file jobs count = %d, want 0", len(fileJobs))
	}
}
