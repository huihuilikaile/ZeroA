package delegation

import (
	"strings"
	"testing"
	"time"

	"zeroa/internal/agents"
	"zeroa/internal/roles"
)

// setupRoleDir 建一个带角色的临时配置目录，并把角色域指过去。
//
// 覆盖包级 host 是有意的：委派域的策略判定的唯一事实源就是 roles.Find，
// 要测"按会话解析角色"就必须让几条假会话各自对应一份真角色文件。
func setupRoleDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	roles.Configure(roles.Host{
		AgentDir: func() string { return dir },
		// 其余保持默认：不补种、技能全缺（这里的角色都不声明技能，无所谓）
	})
	save := func(role roles.Role) {
		t.Helper()
		if err := roles.Save(role, "测试提示词"); err != nil {
			t.Fatalf("保存角色 %s: %v", role.ID, err)
		}
	}
	save(roles.Role{ID: "boss", Name: "总管", Delegation: &roles.Delegation{
		Enabled:      true,
		DeliverOn:    "checkpoint",
		AllowedRoles: []string{"kid"},
		MaxChildren:  3, MaxParallel: 1, MaxDurationSeconds: 60, IdleTimeoutSeconds: 42,
	}})
	save(roles.Role{ID: "boss-turnend", Name: "总管二号", Delegation: &roles.Delegation{
		Enabled: true, AllowedRoles: []string{"kid"}, DeliverOn: "turn_end",
	}})
	save(roles.Role{ID: "plain", Name: "无委派角色"})
	save(roles.Role{ID: "kid", Name: "子角色", Kind: roles.KindAgent})
}

// newTestManager 用一张"会话 → 角色"表当服务侧事实，其余回调保持零值
// （本文件的用例不走委派/投递，不需要它们）。
func newTestManager(roleByParent map[string]string) *Manager {
	return NewManager(Deps{
		RoleForParent: func(parentID string) string { return roleByParent[parentID] },
	})
}

// ★ 委派策略必须按"派活的那条会话"的角色解析：一进程一会话之后，
// 读全局"当前角色"会把 A 会话的策略套到 B 会话的请求上。
func TestPolicyResolvesRolePerParentSession(t *testing.T) {
	setupRoleDir(t)
	manager := newTestManager(map[string]string{"rt-1": "boss", "rt-2": "plain"})

	policy, err := manager.Policy("rt-1")
	if err != nil {
		t.Fatalf("Policy(rt-1): %v", err)
	}
	if !policy.Enabled || policy.DeliverOn != "checkpoint" {
		t.Errorf("rt-1 应当启用委派且 deliverOn=checkpoint: %+v", policy)
	}
	if policy.MaxChildren != 3 || policy.MaxParallel != 1 {
		t.Errorf("角色预算没读出来: %+v", policy)
	}
	if policy.MaxDuration != time.Minute {
		t.Errorf("maxDurationSeconds 换算不对: %v", policy.MaxDuration)
	}
	if policy.IdleTimeout != 42*time.Second {
		t.Errorf("idleTimeoutSeconds 没接进策略: %v", policy.IdleTimeout)
	}

	policy, err = manager.Policy("rt-2")
	if err != nil {
		t.Fatalf("Policy(rt-2): %v", err)
	}
	if policy.Enabled {
		t.Error("rt-2 的角色没声明委派，不应当允许派子智能体")
	}

	// 没有角色的会话：如实拒绝，而不是套用别的角色的策略
	if _, err := manager.Policy("rt-9"); err == nil {
		t.Error("没启用角色的会话应当被拒绝派子智能体")
	}
}

// ★ deliverOn: checkpoint 的回归测试。
//
// 以前两处都断：ShouldHold 读的是**子角色**的 Delegation（于是永远挂起），
// 投递方法又只有 follow_up/prompt 两条路（于是父忙时永远等回合结束）。
// 现在必须是：父忙 + 父角色声明 checkpoint → 不挂起，且用 steer 立刻插话。
func TestCheckpointPicksSteerAndSkipsHold(t *testing.T) {
	setupRoleDir(t)
	manager := newTestManager(map[string]string{"rt-1": "boss", "rt-2": "boss-turnend"})

	if got := manager.deliveryMethodFor("rt-1", true); got != "steer" {
		t.Errorf("父忙 + checkpoint 应当用 steer 插话，得到 %q", got)
	}
	if got := manager.deliveryMethodFor("rt-1", false); got != "prompt" {
		t.Errorf("父空闲时应当用 prompt 唤醒，得到 %q", got)
	}
	if got := manager.deliveryMethodFor("rt-2", true); got != "follow_up" {
		t.Errorf("turn_end 角色在父忙时应当用 follow_up，得到 %q", got)
	}
	if manager.shouldHold(&agents.Instance{ParentID: "rt-1"}) {
		t.Error("checkpoint 角色的结果不应当挂起等回合结束")
	}
	if !manager.shouldHold(&agents.Instance{ParentID: "rt-2"}) {
		t.Error("turn_end 角色的结果应当挂起等回合结束")
	}
	if !manager.shouldHold(nil) {
		t.Error("空实例按默认挂起处理")
	}
}

// 审计证据按会话隔离：A 会话动过手，不能替 B 会话的委派放行。
func TestAuditEvidenceIsPerSession(t *testing.T) {
	manager := newTestManager(nil)
	manager.NoteParentToolCall("rt-a")
	if err := manager.RequireAuditEvidence("rt-a"); err != nil {
		t.Errorf("动过工具的会话应当通过审计闸门: %v", err)
	}
	if err := manager.RequireAuditEvidence("rt-b"); err == nil || !strings.Contains(err.Error(), "审计") {
		t.Errorf("没动过工具的会话应当被拦，实际: %v", err)
	}
	manager.ResetState()
	if err := manager.RequireAuditEvidence("rt-a"); err == nil {
		t.Error("ResetState 之后证据应当清空")
	}
}
