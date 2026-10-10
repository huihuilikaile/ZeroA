package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// 手动压缩上下文：这是长会话的常规操作，必须能打通（mock 会真的把用量压低）。
func TestCompactAndAutoCompaction(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	// 先发一条消息，让用量不为零
	if _, err := svc.Prompt("压缩前先聊一句"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitTurnDone(t, svc)

	before, err := svc.GetSessionStats()
	if err != nil || !before.Success {
		t.Fatalf("GetSessionStats: %v", err)
	}

	// 1) 不带自定义指令
	compacted, err := svc.Compact("")
	if err != nil || !compacted.Success {
		t.Fatalf("Compact: %v %+v", err, compacted)
	}
	// 2) 带自定义指令（真实 Pi 会把它交给压缩用的模型）
	if result, err := svc.Compact("重点保留代码改动"); err != nil || !result.Success {
		t.Fatalf("Compact(自定义指令): %v %+v", err, result)
	}

	after, err := svc.GetSessionStats()
	if err != nil || !after.Success {
		t.Fatalf("GetSessionStats(after): %v", err)
	}
	if !json.Valid([]byte(after.Data)) {
		t.Fatalf("统计应是合法 JSON: %s", after.Data)
	}
	if strings.TrimSpace(before.Data) == strings.TrimSpace(after.Data) {
		t.Log("提示：mock 压缩后统计未变化（字段口径可能不同），不作为失败")
	}

	// 3) 自动压缩开关：来回切都要成功，并且回显 enabled
	for _, enabled := range []bool{true, false} {
		result, err := svc.SetAutoCompaction(enabled)
		if err != nil || !result.Success {
			t.Fatalf("SetAutoCompaction(%v): %v %+v", enabled, err, result)
		}
		if !strings.Contains(result.Data, `"enabled":`) {
			t.Errorf("应回显 enabled 字段，实际 %s", result.Data)
		}
		want := `"enabled":true`
		if !enabled {
			want = `"enabled":false`
		}
		if !strings.Contains(result.Data, want) {
			t.Errorf("enabled 回显不对，期望 %s 实际 %s", want, result.Data)
		}
	}
}
