package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这是角色功能最关键的一条验收：**角色到底有没有真的加载上**。
//
// 对话里最初的三个验证诉求之一就是"角色配置能否正确加载"，而这件事不能靠
// 肉眼看界面。Pi 把本次启动的提示词分段与工具声明写进会话的首条 system 消息
// （pi 的 docs/session-format.md：sections + toolsAdded），我们照这个形状断言：
//
//  1. 角色提示词出现在 sections 里（内容与来源都可核对）；
//  2. 工具清单等于角色声明的白名单，而不是 Pi 的默认四个；
//  3. 技能白名单出现在 sections.skills 里；
//  4. 角色的模型 / 思考级别成为新进程的默认值。
//
// 模拟器会如实复现这些字段（见 mockpi 的 launchSystemMessage），所以这条用例
// 在不需要真实模型的情况下就能覆盖"切换角色后 Pi 收到了什么"。
func TestRoleActuallyLoadedIntoSession(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	// 技能必须真实存在：保存角色时会预检技能名（找不到就拒绝保存），
	// 所以这里先造一个技能，再让角色引用它。
	writeSkill(t, skillsRoot(), "web", "web", "ctf-web", "Web 漏洞利用手册")

	// 造一个特征明显的角色：提示词里放一个不可能自然出现的标记
	const marker = "ROLE-MARKER-CTF-9f2c"
	role := Role{
		ID: "ctf", Name: "CTF 角色",
		Tools:         []string{"read", "bash"},
		Skills:        []string{"ctf-web"},
		Model:         "deepseek/deepseek-v4-flash",
		ThinkingLevel: "high",
	}
	saveRoleForTest(t, service, role, "你是 CTF 选手。\n"+marker)

	workspace := t.TempDir()
	if err := service.Start("mock", "", workspace, "", "", "", "ctf"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 懒启动：角色是**启动参数**，但进程要到第一条消息才起——
	// 这里显式触发那一步，会话里才会真的出现首条 system 消息。
	warmUpSession(t, service)

	// 1~3：会话首条 system 消息携带的角色信息
	systemMessage := leadingSystemMessage(t, service)
	sections, _ := systemMessage["sections"].(map[string]any)
	if sections == nil {
		t.Fatalf("首条 system 消息缺少 sections: %+v", systemMessage)
	}
	prompt, _ := sections["rolePrompt"].(string)
	if !strings.Contains(prompt, marker) {
		t.Errorf("角色提示词没有进入会话（sections.rolePrompt）: %q", prompt)
	}
	// 提示词来源要能看出"角色提示词"在；这个角色**没声明能力**，
	// 按新约定（只有勾选的能力才给角色）不会有环境清单文件。
	sources, _ := sections["rolePromptSources"].([]any)
	var hasRolePrompt, hasEnvManifest bool
	for _, item := range sources {
		if strings.HasSuffix(filepath.ToSlash(fmtSprint(item)), "prompts/ctf.md") {
			hasRolePrompt = true
		}
		if strings.HasSuffix(filepath.ToSlash(fmtSprint(item)), "ctf.env.md") {
			hasEnvManifest = true
		}
	}
	if !hasRolePrompt {
		t.Errorf("提示词来源里应该有角色提示词文件，实际 %v", sources)
	}
	if hasEnvManifest {
		t.Errorf("没声明能力的角色不该有环境清单文件（能力是授权语义：没勾选的不给），实际 %v", sources)
	}
	if tools, _ := sections["tools"].(string); !strings.Contains(tools, "read") || strings.Contains(tools, "edit") {
		t.Errorf("工具清单应该只有角色声明的 read/bash，实际 %q", tools)
	}
	if skills, _ := sections["skills"].(string); !strings.Contains(skills, "ctf-web") {
		t.Errorf("技能白名单没有进入会话，实际 %q", skills)
	}
	added, _ := systemMessage["toolsAdded"].([]any)
	if len(added) != 2 {
		t.Errorf("toolsAdded 应该正好是两个工具，实际 %d 个: %+v", len(added), added)
	}

	// 4：模型与思考级别由角色决定
	state := statePayload(t, service)
	if !strings.Contains(fmtSprint(state["model"]), "deepseek-v4-flash") {
		t.Errorf("模型应该跟随角色，实际 %v", state["model"])
	}
	if level := fmtSprint(state["thinkingLevel"]); level != "high" {
		t.Errorf("思考级别应该跟随角色（high），实际 %v", level)
	}
}

// 没有角色的会话不该带上任何角色痕迹：否则"默认行为"就被悄悄改掉了。
func TestSessionWithoutRoleHasNoRolePrompt(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	if err := service.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 懒启动：会话内容来自 Pi 进程，Start 之后还没有进程
	warmUpSession(t, service)

	sections, _ := leadingSystemMessage(t, service)["sections"].(map[string]any)
	if _, ok := sections["rolePrompt"]; ok {
		t.Errorf("没有角色时不该有 rolePrompt: %+v", sections)
	}
	if tools, _ := sections["tools"].(string); !strings.Contains(tools, "edit") {
		t.Errorf("没有角色时应该用 Pi 默认工具集，实际 %q", tools)
	}
}

// 角色 id 不存在时不能挡住启动：一个坏配置不该让整个界面用不了。
func TestUnknownRoleDoesNotBlockStart(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "does-not-exist"); err != nil {
		t.Fatalf("未知角色不该阻止启动: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	// 懒启动：进程起来之后才谈得上"会话里有没有角色痕迹"
	warmUpSession(t, service)

	sections, _ := leadingSystemMessage(t, service)["sections"].(map[string]any)
	if _, ok := sections["rolePrompt"]; ok {
		t.Errorf("未知角色不该产出提示词参数: %+v", sections)
	}
}

// 每个工作区各自记住角色：切换工作区不该把角色也一起丢了。
func TestRoleRememberedPerWorkspace(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	seedRoles(t, service)

	first := t.TempDir()
	second := t.TempDir()
	// ctf / forensics 已不再是内置角色，测试里手动建出来
	for _, id := range []string{"ctf", "forensics"} {
		raw, _ := json.Marshal(rolePayload{Role: Role{ID: id, Name: id}})
		if _, err := service.SaveRole(string(raw)); err != nil {
			t.Fatalf("SaveRole(%s): %v", id, err)
		}
	}
	service.mu.Lock()
	service.currentWorkspace = first
	service.mu.Unlock()
	if _, err := service.SetActiveRole("ctf"); err != nil {
		t.Fatalf("SetActiveRole: %v", err)
	}
	service.mu.Lock()
	service.currentWorkspace = second
	service.mu.Unlock()
	if _, err := service.SetActiveRole("forensics"); err != nil {
		t.Fatalf("SetActiveRole: %v", err)
	}

	var payload struct {
		RoleByWorkspace map[string]string `json:"roleByWorkspace"`
	}
	if err := json.Unmarshal([]byte(mustList(t, service).Data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RoleByWorkspace[normalizeDir(first)] != "ctf" {
		t.Errorf("第一个工作区应该记住 ctf: %+v", payload.RoleByWorkspace)
	}
	if payload.RoleByWorkspace[normalizeDir(second)] != "forensics" {
		t.Errorf("第二个工作区应该记住 forensics: %+v", payload.RoleByWorkspace)
	}
}

// ---- 辅助 ----

func seedRoles(t *testing.T, service *PiService) {
	t.Helper()
	if _, err := service.ListRoles(); err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
}

func saveRoleForTest(t *testing.T, service *PiService, role Role, prompt string) {
	t.Helper()
	raw, err := json.Marshal(rolePayload{Role: role, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveRole(string(raw)); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}
}

/**
 * waitForState 保证"当前会话真的有一个能应答的 Pi 进程"。
 *
 * ⚠️ 实现变了，契约也变了（别按老用法理解）：
 *
 * 老实现是轮询 `GetState()` 直到不报错——那时 Start 会立刻起进程，
 * "能应答"就等于"进程起来了"。懒启动之后（会话=日志，进程留到第一条消息，
 * 见 service.go 的 Start）GetState 在**没有进程时也会应答**（合成状态），
 * 照旧实现会立刻返回，后面所有依赖进程的断言（get_messages、子智能体投递…）
 * 就会在一个不存在的进程上做——症状是"一堆看起来毫不相关的用例同时失败"。
 *
 * 所以它现在做的是显式触发一次懒启动（与界面上"发出第一条消息"同一个入口），
 * 再等这个进程能应答。调用点保持原样：它们的意图本来就是"这里需要一个活着的会话"。
 */
func waitForState(t *testing.T, service *PiService) {
	t.Helper()
	warmUpSession(t, service)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := service.GetState(); err == nil {
			return
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Fatal("等待会话状态超时（Pi 没起来？）")
}

// leadingSystemMessage 取出会话的第一条 system 消息。
func leadingSystemMessage(t *testing.T, service *PiService) map[string]any {
	t.Helper()
	result, err := service.GetMessages()
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析消息列表失败: %v（%s）", err, truncate(result.Data, 200))
	}
	for _, message := range payload.Messages {
		if fmtSprint(message["role"]) == "system" {
			return message
		}
	}
	t.Fatalf("会话里没有 system 消息（首条应该是它）: %s", truncate(result.Data, 300))
	return nil
}

func statePayload(t *testing.T, service *PiService) map[string]any {
	t.Helper()
	result, err := service.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析 state 失败: %v", err)
	}
	return payload
}

// fmtSprint 把任意 JSON 值变成可读字符串（断言里只想做包含判断）。
func fmtSprint(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// 文件名与内容 id 不一致的角色必须标坏，不能静默放行。
//
// 角色的寻址方式就是文件名（读/改/删全按 <id>.json 找）。手工拷贝重命名、
// 或直接编辑文件里的 id，都会造出"文件叫 a.json、内容自称 b"的角色：
// 列表显示 b，但按 b 删会去找 b.json（不存在），按 b 保存又会新生成一个 b.json，
// 留下 a.json 这个幽灵——排查起来极费劲。宁可标坏让用户看到。
func TestRoleFileNameIDMismatchIsBroken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	service := &PiService{}
	seedRoles(t, service)

	// 文件名 ghost.json，内容却自称 "real"
	raw, err := json.Marshal(map[string]any{"id": "real", "name": "不一致的角色"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "roles", "ghost.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var mismatch *roleView
	views := loadRoles()
	for i := range views {
		if views[i].Role.ID == "real" {
			mismatch = &views[i]
		}
	}
	if mismatch == nil {
		t.Fatalf("列表里应当出现内容 id 为 real 的角色，实际 %+v", views)
	}
	if mismatch.Broken == "" {
		t.Error("文件名与内容 id 不一致必须标 Broken，不能静默放行")
	}
	// 标坏的角色 Find 要返回不可用（界面据此禁用"使用此角色"）
	if _, ok := findRole("real"); ok {
		t.Error("Broken 角色经 Find 应当返回不可用")
	}
}

var _ = os.Getenv
