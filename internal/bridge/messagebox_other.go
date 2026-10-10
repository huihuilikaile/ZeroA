//go:build !windows

package bridge

import (
	"fmt"
	"os"
)

/**
 * 非 Windows 平台没有 user32，退化到 stderr。
 * 本项目的交付形态只有 Windows，这里存在的意义只是让 `go build` 在别的平台也能过。
 */
func MessageBox(title, text string) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", title, text)
}

func MessageBoxError(title, text string) {
	fmt.Fprintf(os.Stderr, "[%s] %s\n", title, text)
}

// stdoutUsable：非 Windows 平台一律按 stderr 可用处理（不弹框）。
func stdoutUsable() bool { return true }
