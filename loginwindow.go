//go:build windows

package main

import (
	"errors"
	"syscall"
	"unsafe"
)

// errLoginCancelled is returned when the user closes the sign-in window; the
// batch stops instead of reopening it for every remaining item.
var errLoginCancelled = errors.New("登入視窗已被關閉，已取消登入")

var (
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procFlashWindowEx            = user32.NewProc("FlashWindowEx")
)

const (
	swRestore      = 9
	hwndTopmost    = ^uintptr(0) // -1
	hwndNoTopmost  = ^uintptr(1) // -2
	swpNoMove      = 0x0002
	swpNoSize      = 0x0001
	swpShowWindow  = 0x0040
	flashwAll      = 0x00000003
	flashwTimerNoF = 0x0000000C
)

type flashWInfo struct {
	CbSize    uint32
	Hwnd      uintptr
	DwFlags   uint32
	UCount    uint32
	DwTimeout uint32
}

// browserTopWindows returns the visible top-level browser windows owned by pid.
func browserTopWindows(pid uint32) []uintptr {
	var found []uintptr
	cb := syscall.NewCallback(func(h, _ uintptr) uintptr {
		var owner uint32
		procGetWindowThreadProcessID.Call(h, uintptr(unsafe.Pointer(&owner)))
		if owner != pid {
			return 1
		}
		if vis, _, _ := procIsWindowVisible.Call(h); vis == 0 {
			return 1
		}
		cls := make([]uint16, 64)
		procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
		if syscall.UTF16ToString(cls) == "Chrome_WidgetWin_1" {
			found = append(found, h)
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// forceToFront makes sure the sign-in window cannot stay hidden behind other
// windows: restore it, briefly make it topmost, ask for the foreground and
// flash its taskbar button (Windows may refuse the foreground request when the
// user is busy in another program; the flashing and the in-app banner cover that).
func (b *cdpBrowser) forceToFront() {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	procAllowSetForegroundWindow.Call(uintptr(b.cmd.Process.Pid))
	for _, h := range browserTopWindows(uint32(b.cmd.Process.Pid)) {
		procShowWindow.Call(h, swRestore)
		procSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
		procSetWindowPos.Call(h, hwndNoTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
		procSetForegroundWindow.Call(h)
		fi := flashWInfo{CbSize: uint32(unsafe.Sizeof(flashWInfo{})), Hwnd: h, DwFlags: flashwAll | flashwTimerNoF}
		procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
	}
}

// showLoginWindow is bound to the 顯示登入視窗 button in the UI.
func (a *app) showLoginWindow() string {
	b := a.loginBrowser.Load()
	if b == nil || !b.alive() {
		return "目前沒有等待中的登入視窗。"
	}
	b.reveal(a.scale)
	return ""
}
