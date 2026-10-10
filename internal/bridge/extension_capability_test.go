package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toolkit "zeroa/internal/tools"
)

/**
 * 这一组测试盯的是"模型能直接调用的工具"这条链。
 *
 * 它以前靠一张写死的扩展表（包名 → 工具名 → 能力）实现，现在换成两个
 * 可配置/可发现的来源：
 *   1. 运行中的会话——Pi 把最终工具集写进 sections.tools（内置 + 扩展 + MCP）；
 *   2. 清单里的 capabilityRules——"工具名 → 能力"的规则，用户可改。
 * 保证没变：角色声明 web.search，就必须能解析到 web_search 这个真实工具，
 * 而且环境清单里要说明"你可以直接调用它"。
 */

// writeSessionWithTools 造一条带 system 消息的会话文件（工具段落格式与 Pi 一致）。
func writeSessionWithTools(t *testing.T, agentDirPath string, tools []string) string {
	t.Helper()
	dir := filepath.Join(agentDirPath, "sessions", "--test--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var builder strings.Builder
	builder.WriteString("<tools>\n")
	for _, tool := range tools {
		builder.WriteString("- " + tool + ": 说明\n")
	}
	builder.WriteString("</tools>")
	line, err := json.Marshal(map[string]any{
		"type": "message",
		"message": map[string]any{
			"role":     "system",
			"sections": map[string]string{"tools": builder.String()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-06T00-00-00-000Z_test.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// setCatalogRules 把"工具名 → 能力"的规则装进生效目录（模拟用户写的 tool-catalog.json）。
func setCatalogRules(t *testing.T, rules []toolkit.CapabilityRule) {
	t.Helper()
	base := toolkit.DefaultCatalog()
	base.Rules = rules
	toolkit.SetActiveCatalog(base)
	t.Cleanup(func() { toolkit.SetActiveCatalog(toolkit.DefaultCatalog()) })
}

// ★ 扩展工具来自会话：会话里有什么，解析器就认什么。
func TestAgentToolsComeFromSession(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	writeSessionWithTools(t, agentDirPath, []string{"bash", "web_search", "fetch_content", "mcp__ida"})

	service := &PiService{}
	list, source := service.sessionTools()
	if len(list) != 4 || source == "" {
		t.Fatalf("应当从会话里解析出 4 个工具，实际 %d 个（来源 %q）", len(list), source)
	}
	kinds := map[string]string{}
	for _, item := range list {
		kinds[item.ID] = item.Kind
	}
	if kinds["bash"] != "builtin" || kinds["web_search"] != "extension" || kinds["mcp__ida"] != "mcp" {
		t.Errorf("工具归类不对: %+v", kinds)
	}

	// 规则把 web_search 认成 web.search 能力 → 角色声明该能力就不再算缺失
	setCatalogRules(t, []toolkit.CapabilityRule{{Match: "web_search", Capabilities: []string{"web.search"}}})
	toolkit.SetAgentToolProvider(func() []toolkit.AgentTool {
		return []toolkit.AgentTool{{Name: "web_search", Kind: "extension", Provider: "pi-web-access"}}
	})
	t.Cleanup(func() { toolkit.SetAgentToolProvider(nil) })

	resolutions, env := toolkit.Resolve([]string{"web.search"})
	if containsString(env.Missing, "web.search") {
		t.Errorf("会话里有 web_search 且规则已配，不该再报缺失: %+v", env.Missing)
	}
	names := map[string]bool{}
	for _, resolution := range resolutions {
		for _, tool := range resolution.Available {
			names[tool.ID] = true
			if !tool.AgentTool {
				t.Errorf("会话提供的工具应当标 AgentTool: %+v", tool.Definition)
			}
			if tool.Provider != "pi-web-access" {
				t.Errorf("应当记录来源包名: %+v", tool.Definition)
			}
		}
	}
	if !names["web_search"] {
		t.Errorf("web.search 应该解析到 web_search: %v", names)
	}
	if !containsString(env.AgentTools, "web_search") {
		t.Errorf("会话工具应进 AgentTools 清单: %+v", env.AgentTools)
	}
	prompt := toolkit.EnvironmentPrompt(env, resolutions)
	if !strings.Contains(prompt, "直接调用") {
		t.Errorf("提示词要说明哪些工具可以直接调用：\n%s", prompt)
	}
}

// 没有会话时不能凭空造工具：清单为空，而不是显示一份假的内置列表。
func TestAgentToolsEmptyWithoutSession(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}
	list, source := service.sessionTools()
	if len(list) != 0 || source != "" {
		t.Fatalf("没有任何会话时应当为空，实际 %d 个（来源 %q）", len(list), source)
	}
	_, env := toolkit.Resolve(nil)
	if len(env.AgentTools) != 0 {
		t.Errorf("没有会话时不该承诺任何可调用工具: %+v", env.AgentTools)
	}
}

// 关掉扩展加载时，环境清单不能再写"你可以直接调用 xxx"。
func TestManifestRespectsExtensionsDisabled(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	writeSessionWithTools(t, agentDirPath, []string{"web_search"})
	toolkit.SetAgentToolProvider(func() []toolkit.AgentTool {
		return []toolkit.AgentTool{{Name: "web_search", Kind: "extension"}}
	})
	t.Cleanup(func() {
		toolkit.SetAgentToolProvider(nil)
		toolkit.SetExtensionsDisabled(false)
	})

	toolkit.SetExtensionsDisabled(false)
	_, env := toolkit.Resolve(nil)
	if !containsString(env.AgentTools, "web_search") {
		t.Errorf("扩展加载时，清单应当列出 web_search: %+v", env.AgentTools)
	}

	toolkit.SetExtensionsDisabled(true)
	_, disabled := toolkit.Resolve(nil)
	if len(disabled.AgentTools) != 0 {
		t.Errorf("关掉扩展后不该再承诺可直接调用的工具: %+v", disabled.AgentTools)
	}
}

// 规则来自清单：用户加一条规则，界面与解析器都能看到这个能力。
func TestCapabilityRulesComeFromCatalog(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	catalogPath := filepath.Join(agentDirPath, "tool-catalog.json")
	if err := os.WriteFile(catalogPath, []byte(`{
	  "version": 1,
	  "capabilityRules": [
	    { "match": "web_*", "capabilities": ["web.search", "web.fetch"] }
	  ],
	  "capabilities": [ { "id": "mycompany.internal", "label": "内网接口" } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	toolkit.SetActiveCatalog(toolkit.LoadCatalog(catalogPath))
	t.Cleanup(func() { toolkit.SetActiveCatalog(toolkit.DefaultCatalog()) })

	matched := toolkit.MatchRules("web_search")
	if !containsString(matched, "web.search") {
		t.Errorf("通配规则应当命中 web_search: %v", matched)
	}
	capabilities := toolkit.KnownCapabilities()
	if !containsString(capabilities, "web.search") {
		t.Errorf("能力清单应包含规则里的能力: %v", capabilities)
	}
	if !containsString(capabilities, "mycompany.internal") {
		t.Errorf("只声明未实现的能力也要能勾选: %v", capabilities)
	}
	if label := toolkit.CapabilityLabels()["mycompany.internal"]; label != "内网接口" {
		t.Errorf("能力中文名没读出来: %q", label)
	}
}

// MCP 的能力（mcp.<服务器>）来自 mcp.json + mcp-cache.json，不写死任何服务器。
func TestMcpCapabilitiesResolve(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	if err := os.WriteFile(filepath.Join(agentDirPath, "mcp.json"), []byte(`{
	  "mcpServers": {
	    "ida": { "url": "http://127.0.0.1:13391/mcp" },
	    "burp": { "command": "java", "args": ["-jar", "proxy.jar"] }
	  }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDirPath, "mcp-cache.json"), []byte(`{
	  "version": 1,
	  "servers": {
	    "ida": { "cachedAt": "2026-10-06T11:00:00Z", "tools": [{"name":"a"},{"name":"b"},{"name":"c"}], "resources": [{"uri":"x"}] },
	    "burp": { "cachedAt": "2026-10-06T11:00:00Z", "discoveryFailed": true, "tools": [] }
	  }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	infos := loadMcpServerInfos()
	if len(infos) != 2 {
		t.Fatalf("应当从配置里读出 2 台服务器，实际 %d", len(infos))
	}
	byName := map[string]mcpServerInfo{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	if byName["ida"].ToolCount != 3 || byName["ida"].Capability != "mcp.ida" {
		t.Errorf("ida 的工具数与能力名不对: %+v", byName["ida"])
	}
	if !byName["burp"].Failed {
		t.Errorf("burp 上次发现失败，界面要能看出来: %+v", byName["burp"])
	}

	// 解析器要能判定角色声明的 mcp.<服务器>
	toolkit.SetExternalCapabilityProvider(mcpCapabilityStatuses)
	t.Cleanup(func() { toolkit.SetExternalCapabilityProvider(nil) })
	toolkit.ResetCache()

	_, env := toolkit.Resolve([]string{"mcp.ida", "mcp.burp", "mcp.不存在"})
	if containsString(env.Missing, "mcp.ida") {
		t.Errorf("ida 有 3 个工具，不该算缺失: %+v", env.Missing)
	}
	for _, capability := range []string{"mcp.burp", "mcp.不存在"} {
		if !containsString(env.Missing, capability) {
			t.Errorf("%s 应当算缺失（发现失败/根本没配）: %+v", capability, env.Missing)
		}
	}
}

// 角色声明能力后，工具白名单里必须真的有解析出来的**模型可调用工具**
// （原有保证，换源后仍要成立）。本机命令行程序不进白名单——Pi 的 --tools 只认工具。
func TestCapabilityStillGrantsConcreteTool(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	setCatalogRules(t, []toolkit.CapabilityRule{{Match: "web_search", Capabilities: []string{"web.search"}}})
	toolkit.SetAgentToolProvider(func() []toolkit.AgentTool {
		return []toolkit.AgentTool{{Name: "web_search", Kind: "extension", Provider: "pi-web-access"}}
	})
	t.Cleanup(func() { toolkit.SetAgentToolProvider(nil) })

	role := Role{ID: "web", Name: "Web", Tools: []string{"read", "bash"}, Capabilities: []string{"web.search"}}
	compiled, err := compileRole(role, true)
	if err != nil {
		t.Fatalf("compileRole: %v", err)
	}
	if !containsString(compiled.Spawn.Tools, "web_search") {
		t.Errorf("能力解析出的工具要进白名单：期望含 web_search，实际 %v", compiled.Spawn.Tools)
	}
	if !containsString(compiled.Spawn.Tools, "read") {
		t.Errorf("原有白名单不能被覆盖: %v", compiled.Spawn.Tools)
	}
}

// 冲突检测随"写死的扩展表"一起删了：现在由 Pi 决定加载谁，清单如实显示结果。
func TestToolConflictsAreNoLongerGuessed(t *testing.T) {
	if conflicts := toolkit.ToolConflicts(); len(conflicts) != 0 {
		t.Errorf("不再由我们猜测扩展冲突，实际报出 %+v", conflicts)
	}
}
