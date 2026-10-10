package bridge

import (
	"encoding/json"
	"testing"
	"time"
)

// A2：给当前会话改名（切换角色后打前缀走的就是它）。
//
// 为什么不用 RenameSession：那个是"改历史会话"，要先 switch_session 切过去
// 再切回来。改当前会话直接 set_session_name，少两次切换。
func TestNameCurrentSession(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	if err := service.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)

	if _, err := service.NameCurrentSession("[CTF] 08-06 09:30"); err != nil {
		t.Fatalf("NameCurrentSession: %v", err)
	}
	state := statePayload(t, service)
	if name := fmtSprint(state["sessionName"]); name != "[CTF] 08-06 09:30" {
		t.Errorf("会话名没生效，get_state 返回 %v", state["sessionName"])
	}

	// 空名字要拒绝：Pi 那边会把名字清掉，界面会看起来"改名失败但没报错"
	if _, err := service.NameCurrentSession("   "); err == nil {
		t.Error("空会话名应该被拒绝")
	}
	// 超长名字截断而不是报错（名字只是给人看的）
	long := make([]rune, 200)
	for i := range long {
		long[i] = '字'
	}
	if _, err := service.NameCurrentSession(string(long)); err != nil {
		t.Fatalf("超长名字应该截断而不是失败: %v", err)
	}
}

// E：角色的自动压缩设置走 RPC 下发（不需要重启），进程起来之后必须真的生效。
//
// 这是"角色只生效一半"最容易发生的地方：提示词与工具是启动参数一定会生效，
// 而压缩是进程起来之后补发的 RPC——忘了发，界面上完全看不出来。
//
// ⚠️ 契约变了：下发点从 Start 挪到了**懒启动**（ensureActiveRuntime；
// Start 已经不创建进程了，见 service.go 的 Start）。所以这里先发一条消息把进程
// 带起来再断言——这正是用户实际经历的顺序（打开会话 → 说第一句话）。
func TestRoleAutoCompactionAppliedAtStart(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)

	off := false
	saveRoleForTest(t, service, Role{
		ID: "quiet", Name: "不自动压缩", AutoCompaction: &off,
	}, "取证角色：尽量保留原始上下文")

	if err := service.Start("mock", "", t.TempDir(), "", "", "", "quiet"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 懒启动：角色的运行时设置是**进程起来之后**下发的，所以先要有一个进程
	if _, err := service.Prompt("第一条消息，把进程带起来"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// set_auto_compaction 是异步下发的，给它一点时间落地
	deadline := time.Now().Add(8 * time.Second)
	enabled := true
	for time.Now().Before(deadline) {
		state := statePayload(t, service)
		if value, ok := state["autoCompactionEnabled"].(bool); ok {
			enabled = value
			if !value {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	if enabled {
		t.Errorf("角色声明 autoCompaction=false，但会话里仍然是开启的（RPC 没下发成功？）state=%v",
			statePayload(t, service)["autoCompactionEnabled"])
	}
}

// 没有角色、或角色没声明压缩策略时，不能去动全局设置。
func TestRoleAutoCompactionUntouchedWhenUnset(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{ID: "plain", Name: "默认角色"}, "什么都不改")

	if err := service.Start("mock", "", t.TempDir(), "", "", "", "plain"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 同上：先让懒启动把进程带起来，否则这条断言测的只是"没有进程时没乱发 RPC"
	if _, err := service.Prompt("第一条消息，把进程带起来"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // 等可能的（不该发生的）RPC 落地

	if enabled, ok := statePayload(t, service)["autoCompactionEnabled"].(bool); ok && !enabled {
		t.Error("角色没声明压缩策略时，不该把全局的自动压缩关掉")
	}
}

// 角色里存的 MCP 服务器选择目前只记录（Pi 没有按服务器过滤的命令行开关），
// 但**必须能往返**：存进去读不回来，界面上的勾选就成了摆设。
func TestRoleMcpServersRoundTrip(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	seedRoles(t, service)

	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF", McpServers: []string{"search", "browser"},
	}, "带 MCP 选择的角色")

	view, ok := findRole("ctf")
	if !ok {
		t.Fatal("角色应该能读回来")
	}
	if len(view.McpServers) != 2 || view.McpServers[0] != "search" {
		t.Errorf("MCP 服务器选择没保住: %+v", view.McpServers)
	}
	raw, _ := json.Marshal(view.Role)
	if !contains(string(raw), `"mcpServers":["search","browser"]`) {
		t.Errorf("线缆上的字段名要和前端一致: %s", raw)
	}
}

func contains(text, part string) bool {
	return len(text) >= len(part) && (func() bool {
		for i := 0; i+len(part) <= len(text); i++ {
			if text[i:i+len(part)] == part {
				return true
			}
		}
		return false
	})()
}
