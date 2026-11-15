//go:build windows

package main

import (
	"syscall"
)

const (
	WS_OVERLAPPED       = 0x00000000
	WS_CAPTION          = 0x00C00000
	WS_SYSMENU          = 0x00080000
	WS_THICKFRAME       = 0x00040000
	WS_MINIMIZEBOX      = 0x00020000
	WS_MAXIMIZEBOX      = 0x00010000
	WS_OVERLAPPEDWINDOW = WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU | WS_THICKFRAME | WS_MINIMIZEBOX | WS_MAXIMIZEBOX
	WS_CHILD            = 0x40000000
	WS_VISIBLE          = 0x10000000
	WS_TABSTOP          = 0x00010000
	WS_BORDER           = 0x00800000
	WS_HSCROLL          = 0x00100000
	WS_VSCROLL          = 0x00200000
	WS_CLIPCHILDREN     = 0x02000000

	CS_VREDRAW = 0x0001
	CS_HREDRAW = 0x0002

	WS_EX_CLIENTEDGE = 0x00000200

	ES_LEFT        = 0x0000
	ES_MULTILINE   = 0x0004
	ES_AUTOVSCROLL = 0x0040
	ES_AUTOHSCROLL = 0x0080
	ES_WANTRETURN  = 0x1000
	ES_READONLY    = 0x0800
	ES_NOHIDESEL   = 0x0100

	BS_PUSHBUTTON    = 0x00000000
	BS_DEFPUSHBUTTON = 0x00000001
	BS_AUTOCHECKBOX  = 0x00000003

	CBS_DROPDOWNLIST = 0x0003
	CBS_HASSTRINGS   = 0x0200

	SS_LEFT           = 0x00000000
	SS_LEFTNOWORDWRAP = 0x0000000C
	SS_ENDELLIPSIS    = 0x00004000
	SS_CENTER         = 0x00000001

	SW_HIDE       = 0
	SW_SHOWNORMAL = 1
	SW_SHOW       = 5
	SW_RESTORE    = 9

	CW_USEDEFAULT = ^uint32(0x7fffffff)

	WM_CREATE         = 0x0001
	WM_DESTROY        = 0x0002
	WM_SIZE           = 0x0005
	WM_DPICHANGED     = 0x02E0
	designWidth       = 1120
	designHeight      = 760
	WM_CLOSE          = 0x0010
	WM_COMMAND        = 0x0111
	WM_SETFONT        = 0x0030
	WM_SETREDRAW      = 0x000B
	WM_APP            = 0x8000
	WM_CTLCOLORSTATIC = 0x0138
	WM_EXITSIZEMOVE   = 0x0232
	WM_SETICON        = 0x0080

	WM_APP_LOG    = WM_APP + 1
	WM_APP_STATUS = WM_APP + 2
	WM_APP_DONE   = WM_APP + 3
	WM_APP_INIT   = WM_APP + 4

	EM_SETSEL              = 0x00B1
	EM_REPLACESEL          = 0x00C2
	EM_SETREADONLY         = 0x00CF
	EM_GETLINECOUNT        = 0x00BA
	EM_GETFIRSTVISIBLELINE = 0x00CE
	EM_LINEINDEX           = 0x00BB
	EM_LINEFROMCHAR        = 0x00C9
	EM_LINESCROLL          = 0x00B6
	EM_SETLIMITTEXT        = 0x00C5
	EM_EXLIMITTEXT         = 0x0435
	EM_EXLINEFROMCHAR      = 0x0436
	EM_EXSETSEL            = 0x0437
	EM_SETBKGNDCOLOR       = 0x0443

	BM_GETCHECK = 0x00F0
	BM_SETCHECK = 0x00F1
	BST_CHECKED = 1

	CB_ADDSTRING       = 0x0143
	CB_GETCURSEL       = 0x0147
	CB_SETCURSEL       = 0x014E
	CB_SETDROPPEDWIDTH = 0x0160

	BN_CLICKED    = 0
	CBN_SELCHANGE = 1

	MB_OK              = 0x00000000
	MB_ICONERROR       = 0x00000010
	MB_ICONQUESTION    = 0x00000020
	MB_ICONWARNING     = 0x00000030
	MB_ICONINFORMATION = 0x00000040
	MB_YESNO           = 0x00000004
	IDYES              = 6

	IDC_ARROW = 32512

	IMAGE_ICON           = 1
	ICON_SMALL           = 0
	ICON_BIG             = 1
	APP_ICON_RESOURCE_ID = 1

	COLOR_WINDOW = 5

	SM_CXSCREEN = 0
	SM_CYSCREEN = 1

	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
	SWP_NOCOPYBITS = 0x0100

	SPI_GETWORKAREA = 0x0030

	BIF_RETURNONLYFSDIRS = 0x0001
	BIF_EDITBOX          = 0x0010
	BIF_NEWDIALOGSTYLE   = 0x0040

	OFN_PATHMUSTEXIST = 0x00000800
	OFN_FILEMUSTEXIST = 0x00001000
	OFN_EXPLORER      = 0x00080000
	OFN_HIDEREADONLY  = 0x00000004

	COINIT_APARTMENTTHREADED = 0x2

	RDW_INVALIDATE  = 0x0001
	RDW_ERASE       = 0x0004
	RDW_ALLCHILDREN = 0x0080
	RDW_UPDATENOW   = 0x0100
	RDW_FRAME       = 0x0400

	OPAQUE   = 2
	ASFW_ANY = 0xffffffff

	CREATE_NO_WINDOW         = 0x08000000
	CREATE_NEW_PROCESS_GROUP = 0x00000200
)

const (
	idURL = 1001 + iota
	idMode
	idFormat
	idOutput
	idBrowse
	idNoPlaylist
	idPlaylistFolder
	idExtra
	idExtraPreset
	idCookieBrowser
	idCookieFileBrowse
	idCookieFileClear
	idSequence
	idSequenceStart
	idSequenceEnd
	idResume
	idStart
	idStop
	idUpdate
	idOpenFolder
	idLog
	idStatus
	idEnv
	idCopyLog
	idSafeMode
	idAdvanced
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type charRange struct{ CpMin, CpMax int32 }
type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type browseInfo struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName *uint16
	LpszTitle      *uint16
	UlFlags        uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
}

type openFileName struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	comdlg32 = syscall.NewLazyDLL("comdlg32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	shcore   = syscall.NewLazyDLL("shcore.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procUpdateWindow                  = user32.NewProc("UpdateWindow")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procSendMessageW                  = user32.NewProc("SendMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procSetWindowTextW                = user32.NewProc("SetWindowTextW")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = user32.NewProc("GetWindowTextLengthW")
	procEnableWindow                  = user32.NewProc("EnableWindow")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procGetWindowPlacement            = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement            = user32.NewProc("SetWindowPlacement")
	procIsIconic                      = user32.NewProc("IsIconic")
	procRedrawWindow                  = user32.NewProc("RedrawWindow")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procAllowSetForegroundWindow      = user32.NewProc("AllowSetForegroundWindow")
	procGetClientRect                 = user32.NewProc("GetClientRect")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	procSetFocus                      = user32.NewProc("SetFocus")
	procSetBkMode                     = gdi32.NewProc("SetBkMode")
	procSetBkColor                    = gdi32.NewProc("SetBkColor")
	procSetTextColor                  = gdi32.NewProc("SetTextColor")
	procCreateSolidBrush              = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW                   = gdi32.NewProc("CreateFontW")
	procDeleteObject                  = gdi32.NewProc("DeleteObject")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	procLoadLibraryW                  = kernel32.NewProc("LoadLibraryW")
	procFreeLibrary                   = kernel32.NewProc("FreeLibrary")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procSHBrowseForFolderW            = shell32.NewProc("SHBrowseForFolderW")
	procGetOpenFileNameW              = comdlg32.NewProc("GetOpenFileNameW")
	procSHGetPathFromIDListW          = shell32.NewProc("SHGetPathFromIDListW")
	procShellExecuteW                 = shell32.NewProc("ShellExecuteW")
	procCoTaskMemFree                 = ole32.NewProc("CoTaskMemFree")
	procCoInitializeEx                = ole32.NewProc("CoInitializeEx")
	procCoUninitialize                = ole32.NewProc("CoUninitialize")
)
