package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★ 两个安装必须拿到**不同的** WebView2 用户数据目录。
//
// 这是用户报的 bug：从 E:\...\ZeroA-blank 启动，侧栏却显示另一个安装的工作区与历史——
// 因为 Wails 默认把 WebView2 数据放在 `%APPDATA%\<exe名>`，同名可执行文件共用一份，
// localStorage（工作区列表、上次会话、界面偏好）就跨安装串台了；
// 顺带还导致两份安装不能同时运行（第二个实例 WebView2 Error 8007139f）。
func TestWebviewProfileIsPerInstall(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)

	dirA := filepath.Join(t.TempDir(), "install-a")
	dirB := filepath.Join(t.TempDir(), "install-b")
	for _, dir := range []string{dirA, dirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	profileFor := func(install string) string { return webviewProfileDirFor(local, install) }

	profileA := profileFor(dirA)
	profileB := profileFor(dirB)

	if profileA == "" || profileB == "" {
		t.Fatal("应当给出各自的 WebView 数据目录")
	}
	if profileA == profileB {
		t.Fatalf("不同安装不能共用同一份数据目录：%s", profileA)
	}
	for _, profile := range []string{profileA, profileB} {
		if !strings.HasPrefix(profile, local+string(os.PathSeparator)) {
			t.Errorf("应当放在 LOCALAPPDATA 下（安装目录要保持干净，便于打包分发）：%s", profile)
		}
	}

	// 同一个安装重复调用必须稳定，否则每次启动都换一份配置（等于没记住任何偏好）
	if again := profileFor(dirA); again != profileA {
		t.Errorf("同一安装两次调用结果不一致：%s vs %s", profileA, again)
	}
}

// 拿不到 LOCALAPPDATA 时退回 Wails 默认值（空串），而不是崩在这里。
func TestWebviewProfileFallsBackToDefault(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "")
	if got := webviewProfileDir(); got != "" {
		t.Errorf("拿不到 LOCALAPPDATA 时应返回空串（即用默认），得到 %q", got)
	}
}
