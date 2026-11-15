//go:build windows

package main

import (
	"cmp"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// Download tasks ("jobs"). Every press of 開始下載 adds one task; up to
// settings.MaxJobs tasks run at the same time, each on its own *app (own stop
// flag, process, log file). Tasks for the same website never run together:
// a site sees one task's requests at a time, which keeps the request rate of
// a single download even without sign-in (sites also block by IP).
// Tasks are saved to tasks.json: unfinished tasks come back paused next
// time (except tasks paused for an update, which resume). Up to 50 newest
// finished tasks (done, aborted, partial) are also kept across restarts
// in their original order and state until removed by 「清除已完成」.
// The order of a.jobs is the order of the cards and the priority: tasks
// start in that order, and moving a task up (drag, 優先下載) makes a lower
// running task that is in its way step aside (jobYield) and queue again.

const (
	jobQueued  = "queued"
	jobRunning = "running"
	jobPaused  = "paused"
	jobDone    = "done"
	jobPartial = "partial"
	jobFailed  = "failed"
	jobAborted = "aborted"
	// jobYield is a stopAs only: the task steps aside for a task moved
	// above it and is queued again, resuming at the item it stopped at.
	jobYield = "yield"

	maxJobsLimit        = 8
	jobsFileName        = "tasks.json"
	defaultMaxJobs      = 2
	maxFinishedJobsKept = 50
)

type jobItem struct {
	State string `json:"state"` // pending, running, done, failed
	Msg   string `json:"msg,omitempty"`
	Brief string `json:"brief,omitempty"` // short Chinese reason of a failure (error_brief.go)
}

type job struct {
	ID       int       `json:"id"`
	URLs     []string  `json:"urls"`
	Items    []jobItem `json:"items"`
	Pos      int       `json:"pos"` // first item 繼續 starts from
	Mode     int       `json:"mode"`
	FormatID string    `json:"formatId"`
	Output   string    `json:"output"`
	Cfg      settings  `json:"cfg"`
	State    string    `json:"state"`
	Message  string    `json:"message,omitempty"`
	LogFile  string    `json:"logFile,omitempty"`
	Created  time.Time `json:"created"`
	// Name is the series or video name the card shows: the 劇名 the user
	// gave, else taken from the first saved file.
	Name string `json:"name,omitempty"`
	// AutoResume marks a task paused only for a version update; the updated
	// program resumes it (tasks the user paused are left paused).
	AutoResume bool `json:"autoResume,omitempty"`
	// SeqHashes are the files a 連續集數 task already checked (media_check.go).
	SeqHashes map[string]seqRecord `json:"seqHashes,omitempty"`
	// EpisodeHashes / EpisodeStreams: the ?ep=N episodes already saved, by
	// file hash and by stream (episode_capture.go).
	EpisodeHashes  map[string]int `json:"episodeHashes,omitempty"`
	EpisodeStreams map[string]int `json:"episodeStreams,omitempty"`

	notes  []string // shown once, when the task first starts
	status string   // latest status line while running
	stopAs string   // jobPaused or jobAborted while a stop is in progress
	worker *app
}

// jobView is what the page shows for a task.
type jobView struct {
	ID      int       `json:"id"`
	Title   string    `json:"title"`
	Name    string    `json:"name,omitempty"` // 劇名 shown on the card
	Order   int       `json:"order"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Brief   string    `json:"brief,omitempty"` // short Chinese reason when the task failed
	Status  string    `json:"status,omitempty"`
	URLs    []string  `json:"urls"`
	Items   []jobItem `json:"items"`
	Output  string    `json:"output"`
	Folder  string    `json:"folder"`
	Sites   []string  `json:"sites"`
	Waiting string    `json:"waiting,omitempty"`
}

func clampMaxJobs(n int) int {
	if n <= 0 {
		return defaultMaxJobs
	}
	if n > maxJobsLimit {
		return maxJobsLimit
	}
	return n
}

// siteKey groups addresses by website: the registrable domain
// (example.com, example.com.tw), so sub-domains count as the same site.
func siteKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	labels := strings.Split(strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")), ".")
	n := len(labels)
	if n <= 2 {
		return strings.Join(labels, ".")
	}
	switch labels[n-2] {
	case "com", "co", "net", "org", "gov", "edu", "ac", "or", "ne", "go":
		if len(labels[n-1]) == 2 {
			return strings.Join(labels[n-3:], ".")
		}
	}
	return strings.Join(labels[n-2:], ".")
}

func jobSites(urls []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urls {
		if k := siteKey(u); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func (j *job) view() jobView {
	sites := jobSites(j.URLs)
	name := j.Name
	if name != "" && !j.Cfg.Sequence && len(j.URLs) > 1 {
		name += fmt.Sprintf(" 等 %d 個", len(j.URLs))
	}
	title := fmt.Sprintf("任務 %d", j.ID)
	for _, part := range []string{name, strings.Join(sites, "、")} {
		if part != "" {
			title += "｜" + part
		}
	}
	brief := ""
	if j.State == jobFailed {
		brief = errorBrief(j.Message)
	}
	return jobView{ID: j.ID, Title: title, Name: name, State: j.State, Message: j.Message, Brief: brief, Status: j.status,
		URLs: j.URLs, Items: append([]jobItem(nil), j.Items...), Output: j.Output, Folder: filepath.Base(filepath.Clean(j.Output)), Sites: sites}
}

func isJobFinished(j *job) bool {
	if j == nil {
		return false
	}
	return j.State == jobDone || j.State == jobAborted || j.State == jobPartial
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

func (a *app) jobViews() []jobView {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	out := make([]jobView, 0, len(a.jobs))
	// Worked out once for all cards, not once per card.
	busy, running := a.busySitesLocked(), a.runningCountLocked()
	for i, j := range a.jobs {
		out = append(out, a.viewWith(j, i, busy, running))
	}
	return out
}

// viewLocked adds why a queued task is not running yet; jobsMu must be held.
func (a *app) viewLocked(j *job) jobView {
	order := 0
	for i, x := range a.jobs {
		if x == j {
			order = i
			break
		}
	}
	var busy map[string]int
	if j.State == jobQueued {
		busy = a.busySitesLocked()
	}
	return a.viewWith(j, order, busy, a.runningCountLocked())
}

// viewWith is viewLocked with the card's place, the sites in use and the
// number of running tasks already known.
func (a *app) viewWith(j *job, order int, busy map[string]int, running int) jobView {
	v := j.view()
	v.Order = order
	if j.State == jobRunning && j.stopAs == jobYield {
		v.Status = "正在讓位給排在前面的任務，稍後從這一集接著下載…"
	}
	if j.State == jobQueued {
		for _, s := range v.Sites {
			if busy[s] != 0 {
				v.Waiting = fmt.Sprintf("等待任務 %d 完成（同一網站不同時下載）", busy[s])
				return v
			}
		}
		if running >= int(a.maxJobs.Load()) {
			v.Waiting = "等待空位（同時執行上限）"
		}
	}
	return v
}

func (a *app) emitJob(j *job) {
	a.jobsMu.Lock()
	v := a.viewLocked(j)
	a.jobsMu.Unlock()
	a.emit("job", v)
}

// emitAllJobs refreshes every card (waiting reasons change when tasks end).
func (a *app) emitAllJobs() { a.emit("jobs", a.jobViews()) }

func (a *app) findJobLocked(id int) *job {
	for _, j := range a.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// addJob queues a new task and starts it when a slot and its sites are free.
func (a *app) addJob(urls []string, mode int, formatID, output string, cfg settings, notes []string) *job {
	a.jobsMu.Lock()
	a.nextJobID++
	j := &job{ID: a.nextJobID, URLs: urls, Items: make([]jobItem, len(urls)), Mode: mode, FormatID: formatID,
		Output: output, Cfg: cfg, State: jobQueued, Created: time.Now(), Name: cmp.Or(cfg.SeriesName, cfg.FileName), notes: notes}
	for i := range j.Items {
		j.Items[i].State = "pending"
	}
	a.jobs = append(a.jobs, j)
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	a.emitJob(j)
	a.schedule()
	return j
}

// jobLogFile is the log file of task id, or of the latest task when id is 0.
func (a *app) jobLogFile(id int) string {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	best := ""
	for _, j := range a.jobs {
		if j.LogFile != "" && (j.ID == id || id == 0) {
			best = j.LogFile
		}
	}
	return best
}

// setJobItem records an item's state for a task's worker; named reports
// that the task just got its name from the item's file.
func (a *app) setJobItem(index int, state, message string) (named bool) {
	if a.job == nil {
		return
	}
	a.jobsMu.Lock()
	if index >= 0 && index < len(a.job.Items) {
		a.job.Items[index].State = state
		if message != "" || state == "running" {
			a.job.Items[index].Msg = message
			a.job.Items[index].Brief = ""
			if state == "failed" {
				a.job.Items[index].Brief = errorBrief(message)
			}
		}
		if state == "done" && a.job.Name == "" {
			// itemFiles: the checked files of this item (as in the download history)
			for _, f := range append(append([]string(nil), a.itemFiles...), a.outputs...) {
				ext := strings.ToLower(filepath.Ext(f))
				if mediaExts[ext] {
					a.job.Name = jobNameFromFile(f)
					named = a.job.Name != ""
					break
				} else if ext == ".srt" || ext == ".vtt" {
					if name := jobNameFromSubtitleFile(f); name != "" {
						a.job.Name = name
						named = true
						break
					}
				}
			}
		}
		if state == "done" || state == "failed" {
			a.saveJobsLocked()
		}
	}
	a.jobsMu.Unlock()
	return named
}

func (a *app) setJobStatus(s string) {
	a.jobsMu.Lock()
	a.job.status = s
	a.jobsMu.Unlock()
	a.emit("jobStatus", map[string]interface{}{"id": a.job.ID, "status": s})
}
