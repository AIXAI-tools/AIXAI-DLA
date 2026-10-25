//go:build windows && amd64

package main

import "unsafe"

// Shell dialogs (folder picker, right-click menus) load third-party shell
// extensions into this process. Some of them host .NET, and the CLR raises
// access violations on purpose and handles them itself ("continue
// execution"). Go's last vectored continue handler treats any exception from
// non-Go code that reaches it as fatal and ends the program, so right-clicking
// in the folder dialog closed the whole app.
//
// foreignContinueHandler runs before Go's handlers: an exception outside this
// executable's image that was already handled keeps running; exceptions in Go
// code still go to the Go runtime (panics, recover and crash reports are
// unchanged). Written in assembly because calling into Go during exception
// dispatch is not safe.

var goImageLo, goImageHi uintptr

func foreignContinueHandler()
func foreignContinueHandlerAddr() uintptr

var (
	procGetCurrentProcess          = kernel32.NewProc("GetCurrentProcess")
	procK32GetModuleInformation    = kernel32.NewProc("K32GetModuleInformation")
	procAddVectoredContinueHandler = kernel32.NewProc("AddVectoredContinueHandler")
)

type moduleInfo struct {
	BaseOfDll   uintptr
	SizeOfImage uint32
	EntryPoint  uintptr
}

func installForeignContinueHandler() {
	base, _, _ := procGetModuleHandleW.Call(0)
	proc, _, _ := procGetCurrentProcess.Call()
	var mi moduleInfo
	if ok, _, _ := procK32GetModuleInformation.Call(proc, base, uintptr(unsafe.Pointer(&mi)), unsafe.Sizeof(mi)); ok == 0 || mi.SizeOfImage == 0 {
		return
	}
	goImageLo, goImageHi = mi.BaseOfDll, mi.BaseOfDll+uintptr(mi.SizeOfImage)
	procAddVectoredContinueHandler.Call(1, foreignContinueHandlerAddr())
}
