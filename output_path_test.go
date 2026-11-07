//go:build windows

package main

import (
	"testing"
)

func TestIsFullWindowsPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`E:\x`, true},
		{`E:/x`, true},
		{`\\server\share\x`, true},
		{`abc\def`, false},
		{`\abc`, false},
		{`C:abc`, false},
		{`""`, false},
		{``, false},
		{`.\x`, false},
		{`..\x`, false},
		{`  E:\x  `, true},
		{`  abc\def  `, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			got := isFullWindowsPath(tc.path)
			if got != tc.want {
				t.Errorf("isFullWindowsPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestValidateStartOutput(t *testing.T) {
	// 空白儲存位置維持現有行為（回傳空字串表示交由現有空白驗證處理）
	if got := validateStartOutput(""); got != "" {
		t.Errorf("validateStartOutput(\"\") = %q, want empty", got)
	}
	if got := validateStartOutput("   "); got != "" {
		t.Errorf("validateStartOutput(\"   \") = %q, want empty", got)
	}

	// 完整路徑回傳空字串（驗證通過）
	if got := validateStartOutput(`E:\影片\劇名`); got != "" {
		t.Errorf("validateStartOutput(`E:\\影片\\劇名`) = %q, want empty", got)
	}

	// 相對路徑回傳指定提示文字
	input := `測試劇\第一季`
	wantErr := "儲存位置必須是完整路徑（例如 E:\\影片\\劇名）。目前填的是「測試劇\\第一季」，沒有磁碟代號，檔案會存到找不到的地方。"
	got := validateStartOutput(input)
	if got != wantErr {
		t.Errorf("validateStartOutput(%q) = %q, want %q", input, got, wantErr)
	}
}
