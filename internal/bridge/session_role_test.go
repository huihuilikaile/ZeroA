package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 会话记忆里必须包含**角色**：这条会话当初是在哪个角色下做的。
//
// 为什么单独立一条用例：界面上"打开会话自动切回角色"整条链路都建立在这份数据上
// （RolesPage/Sidebar 读 roleBySession）。数据丢了或读错了，表现是
// "打开历史会话角色没跟回来"，而那种问题在界面上看起来像"记忆没生效"。
func TestSessionRoleMemory(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}
	seedRoles(t, service)

	sessionPath := filepath.Join(t.TempDir(), "s.md", "2026-10-06T01-00-00-000Z_a.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := service.RememberSessionRole(sessionPath, "CTF"); err != nil {
		t.Fatalf("RememberSessionRole: %v", err)
	}
	// 重新读（模拟重启）：角色记忆必须跟着 ListRoles 一起回来
	payload := rolesPayload(t, service)
	if got := payload.RoleBySession[sessionPath]; got != "ctf" {
		t.Errorf("会话的角色记忆没保存/没读回: %q（全部：%+v）", got, payload.RoleBySession)
	}
	// 大小写与路径写法差异不该产生第二条记录
	if _, err := service.RememberSessionRole(normalizeDir(sessionPath), "ir"); err != nil {
		t.Fatalf("RememberSessionRole(2): %v", err)
	}
	payload = rolesPayload(t, service)
	if len(payload.RoleBySession) != 1 {
		t.Errorf("同一个会话不该有两条角色记录: %+v", payload.RoleBySession)
	}
	// 键保留**最后一次写入的写法**，所以断言语义而不是拼写：
	// 规范化之后必须是同一个会话，值必须是最新那个角色。
	for key, value := range payload.RoleBySession {
		if normalizeDir(key) != normalizeDir(sessionPath) {
			t.Errorf("角色记录的键指向了别的会话: %q", key)
		}
		if value != "ir" {
			t.Errorf("重复记录应该是覆盖而不是新增: %+v", payload.RoleBySession)
		}
	}
	// 清空角色（"不使用角色"）时要把记录删掉，而不是留一个空值
	if _, err := service.RememberSessionRole(sessionPath, ""); err != nil {
		t.Fatalf("RememberSessionRole(空): %v", err)
	}
	if _, exists := rolesPayload(t, service).RoleBySession[sessionPath]; exists {
		t.Error("清空角色后不该还留着记录")
	}
}

// 会话文件被删掉之后，它的角色记忆也必须清掉。
//
// 不清的后果不是"多一行垃圾"：打开一条不存在的会话时，界面会拿着一个陈旧的
// 角色去重启 Pi——用户看到的是"点了没反应"或者角色莫名变了。
func TestSessionRoleClearedOnDelete(t *testing.T) {
	root := t.TempDir()
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	service := &PiService{}
	seedRoles(t, service)

	path := writeSession(t, root, "ws", t.TempDir(), "2026-10-06T02-00-00-000Z_x", "会被删掉的会话")
	if _, err := service.RememberSessionRole(path, "ctf"); err != nil {
		t.Fatalf("RememberSessionRole: %v", err)
	}
	if _, exists := rolesPayload(t, service).RoleBySession[path]; !exists {
		t.Fatal("前置条件不成立：角色记忆没写进去")
	}

	// 不是当前会话，删除不需要 new_session
	service.mu.Lock()
	service.sessionFile = ""
	service.mu.Unlock()
	if _, err := service.DeleteSession(path); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, exists := rolesPayload(t, service).RoleBySession[path]; exists {
		t.Error("会话删掉之后，它的角色记忆也该一起清掉")
	}
}

// rolesPayload 读出 ListRoles 的返回，供断言使用。
func rolesPayload(t *testing.T, service *PiService) struct {
	RoleBySession   map[string]string `json:"roleBySession"`
	RoleByWorkspace map[string]string `json:"roleByWorkspace"`
} {
	t.Helper()
	result, err := service.ListRoles()
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	var payload struct {
		RoleBySession   map[string]string `json:"roleBySession"`
		RoleByWorkspace map[string]string `json:"roleByWorkspace"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析 ListRoles 失败: %v", err)
	}
	return payload
}
