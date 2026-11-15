//go:build windows

package main

// moveJob puts task id right before (or after) task target in the card
// order; both must be running or queued. Tasks now in the way of a task
// above them step aside (finishMoveLocked).
func (a *app) moveJob(id, target int, after bool) string {
	a.jobsMu.Lock()
	j, t := a.findJobLocked(id), a.findJobLocked(target)
	if j == nil || t == nil {
		a.jobsMu.Unlock()
		return "找不到這個任務。"
	}
	if !jobMovable(j) || !jobMovable(t) {
		a.jobsMu.Unlock()
		return "只有下載中或排隊中的任務可以調整順序。"
	}
	if j != t {
		a.jobs = moveJobInList(a.jobs, j, t, after)
	}
	a.finishMoveLocked()
	return ""
}

// prioritizeJob (優先下載) moves a queued task above every running or queued
// task.
func (a *app) prioritizeJob(id int) string {
	a.jobsMu.Lock()
	j := a.findJobLocked(id)
	if j == nil || j.State != jobQueued {
		a.jobsMu.Unlock()
		return "只有排隊中的任務可以優先下載。"
	}
	for _, t := range a.jobs {
		if jobMovable(t) {
			if t != j {
				a.jobs = moveJobInList(a.jobs, j, t, false)
			}
			break
		}
	}
	a.finishMoveLocked()
	return ""
}

// groupJobsBySite (整理任務排序) puts the tasks of the same website next to
// each other: the websites keep the order of their first task, and the tasks
// of one website keep their order.
func (a *app) groupJobsBySite() {
	a.jobsMu.Lock()
	a.jobs = groupJobsBySite(a.jobs)
	a.finishMoveLocked()
}

func groupJobsBySite(jobs []*job) []*job {
	var keys []string
	groups := map[string][]*job{}
	for _, j := range jobs {
		k := ""
		if s := jobSites(j.URLs); len(s) > 0 {
			k = s[0]
		}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], j)
	}
	out := make([]*job, 0, len(jobs))
	for _, k := range keys {
		out = append(out, groups[k]...)
	}
	return out
}

// finishMoveLocked saves the new order, makes the tasks in the way step
// aside and starts what can start; jobsMu must be held and is released.
func (a *app) finishMoveLocked() {
	var workers []*app
	for _, j := range yieldingJobs(a.jobs, int(a.maxJobs.Load())) {
		j.stopAs = jobYield
		workers = append(workers, j.worker)
	}
	a.saveJobsLocked()
	a.jobsMu.Unlock()
	for _, w := range workers {
		if w != nil {
			w.stopCurrent()
		}
	}
	a.emitAllJobs()
	a.schedule()
}

// jobMovable: running (and not being paused or aborted) or queued.
func jobMovable(j *job) bool {
	return j.State == jobQueued || (j.State == jobRunning && (j.stopAs == "" || j.stopAs == jobYield))
}

// moveJobInList returns jobs with j moved right before (or after) t.
func moveJobInList(jobs []*job, j, t *job, after bool) []*job {
	out := make([]*job, 0, len(jobs))
	for _, x := range jobs {
		if x != j {
			out = append(out, x)
		}
	}
	for i, x := range out {
		if x == t {
			if after {
				i++
			}
			return append(out[:i], append([]*job{j}, out[i:]...)...)
		}
	}
	return append(out, j)
}

// yieldingJobs are the running tasks that must step aside: going down the
// card order, running and queued tasks take the free slots and websites,
// and a running task left without them because a task above took them
// yields. Tasks being paused or aborted keep theirs until they stop.
func yieldingJobs(jobs []*job, limit int) []*job {
	used := 0
	busy := map[string]bool{}
	for _, j := range jobs {
		if j.State == jobRunning && !jobMovable(j) {
			used++
			for _, s := range jobSites(j.URLs[j.Pos:]) {
				busy[s] = true
			}
		}
	}
	var out []*job
	for _, j := range jobs {
		if !jobMovable(j) {
			continue
		}
		sites := jobSites(j.URLs[j.Pos:])
		free := used < limit
		for _, s := range sites {
			if busy[s] {
				free = false
			}
		}
		if free {
			used++
			for _, s := range sites {
				busy[s] = true
			}
			if j.State == jobRunning && j.stopAs == jobYield {
				j.stopAs = "" // moved back up before it stopped: keep going
			}
			continue
		}
		if j.State == jobRunning && j.stopAs == "" {
			out = append(out, j)
		}
	}
	return out
}
