//go:build windows

package main

import (
	"errors"
	"fmt"
	"time"
)

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

// schedule starts queued tasks in card order while there are free slots;
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

// pickRunnable chooses the queued tasks to start now, in card order: while
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

// savedSeqFiles is the sequence comparison a run starts from: the files the
// task checked in its earlier runs.
func (a *app) savedSeqFiles() map[string]seqFile {
	out := map[string]seqFile{}
	if a.job == nil {
		return out
	}
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	for sum, r := range a.job.SeqHashes {
		out[sum] = seqFile{item: r.Item, path: r.Path}
	}
	return out
}

// startJobLocked runs a task on its own *app; jobsMu must be held.
func (a *app) startJobLocked(j *job) {
	if j.Output != "" && !isFullWindowsPath(j.Output) {
		j.State = jobFailed
		j.Message = invalidOutputPathMsg(j.Output)
		a.saveJobsLocked()
		return
	}
	w := &app{appShared: a.appShared, job: j, cfg: j.Cfg, itemOffset: j.Pos}
	w.busy.Store(true)
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
		if saveFolderTooLong(j.Output) {
			w.postLog(fmt.Sprintf("⚠ 儲存位置的路徑很長（%d 個字元），下載的檔案完整路徑可能超過 Windows 的 260 字元上限，部分播放器會打不開。建議改選較短的資料夾。\r\n", pathUnits(j.Output)))
		}
		cfg := j.Cfg
		if cfg.FileName != "" {
			if name := reserveCustomName(j.Output, cfg.FileName, j.ID); name != cfg.FileName {
				w.postLog("ℹ 儲存位置已有「" + cfg.FileName + "」，這次存成「" + name + "」。\r\n")
				cfg.FileName = name
			}
			w.cfg.FileName = cfg.FileName
			defer releaseCustomNames(j.ID)
		}
		err := w.ensureEnvironment(ctx, false)
		if err == nil {
			err = w.runURLs(ctx, urls, j.Mode, j.FormatID, j.Output, cfg)
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
	case stopped && j.stopAs == jobYield:
		j.State, j.Pos, j.Message = jobQueued, pos, ""
		final = fmt.Sprintf("=== 讓位給排在前面的任務，稍後從第 %d/%d 項接著下載 ===", pos+1, len(j.URLs))
	case stopped:
		j.State, j.Pos = jobPaused, pos
		j.Message = fmt.Sprintf("已暫停：按「繼續」從第 %d/%d 項接著下載，未完成的檔案會保留。", pos+1, len(j.URLs))
		if j.AutoResume {
			j.Message = updatePauseMessage
		}
		final = "=== 任務已暫停 ==="
	case err == nil:
		j.State, j.Pos, j.Message = jobDone, len(j.URLs), "全部完成。"
		final = "=== 全部任務完成 ==="
	default:
		var partial *batchPartialError
		if errors.As(err, &partial) {
			j.State, j.Pos = jobPartial, len(j.URLs)
			j.Message = fmt.Sprintf("完成，%d 項失敗；成功的項目已保留。", len(partial.Failures))
			final = "=== 下載完成（部分項目失敗）===\r\n" + err.Error()
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
		j.AutoResume = false
	case "resume":
		if j.State == jobPaused || j.State == jobFailed {
			if j.Output != "" && !isFullWindowsPath(j.Output) {
				return invalidOutputPathMsg(j.Output)
			}
			j.State, j.Message = jobQueued, ""
		}
		j.AutoResume = false
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

const updatePauseMessage = "為了更新版本暫時暫停，更新完會自動恢復。"

// pauseAllJobs pauses every running and queued task (全部暫停). forUpdate
// marks them to be resumed after the version update. Returns how many.
func (a *app) pauseAllJobs(forUpdate bool) int {
	a.jobsMu.Lock()
	var workers []*app
	n := 0
	for _, j := range a.jobs {
		switch j.State {
		case jobRunning:
			if j.stopAs == "" || j.stopAs == jobYield {
				if j.stopAs == "" {
					workers = append(workers, j.worker)
				}
				j.stopAs = jobPaused
			}
		case jobQueued:
			j.State, j.Message = jobPaused, "已暫停，按「繼續」重新排入佇列。"
			if forUpdate {
				j.Message = updatePauseMessage
			}
		default:
			continue
		}
		j.AutoResume = forUpdate
		n++
	}
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	for _, w := range workers {
		if w != nil {
			w.stopCurrent()
		}
	}
	if n > 0 {
		a.emitAllJobs()
	}
	return n
}

// resumeAllJobs queues paused tasks again (全部繼續); onlyAuto limits it to
// the tasks paused for a version update. Returns how many.
func (a *app) resumeAllJobs(onlyAuto bool) int {
	a.jobsMu.Lock()
	n := 0
	for _, j := range a.jobs {
		if onlyAuto && !j.AutoResume {
			continue
		}
		if j.State == jobPaused {
			j.State, j.Message = jobQueued, ""
			n++
		}
		j.AutoResume = false
	}
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	if n > 0 {
		a.emitAllJobs()
		a.schedule()
	}
	return n
}

// waitJobsStopped waits until no task is running.
func (a *app) waitJobsStopped(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for a.anyJobRunning() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return true
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
