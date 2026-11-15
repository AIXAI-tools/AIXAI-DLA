//go:build windows

package main

import "unsafe"

// Background mode: the capture browser works out of the user's way (minimised,
// never taking keyboard focus) and is only brought forward when the user has
// to act in it — signing in, solving a check, or pressing play.

func (b *cdpBrowser) setWindowState(state string) {
	if b.windowID == 0 {
		return
	}
	_, _ = b.browserCall("Browser.setWindowBounds", map[string]interface{}{"windowId": b.windowID, "bounds": map[string]interface{}{"windowState": state}})
}

// hide sends the window to the background without stealing focus.
func (b *cdpBrowser) hide() {
	b.setFocusEmulation(true)
	b.setWindowState("minimized")
	b.visible = false
	b.setRestorePlacement()
}

// followUserRestore notices the user bringing the minimised window back from
// the taskbar on their own (before the capture shows it) and stops the focus
// emulation, as reveal does, so the page sees where the user really is.
func (b *cdpBrowser) followUserRestore() {
	if b.visible || b.cmd == nil || b.cmd.Process == nil {
		return
	}
	for _, hw := range browserTopWindows(uint32(b.cmd.Process.Pid)) {
		if iconic, _, _ := procIsIconic.Call(hw); iconic == 0 {
			b.visible = true
			b.setFocusEmulation(false)
			return
		}
	}
}

// windowPlacement is the Win32 WINDOWPLACEMENT structure.
type windowPlacement struct {
	Length         uint32
	Flags          uint32
	ShowCmd        uint32
	MinPosition    point
	MaxPosition    point
	NormalPosition rect
}

const swShowMinNoActive = 7

// setRestorePlacement makes the minimised window come back centred on the
// work area when the user restores it from the taskbar. It starts off screen
// (--window-position), and restored there it could be seen in the taskbar
// preview but not reached.
func (b *cdpBrowser) setRestorePlacement() {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	scale := b.scale
	if scale <= 0 {
		scale = 1
	}
	var work rect
	procSystemParametersInfoW.Call(SPI_GETWORKAREA, 0, uintptr(unsafe.Pointer(&work)), 0)
	// The normal position is in work-area coordinates.
	areaW, areaH := work.Right-work.Left, work.Bottom-work.Top
	ww, wh := int32(1180*scale), int32(820*scale)
	if ww > areaW {
		ww = areaW
	}
	if wh > areaH {
		wh = areaH
	}
	left, top := (areaW-ww)/2, (areaH-wh)/2
	for _, hw := range browserTopWindows(uint32(b.cmd.Process.Pid)) {
		wp := windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
		if ok, _, _ := procGetWindowPlacement.Call(hw, uintptr(unsafe.Pointer(&wp))); ok == 0 {
			continue
		}
		if n := wp.NormalPosition; n.Left >= 0 && n.Top >= 0 && n.Right <= areaW && n.Bottom <= areaH {
			continue
		}
		wp.NormalPosition = rect{left, top, left + ww, top + wh}
		wp.ShowCmd = swShowMinNoActive
		procSetWindowPlacement.Call(hw, uintptr(unsafe.Pointer(&wp)))
	}
}

// setFocusEmulation makes the page act focused and visible even while
// minimised (players may refuse to start otherwise). It is turned off while
// the user acts in the window, so pages that check the window really lost
// focus (e.g. while an ad opened in another tab) see what actually happens.
func (b *cdpBrowser) setFocusEmulation(on bool) {
	if b.session() == "" {
		return
	}
	_ = b.send("Emulation.setFocusEmulationEnabled", map[string]interface{}{"enabled": on})
}

// reveal brings the window on screen, centred on the work area, for user action.
func (b *cdpBrowser) reveal(scale float64) {
	if b.windowID == 0 {
		return
	}
	b.setWindowState("normal")
	b.setFocusEmulation(false)
	var work rect
	procSystemParametersInfoW.Call(SPI_GETWORKAREA, 0, uintptr(unsafe.Pointer(&work)), 0)
	ww, wh := int(float64(1180)*scale), int(float64(820)*scale)
	if ww > int(work.Right-work.Left) {
		ww = int(work.Right - work.Left)
	}
	if wh > int(work.Bottom-work.Top) {
		wh = int(work.Bottom - work.Top)
	}
	left := int(work.Left) + (int(work.Right-work.Left)-ww)/2
	top := int(work.Top) + (int(work.Bottom-work.Top)-wh)/2
	_, _ = b.browserCall("Browser.setWindowBounds", map[string]interface{}{"windowId": b.windowID,
		"bounds": map[string]interface{}{"left": left, "top": top, "width": ww, "height": wh}})
	_ = b.send("Page.bringToFront", nil)
	b.visible = true
	b.forceToFront()
	// The window starts off screen (--window-position); when the DevTools
	// move above does not take (seen with display scaling), move it with
	// Windows itself so it never stays out of reach.
	b.moveOnScreen(work, left, top, ww, wh)
}

// moveOnScreen places the browser's top windows at (left, top, w, h), in
// screen pixels, when they do not lie wholly inside the work area (DevTools
// bounds are in the browser's own scaled units, so with display scaling the
// window may come out too large or off screen).
func (b *cdpBrowser) moveOnScreen(work rect, left, top, w, h int) {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	for _, hw := range browserTopWindows(uint32(b.cmd.Process.Pid)) {
		var r rect
		procGetWindowRect.Call(hw, uintptr(unsafe.Pointer(&r)))
		// A few pixels of slack: maximised and snapped windows reach just past
		// the work area's edges.
		const slack = 16
		if r.Left >= work.Left-slack && r.Top >= work.Top-slack && r.Right <= work.Right+slack && r.Bottom <= work.Bottom+slack {
			continue
		}
		procShowWindow.Call(hw, swRestore)
		procSetWindowPos.Call(hw, 0, uintptr(left), uintptr(top), uintptr(w), uintptr(h), swpShowWindow)
	}
}
