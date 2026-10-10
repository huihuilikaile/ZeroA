package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// 回放历史时的工具配对：会话里一次工具调用是两条记录
// （assistant 的 toolCall 块 + role=toolResult 且带消息级 toolCallId 的消息）。
// 以前 mock 只产出前者，于是"回放后工具行变成『没有对应记录』"这个 bug 一直没被测到。
// 这条测试同时钉住 mock 的产出形状：必须真的给出 toolResult 消息与 toolCallId。
func TestMockHistoryHasToolResults(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	if _, err := svc.Prompt("跑一下工具"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitTurnDone(t, svc)

	result, err := svc.GetMessages()
	if err != nil || !result.Success {
		t.Fatalf("GetMessages: %v %+v", err, result)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
			// toolResult 的配对信息在消息级
			ToolCallID string `json:"toolCallId"`
			ToolName   string `json:"toolName"`
			IsError    *bool  `json:"isError"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 1) 要有 toolResult 消息，且带 toolCallId
	var results int
	for _, m := range payload.Messages {
		if m.Role == "toolResult" {
			results++
			if m.ToolCallID == "" {
				t.Errorf("toolResult 必须带消息级 toolCallId（界面靠它配对）：%+v", m)
			}
			if m.ToolName == "" {
				t.Errorf("toolResult 应带 toolName：%+v", m)
			}
			if m.IsError == nil {
				t.Errorf("toolResult 应带 isError（哪怕是 false）：%+v", m)
			}
		}
	}
	if results == 0 {
		t.Fatalf("mock 必须产出 toolResult 消息，否则历史回放路径无法测试：%s", result.Data)
	}

	// 2) 助手消息里的 toolCall 块要能被界面认出 id（两种拼写都认，这里校验 mock 侧的形状）
	if !strings.Contains(result.Data, "toolCallId") {
		t.Errorf("助手消息里的工具调用块应带 id/toolCallId：%s", result.Data)
	}
}
