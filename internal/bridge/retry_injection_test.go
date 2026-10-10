package bridge

import (
	"strings"
	"testing"
	"time"
)

// PI_MOCK_RETRY_ONCE=1 时应先演一次"瞬时错误 → 自动重试"：
// 界面据此能进入"重试中"状态（「停止重试」按钮只在那个状态下出现）。
// 这里从**服务层**观察：事件会经 pi:event 推给界面，测试里看 mock 的状态与请求结果。
func TestMockRetryInjection(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	t.Setenv("PI_MOCK_RETRY_ONCE", "1")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	if _, err := svc.Prompt("重试注入测试"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 重试等待 4 秒：期间用户按下「停止重试」（= abort_retry）应当被接受
	time.Sleep(700 * time.Millisecond)
	result, err := svc.AbortRetry()
	if err != nil {
		t.Fatalf("AbortRetry: %v", err)
	}
	if !result.Success {
		t.Fatalf("重试等待中调用 abort_retry 应该成功: %+v", result)
	}
	// mock 只在"确实在重试"时回 aborted=true，否则回 false——
	// 这正好验证了 abort_retry 没有误伤正常回合
	if !strings.Contains(result.Data, `"aborted":true`) {
		t.Errorf("重试等待中应回 aborted=true，实际 %s（说明注入没生效或时机不对）", result.Data)
	}
	waitTurnDone(t, svc)

	// 中止之后这一轮应当结束，不再有后续输出
	state, err := svc.GetState()
	if err != nil || !state.Success {
		t.Fatalf("GetState: %v", err)
	}
	if strings.Contains(state.Data, `"isStreaming":true`) {
		t.Error("中止重试后不该还在流式")
	}

	// 第二次 prompt 不应再演重试（只演一次）
	if _, err := svc.Prompt("第二次不该再重试"); err != nil {
		t.Fatalf("Prompt(2): %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	second, err := svc.AbortRetry()
	if err != nil {
		t.Fatalf("AbortRetry(2): %v", err)
	}
	if !strings.Contains(second.Data, `"aborted":false`) {
		t.Errorf("第二次不该再有重试在等，实际 %s", second.Data)
	}
	waitTurnDone(t, svc)
}
