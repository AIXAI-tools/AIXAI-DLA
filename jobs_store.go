//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (a *app) jobsPath() string { return filepath.Join(a.appDir, jobsFileName) }

// saveJobsLocked writes tasks to tasks.json; jobsMu must be held.
// Unfinished tasks are always kept. Among finished tasks (done, aborted,
// partial), only the 50 newest by Created are kept. The tasks are written
// in a.jobs order, and the in-memory list is not changed.
func (a *app) saveJobsLocked() {
	var finished []*job
	for _, j := range a.jobs {
		if isJobFinished(j) {
			finished = append(finished, j)
		}
	}
	var keepFinished map[*job]bool
	if len(finished) > maxFinishedJobsKept {
		// Sort descending by Created (newest first). Break ties by higher ID.
		sort.SliceStable(finished, func(i, j int) bool {
			if !finished[i].Created.Equal(finished[j].Created) {
				return finished[i].Created.After(finished[j].Created)
			}
			return finished[i].ID > finished[j].ID
		})
		keepFinished = make(map[*job]bool, maxFinishedJobsKept)
		for _, j := range finished[:maxFinishedJobsKept] {
			keepFinished[j] = true
		}
	}
	keep := make([]*job, 0, len(a.jobs))
	for _, j := range a.jobs {
		if j == nil {
			continue
		}
		if !isJobFinished(j) || keepFinished == nil || keepFinished[j] {
			keep = append(keep, j)
		}
	}
	data, err := json.MarshalIndent(keep, "", "  ")
	if err != nil {
		return
	}
	a.notePersistError("任務清單", writeFileAtomic(a.jobsPath(), data))
}

// loadJobs restores the tasks from tasks.json. Finished tasks (done,
// partial, aborted) keep their state, message, items, Name and LogFile.
// Unfinished tasks come back paused, except tasks paused for a version
// update, which are queued again right after that update. Returns how
// many were queued again.
func (a *app) loadJobs() (resumed int) {
	data, err := os.ReadFile(a.jobsPath())
	if err != nil {
		return
	}
	var list []*job
	if json.Unmarshal(data, &list) != nil {
		return
	}
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for _, j := range list {
		if j == nil || len(j.URLs) == 0 {
			continue
		}
		if len(j.Items) != len(j.URLs) {
			j.Items = make([]jobItem, len(j.URLs))
		}
		if isJobFinished(j) {
			j.AutoResume = false
			if j.Pos < 0 || j.Pos > len(j.URLs) {
				j.Pos = 0
			}
			a.jobs = append(a.jobs, j)
			if j.ID >= a.nextJobID {
				a.nextJobID = j.ID
			}
			continue
		}
		for i := range j.Items {
			if j.Items[i].State == "" || j.Items[i].State == "running" {
				j.Items[i].State = "pending"
			}
		}
		if j.State == jobQueued || j.State == jobRunning || j.State == "" {
			j.State = jobPaused
			j.Message = "上次關閉程式時尚未完成，按「繼續」接著下載。"
		}
		if j.AutoResume {
			if j.State == jobPaused && launchedByUpdate() {
				j.State, j.Message = jobQueued, ""
				resumed++
			}
			j.AutoResume = false
		}
		if j.Pos < 0 || j.Pos > len(j.URLs) {
			j.Pos = 0
		}
		a.jobs = append(a.jobs, j)
		if j.ID >= a.nextJobID {
			a.nextJobID = j.ID
		}
	}
	return resumed
}

// clearFinishedJobs removes finished and aborted cards.
func (a *app) clearFinishedJobs() {
	a.jobsMu.Lock()
	var keep []*job
	for _, j := range a.jobs {
		if !isJobFinished(j) {
			keep = append(keep, j)
		}
	}
	a.jobs = keep
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	a.emitAllJobs()
}
