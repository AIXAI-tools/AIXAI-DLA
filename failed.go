//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 無法下載的網址: every item a task gives up on is listed here, apart from
// the task cards, so the user can follow up on it after the rest of the batch
// has finished: copy the address, open it in a browser, try it again as a new
// task, or remove it from the list. The list survives restarts
// (failed_urls.json) and an address drops off by itself once any task
// downloads it successfully.

const (
	failedFileName = "failed_urls.json"
	failedMax      = 300
)

type failedEntry struct {
	ID       int       `json:"id"`
	URL      string    `json:"url"`
	Reason   string    `json:"reason"`
	JobID    int       `json:"jobId"`
	Item     int       `json:"item"` // 1-based position in that task
	Time     time.Time `json:"time"`
	Count    int       `json:"count"` // how many times it has failed
	Mode     int       `json:"mode"`
	FormatID string    `json:"formatId,omitempty"`
	Output   string    `json:"output"`
	Cfg      settings  `json:"cfg"`
}

// failedView is what the page shows for one entry.
type failedView struct {
	ID     int    `json:"id"`
	URL    string `json:"url"`
	Reason string `json:"reason"`
	JobID  int    `json:"jobId"`
	Item   int    `json:"item"`
	Time   string `json:"time"`
	Count  int    `json:"count"`
}

func (a *app) failedPath() string { return filepath.Join(a.appDir, failedFileName) }

func (a *app) loadFailed() {
	data, err := os.ReadFile(a.failedPath())
	if err != nil {
		return
	}
	var list []failedEntry
	if json.Unmarshal(data, &list) != nil {
		return
	}
	a.failedMu.Lock()
	defer a.failedMu.Unlock()
	for _, e := range list {
		if strings.TrimSpace(e.URL) == "" {
			continue
		}
		a.failed = append(a.failed, e)
		if e.ID > a.failedNextID {
			a.failedNextID = e.ID
		}
	}
}

// saveFailedLocked writes the list; failedMu must be held.
func (a *app) saveFailedLocked() {
	data, err := json.MarshalIndent(a.failed, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(a.appDir, 0755)
	tmp := a.failedPath() + ".tmp"
	if os.WriteFile(tmp, data, 0644) == nil {
		_ = os.Rename(tmp, a.failedPath())
	}
}

func (a *app) failedViews() []failedView {
	a.loadFailedOnce.Do(a.loadFailed)
	a.failedMu.Lock()
	defer a.failedMu.Unlock()
	out := make([]failedView, 0, len(a.failed))
	for _, e := range a.failed {
		out = append(out, failedView{ID: e.ID, URL: e.URL, Reason: e.Reason, JobID: e.JobID, Item: e.Item,
			Time: e.Time.Format("01/02 15:04"), Count: e.Count})
	}
	return out
}

func (a *app) emitFailed() { a.emit("failed", a.failedViews()) }

// noteItemResult keeps the list in step with a task item that just ended.
// index is the item's position in the whole task.
func (a *app) noteItemResult(index int, state, message string) {
	if a.job == nil || index < 0 || index >= len(a.job.URLs) || (state != "failed" && state != "done") {
		return
	}
	url := a.job.URLs[index]
	a.loadFailedOnce.Do(a.loadFailed)
	a.failedMu.Lock()
	pos := -1
	for i, e := range a.failed {
		if e.URL == url {
			pos = i
			break
		}
	}
	changed := false
	if state == "done" {
		if pos >= 0 {
			a.failed = append(a.failed[:pos], a.failed[pos+1:]...)
			changed = true
		}
	} else {
		reason := strings.TrimSpace(message)
		if reason == "" {
			reason = "下載失敗（原因請看紀錄檔）"
		}
		e := failedEntry{URL: url, Reason: reason, JobID: a.job.ID, Item: index + 1, Time: time.Now(), Count: 1,
			Mode: a.job.Mode, FormatID: a.job.FormatID, Output: a.job.Output, Cfg: a.job.Cfg}
		if pos >= 0 {
			e.ID, e.Count = a.failed[pos].ID, a.failed[pos].Count+1
			a.failed = append(a.failed[:pos], a.failed[pos+1:]...)
		} else {
			a.failedNextID++
			e.ID = a.failedNextID
		}
		a.failed = append(a.failed, e)
		if len(a.failed) > failedMax {
			a.failed = a.failed[len(a.failed)-failedMax:]
		}
		changed = true
	}
	if changed {
		a.saveFailedLocked()
	}
	a.failedMu.Unlock()
	if changed {
		a.emitFailed()
	}
}

// retryFailed starts one new task for the given entries (all entries when ids
// is empty). Entries saved with different download settings go to separate
// tasks. The entries stay listed until a download succeeds.
func (a *app) retryFailed(ids []int) string {
	a.loadFailedOnce.Do(a.loadFailed)
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	a.failedMu.Lock()
	type group struct {
		first failedEntry
		urls  []string
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, e := range a.failed {
		if len(ids) > 0 && !want[e.ID] {
			continue
		}
		cfgKey, _ := json.Marshal(e.Cfg)
		key := strings.Join([]string{string(rune('0' + e.Mode)), e.FormatID, e.Output, string(cfgKey)}, "\x00")
		g := byKey[key]
		if g == nil {
			g = &group{first: e}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.urls = append(g.urls, e.URL)
	}
	a.failedMu.Unlock()
	if len(groups) == 0 {
		return "清單中沒有可重試的網址。"
	}
	for _, g := range groups {
		cfg := g.first.Cfg
		// Each address is retried as it is; a sequence range was already
		// expanded into these addresses.
		cfg.Sequence = false
		a.addJob(g.urls, g.first.Mode, g.first.FormatID, g.first.Output, cfg,
			[]string{"ℹ 重試「無法下載的網址」清單中的項目。\r\n"})
	}
	return ""
}

// removeFailed drops the given entries (all entries when ids is empty).
func (a *app) removeFailed(ids []int) {
	a.loadFailedOnce.Do(a.loadFailed)
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	a.failedMu.Lock()
	keep := a.failed[:0]
	for _, e := range a.failed {
		if len(ids) > 0 && !want[e.ID] {
			keep = append(keep, e)
		}
	}
	a.failed = keep
	a.saveFailedLocked()
	a.failedMu.Unlock()
	a.emitFailed()
}
