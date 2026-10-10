package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 导出会话这条路径以前只在真实模式里才被走到，mock 的 export_html 直接报"不支持命令"，
// 于是"点了导出没反应"这类问题平时测不出来。现在 mock 也真的写文件，可以免费回归。
//
// 断言三件事：回传的 path 与请求一致、文件真的落盘且是 HTML、
// 不传路径时服务层会自己挑一个（并真的写出来）。
func TestExportHTMLWritesFile(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	// 默认导出路径落在会话目录下，用临时 agent 目录避免碰真实配置
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	service := &PiService{}
	if err := service.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 懒启动：导出要 Pi 进程真的写文件，而 Start 之后还没有进程
	warmUpSession(t, service)

	// 1) 指定路径
	target := filepath.Join(t.TempDir(), "session.html")
	result, err := service.ExportHTML(target)
	if err != nil {
		t.Fatalf("ExportHTML: %v", err)
	}
	if !result.Success {
		t.Fatalf("导出应该成功: %+v", result)
	}
	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("返回的 data 应是 {\"path\":...}: %v（%s）", err, result.Data)
	}
	if payload.Path != target {
		t.Errorf("回传路径应与请求一致，期望 %s 实际 %s", target, payload.Path)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("应该真的写出文件: %v", err)
	}
	if !strings.HasPrefix(string(content), "<!doctype") {
		t.Errorf("导出的应是 HTML，实际开头: %q", string(content[:minInt(40, len(content))]))
	}

	// 2) 不传路径：服务层自己挑（会话目录下），并且文件要真的存在
	result, err = service.ExportHTML("")
	if err != nil || !result.Success {
		t.Fatalf("不传路径也应导出成功: %v %+v", err, result)
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Path == "" {
		t.Fatal("不传路径时应该回一个自动挑选的路径")
	}
	if _, err := os.Stat(payload.Path); err != nil {
		t.Errorf("自动挑选的路径也该真有文件: %v（%s）", err, payload.Path)
	}
	_ = os.Remove(payload.Path)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
