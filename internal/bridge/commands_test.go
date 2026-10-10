package bridge

import (
	"encoding/json"
	"testing"
)

// 输入区的斜杠面板靠 get_commands 提供数据。这条链路（service → mock）必须有测试，
// 否则面板"空着不显示"这种现象分不清是前端没渲染还是后端没数据。
func TestGetCommandsFromMock(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	service := &PiService{}
	if err := service.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 懒启动：命令列表来自 Pi 进程（get_commands），Start 之后还没有进程
	warmUpSession(t, service)

	result, err := service.GetCommands()
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if !result.Success {
		t.Fatalf("get_commands 应该成功: %+v", result)
	}
	var payload struct {
		Commands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      string `json:"source"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("data 应是 {commands:[...]}: %v（%s）", err, result.Data)
	}
	if len(payload.Commands) != 3 {
		t.Fatalf("mock 应返回 3 条命令，实际 %d: %s", len(payload.Commands), result.Data)
	}
	if payload.Commands[0].Name == "" || payload.Commands[0].Source == "" {
		t.Errorf("命令要有 name 与 source: %+v", payload.Commands[0])
	}
}
