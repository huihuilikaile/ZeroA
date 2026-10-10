package bridge

import (
	"strings"
	"testing"
)

// 子智能体是独立定义的实体（kind=agent），普通角色不再兼任：
//   - 保存时：白名单引用"已存在的普通角色"直接报错（引用还不存在的 id 放行，
//     用户完全可能先配父角色、后建子智能体）；
//   - 派发时：Manager.Delegate 再兜一道（子角色定义可能在保存后被改坏）；
//   - 主会话：子智能体不能被设为当前角色（输入框选择器也不显示它，见前端）。
func TestSubagentKindGates(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}

	// 白名单引用已存在的**普通角色** → 保存时报错，说明怎么修
	saveRoleForTest(t, service, Role{ID: "kid", Name: "普通角色"}, "")
	boss := Role{ID: "boss", Name: "总管", Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"kid"}}}
	if err := saveRole(boss, ""); err == nil || !strings.Contains(err.Error(), "不是子智能体") {
		t.Fatalf("白名单引用普通角色应当保存失败，实际: %v", err)
	}

	// 把这个定义改成子智能体 → 能存，且能当派发目标
	saveRoleForTest(t, service, Role{ID: "kid", Name: "检索子智能体", Kind: "agent"}, "")
	saveRoleForTest(t, service, boss, "")

	// 白名单引用还不存在的 id → 保存放行（先配父、后建子是正常顺序）
	early := Role{ID: "early", Name: "先配的", Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"not-yet"}}}
	if err := saveRole(early, ""); err != nil {
		t.Fatalf("引用不存在的子智能体 id 应当放行（派发时再拦）: %v", err)
	}

	// 子智能体不能被设为当前工作区角色
	if _, err := service.SetActiveRole("kid"); err == nil || !strings.Contains(err.Error(), "子智能体") {
		t.Fatalf("子智能体不该能当主会话角色，实际: %v", err)
	}

	// 派发闸门：普通角色（即使悄悄去掉了 kind 或压根没标）不能被派。
	// 走 DelegateTask 全程：policy/白名单都过，应当被 kind 闸门拦下。
	service.activeRole = "boss"
	saveRoleForTest(t, service, Role{ID: "kid", Name: "普通角色"}, "") // 覆盖回普通角色
	if _, err := service.DelegateTask("kid", "查资料"); err == nil || !strings.Contains(err.Error(), "普通角色") {
		t.Fatalf("派发普通角色应当被 kind 闸门拦下，实际: %v", err)
	}
	if _, err := service.DelegateTask("ghost", "查资料"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("派发不存在的子智能体应当如实报错，实际: %v", err)
	}
}

// kind 字段必须原样活过一次读写：丢了它，子智能体就悄悄变回普通角色，
// 表现是"输入框里突然冒出一堆子智能体"。
func TestRoleKindRoundTrip(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}

	saveRoleForTest(t, service, Role{ID: "kid", Name: "子智能体", Kind: "agent"}, "")
	view, ok := findRole("kid")
	if !ok {
		t.Fatal("角色没读回来")
	}
	if !view.Role.IsAgent() {
		t.Errorf("kind=agent 没保住: %+v", view.Role)
	}

	// 留空 = 普通角色（老配置文件的语义不能变）
	saveRoleForTest(t, service, Role{ID: "plain", Name: "普通角色"}, "")
	view, ok = findRole("plain")
	if !ok {
		t.Fatal("角色没读回来")
	}
	if view.Role.IsAgent() {
		t.Errorf("没有 kind 的角色应当是普通角色: %+v", view.Role)
	}
}
