//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

// The Open dialog through IFileOpenDialog. GetOpenFileName lets Windows
// replace lpstrInitialDir with the folder this exe last used (any dialog,
// e.g. the screenshot picker), so 加入紀錄檔… did not open at the run logs;
// SetFolder always opens the folder we pass.

var (
	procCoCreateInstance             = ole32.NewProc("CoCreateInstance")
	procSHCreateItemFromParsingNameW = shell32.NewProc("SHCreateItemFromParsingName")

	clsidFileOpenDialog = syscall.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = syscall.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidShellItem        = syscall.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	clsctxInprocServer = 0x1
	fosNoChangeDir     = 0x8
	fosForceFileSystem = 0x40
	fosAllowMulti      = 0x200
	fosPathMustExist   = 0x800
	fosFileMustExist   = 0x1000
	sigdnFileSysPath   = 0x80058000

	// Vtable slots (IUnknown 0-2, IModalWindow::Show 3, IFileDialog 4-26, IFileOpenDialog 27-28).
	fdShow          = 3
	fdSetFileTypes  = 4
	fdSetOptions    = 9
	fdGetOptions    = 10
	fdSetFolder     = 12
	fdSetTitle      = 17
	fdGetResults    = 27
	unkRelease      = 2
	siaGetCount     = 7
	siaGetItemAt    = 8
	siGetDisplayNam = 5
)

type comdlgFilterSpec struct {
	name *uint16
	spec *uint16
}

// comCall calls method slot of the COM object obj.
func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	// obj points at the object, whose first word points at the vtable.
	head := *(**uintptr)(unsafe.Pointer(&obj))
	vtbl := *(**[64]uintptr)(unsafe.Pointer(head))
	r, _, _ := syscall.SyscallN(vtbl[slot], append([]uintptr{obj}, args...)...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, unkRelease)
	}
}

// comOpenFileDialog returns the chosen files (nil when cancelled); ok is false
// only when the dialog could not be created, so the caller can fall back.
func comOpenFileDialog(owner uintptr, title, initialDir string, multi bool, filterPairs ...string) (files []string, ok bool) {
	var dlg uintptr
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)), uintptr(unsafe.Pointer(&dlg))); hr != 0 || dlg == 0 {
		return nil, false
	}
	defer comRelease(dlg)

	var specs []comdlgFilterSpec
	for i := 0; i+1 < len(filterPairs); i += 2 {
		specs = append(specs, comdlgFilterSpec{utf16Ptr(filterPairs[i]), utf16Ptr(filterPairs[i+1])})
	}
	if len(specs) > 0 {
		comCall(dlg, fdSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	}
	var opts uint32
	comCall(dlg, fdGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosNoChangeDir | fosForceFileSystem | fosPathMustExist | fosFileMustExist
	if multi {
		opts |= fosAllowMulti
	}
	comCall(dlg, fdSetOptions, uintptr(opts))
	titlePtr := utf16Ptr(title)
	comCall(dlg, fdSetTitle, uintptr(unsafe.Pointer(titlePtr)))
	if dir := existingFolder(initialDir); dir != "" {
		var folder uintptr
		dirPtr := utf16Ptr(dir)
		if hr, _, _ := procSHCreateItemFromParsingNameW.Call(uintptr(unsafe.Pointer(dirPtr)), 0,
			uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&folder))); hr == 0 && folder != 0 {
			comCall(dlg, fdSetFolder, folder)
			comRelease(folder)
		}
		runtime.KeepAlive(dirPtr)
	}
	hr := comCall(dlg, fdShow, owner)
	runtime.KeepAlive(specs)
	runtime.KeepAlive(titlePtr)
	if hr != 0 { // cancelled (HRESULT_FROM_WIN32(ERROR_CANCELLED)) or failed after showing
		return nil, true
	}

	var items uintptr
	if comCall(dlg, fdGetResults, uintptr(unsafe.Pointer(&items))) != 0 || items == 0 {
		return nil, true
	}
	defer comRelease(items)
	var count uint32
	comCall(items, siaGetCount, uintptr(unsafe.Pointer(&count)))
	for i := uint32(0); i < count; i++ {
		var item uintptr
		if comCall(items, siaGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item))) != 0 || item == 0 {
			continue
		}
		var p *uint16
		if comCall(item, siGetDisplayNam, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))) == 0 && p != nil {
			if path := utf16PtrToString(p); path != "" {
				files = append(files, path)
			}
			procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
		}
		comRelease(item)
	}
	return files, true
}
