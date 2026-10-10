//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// One folder for everything the user may want to read: the log of every
// run (by month), report packs and the app log. It is "文件\AIXAI 萬能下載工具"
// unless the user picks another one in 進階設定 (settings.DataFolder). App
// state that is not meant for reading (settings, task list, lists, browser
// profile) stays in the app data folder. When the folder cannot be made,
// everything falls back to the app data folder so nothing is lost.

const (
	dataFolderName = "AIXAI 萬能下載工具"
	runLogsSubdir  = "下載紀錄"
	reportsSubdir  = "回報包"
)

var (
	procSHGetKnownFolderPath = shell32.NewProc("SHGetKnownFolderPath")
	// FOLDERID_Documents {FDD39AD0-238F-46AF-ADB4-6C85480369C7}
	folderIDDocuments = syscall.GUID{Data1: 0xFDD39AD0, Data2: 0x238F, Data3: 0x46AF, Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7}}
)

// documentsFolder is the user's Documents folder, wherever Windows (or
// OneDrive) has moved it.
func documentsFolder() string {
	var p *uint16
	r, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&folderIDDocuments)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if r == 0 && p != nil {
		defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
		if dir := utf16PtrToString(p); dir != "" {
			return dir
		}
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "Documents")
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	var buf []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Add(ptr, 2) {
		c := *(*uint16)(ptr)
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return syscall.UTF16ToString(buf)
}

// defaultDataFolder is used while settings.DataFolder is empty.
func defaultDataFolder() string {
	if docs := documentsFolder(); docs != "" {
		return filepath.Join(docs, dataFolderName)
	}
	return ""
}

// setDataFolderPref records the user's choice (empty = default).
func (a *app) setDataFolderPref(dir string) { a.dataFolderPref.Store(strings.TrimSpace(dir)) }

// dataFolder returns the folder to show the user, made if needed.
func (a *app) dataFolder() string {
	if a.appDir == "" {
		return "" // unit tests: never write outside a test folder
	}
	dir, _ := a.dataFolderPref.Load().(string)
	if dir == "" {
		dir = defaultDataFolder()
	}
	if dir != "" && os.MkdirAll(dir, 0755) == nil {
		return dir
	}
	return a.appDir
}

// dataSubdir returns a made subfolder of the data folder ("" when even the
// app data folder is unknown, as in unit tests).
func (a *app) dataSubdir(parts ...string) string {
	base := a.dataFolder()
	if base == "" {
		return ""
	}
	dir := filepath.Join(append([]string{base}, parts...)...)
	if os.MkdirAll(dir, 0755) != nil {
		return base
	}
	return dir
}

// runLogsDir is where the log of a run started at t goes: one folder a month.
func (a *app) runLogsDir(t time.Time) string {
	return a.dataSubdir(runLogsSubdir, t.Format("2006-01"))
}
