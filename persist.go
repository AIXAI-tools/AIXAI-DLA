//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data next to path and renames it into place, so an
// interrupted write never leaves a half-written file behind.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// notePersistError tells the user, once per run, that something they did
// could not be saved and will be gone after a restart.
func (a *app) notePersistError(what string, err error) {
	if err == nil || !a.persistWarned.CompareAndSwap(false, true) {
		return
	}
	a.postLog(fmt.Sprintf("⚠ 無法保存%s（%v）；重新開啟程式後可能回到之前的狀態。請確認磁碟空間與資料夾權限。\r\n", what, err))
}
