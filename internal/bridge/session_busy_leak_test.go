package bridge

import (
	"os"
	"strings"
	"testing"

	"zeroa/internal/agent"
)

/**
 * "有回合在跑"这个状态**漏气**导致的故障，以及一进程一会话之后它还剩什么用处。
 *
 * 故障现场（用户实测日志）：角色切换走的是"停进程 + 起进程"，旧进程里那一轮的
 * agent_settled 永远不会到达，parentBusy 就一直是 true；单进程时代这会让
 * **每一次点会话都被"排队等回合结束"**，而那个回合根本不存在（表现就是点了没反应，
 * 只能靠「立即切换」硬切——那个才是真的打断）。
 *
 * 一进程一会话之后，"能不能切换"不再看这个标记（切换根本不碰正在跑的进程），
 * 但它仍然决定另外两件事，所以这几条用例保留：
 *   · **关窗保护**：界面据此弹"Pi 正在运行，确定关闭吗"；
 *   · **子智能体结果的投递时机**：父回合结束才投，避免把结果插进半截对话里。
 */

func newMockServiceForBusyTest(t *testing.T) (*PiService, string) {
	t.Helper()
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	svc := &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return svc, root
}

// 进程退出：忙碌标记必须一起清掉，否则关窗保护会一直以为"Pi 还在跑"。
func TestProcessExitClearsBusy(t *testing.T) {
	svc, _ := newMockServiceForBusyTest(t)
	svc.markParentBusy(true)
	svc.SetRunningWork(true)
	if !svc.turnRunning() {
		t.Fatal("前置条件：此刻应当被视为在跑")
	}

	svc.ObserveAgentEvent(agent.Envelope{Type: "process_exit", Record: "{}"})

	if svc.turnRunning() {
		t.Fatalf("进程退出之后不该再被视为在跑（%s）", mustBusySources(svc))
	}
}

// 停止进程（前端的 stop + start 重启路径）：忙碌标记同样必须清掉。
func TestStopClearsBusyState(t *testing.T) {
	svc, _ := newMockServiceForBusyTest(t)
	svc.markParentBusy(true)
	svc.SetRunningWork(true)

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if svc.turnRunning() {
		t.Fatalf("停掉进程之后不该再被视为在跑（%s）", mustBusySources(svc))
	}
}

// 会话真的切过去之后：新会话上不可能跑着"上一个会话的回合"。
func TestSessionSwitchClearsBusyState(t *testing.T) {
	svc, root := newMockServiceForBusyTest(t)
	target := sessionFixture(t, root, root, "after-switch")

	if _, err := svc.SwitchSession(target); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if svc.turnRunning() {
		t.Fatalf("切换完成之后不该再被视为在跑（%s）", mustBusySources(svc))
	}
}

// Start 之后必须清掉"上一个进程留下的忙"。
//
// 用源码级断言而不是跑真进程：这里要盯的是"接线还在不在"。
//
// ⚠️ 契约变了：Start 现在**不创建进程**（会话=日志，进程留到第一条消息），
// 所以"拿到新进程之后清忙碌"这一步没有了；要钉的是新的接线——
//   · Start 写完启动参数模板（optionsReady）之后必须清一次忙碌状态；
//   · Start 里**不该**再出现 startRuntime（否则"进入应用不再冷启动 Pi"这条
//     最关键的保证会在一次重构里悄悄退回去，而界面看起来毫无变化）。
func TestStartPathClearsStaleBusyState(t *testing.T) {
	raw, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("读 service.go: %v", err)
	}
	source := string(raw)

	body := functionBody(t, source, "func (s *PiService) Start(")
	if strings.Contains(body, "startRuntime(") {
		t.Fatal("Start 又去创建进程了：进入应用会重新出现 1–3.5 秒的冷启动")
	}
	marker := "s.optionsReady = true"
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatalf("service.go 的 Start 里找不到 %q（接线可能变了）", marker)
	}
	if !strings.Contains(body[index:], `clearTurnState("process_started")`) {
		t.Fatal("Start 准备完启动参数之后没有清忙碌状态：重启之后关窗保护会一直以为 Pi 还在跑")
	}
	for _, needle := range []string{`clearTurnState("process_exit")`, `clearTurnState("stop")`} {
		if !strings.Contains(source, needle) {
			t.Errorf("缺少清理点：%s", needle)
		}
	}
	if !strings.Contains(source, `clearTurnState("session_switched")`) {
		// 这条现在在 session_runtime.go 里（activateRuntime）——找不到源码会变，给个明确提示
		raw2, err := os.ReadFile("session_runtime.go")
		if err != nil || !strings.Contains(string(raw2), "clearTurnState") {
			t.Error("切换会话的清理点不见了（session_runtime.go / service.go 都没有）")
		}
	}
}

// functionBody 取一个函数的源码正文（从函数头到下一个顶层 func）。
//
// 为什么要它：源码级断言如果只看整份文件，"startRuntime 出现在 Start 里"
// 这种问题根本断言不出来（别处也有这个调用）。
func functionBody(t *testing.T, source, header string) string {
	t.Helper()
	start := strings.Index(source, header)
	if start < 0 {
		t.Fatalf("找不到函数 %q", header)
	}
	rest := source[start:]
	if next := strings.Index(rest[len(header):], "\nfunc "); next >= 0 {
		return rest[:len(header)+next]
	}
	return rest
}

func mustBusySources(svc *PiService) string {
	parentBusy, frontendWork := svc.busySources()
	return "parent_busy=" + boolText(parentBusy) + " frontend_work=" + boolText(frontendWork)
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
