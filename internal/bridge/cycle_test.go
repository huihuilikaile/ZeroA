package bridge

import (
	"strings"
	"testing"
)

// 「下一个模型 / 下一个等级」：cycle_model / cycle_thinking_level。
// 这类快捷动作最容易出的问题是"切了但没生效"，所以断言要看到**返回值变了**。
func TestCycleCommands(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// 懒启动：cycle_model 要问 Pi 进程，而 Start 之后还没有进程
	warmUpSession(t, svc)

	// 模型：连切两次，两次的返回值应当不同（说明真的在列表里循环）
	first, err := svc.CycleModel()
	if err != nil || !first.Success {
		t.Fatalf("CycleModel: %v %+v", err, first)
	}
	second, err := svc.CycleModel()
	if err != nil || !second.Success {
		t.Fatalf("CycleModel(2): %v %+v", err, second)
	}
	if first.Data == second.Data {
		t.Errorf("连切两次应当换到不同模型，两次都是 %s", first.Data)
	}
	if !strings.Contains(first.Data, "modelId") {
		t.Errorf("应回切换后的模型 id，实际 %s", first.Data)
	}

	// 等级：同样连切，并且每次都应是合法档位
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		result, err := svc.CycleThinkingLevel()
		if err != nil || !result.Success {
			t.Fatalf("CycleThinkingLevel(%d): %v %+v", i, err, result)
		}
		if !strings.Contains(result.Data, `"level"`) {
			t.Errorf("应回 level 字段，实际 %s", result.Data)
		}
		seen[result.Data] = true
	}
	if len(seen) < 2 {
		t.Errorf("连切四次应当出现多个档位，实际只有 %d 种", len(seen))
	}
}
