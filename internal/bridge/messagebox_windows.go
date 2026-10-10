//go:build windows

package bridge

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

/**
 * MessageBox 是"没有控制台也要让用户看见一句话"的兜底通道。
 *
 * 为什么必须自己调 user32：发布构建带 -H windowsgui，是 GUI 子系统进程，
 * 从资源管理器或 cmd 启动时都没有可用 stdout——
 * `zeroa.exe --machine-code` 只 fmt.Println 的话，用户看到的是"双击了，什么都没发生"。
 * 同理，app.Run() 失败（WebView2 缺失、端口占用）时也只有这条路能把原因显示出来。
 *
 * 用 x/sys/windows 直接调，不引第三方依赖。
 */
var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbIconInfo      = 0x00000040
	mbSetForeground = 0x00010000
)

func MessageBox(title, text string) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	// 失败（例如没有桌面会话）也不做事：这是一条"尽力而为"的兜底通道。
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconInfo|mbSetForeground))
}

// MessageBoxError 用一个更醒目的图标显示错误。
func MessageBoxError(title, text string) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconError|mbSetForeground))
}

/**
 * stdoutUsable 判断当前进程的 stdout 有没有去处。
 *
 * 为什么不能只看"有没有控制台"：GUI 子系统进程被 cmd/PowerShell 启动时同样没有控制台，
 * 但调用方**给了 stdout 句柄**（管道或重定向的文件），输出是能拿到的。
 * 反过来，从资源管理器双击启动时句柄是无效的，写出去就消失了。
 *
 * 判定结果决定"要不要弹消息框"：
 *   - 有可用 stdout（脚本、重定向、CI）→ 只打印，不弹模态框（否则脚本被卡住等点击）；
 *   - 没有（双击/快捷方式）→ 弹框，否则用户什么都看不到。
 */
func stdoutUsable() bool {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return false
	}
	return h != 0 && h != windows.InvalidHandle
}
