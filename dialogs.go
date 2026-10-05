//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const OFN_ALLOWMULTISELECT = 0x00000200

// Native Windows dialogs used by the web UI. They run on the UI thread (bound
// calls arrive there) and are owned by the main window.

func (a *app) pickFolder(current string) string {
	display := make([]uint16, 260)
	bi := browseInfo{
		HwndOwner:      a.hwnd,
		PszDisplayName: &display[0],
		LpszTitle:      utf16Ptr("選擇下載檔案的儲存資料夾"),
		UlFlags:        BIF_RETURNONLYFSDIRS | BIF_EDITBOX | BIF_NEWDIALOGSTYLE,
	}
	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)
	pathBuf := make([]uint16, 32768)
	if ok, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0]))); ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(pathBuf)
}

// openFileDialog shows the standard Open dialog; multi returns every selection.
func (a *app) openFileDialog(title, initialDir string, multi bool, filterPairs ...string) []string {
	fileBuf := make([]uint16, 65536)
	filter := multiString(filterPairs...)
	var initialPtr *uint16
	if strings.TrimSpace(initialDir) != "" {
		initialPtr = utf16Ptr(initialDir)
	}
	flags := uint32(OFN_PATHMUSTEXIST | OFN_FILEMUSTEXIST | OFN_EXPLORER | OFN_HIDEREADONLY)
	if multi {
		flags |= OFN_ALLOWMULTISELECT
	}
	ofn := openFileName{
		LStructSize:     uint32(unsafe.Sizeof(openFileName{})),
		HwndOwner:       a.hwnd,
		HInstance:       a.hInstance,
		LpstrFilter:     &filter[0],
		NFilterIndex:    1,
		LpstrFile:       &fileBuf[0],
		NMaxFile:        uint32(len(fileBuf)),
		LpstrInitialDir: initialPtr,
		LpstrTitle:      utf16Ptr(title),
		Flags:           flags,
	}
	if ok, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn))); ok == 0 {
		return nil
	}
	// Multi-select buffer: "dir\0file1\0file2\0\0"; single: "full path\0".
	var parts []string
	start := 0
	for i, c := range fileBuf {
		if c == 0 {
			if i == start {
				break
			}
			parts = append(parts, syscall.UTF16ToString(fileBuf[start:i]))
			start = i + 1
		}
	}
	if len(parts) <= 1 {
		return parts
	}
	dir := parts[0]
	var out []string
	for _, name := range parts[1:] {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

func (a *app) pickCookieFile() string {
	files := a.openFileDialog("選擇 Netscape 格式 cookies.txt", "", false, "Cookie 文字檔 (*.txt)", "*.txt", "所有檔案 (*.*)", "*.*")
	if len(files) == 0 {
		return ""
	}
	if err := validateCookieFile(files[0]); err != nil {
		if messageBox(a.hwnd, "Cookie 檔格式提醒", err.Error()+"\n\n仍要使用這個檔案嗎？", MB_YESNO|MB_ICONWARNING) != IDYES {
			return ""
		}
	}
	return files[0]
}

// pickResumeFile selects an unfinished .part/.ytdl file; the download folder is
// switched to its folder so yt-dlp resumes into the same file.
func (a *app) pickResumeFile(current string) string {
	files := a.openFileDialog("選擇先前未完成的 .part 或 .ytdl 檔案", current, false, "未完成下載檔案 (*.part;*.ytdl)", "*.part;*.ytdl", "所有檔案 (*.*)", "*.*")
	if len(files) == 0 {
		return ""
	}
	path := files[0]
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		messageBox(a.hwnd, "無法接續", "選取的檔案不存在或不是一般檔案。", MB_OK|MB_ICONERROR)
		return ""
	}
	a.resumePath = path
	a.postLog("✓ 已選擇接續檔：" + path + "\r\n請確認網址與原下載任務相同；yt-dlp 會依相同檔名與 .part／.ytdl 資訊接續。\r\n")
	return filepath.Dir(path)
}

func (a *app) openFolder(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = a.cfg.OutputFolder
	}
	if path == "" {
		return "尚未設定儲存位置。"
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return "無法開啟資料夾：" + err.Error()
	}
	procAllowSetForegroundWindow.Call(ASFW_ANY)
	r, _, callErr := procShellExecuteW.Call(a.hwnd, uintptr(unsafe.Pointer(utf16Ptr("open"))), uintptr(unsafe.Pointer(utf16Ptr(path))), 0, 0, SW_SHOWNORMAL)
	if r <= 32 {
		return fmt.Sprintf("無法開啟資料夾（代碼 %d）：%v", r, callErr)
	}
	return ""
}
