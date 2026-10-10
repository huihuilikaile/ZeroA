package bridge

import (
	"strings"
	"testing"
)

// 插入 / 排到后面的投递模式：Pi 只接受 all 与 one-at-a-time。
// 本地先校验取值，而不是把错值发给 Pi 再翻译它那句英文错误。
func TestDeliveryModes(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// 懒启动：投递模式是发给 Pi 进程的命令，而 Start 之后还没有进程
	warmUpSession(t, svc)

	for _, mode := range []string{"all", "one-at-a-time"} {
		steering, err := svc.SetSteeringMode(mode)
		if err != nil || !steering.Success {
			t.Fatalf("SetSteeringMode(%s): %v %+v", mode, err, steering)
		}
		if !strings.Contains(steering.Data, mode) {
			t.Errorf("应回显 mode=%s，实际 %s", mode, steering.Data)
		}
		followUp, err := svc.SetFollowUpMode(mode)
		if err != nil || !followUp.Success {
			t.Fatalf("SetFollowUpMode(%s): %v %+v", mode, err, followUp)
		}
		if !strings.Contains(followUp.Data, mode) {
			t.Errorf("应回显 mode=%s，实际 %s", mode, followUp.Data)
		}
	}

	// 非法取值要在本地拦住（不产生 RPC 往返）
	for _, bad := range []string{"", "  ", "sometimes", "ALL"} {
		if _, err := svc.SetSteeringMode(bad); err == nil {
			t.Errorf("非法模式 %q 应该被本地拒绝", bad)
		}
		if _, err := svc.SetFollowUpMode(bad); err == nil {
			t.Errorf("非法模式 %q 应该被本地拒绝", bad)
		}
	}
}
