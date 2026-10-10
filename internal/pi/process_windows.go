package pi

import (
	"os/exec"
	"syscall"
)

// 文件名以 _windows.go 结尾，Go 就只在 Windows 上编译这个文件——
// 这叫构建约束，等价于在文件顶部写 //go:build windows。
//
// HideWindow 让子进程不弹出黑色控制台窗口。桌面程序本身是 GUI 程序，
// 若不设置，每次启动 Pi 都会在屏幕上闪一个控制台窗口。
// 对照 process_other.go：同一个函数名在其他平台是空实现。
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
