//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

// hideWindow 让检测命令不闪出控制台窗口。
//
// 检测会在启动时跑几十个子进程；不隐藏窗口的话，用户会看到一串黑框闪过
// ——那是"这个程序在偷跑东西"的观感，很糟糕。
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags = 0x08000000 // CREATE_NO_WINDOW
}
