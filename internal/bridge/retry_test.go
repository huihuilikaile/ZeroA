package bridge

import (
	"strings"
	"testing"
)

// 重试控制：以前界面只能被动看 willRetry 事件，用户无法干预。
// 这两条命令要能打通：关掉自动重试、以及中止正在进行的重试。
func TestRetryControls(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// 懒启动：重试开关是发给 Pi 进程的命令，而 Start 之后还没有进程
	warmUpSession(t, svc)

	for _, enabled := range []bool{false, true} {
		result, err := svc.SetAutoRetry(enabled)
		if err != nil || !result.Success {
			t.Fatalf("SetAutoRetry(%v): %v %+v", enabled, err, result)
		}
		want := `"enabled":true`
		if !enabled {
			want = `"enabled":false`
		}
		if !strings.Contains(result.Data, want) {
			t.Errorf("enabled 回显不对，期望 %s 实际 %s", want, result.Data)
		}
	}

	aborted, err := svc.AbortRetry()
	if err != nil || !aborted.Success {
		t.Fatalf("AbortRetry: %v %+v", err, aborted)
	}
	// 没有重试在跑时也应当是"成功"（幂等）：用户点了不该报错
	if !strings.Contains(aborted.Data, "aborted") {
		t.Errorf("应如实回报结果，实际 %s", aborted.Data)
	}
}
