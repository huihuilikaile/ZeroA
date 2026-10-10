//go:build !windows

package tools

import "os/exec"

// hideWindow 在非 Windows 平台无事可做（没有控制台窗口闪出的问题）。
func hideWindow(cmd *exec.Cmd) {}
