package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 文件面板的边界必须跟着"界面正在看的工作区"走，而不是"Pi 进程启动时用的目录"。
//
// 用户报的真实 bug：切换工作区要 Stop→Start，中间 workspaceDir 是空的（或还是旧目录），
// 那段时间文件面板请求新工作区的目录，就被判成越界并弹出
// "只能访问当前工作区内的文件"。
func TestFileBrowserFollowsCurrentWorkspace(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "note.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pi 还停在旧工作区：界面已经切到 second，此时列 second 必须放行
	svc := &PiService{workspaceDir: first}
	svc.SetActiveWorkspace(second)
	if result, err := svc.ListDir(second); err != nil || !result.Success {
		t.Fatalf("切到新工作区后应该能列它的目录: %v %+v", err, result)
	}
	if _, err := svc.ReadFileTextAt(filepath.Join(second, "note.txt"), 0, 1024); err != nil {
		t.Fatalf("新工作区里的文件应该能预览: %v", err)
	}
	// 旧工作区则应该被挡住（边界确实生效，不是把校验取消了）
	if _, err := svc.ListDir(first); err == nil {
		t.Error("已经切走之后，旧工作区应该越界")
	}

	// 进程停掉（workspaceDir 清空）之后，界面还停在这个工作区，面板仍要能用
	svc.mu.Lock()
	svc.workspaceDir = ""
	svc.mu.Unlock()
	if _, err := svc.ListDir(second); err != nil {
		t.Fatalf("进程停掉后仍应能列当前工作区: %v", err)
	}

	// 越界错误要把"当前工作区是谁"说清楚，否则用户没法判断问题出在哪
	svc2 := &PiService{}
	svc2.SetActiveWorkspace(first)
	_, err := svc2.ListDir(second)
	if err == nil || !strings.Contains(err.Error(), "当前工作区") {
		t.Errorf("越界错误应该带上当前工作区，实际 %v", err)
	}
}
