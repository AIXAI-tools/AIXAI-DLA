//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// isFullWindowsPath 判斷路徑是否為 Windows 上的完整絕對路徑。
// 判斷前會先去除頭尾空白。
// 完整路徑必須具有磁碟代號與根分隔符（例如 E:\x、E:/x），或是 UNC 網路共用路徑（例如 \\server\share\x）。
// 相對路徑（如 abc\def、.\x、..\x）、缺少磁碟代號（如 \abc）、缺少根分隔符（如 C:abc）或空字串皆判定為 false。
// 在 Windows 系統下，filepath.IsAbs 原生即要求磁碟代號後接分隔符或完整 UNC 路徑，符合此需求。
func isFullWindowsPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || p == `""` {
		return false
	}
	return filepath.IsAbs(p)
}

// invalidOutputPathMsg 產生儲存位置非完整路徑時的中文提示文字。
func invalidOutputPathMsg(p string) string {
	return fmt.Sprintf("儲存位置必須是完整路徑（例如 E:\\影片\\劇名）。目前填的是「%s」，沒有磁碟代號，檔案會存到找不到的地方。", p)
}

// validateStartOutput 檢查介面傳入的儲存位置：
// 空白儲存位置維持現有行為（回傳空字串，交由現有空白驗證處理，避免改變行為）；
// 若有填寫但非完整 Windows 路徑，則回傳錯誤提示字串。
func validateStartOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	if !isFullWindowsPath(trimmed) {
		return invalidOutputPathMsg(output)
	}
	return ""
}
