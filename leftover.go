//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Keeping the hidden capture browser from outliving the app, using standard
// Windows mechanisms only (no scripting, no scanning of other processes):
//
//  1. Job object with KILL_ON_JOB_CLOSE: every capture browser is placed in a
//     job owned by this process. When the app exits for any reason — normal
//     close, crash or being killed — Windows closes the job and ends the
//     browser with it.
//  2. Restart Manager: to clean up leftovers from versions that predate (1),
//     ask Windows which processes hold the capture profile's "lockfile" (the
//     same API installers use to find programs locking a file) and end only
//     those, after checking they are browser executables.

// errBrowserExitedEarly means the capture browser quit right after launch,
// almost always because a leftover capture browser still owns the profile and
// Chromium handed the launch over to it.
var errBrowserExitedEarly = errors.New("擷取用的瀏覽器啟動後立即結束（專用瀏覽器資料正被另一個背景程序使用）")

// captureProfileDir is the dedicated capture-browser profile. AIXAI_CAPTURE_PROFILE
// overrides it (testing/troubleshooting only, like AIXAI_UI_DEBUG).
func (a *app) captureProfileDir() string {
	if dir := strings.TrimSpace(os.Getenv("AIXAI_CAPTURE_PROFILE")); dir != "" {
		return dir
	}
	return filepath.Join(a.appDir, "capture-cdp-profile")
}

// ---------------------------------------------------------------- job object

var (
	captureJobOnce sync.Once
	captureJob     windows.Handle
)

func captureJobHandle() windows.Handle {
	captureJobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return
		}
		captureJob = h // intentionally kept open for the life of the app
	})
	return captureJob
}

// bindToAppLifetime places a launched capture browser in the kill-on-close job.
func bindToAppLifetime(pid int) {
	job := captureJobHandle()
	if job == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(job, h)
}

// ----------------------------------------------------------- restart manager

var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
	procQueryFullImageName  = kernel32.NewProc("QueryFullProcessImageNameW")
)

type rmUniqueProcess struct {
	ProcessID     uint32
	StartTimeLow  uint32
	StartTimeHigh uint32
}

type rmProcessInfo struct {
	Process          rmUniqueProcess
	AppName          [256]uint16
	ServiceShortName [64]uint16
	ApplicationType  uint32
	AppStatus        uint32
	TSSessionID      uint32
	Restartable      int32
}

// lockHolders returns the PIDs of processes that hold the given file open.
func lockHolders(path string) []uint32 {
	if procRmStartSession.Find() != nil {
		return nil
	}
	var session uint32
	key := make([]uint16, 33) // CCH_RM_SESSION_KEY + 1
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil
	}
	defer procRmEndSession.Call(uintptr(session))
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	files := []*uint16{p}
	if r, _, _ := procRmRegisterResources.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&files[0])), 0, 0, 0, 0); r != 0 {
		return nil
	}
	var needed, count, reasons uint32
	infos := make([]rmProcessInfo, 16)
	count = uint32(len(infos))
	r, _, _ := procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)),
		uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
	if r == 234 { // ERROR_MORE_DATA
		infos = make([]rmProcessInfo, needed)
		count = needed
		r, _, _ = procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)),
			uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
	}
	if r != 0 {
		return nil
	}
	var pids []uint32
	for i := uint32(0); i < count; i++ {
		pids = append(pids, infos[i].Process.ProcessID)
	}
	return pids
}

func processImageBase(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if r, _, _ := procQueryFullImageName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:size])))
}

// killCaptureProfileBrowsers ends the browser process trees that hold the
// capture profile's lock file. The user's everyday browser windows use other
// profiles and are never affected. Returns how many browser processes held it.
func killCaptureProfileBrowsers(profileDir string) int {
	n := 0
	for _, pid := range lockHolders(filepath.Join(profileDir, "lockfile")) {
		switch processImageBase(pid) {
		case "msedge.exe", "chrome.exe", "brave.exe", "vivaldi.exe":
		default:
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		kill := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(int(pid)), "/T", "/F")
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: CREATE_NO_WINDOW}
		_ = kill.Run()
		cancel()
		n++
	}
	return n
}

// cleanupLeftoverCaptureBrowsers stops capture browsers left behind by an
// earlier run (e.g. by an older version) and logs it.
func (a *app) cleanupLeftoverCaptureBrowsers() int {
	n := killCaptureProfileBrowsers(a.captureProfileDir())
	if n > 0 {
		a.postLog(fmt.Sprintf("✓ 已自動關閉上次殘留在背景的擷取瀏覽器（%d 個）。\r\n", n))
		time.Sleep(1500 * time.Millisecond) // let Chromium release the profile lock
	}
	return n
}

func captureStartStopError(err error) error {
	return &commandRunError{Cause: err, Summary: "擷取用的瀏覽器無法啟動，已停止整批下載（避免每一項都重複失敗）。\n\n" +
		"程式已自動嘗試關閉殘留的背景瀏覽器並重試，仍無法啟動。請先關閉本程式，再重新開機後重試；" +
		"若仍發生，請用右上角「回報」附上紀錄。\n\n錯誤：" + firstLine(err.Error())}
}
