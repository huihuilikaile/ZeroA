package bridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"zeroa/internal/agent"
	"zeroa/internal/agents"
)

// ★ 核心行为：主 Agent 给出候选 flag 时，停掉还在跑的子智能体。
//
// 这是用户要的"主 agent 结束（解出来了）就停子 agent"，但判定依据是
// **是否拿到答案**而不是"回合是否结束"——后者每个回合都成立，会杀死委派。
func TestStopChildrenOnFlag(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	// 让 mock 子智能体慢一点：我们要在它"还在跑"的时候把 flag 打出来
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "4000")

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
	service.noteParentToolCall("")

	sup := service.supervisorFor()
	if _, err := service.DelegateTask("ctf-recon", "查资料"); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	// 等它真的跑起来
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sup.RunningCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sup.RunningCount() == 0 {
		t.Fatal("前置条件不成立：子智能体没有进入运行状态")
	}

	// 主 Agent 报出候选 flag —— 走的是真实的事件入口（message_end）
	record, _ := json.Marshal(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "解出来了，flag{stop_children_now}"}},
		},
	})
	service.ObserveAgentEvent(agent.Envelope{Kind: "event", Type: "message_end", Record: string(record)})

	// 子智能体应当被停掉，原因写清楚
	deadline = time.Now().Add(5 * time.Second)
	var stopped *agents.Instance
	for time.Now().Before(deadline) {
		if all := sup.All(); len(all) > 0 && all[0].Status.Finished() {
			stopped = all[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stopped == nil {
		t.Fatalf("给出 flag 之后子智能体应当被停掉（running=%d）", sup.RunningCount())
	}
	if stopped.Status != agents.StatusKilled {
		t.Errorf("应当是 killed，实际 %s", stopped.Status)
	}
	if !strings.Contains(stopped.Err, "flag") {
		t.Errorf("停止原因要说清是 flag 引起的，实际 %q", stopped.Err)
	}
}

// 反过来：普通回复（没有 flag）绝不能停子智能体。
//
// 这条是防止"回合结束就停"那种实现混进来——父派完活这一轮就结束了，
// 若按回合结束停，委派会完全失效。
func TestNormalReplyDoesNotStopChildren(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "4000")

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
	service.noteParentToolCall("")

	sup := service.supervisorFor()
	if _, err := service.DelegateTask("ctf-recon", "查资料"); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sup.RunningCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 普通回复 + 回合结束事件
	record, _ := json.Marshal(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "我先派一个子智能体去查资料，同时自己继续分析二进制。"}},
		},
	})
	service.ObserveAgentEvent(agent.Envelope{Kind: "event", Type: "message_end", Record: string(record)})
	service.ObserveAgentEvent(agent.Envelope{Kind: "event", Type: "agent_settled", Record: `{"type":"agent_settled"}`})

	time.Sleep(400 * time.Millisecond)
	if sup.RunningCount() == 0 {
		t.Fatal("普通回复 / 回合结束都不该停掉子智能体（那会让委派完全失效）")
	}
}

// 角色显式关掉 stopOnFlag 时，即使出现 flag 也不停。
func TestStopOnFlagCanBeDisabled(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "4000")

	service := &PiService{}
	seedRoles(t, service)
	off := false
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}, StopOnFlag: &off},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	service.noteParentToolCall("")

	sup := service.supervisorFor()
	if _, err := service.DelegateTask("ctf-recon", "查资料"); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sup.RunningCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	record, _ := json.Marshal(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": "flag{but_i_want_them_alive}"}},
		},
	})
	service.ObserveAgentEvent(agent.Envelope{Kind: "event", Type: "message_end", Record: string(record)})

	time.Sleep(400 * time.Millisecond)
	if sup.RunningCount() == 0 {
		t.Error("角色关掉 stopOnFlag 后不该自动停")
	}
}
