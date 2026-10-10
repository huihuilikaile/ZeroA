//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"zeroa/internal/bridge"
	"zeroa/internal/sessionstore"
)

// 单实例：同一安装（= 同一份 WebView2 用户数据目录）只允许跑一个窗口。
//
// 为什么必须拦在这里（实测踩到）：WebView2 的用户数据目录被第一个实例占着时，
// 第二个实例会在 Wails 内部无声地挂死——不弹窗、不写日志、进程留着（24MB 僵尸）。
// main.go 的 app_run_failed 兜底根本走不到（错误不经过 app.Run 返回）。
// 用户看到的就是"双击了，什么都没发生"——最难排查的一种故障。
//
// 互斥锁按**安装目录**命名（与 webviewProfileDir 同一口径）：
// 便携应用的不同目录是不同的安装，各有各的 WebView2 配置，可以同时跑；
// 同一目录的第二份才是要拦的。进程退出时由系统收回句柄，不用显式释放。

var user32 = windows.NewLazySystemDLL("user32.dll")

var (
	procFindWindow        = user32.NewProc("FindWindowW")
	procShowWindow        = user32.NewProc("ShowWindow")
	procSetForeground     = user32.NewProc("SetForegroundWindow")
	procEnumWindows       = user32.NewProc("EnumWindows")
	procGetWindowTextW    = user32.NewProc("GetWindowTextW")
	procGetWindowTextLenW = user32.NewProc("GetWindowTextLengthW")
	procIsWindowVisible   = user32.NewProc("IsWindowVisible")
)

const errorAlreadyExists = syscall.Errno(183)

// ensureSingleInstance 返回 true 表示本进程可以继续（是第一个实例）。
// 返回 false 表示已有一个实例在跑：先把它的窗口翻到前台，然后本进程安静退出。
func ensureSingleInstance() bool {
	identity := strings.ToLower(filepath.Clean(sessionstore.ExeDir()))
	sum := sha256.Sum256([]byte(identity))
	name, err := windows.UTF16PtrFromString("ZeroA-SingleInstance-" + hex.EncodeToString(sum[:8]))
	if err != nil {
		return true // 名字都拼不出来时不拦：别让保险丝变成点火器
	}
	_, err = windows.CreateMutex(nil, false, name)
	if err != errorAlreadyExists {
		// nil = 拿到锁（第一个实例）；其它错误（权限等）同样不拦
		return true
	}
	bridge.Logf("single_instance_refused install=%s", identity)
	bringExistingToFront()
	return false
}

// bringExistingToFront 把已经跑着那个实例的窗口翻到前台。
//
// 找不到窗口（它可能正在启动、窗口还没建出来）就退而弹一个提示框——
// 用户至少知道"不是没反应，是已经开着一个"。
func bringExistingToFront() {
	if hwnd := findMainWindow(); hwnd != 0 {
		const swRestore = 9
		procShowWindow.Call(hwnd, swRestore)
		procSetForeground.Call(hwnd)
		return
	}
	bridge.MessageBoxError("ZeroA 已在运行", "ZeroA 已经有一个实例在运行。\n同一安装目录只能运行一个窗口（WebView2 用户数据目录被占用）。\n请到任务栏找到已有窗口，或先关闭它再启动。")
}

// findMainWindow 按窗口标题找第一个实例的主窗口。
//
// 为什么用 EnumWindows 而不是 FindWindowW：标题里有全角破折号与特殊空格，
// 精确匹配容易因版本微调漏掉；枚举 + 前缀匹配更稳。
func findMainWindow() uintptr {
	const titlePrefix = "ZeroA — Multi-Agent Security Workbench"
	var found uintptr
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1 // 继续枚举
		}
		length, _, _ := procGetWindowTextLenW.Call(hwnd)
		if length == 0 {
			return 1
		}
		buffer := make([]uint16, length+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(length+1))
		if strings.HasPrefix(syscall.UTF16ToString(buffer), titlePrefix) {
			found = hwnd
			return 0 // 找到就停
		}
		return 1
	})
	procEnumWindows.Call(callback, 0)
	return found
}
