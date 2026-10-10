package bridge

import (
	"strings"
	"testing"
	"time"
)

// 委派配置的保存校验：枚举值拼错必须在保存时挡住，而不是被静默当成默认值
// （lifecycle 写成 "oneshot" 会变成可继续驻留，平白多占一份进程）。
func TestDelegationValidation(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	base := Delegation{Enabled: true, AllowedRoles: []string{"kid"}}
	save := func(config Delegation) error {
		role := Role{ID: "boss", Name: "总管", Delegation: &config}
		return saveRole(role, "提示词")
	}

	// 合法的最小配置能存
	if err := save(base); err != nil {
		t.Fatalf("合法配置应当能保存: %v", err)
	}

	bad := []struct {
		name   string
		mutate func(*Delegation)
		want   string
	}{
		{"模式拼错", func(d *Delegation) { d.Mode = "backgroud" }, "委派模式"},
		{"投递时机拼错", func(d *Delegation) { d.DeliverOn = "check_point" }, "投递时机"},
		{"形态拼错", func(d *Delegation) { d.Lifecycle = "oneshot" }, "形态"},
		{"负数预算", func(d *Delegation) { d.MaxChildren = -1 }, "不能是负数"},
		{"白名单 id 不合法", func(d *Delegation) { d.AllowedRoles = []string{"../etc"} }, "不合法"},
	}
	for _, item := range bad {
		config := base
		item.mutate(&config)
		if err := save(config); err == nil || !strings.Contains(err.Error(), item.want) {
			t.Errorf("%s 应当保存失败且提到%q，实际: %v", item.name, item.want, err)
		}
	}
}

// idleTimeoutSeconds 必须真的接到策略上（以前 Policy 结构体有这个字段，但没人给它赋值）。
func TestDelegationIdleTimeoutRoundTrip(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	saveRoleForTest(t, service, Role{
		ID: "boss", Name: "总管",
		Delegation: &Delegation{
			Enabled: true, AllowedRoles: []string{"kid"},
			IdleTimeoutSeconds: 42,
		},
	}, "")
	service.activeRole = "boss"

	policy, err := service.delegationPolicy()
	if err != nil {
		t.Fatalf("delegationPolicy: %v", err)
	}
	if policy.IdleTimeout != 42*time.Second {
		t.Errorf("idleTimeoutSeconds 应当换算成 42s，实际 %v", policy.IdleTimeout)
	}
}

// 声明了但还没实现的委派配置（blocking / inherit）必须在编译警告里说出来：
// 静默忽略的结果只有一个——用户以为配上了，实际按默认行为跑。
func TestDelegationUnimplementedFieldsWarn(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	saveRoleForTest(t, service, Role{
		ID: "boss", Name: "总管",
		Delegation: &Delegation{
			Enabled: true, AllowedRoles: []string{"kid"},
			Mode:    "blocking",
			Inherit: &DelegationInherit{Model: "inherit"},
		},
	}, "")

	view, ok := findRole("boss")
	if !ok {
		t.Fatal("角色应当能读回来")
	}
	compiled, err := compileRole(view.Role, false)
	if err != nil {
		t.Fatalf("compileRole: %v", err)
	}
	joined := strings.Join(compiled.Warnings, "\n")
	for _, want := range []string{"blocking 暂未实现", "inherit 暂未实现"} {
		if !strings.Contains(joined, want) {
			t.Errorf("编译警告应当提到 %q，实际:\n%s", want, joined)
		}
	}
}
