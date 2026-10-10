package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"zeroa/internal/bridge"
	"zeroa/internal/sessionstore"
)

// webviewProfileDir 给**每个安装**一个独立的 WebView2 用户数据目录。
//
// 为什么必须这么做（实测踩到的）：
// Wails 的 WindowsOptions.WebviewUserDataPath 默认为 `%APPDATA%\<可执行文件名>`，
// 于是同一台机器上所有 ZeroA 安装（便携包解压到不同目录、开发目录 bin/）**共用一份**
// WebView2 配置。而工作区列表、上次会话、界面偏好都存在 localStorage 里——
// 结果是：从 E:\...\ZeroA-blank 启动，侧栏里却列着另一个安装的工作区与历史，
// 看起来像"这次打开的目录没生效"。更糟的是两份安装不能同时运行：
// 第二个实例会以 `WebView2 Error 8007139f` 直接退出（也在实测里撞到过）。
//
// 身份取**可执行文件所在目录**：便携应用换目录就是换了一份安装，
// 这符合"拷走整个目录就是全部状态"的承诺。
//
// 为什么不放在安装目录里（比如 <exeDir>/.webview）：那是用户用来打包分发的目录，
// 塞一个几百 MB 的浏览器缓存进去，下一次打包就会被打进去。所以放在 LOCALAPPDATA 下，
// 用目录路径的短哈希做后缀——安装目录保持干净，各安装互不干扰。
//
// 返回空串表示"用 Wails 默认值"（只在拿不到 LOCALAPPDATA 时发生）。
func webviewProfileDir() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return webviewProfileDirFor(strings.TrimSpace(os.Getenv("LOCALAPPDATA")), sessionstore.ExeDir())
}

// webviewProfileDirFor 是纯函数版本（身份作为参数），便于测试：
// 测试进程的 exeDir() 永远是测试二进制自己，没法用"换目录"来验证隔离。
func webviewProfileDirFor(base, installDir string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	identity := strings.ToLower(filepath.Clean(installDir))
	if identity == "" || identity == "." {
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	suffix := hex.EncodeToString(sum[:4]) // 8 位十六进制足够区分本机上的若干安装
	dir := filepath.Join(base, "ZeroA", "webview-"+suffix)
	bridge.Logf("webview_profile dir=%s install=%s", dir, identity)
	return dir
}
