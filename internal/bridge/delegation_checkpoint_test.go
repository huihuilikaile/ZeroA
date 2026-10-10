package bridge

import (
	"context"
	"testing"
	"time"

	"zeroa/internal/agents"
)

// ★ deliverOn: checkpoint 的端到端回归。
//
// 与 TestDelegationHoldsWhileParentBusy 互为反面：父正在跑回合时，
// turn_end（默认）会把子智能体结果挂起等回合结束；而声明了 checkpoint 的父角色
// 必须**不挂起**——立刻用 steer 插进最近的检查点。
// 以前的实现读的是**子角色**的 Delegation（几乎总是 nil），checkpoint 永远触发不了。
func TestDelegationCheckpointDeliversWhileBusy(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}, DeliverOn: "checkpoint"},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	service.noteParentToolCall("")

	// 父正在跑回合（走真实事件路径：忙是**那条会话的进程**的状态）
	parentID := service.parentID()
	service.noteRuntimeEvent(service.runtimeByID(parentID), `{"type":"agent_start"}`)
	if !service.runtimeBusy(parentID) {
		t.Fatal("前置条件不成立：这条会话应当是忙的")
	}

	if _, err := service.DelegateTask("ctf-recon", "查资料"); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	sup := service.supervisorFor()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		all := sup.All()
		if len(all) > 0 && all[0].TurnSettled() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// checkpoint：结果不能被挂起（等合并窗口过去再断言）
	time.Sleep(2 * time.Second)
	router := service.delegationManager().Router()
	if router == nil {
		t.Fatal("路由应当在（已经派过子智能体）")
	}
	if router.Held(parentID) {
		t.Fatal("checkpoint 角色的结果不应当挂起等父回合结束")
	}
	if pending := router.Pending(parentID); pending != 0 {
		t.Errorf("checkpoint 角色的结果应当立刻投递（pending=%d）", pending)
	}
}

// ★ 桥必须按会话隔离：一个会话的模型只能看到、只能操作自己会话的子智能体。
//
// 以前 /agents 返回全部会话的子智能体，delegation_stop 照着这份列表
// 会把别的会话正在跑的活一起停掉。
func TestDelegateBridgeSessionIsolation(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)

	manager := service.delegationManager()
	parentID := service.parentID()

	// 会话 A 派一个子智能体（经 Manager 直接派，桥只是它的 HTTP 外壳）
	if _, err := manager.Delegate(parentID, "ctf-recon", "查资料"); err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	var agentID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if all := service.supervisorFor().All(); len(all) > 0 {
			agentID = all[0].ID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if agentID == "" {
		t.Fatal("子智能体应当已经派出")
	}

	listFor := func(runtimeID string) []any {
		payload, err := manager.ListFor(runtimeID)
		if err != nil {
			t.Fatalf("ListFor(%q): %v", runtimeID, err)
		}
		list, _ := payload["agents"].([]*agents.Instance)
		out := make([]any, 0, len(list))
		for _, inst := range list {
			out = append(out, inst.ID)
		}
		return out
	}
	if got := listFor(parentID); len(got) != 1 || got[0] != agentID {
		t.Errorf("会话 A 应当看到自己的子智能体: %v", got)
	}
	if got := listFor("runtime-999"); len(got) != 0 {
		t.Errorf("别的会话不该看到 A 的子智能体: %v", got)
	}
	if got := listFor(""); len(got) == 0 {
		t.Error("空 runtimeId（Agents 页/老客户端）应当看到全部")
	}

	// 归属校验：别的会话不能停它、不能给它发消息、不能中断它
	if manager.AgentBelongsTo(agentID, "runtime-999") {
		t.Error("A 的子智能体不该判给别的会话")
	}
	if !manager.AgentBelongsTo(agentID, parentID) {
		t.Error("A 的子智能体应当判给 A")
	}

	// 等它跑完，结果应当投递回 A（runtime 活着，会话没关）
	snap, found := service.supervisorFor().WaitTurn(context.Background(), agentID, 15*time.Second)
	if !found || !snap.TurnSettled() {
		t.Errorf("子智能体应当正常跑完这一轮: found=%v status=%v", found, snap.Status)
	}
	service.markParentBusy(false)
}
