package bridge

import (
	"strings"
	"testing"
)

// 直接在应用里跑命令（Pi 的 bash）：命令与输出会进会话上下文，
// 所以"空命令"必须被挡住，且返回值要带输出与退出码。
func TestBashCommand(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	// 懒启动：Start 只准备参数模板，进程等第一条消息（这里等价于那一步）
	warmUpSession(t, svc)

	result, err := svc.Bash("echo hello")
	if err != nil || !result.Success {
		t.Fatalf("Bash: %v %+v", err, result)
	}
	for _, want := range []string{`"output"`, `"exitCode"`, `"cancelled"`, "echo hello"} {
		if !strings.Contains(result.Data, want) {
			t.Errorf("返回里应该含 %s，实际 %s", want, result.Data)
		}
	}

	// 空命令、纯空白：本地拦住，不产生 RPC 往返
	for _, bad := range []string{"", "   ", "\t"} {
		if _, err := svc.Bash(bad); err == nil {
			t.Errorf("空命令 %q 应该被本地拒绝", bad)
		}
	}

	aborted, err := svc.AbortBash()
	if err != nil || !aborted.Success {
		t.Fatalf("AbortBash: %v %+v", err, aborted)
	}
}
