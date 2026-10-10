package bridge

import (
	"strings"
	"testing"
	"time"

	"zeroa/internal/delegation"
)

// 端到端：子智能体结束后，父会话即使已经空闲也必须被唤醒并读到结论。
//
// 走真实的投递路径（ResultSink → Router → deliverToParent → Pi 的 RPC），
// 用 mock Pi 起父会话与子会话，全程零 token。
func TestDelegationResultWakesIdleParent(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	// 子智能体要能跑完（mock 很快），父会话则先停在"已结束"的状态。
	// 注意不能写 0：delayFromEnv 会把 0 钳回默认值（0 会让 abort 变成抽签），
	// 1ms 同样是"跑完只要一瞬"，但停顿不为零。
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}, DeliverOn: "turn_end"},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")

	workspace := t.TempDir()
	if err := service.Start("mock", "", workspace, "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	service.noteParentToolCall("")

	sup := service.supervisorFor()
	if _, err := service.DelegateTask("ctf-recon", "查 RSA 小指数攻击"); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}

	// 等子智能体**这一轮**跑完（mock 会立即结束）。
	//
	// 注意判据从 "Status.Finished()" 改成了 TurnSettled()：默认形态已经是"可继续"，
	// 一轮结束后它是 idle（进程还在、还能继续说话），而不是终态。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		all := sup.All()
		if len(all) > 0 && all[0].TurnSettled() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if all := sup.All(); len(all) == 0 || !all[0].TurnSettled() {
		t.Fatalf("子智能体没有得出结论：%+v", all)
	}

	// 父会话此刻是空闲的（mock 的回合早已结束）——投递必须走 prompt，
	// 所以结果应当作为**新一轮的输入**出现在父会话的对话里。
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := service.GetMessages()
		if err == nil && strings.Contains(messages.Data, "[子智能体结果]") {
			return // 结果真的回到了父会话
		}
		time.Sleep(50 * time.Millisecond)
	}
	state, _ := service.GetState()
	t.Fatalf("父会话空闲时结果应当直接唤醒它（prompt），而不是排进队列：%s", state.Data)
}

// 对照：父会话**正在跑**时投递要用 follow_up（不能打断当前回合）。
func TestDelegationResultQueuesWhileParentBusy(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1500")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}, DeliverOn: "turn_end"},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	service.noteParentToolCall("")

	// 人为把**这条会话**标记成"正在跑"（一进程一会话之后，忙是每条会话自己的状态）
	parentID := service.parentID()
	service.noteRuntimeEvent(service.runtimeByID(parentID), `{"type":"agent_start"}`)
	if !service.runtimeBusy(parentID) {
		t.Fatal("前置条件不成立：这条会话应当是忙的")
	}
	if got := delegation.DeliveryMethod(service.runtimeBusy(parentID)); got != "follow_up" {
		t.Errorf("父在跑时应当排队，得到 %q", got)
	}
}
