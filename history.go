//go:build windows

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// 歷史下載清單: one entry for every item a task finishes, downloaded or not.
// Kept in the app data folder (download_history.json); the page shows it in
// a dialog where entries can be searched, put back into the address box,
// exported (CSV for Excel, TXT one address a line) or removed.
// The address box itself can be copied, cleared, imported from a TXT/CSV
// file and exported.

const (
	historyFileName = "download_history.json"
	historyMax      = 5000
)

type historyEntry struct {
	ID     int       `json:"id"`
	Time   time.Time `json:"time"`
	URL    string    `json:"url"`
	State  string    `json:"state"` // done, failed
	Title  string    `json:"title,omitempty"`
	File   string    `json:"file,omitempty"`
	Folder string    `json:"folder,omitempty"`
	Reason string    `json:"reason,omitempty"`
	JobID  int       `json:"jobId,omitempty"`
}

type historyView struct {
	ID     int    `json:"id"`
	Time   string `json:"time"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Title  string `json:"title"`
	File   string `json:"file"`
	Folder string `json:"folder"`
	Reason string `json:"reason"`
	Brief  string `json:"brief"`
}

func (a *app) historyPath() string { return filepath.Join(a.appDir, historyFileName) }

func (a *app) loadHistory() {
	if a.appDir == "" {
		return
	}
	data, err := os.ReadFile(a.historyPath())
	if err != nil {
		return
	}
	var list []historyEntry
	if json.Unmarshal(data, &list) != nil {
		return
	}
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	a.history = list
	for _, e := range list {
		if e.ID > a.historyNextID {
			a.historyNextID = e.ID
		}
	}
}

// saveHistoryLocked writes the list; historyMu must be held.
func (a *app) saveHistoryLocked() {
	if a.appDir == "" {
		return
	}
	data, err := json.Marshal(a.history)
	if err != nil {
		return
	}
	tmp := a.historyPath() + ".tmp"
	if os.WriteFile(tmp, data, 0644) == nil {
		_ = os.Rename(tmp, a.historyPath())
	}
}

// recordHistory adds the outcome of a finished task item.
func (a *app) recordHistory(index int, state, message string) {
	if a.job == nil || index < 0 || index >= len(a.job.URLs) || (state != "done" && state != "failed") {
		return
	}
	e := historyEntry{Time: time.Now(), URL: a.job.URLs[index], State: state, Folder: a.job.Output, JobID: a.job.ID}
	if state == "done" && len(a.itemFiles) > 0 {
		e.File = a.itemFiles[0]
		e.Folder = filepath.Dir(e.File)
		e.Title = strings.TrimSuffix(filepath.Base(e.File), filepath.Ext(e.File))
	}
	if state == "failed" {
		e.Reason = strings.TrimSpace(message)
	}
	a.loadHistoryOnce.Do(a.loadHistory)
	a.historyMu.Lock()
	a.historyNextID++
	e.ID = a.historyNextID
	a.history = append(a.history, e)
	if len(a.history) > historyMax {
		a.history = a.history[len(a.history)-historyMax:]
	}
	a.saveHistoryLocked()
	a.historyMu.Unlock()
	a.emit("historyChanged", nil)
}

// historyViews lists the entries, newest first.
func (a *app) historyViews() []historyView {
	a.loadHistoryOnce.Do(a.loadHistory)
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	out := make([]historyView, 0, len(a.history))
	for i := len(a.history) - 1; i >= 0; i-- {
		e := a.history[i]
		out = append(out, historyView{ID: e.ID, Time: e.Time.Format("2006-01-02 15:04"), URL: e.URL, State: e.State,
			Title: e.Title, File: e.File, Folder: e.Folder, Reason: e.Reason, Brief: errorBrief(e.Reason)})
	}
	return out
}

// pickHistory returns the chosen entries in list order (all when ids is empty).
func (a *app) pickHistory(ids []int) []historyEntry {
	a.loadHistoryOnce.Do(a.loadHistory)
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	a.historyMu.Lock()
	defer a.historyMu.Unlock()
	var out []historyEntry
	for i := len(a.history) - 1; i >= 0; i-- {
		if len(ids) == 0 || want[a.history[i].ID] {
			out = append(out, a.history[i])
		}
	}
	return out
}

// removeHistory drops the given entries (all when ids is empty).
func (a *app) removeHistory(ids []int) {
	a.loadHistoryOnce.Do(a.loadHistory)
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	a.historyMu.Lock()
	keep := a.history[:0]
	for _, e := range a.history {
		if len(ids) > 0 && !want[e.ID] {
			keep = append(keep, e)
		}
	}
	a.history = keep
	a.saveHistoryLocked()
	a.historyMu.Unlock()
	a.emit("historyChanged", nil)
}

// revealHistory shows the downloaded file (or its folder) in Explorer.
func (a *app) revealHistory(id int) string {
	list := a.pickHistory([]int{id})
	if len(list) == 0 {
		return "找不到這筆紀錄。"
	}
	e := list[0]
	if e.File != "" && validFile(e.File, 1) {
		openExplorerSelecting(e.File)
		return ""
	}
	if e.Folder != "" {
		if st, err := os.Stat(e.Folder); err == nil && st.IsDir() {
			if e.File != "" {
				return "檔案已不在原位置（可能被移動或刪除），已開啟原本的資料夾。" + a.openFolder(e.Folder)
			}
			return a.openFolder(e.Folder)
		}
	}
	return "原本的檔案和資料夾都已不存在。"
}

func historyStateLabel(state string) string {
	if state == "done" {
		return "成功"
	}
	return "失敗"
}

// historyCSV is the CSV export: UTF-8 with BOM and CRLF so Excel opens it.
func historyCSV(list []historyEntry) []byte {
	var b bytes.Buffer
	b.WriteString("\uFEFF")
	w := csv.NewWriter(&b)
	w.UseCRLF = true
	_ = w.Write([]string{"時間", "結果", "標題", "網址", "存放位置", "失敗說明", "原始錯誤訊息"})
	for _, e := range list {
		where := e.File
		if where == "" {
			where = e.Folder
		}
		brief := ""
		if e.Reason != "" {
			brief = errorBrief(e.Reason)
		}
		_ = w.Write([]string{e.Time.Format("2006-01-02 15:04:05"), historyStateLabel(e.State), e.Title, e.URL, where, brief, e.Reason})
	}
	w.Flush()
	return b.Bytes()
}

// urlLines is the TXT export: one address a line, duplicates removed.
func urlLines(urls []string) []byte {
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("\uFEFF")
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		b.WriteString(u + "\r\n")
	}
	return []byte(b.String())
}

// exportHistory saves the chosen entries; format is "csv" or "txt".
func (a *app) exportHistory(ids []int, format string) string {
	list := a.pickHistory(ids)
	if len(list) == 0 {
		return "沒有可匯出的紀錄。"
	}
	stamp := time.Now().Format("20060102_150405")
	var data []byte
	var path string
	if format == "txt" {
		urls := make([]string, len(list))
		for i, e := range list {
			urls[i] = e.URL
		}
		data = urlLines(urls)
		path = a.saveFileDialog("匯出歷史網址", a.dataFolder(), "下載歷史網址_"+stamp+".txt", "txt", "文字檔 (*.txt)", "*.txt")
	} else {
		data = historyCSV(list)
		path = a.saveFileDialog("匯出歷史下載清單", a.dataFolder(), "下載歷史_"+stamp+".csv", "csv", "CSV（Excel 可開啟） (*.csv)", "*.csv")
	}
	if path == "" {
		return ""
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "匯出失敗：" + err.Error()
	}
	openExplorerSelecting(path)
	return fmt.Sprintf("已匯出 %d 筆：%s", len(list), filepath.Base(path))
}

// exportURLText saves the address box as a TXT file.
func (a *app) exportURLText(text string) string {
	urls := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	data := urlLines(urls)
	if len(bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\uFEFF")))) == 0 {
		return "網址欄是空的。"
	}
	path := a.saveFileDialog("匯出網址", a.dataFolder(), "網址清單_"+time.Now().Format("20060102_150405")+".txt", "txt", "文字檔 (*.txt)", "*.txt")
	if path == "" {
		return ""
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "匯出失敗：" + err.Error()
	}
	return "已匯出：" + filepath.Base(path)
}

var urlInTextRE = regexp.MustCompile(`https?://[^\s"'<>,，、]+`)

// decodeTextFile reads UTF-8 (with or without BOM) or UTF-16 text.
func decodeTextFile(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:])
	case len(data) >= 2 && (data[0] == 0xFF && data[1] == 0xFE || data[0] == 0xFE && data[1] == 0xFF):
		le := data[0] == 0xFF
		u := make([]uint16, 0, len(data)/2)
		for i := 2; i+1 < len(data); i += 2 {
			if le {
				u = append(u, uint16(data[i])|uint16(data[i+1])<<8)
			} else {
				u = append(u, uint16(data[i])<<8|uint16(data[i+1]))
			}
		}
		return string(utf16.Decode(u))
	}
	return string(data)
}

// urlsInText finds every address in a TXT or CSV file, in order, once each.
func urlsInText(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urlInTextRE.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".;)]}")
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

type importResult struct {
	URLs  []string `json:"urls"`
	Error string   `json:"error,omitempty"`
}

// importURLs reads addresses from a TXT or CSV file the user picks.
func (a *app) importURLs() importResult {
	files := a.openFileDialog("匯入網址（TXT 或 CSV）", a.dataFolder(), false, "網址清單 (*.txt;*.csv)", "*.txt;*.csv", "所有檔案 (*.*)", "*.*")
	if len(files) == 0 {
		return importResult{}
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return importResult{Error: "無法讀取檔案：" + err.Error()}
	}
	urls := urlsInText(decodeTextFile(data))
	if len(urls) == 0 {
		return importResult{Error: "檔案中沒有找到 http:// 或 https:// 開頭的網址。"}
	}
	return importResult{URLs: urls}
}
