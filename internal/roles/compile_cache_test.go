package roles

import (
	"os"
	"path/filepath"
	"testing"

	"zeroa/internal/tools"
)

// setupCompileCacheTest 把角色目录放进临时目录，并把工具目录换成空的
// （replace 模式：零探测目标，编译不碰开发机的真实环境，跑得快且结果确定）。
func setupCompileCacheTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Configure(Host{AgentDir: func() string { return dir }})
	catalogPath := filepath.Join(dir, "tool-catalog.json")
	catalog := `{"version":1,"mode":"replace","tools":[],
		"capabilityRules":[{"match":"web_search","capabilities":["web.search"]}]}`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	tools.SetActiveCatalog(tools.LoadCatalog(catalogPath))
	InvalidateCompileCache()
	t.Cleanup(func() {
		Configure(Host{AgentDir: func() string { return "" }})
		tools.SetActiveCatalog(tools.DefaultCatalog())
		InvalidateCompileCache()
	})
	return dir
}

// 编译缓存：同一角色同一批输入，重复编译必须命中（不再重扫工具）。
// 角色内容变了、检测版本变了、手动作废，都必须让缓存失效。
func TestCompileCachesByRoleID(t *testing.T) {
	setupCompileCacheTest(t)
	role := Role{ID: "alpha", Name: "A"}

	// 前两次编译用来"焐热"：第一次编译本身会触发一轮工具重扫，
	// 检测版本号随之变化——所以真正的稳态从第三次开始。
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(第二次): %v", err)
	}
	hitsBefore, missesBefore := compileCacheStats()
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(第三次): %v", err)
	}
	hitsAfter, missesAfter := compileCacheStats()
	if hitsAfter != hitsBefore+1 || missesAfter != missesBefore {
		t.Errorf("稳态下同输入的编译应命中缓存: hits %d→%d, misses %d→%d", hitsBefore, hitsAfter, missesBefore, missesAfter)
	}

	// 角色定义变了 → 缓存失效
	changed := role
	changed.Name = "B"
	if _, err := Compile(changed, true); err != nil {
		t.Fatalf("Compile(改过的角色): %v", err)
	}
	_, missesChanged := compileCacheStats()
	if missesChanged != missesAfter+1 {
		t.Error("角色内容变化后编译不能命中旧缓存")
	}

	// 工具检测版本变了（用户点了"重新检测"）→ 缓存失效
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(还原的角色): %v", err)
	}
	tools.ResetCache()
	_, missesReset := compileCacheStats()
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(重扫后): %v", err)
	}
	if _, missesNow := compileCacheStats(); missesNow != missesReset+1 {
		t.Error("工具重扫后编译不能命中旧缓存")
	}

	// 手动作废（导入技能等"目录变了但角色没变"的场景）
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(重扫后第二次): %v", err)
	}
	InvalidateCompileCache()
	_, missesInvalidate := compileCacheStats()
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(作废后): %v", err)
	}
	if _, missesNow := compileCacheStats(); missesNow != missesInvalidate+1 {
		t.Error("InvalidateCompileCache 之后编译不能命中缓存")
	}
}

// 产物文件（env/delegation 提示词）被外部删掉时，缓存必须作废并重编译补写——
// 否则启动参数会指向一个不存在的文件。
func TestCompileCacheDetectsDeletedArtifacts(t *testing.T) {
	setupCompileCacheTest(t)
	// 声明一个本机没有的能力：env 清单照样生成（要如实说"缺这个"），产物文件存在
	role := Role{ID: "beta", Name: "B", Capabilities: []string{"forensics.memory"}}

	compiled, err := Compile(role, true)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(compiled.Spawn.AppendSystemPrompt) == 0 {
		t.Fatal("声明了能力的角色应有环境清单文件（哪怕内容是说缺工具）")
	}
	if _, err := Compile(role, true); err != nil {
		t.Fatalf("Compile(第二次): %v", err)
	}

	if err := os.RemoveAll(filepath.Join(Dir(), "prompts")); err != nil {
		t.Fatal(err)
	}
	hitsBefore, missesBefore := compileCacheStats()
	rewritten, err := Compile(role, true)
	if err != nil {
		t.Fatalf("Compile(文件被删后): %v", err)
	}
	hitsAfter, missesAfter := compileCacheStats()
	if hitsAfter != hitsBefore || missesAfter != missesBefore+1 {
		t.Error("产物文件被删后不能用缓存")
	}
	for _, path := range rewritten.Spawn.AppendSystemPrompt {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			t.Errorf("重编译应把产物文件补写回来: %s", path)
		}
	}
}

// 能力是授权语义（用户要求）：没声明能力的角色不给环境清单——
// 编译产物里没有 env 文件、Environment 为空；声明了的才有，且只含勾选的能力。
func TestCompileScopesEnvironmentToDeclaredCapabilities(t *testing.T) {
	setupCompileCacheTest(t)
	// web_search 由"运行中的会话"提供（扩展注册的工具不在 PATH 里）：
	// 规则（web_search → web.search）已在目录里，这里注册提供方让解析能命中。
	tools.SetAgentToolProvider(func() []tools.AgentTool {
		return []tools.AgentTool{{Name: "web_search", Kind: "extension", Provider: "pi-web-access"}}
	})
	t.Cleanup(func() { tools.SetAgentToolProvider(nil) })

	plain, err := Compile(Role{ID: "plain", Name: "P"}, true)
	if err != nil {
		t.Fatalf("Compile(无能力角色): %v", err)
	}
	if len(plain.Spawn.AppendSystemPrompt) != 0 {
		t.Errorf("没声明能力的角色不该有任何环境清单文件: %v", plain.Spawn.AppendSystemPrompt)
	}
	if len(plain.Environment.Commands) != 0 || len(plain.Environment.AgentTools) != 0 ||
		len(plain.Environment.PythonPackages) != 0 {
		t.Errorf("没声明能力的角色不该拿到任何工具清单: %+v", plain.Environment)
	}

	scoped, err := Compile(Role{ID: "scoped", Name: "S", Capabilities: []string{"web.search"}}, true)
	if err != nil {
		t.Fatalf("Compile(声明能力的角色): %v", err)
	}
	if len(scoped.Environment.AgentTools) != 1 || scoped.Environment.AgentTools[0] != "web_search" {
		t.Errorf("勾选的能力对应的工具要进环境清单: %+v", scoped.Environment.AgentTools)
	}
	if len(scoped.Spawn.AppendSystemPrompt) == 0 {
		t.Error("声明了能力且本机有对应工具时，环境清单文件要生成")
	}
}
