package bridge

import (
	"strings"
	"testing"
)

// 「复制上一条回答」的数据来源：get_last_assistant_text。
// 聊天之后必须能拿到一条非空文本；没聊过则应当是空串（而不是报错）。
func TestLastAssistantText(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// 懒启动：最后一条回答要问 Pi 进程，而 Start 之后还没有进程
	warmUpSession(t, svc)

	// 没聊过：空串，且不是错误
	empty, err := svc.GetLastAssistantText()
	if err != nil || !empty.Success {
		t.Fatalf("GetLastAssistantText(空会话): %v %+v", err, empty)
	}
	if !strings.Contains(empty.Data, `"text"`) {
		t.Errorf("应含 text 字段，实际 %s", empty.Data)
	}

	if _, err := svc.Prompt("问一句"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitTurnDone(t, svc)

	after, err := svc.GetLastAssistantText()
	if err != nil || !after.Success {
		t.Fatalf("GetLastAssistantText: %v %+v", err, after)
	}
	if !strings.Contains(after.Data, "这一轮发生了什么") {
		t.Errorf("应回最后一条助手回答的正文（mock 的固定文案），实际: %s", after.Data)
	}
}
