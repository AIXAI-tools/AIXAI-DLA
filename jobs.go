//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Download tasks ("jobs"). Every press of 開始下載 adds one task; up to
// settings.MaxJobs tasks run at the same time, each on its own *app (own stop
// flag, process, log file). Tasks for the same website never run together:
// a site sees one task's requests at a time, which keeps the request rate of
// a single download even without sign-in (sites also block by IP).
// Unfinished tasks are saved to tasks.json and come back paused next time.

const (
	jobQueued  = "queued"
	jobRunning = "running"
	jobPaused  = "paused"
	jobDone    = "done"
	jobPartial = "partial"
	jobFailed  = "failed"
	jobAborted = "aborted"

	maxJobsLimit   = 8
	jobsFileName   = "tasks.json"
	defaultMaxJobs = 2
)

type jobItem struct {
	State string `json:"state"` // pending, running, done, failed
	Msg   string `json:"msg,omitempty"`
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

	notes  []string // shown once, when the task first starts
	status string   // latest status line while running
	stopAs string   // jobPaused or jobAborted while a stop is in progress
	worker *app
}

// jobView is what the page shows for a task.
type jobView struct {
	ID      int       `json:"id"`
	Title   string    `json:"title"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Status  string    `json:"status,omitempty"`
	URLs    []string  `json:"urls"`
	Items   []jobItem `json:"items"`
	Output  string    `json:"output"`
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
	title := fmt.Sprintf("任務 %d", j.ID)
	if len(sites) > 0 {
		title += "｜" + strings.Join(sites, "、")
	}
	return jobView{ID: j.ID, Title: title, State: j.State, Message: j.Message, Status: j.status,
		URLs: j.URLs, Items: append([]jobItem(nil), j.Items...), Output: j.Output, Sites: sites}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (a *app) jobsPath() string { return filepath.Join(a.appDir, jobsFileName) }

// saveJobsLocked writes the unfinished tasks; jobsMu must be held.
func (a *app) saveJobsLocked() {
	var keep []*job
	for _, j := range a.jobs {
		if j.State != jobDone && j.State != jobAborted {
			keep = append(keep, j)
		}
	}
	data, err := json.MarshalIndent(keep, "", "  ")
	if err != nil {
		return
	}
	tmp := a.jobsPath() + ".tmp"
	if os.WriteFile(tmp, data, 0644) == nil {
		_ = os.Rename(tmp, a.jobsPath())
	}
}

// loadJobs restores the tasks left unfinished last time, all paused.
func (a *app) loadJobs() {
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
		for i := range j.Items {
			if j.Items[i].State == "" || j.Items[i].State == "running" {
				j.Items[i].State = "pending"
			}
		}
		if j.State == jobQueued || j.State == jobRunning || j.State == "" {
			j.State = jobPaused
			j.Message = "上次關閉程式時尚未完成，按「繼續」接著下載。"
		}
		if j.Pos < 0 || j.Pos > len(j.URLs) {
			j.Pos = 0
		}
		a.jobs = append(a.jobs, j)
		if j.ID >= a.nextJobID {
			a.nextJobID = j.ID
		}
	}
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

func (a *app) jobViews() []jobView {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	out := make([]jobView, 0, len(a.jobs))
	for _, j := range a.jobs {
		out = append(out, a.viewLocked(j))
	}
	return out
}

// viewLocked adds why a queued task is not running yet; jobsMu must be held.
func (a *app) viewLocked(j *job) jobView {
	v := j.view()
	if j.State == jobQueued {
		busy := a.busySitesLocked()
		for _, s := range v.Sites {
			if busy[s] != 0 {
				v.Waiting = fmt.Sprintf("等待任務 %d 完成（同一網站不同時下載）", busy[s])
				return v
			}
		}
		if a.runningCountLocked() >= int(a.maxJobs.Load()) {
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

func (a *app) runningCountLocked() int {
	n := 0
	for _, j := range a.jobs {
		if j.State == jobRunning {
			n++
		}
	}
	return n
}

// busySitesLocked maps each website in use to the task using it.
func (a *app) busySitesLocked() map[string]int {
	busy := map[string]int{}
	for _, j := range a.jobs {
		if j.State == jobRunning {
			for _, s := range jobSites(j.URLs[j.Pos:]) {
				busy[s] = j.ID
			}
		}
	}
	return busy
}

func (a *app) anyJobRunning() bool {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	return a.runningCountLocked() > 0
}

// otherJobsRunning reports whether a task other than this one is running.
func (a *app) otherJobsRunning() bool {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for _, j := range a.jobs {
		if j.State == jobRunning && j != a.job {
			return true
		}
	}
	return false
}

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
		Output: output, Cfg: cfg, State: jobQueued, Created: time.Now(), notes: notes}
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

// schedule starts queued tasks, oldest first, while there are free slots;
// a task whose website is already being downloaded waits for that task.
func (a *app) schedule() {
	if a.closing.Load() {
		return
	}
	a.jobsMu.Lock()
	started := pickRunnable(a.jobs, int(a.maxJobs.Load()))
	for _, j := range started {
		a.startJobLocked(j)
	}
	a.jobsMu.Unlock()
	if len(started) > 0 {
		a.emitAllJobs()
	}
}

// pickRunnable chooses the queued tasks to start now, oldest first: while
// fewer than limit tasks run, and only tasks whose websites no running (or
// just chosen) task is using.
func pickRunnable(jobs []*job, limit int) []*job {
	running := 0
	busy := map[string]bool{}
	for _, j := range jobs {
		if j.State == jobRunning {
			running++
			for _, s := range jobSites(j.URLs[j.Pos:]) {
				busy[s] = true
			}
		}
	}
	var out []*job
	for _, j := range jobs {
		if running >= limit {
			break
		}
		if j.State != jobQueued {
			continue
		}
		sites := jobSites(j.URLs[j.Pos:])
		free := true
		for _, s := range sites {
			if busy[s] {
				free = false
				break
			}
		}
		if !free {
			continue
		}
		out = append(out, j)
		running++
		for _, s := range sites {
			busy[s] = true
		}
	}
	return out
}

// startJobLocked runs a task on its own *app; jobsMu must be held.
func (a *app) startJobLocked(j *job) {
	w := &app{appShared: a.appShared, job: j, cfg: j.Cfg, itemOffset: j.Pos}
	w.busy.Store(true)
	w.logDir.Store(j.Output)
	j.worker = w
	j.State = jobRunning
	j.Message = ""
	j.status = "正在準備…"
	j.stopAs = ""
	urls := append([]string(nil), j.URLs[j.Pos:]...)
	notes := j.notes
	j.notes = nil
	if j.Pos > 0 {
		notes = append(notes, fmt.Sprintf("✓ 繼續任務 %d：從第 %d/%d 項接著下載。\r\n", j.ID, j.Pos+1, len(j.URLs)))
	}
	for i := j.Pos; i < len(j.Items); i++ {
		if j.Items[i].State == "running" {
			j.Items[i].State = "pending"
		}
	}
	a.saveJobsLocked()

	started := time.Now()
	w.beginRunLog(started)
	j.LogFile = w.latestRunLog()
	ctx := w.beginTask()
	a.workerWG.Add(1)
	go func() {
		defer a.workerWG.Done()
		w.postLog(fmt.Sprintf("\r\n=== 開始處理 %s（任務 %d）===\r\n", started.Format("2006-01-02 15:04:05"), j.ID))
		for _, n := range notes {
			w.postLog(n)
		}
		w.logTaskHeader(urls, j.Mode, j.FormatID, j.Cfg)
		err := w.ensureEnvironment(ctx, false)
		if err == nil {
			err = w.runURLs(ctx, urls, j.Mode, j.FormatID, j.Output, j.Cfg)
		}
		w.endTask()
		a.finishJob(j, w, err)
	}()
}

// finishJob records how a task ended and lets the next tasks start.
func (a *app) finishJob(j *job, w *app, err error) {
	if a.closing.Load() {
		return // shutdown already saved the task as paused
	}
	stopped := w.stopRequested.Load()
	a.jobsMu.Lock()
	pos := w.itemOffset + int(w.runPos.Load())
	var final string
	switch {
	case stopped && j.stopAs == jobAborted:
		j.State, j.Message = jobAborted, "已中止。"
		final = "=== 任務已中止 ==="
	case stopped:
		j.State, j.Pos = jobPaused, pos
		j.Message = fmt.Sprintf("已暫停：按「繼續」從第 %d/%d 項接著下載，未完成的檔案會保留。", pos+1, len(j.URLs))
		final = "=== 任務已暫停 ==="
	case err == nil:
		j.State, j.Pos, j.Message = jobDone, len(j.URLs), "全部完成。"
		final = "=== 全部任務完成 ==="
	default:
		var partial *batchPartialError
		if errors.As(err, &partial) {
			j.State, j.Pos = jobPartial, len(j.URLs)
			j.Message = fmt.Sprintf("完成，%d 項失敗；成功的項目已保留。", len(partial.Failures))
			final = "=== 連續下載完成（部分項目失敗）===\r\n" + err.Error()
		} else {
			j.State, j.Pos = jobFailed, pos
			j.Message = err.Error()
			final = "錯誤：" + err.Error()
		}
	}
	for i := j.Pos; i < len(j.Items); i++ {
		if j.Items[i].State == "running" {
			j.Items[i].State = "pending"
		}
	}
	j.worker, j.stopAs, j.status = nil, "", ""
	a.saveJobsLocked()
	idle := a.runningCountLocked() == 0
	a.jobsMu.Unlock()

	w.postLog(final + "\r\n")
	w.endRunLog()
	a.emitJob(j)
	if idle {
		a.closeCaptureBrowser()
	}
	a.schedule()
	a.emitAllJobs()
}

// jobAction carries out a button on a task card.
func (a *app) jobAction(id int, action string) string {
	a.jobsMu.Lock()
	j := a.findJobLocked(id)
	if j == nil {
		a.jobsMu.Unlock()
		return "找不到這個任務。"
	}
	var stopWorker *app
	switch action {
	case "pause":
		switch j.State {
		case jobRunning:
			j.stopAs, stopWorker = jobPaused, j.worker
		case jobQueued:
			j.State, j.Message = jobPaused, "已暫停，按「繼續」重新排入佇列。"
		}
	case "resume":
		if j.State == jobPaused || j.State == jobFailed {
			j.State, j.Message = jobQueued, ""
		}
	case "abort":
		switch j.State {
		case jobRunning:
			j.stopAs, stopWorker = jobAborted, j.worker
		case jobQueued, jobPaused, jobFailed, jobPartial:
			j.State, j.Message = jobAborted, "已中止。"
		}
	case "remove":
		if j.State == jobRunning {
			a.jobsMu.Unlock()
			return "請先暫停或中止這個任務。"
		}
		for i, x := range a.jobs {
			if x == j {
				a.jobs = append(a.jobs[:i], a.jobs[i+1:]...)
				break
			}
		}
		a.saveJobsLocked()
		a.jobsMu.Unlock()
		a.emit("jobRemoved", map[string]int{"id": id})
		a.emitAllJobs()
		return ""
	default:
		a.jobsMu.Unlock()
		return "未知的操作。"
	}
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	if stopWorker != nil {
		stopWorker.stopCurrent()
	}
	a.emitJob(j)
	a.schedule()
	return ""
}

// clearFinishedJobs removes finished and aborted cards.
func (a *app) clearFinishedJobs() {
	a.jobsMu.Lock()
	var keep []*job
	for _, j := range a.jobs {
		if j.State != jobDone && j.State != jobAborted && j.State != jobPartial {
			keep = append(keep, j)
		}
	}
	a.jobs = keep
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	a.emitAllJobs()
}

// pauseAllForShutdown saves running tasks as paused at their current item.
func (a *app) pauseAllForShutdown() {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for _, j := range a.jobs {
		if j.State == jobRunning && j.worker != nil {
			j.Pos = j.worker.itemOffset + int(j.worker.runPos.Load())
			j.State = jobPaused
			j.Message = "上次關閉程式時尚未完成，按「繼續」接著下載。"
			for i := j.Pos; i < len(j.Items); i++ {
				if j.Items[i].State == "running" {
					j.Items[i].State = "pending"
				}
			}
		}
		if j.State == jobQueued {
			j.State = jobPaused
		}
	}
	a.saveJobsLocked()
}

// stopAllWorkers stops every running task's process (used at shutdown).
func (a *app) stopAllWorkers() {
	a.jobsMu.Lock()
	var workers []*app
	for _, j := range a.jobs {
		if j.worker != nil {
			workers = append(workers, j.worker)
		}
	}
	a.jobsMu.Unlock()
	for _, w := range workers {
		w.stopRequested.Store(true)
		w.cancelCurrentTask()
		w.killCurrentProcessTree(3 * time.Second)
	}
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

// setJobItem records an item's state for a task's worker.
func (a *app) setJobItem(index int, state, message string) {
	if a.job == nil {
		return
	}
	a.jobsMu.Lock()
	if index >= 0 && index < len(a.job.Items) {
		a.job.Items[index].State = state
		if message != "" || state == "running" {
			a.job.Items[index].Msg = message
		}
		if state == "done" || state == "failed" {
			a.saveJobsLocked()
		}
	}
	a.jobsMu.Unlock()
}

func (a *app) setJobStatus(s string) {
	a.jobsMu.Lock()
	a.job.status = s
	a.jobsMu.Unlock()
	a.emit("jobStatus", map[string]interface{}{"id": a.job.ID, "status": s})
}
