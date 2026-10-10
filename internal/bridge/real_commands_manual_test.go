package bridge

import (
	"encoding/json"
	"testing"

	"zeroa/internal/pi"
)

// 真实模式验证：加载扩展/技能/模板之后，get_commands 应当能列出命令
// ——这正是"输入 / 弹出命令"的数据来源。不发模型请求，所以不花钱。
//
// 只有在能找到 Pi 时才跑（没有 Pi 的环境跳过，不当失败）。
func TestRealPiCommandsWithResources(t *testing.T) {
	if _, err := pi.ResolveCLIPath(""); err != nil {
		t.Skip("本机没有可用的 Pi，跳过真实模式验证")
	}
	svc = &PiService{}
	// LoadResources 默认 true（没显式设置过）
	if err := svc.Start("real", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start(real): %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// 懒启动：Start 只准备启动参数，真实 Pi 进程等第一条消息才起
	warmUpSession(t, svc)

	result, err := svc.GetCommands()
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	if !result.Success {
		t.Fatalf("真实模式下 get_commands 应成功: %+v", result)
	}
	var payload struct {
		Commands []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("data 应是 {commands:[...]}: %v（%s）", err, result.Data)
	}
	t.Logf("真实 Pi 返回 %d 条命令", len(payload.Commands))
	for i, cmd := range payload.Commands {
		if i >= 8 {
			break
		}
		t.Logf("  /%s  (%s)", cmd.Name, cmd.Source)
	}
	if len(payload.Commands) == 0 {
		t.Log("注意：本机没有装任何扩展/技能/提示模板，所以列表为空——界面上会显示提示而不是没反应")
	}
}
