package bridge

import (
	"strings"
	"testing"
)

// TestRespondAgentUIValidation 子智能体 UI 应答的入站校验：
// 规则与主会话 RespondUI 完全同一套（parseExtensionUIResponse），
// 非法载荷必须在碰到任何进程句柄之前就被拦下。
func TestRespondAgentUIValidation(t *testing.T) {
	svc := &PiService{}

	valid := `{"type":"extension_ui_response","id":"req-1","confirmed":true}`

	cases := []struct {
		name    string
		agentID string
		payload string
		wantErr string
	}{
		{"空载荷", "agent-1", "", "不能为空"},
		{"非 JSON", "agent-1", "not-json", "不是合法 JSON"},
		{"type 不对", "agent-1", `{"type":"nope","id":"req-1"}`, "extension_ui_response"},
		{"缺 id", "agent-1", `{"type":"extension_ui_response","confirmed":true}`, "缺少 id"},
		{"三选一都没有", "agent-1", `{"type":"extension_ui_response","id":"req-1"}`, "confirmed、value 或 cancelled"},
		{"id 路径穿越", "../evil", valid, "不合法"},
		{"id 形状不对", "main", valid, "不合法"},
		{"子智能体不存在", "agent-99", valid, "没有这个子智能体"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RespondAgentUI(tc.agentID, tc.payload)
			if err == nil {
				t.Fatalf("应当拒绝，却成功了")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息应含 %q，实际: %v", tc.wantErr, err)
			}
		})
	}
}
