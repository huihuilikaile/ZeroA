package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useEmptyCatalog 把目录换成"零工具"（replace 模式），并让测试结束后还原。
// 这样 Resolve/DetectAll 不会去真实探测开发机上的几十个工具——测试快且结果确定。
func useEmptyCatalog(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"mode":"replace","tools":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	SetActiveCatalog(LoadCatalog(path))
	t.Cleanup(func() { SetActiveCatalog(DefaultCatalog()) })
}

// 能力没有任何工具认领时，Available/Missing 必须是空数组而不是 nil：
// nil 在 JSON 里是 null，前端按数组读会让整个页面崩掉
// （"Cannot read properties of null (reading 'length')"，能力页实测踩过）。
func TestResolveReturnsEmptySlicesNotNull(t *testing.T) {
	useEmptyCatalog(t)

	resolutions, env := Resolve([]string{"nonexistent.capability"})
	if len(resolutions) != 1 {
		t.Fatalf("一个声明的能力要有一条解析结果，实际 %d 条", len(resolutions))
	}
	if resolutions[0].Available == nil || resolutions[0].Missing == nil {
		t.Fatalf("Available/Missing 不能是 nil: %+v", resolutions[0])
	}
	data, err := json.Marshal(resolutions[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"available":null`) || strings.Contains(string(data), `"missing":null`) {
		t.Errorf("解析结果的 JSON 里不能有 null 数组: %s", data)
	}
	for name, slice := range map[string][]string{
		"pythonPackages": env.PythonPackages, "nodeModules": env.NodeModules,
		"applications": env.Applications, "agentTools": env.AgentTools, "missing": env.Missing,
	} {
		if slice == nil {
			t.Errorf("Environment.%s 不能是 nil（JSON 会变成 null）", name)
		}
	}
	if len(env.Missing) != 1 || env.Missing[0] != "nonexistent.capability" {
		t.Errorf("没人认领的能力要进 Missing: %+v", env.Missing)
	}
}

// 环境清单收拢（用户要求）：只有角色声明的能力对应的工具才进清单，
// 没声明的一律不给；扩展关闭时"可直接调用"一句都不能留。
func TestScopedEnvironmentOnlySelectedCapabilities(t *testing.T) {
	resolutions := []Resolution{
		{Capability: "web.scan", Available: []Status{
			{Definition: Definition{ID: "nuclei", Name: "nuclei", Type: KindCommand}, Available: true, Path: `C:\tools\nuclei.exe`},
			{Definition: Definition{ID: "web_search", Name: "web_search", Type: KindExtension, AgentTool: true}, Available: true},
			// 同一工具被两个能力命中时只进一次
			{Definition: Definition{ID: "nuclei", Name: "nuclei", Type: KindCommand}, Available: true, Path: `C:\tools\nuclei.exe`},
		}},
		{Capability: "crypto.hash", Available: []Status{
			{Definition: Definition{ID: "pycryptodome", Name: "PyCryptodome", Type: KindPythonPackage}, Available: true},
		}},
		{Capability: "mcp.ida", Available: []Status{
			{Definition: Definition{ID: "mcp:ida", Name: "ida", Type: KindMCP, AgentTool: true}, Available: true},
		}},
		// 没有可用工具的能力：不进清单，只在 Missing 里
		{Capability: "forensics.memory", Available: []Status{}, Missing: []Status{}},
	}

	env := ScopedEnvironment(resolutions, []string{"forensics.memory"}, false)
	if len(env.Commands) != 1 || env.Commands["nuclei"] != `C:\tools\nuclei.exe` {
		t.Errorf("命令类工具要按 id 收进 Commands 并去重: %+v", env.Commands)
	}
	if len(env.AgentTools) != 2 {
		t.Errorf("扩展与 MCP 工具要进 AgentTools（模型直接调用）: %+v", env.AgentTools)
	}
	if len(env.PythonPackages) != 1 || env.PythonPackages[0] != "PyCryptodome" {
		t.Errorf("Python 包要进 PythonPackages: %+v", env.PythonPackages)
	}
	if len(env.Missing) != 1 || env.Missing[0] != "forensics.memory" {
		t.Errorf("Missing 要照抄解析结果: %+v", env.Missing)
	}

	disabled := ScopedEnvironment(resolutions, nil, true)
	if len(disabled.AgentTools) != 0 || !disabled.ExtensionsDisabled {
		t.Errorf("扩展关闭时不能承诺任何可直接调用的工具: %+v", disabled.AgentTools)
	}
	if len(disabled.Commands) != 1 {
		t.Errorf("扩展关闭不影响环境程序: %+v", disabled.Commands)
	}
}

// 编译缓存的失效键之一：MCP / 会话工具这些"探测不到的输入"变了，指纹必须变。
func TestCapabilityInputsFingerprint(t *testing.T) {
	SetExternalCapabilityProvider(nil)
	SetAgentToolProvider(nil)
	base := CapabilityInputsFingerprint()
	if base != CapabilityInputsFingerprint() {
		t.Error("输入没变时指纹要稳定")
	}

	SetAgentToolProvider(func() []AgentTool {
		return []AgentTool{{Name: "web_search", Kind: "extension", Provider: "pi-web-access"}}
	})
	t.Cleanup(func() { SetAgentToolProvider(nil) })
	if CapabilityInputsFingerprint() == base {
		t.Error("会话工具变了，指纹必须变（否则角色编译缓存会沿用旧结论）")
	}
}
