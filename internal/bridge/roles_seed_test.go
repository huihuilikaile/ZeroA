package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ★ "无角色包"必须真的没有角色：首次启动不能补种内置角色。
//
// 这是用户要求的第二种分发形态：一个空白起点，角色全部自建。
// 开关放在 gui-state.json（应用自己的状态），不动 Pi 的 settings.json。
func TestSeedBuiltinRolesCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	// ① 默认：补种内置角色
	seedBuiltinRoles()
	entries, err := os.ReadDir(filepath.Join(dir, "roles"))
	if err != nil {
		t.Fatalf("读角色目录: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("默认应当补种内置角色")
	}
	t.Logf("默认补种了 %d 个文件", len(entries))

	// ② 打开开关、清空角色目录：不应再补种
	for _, entry := range entries {
		path := filepath.Join(dir, "roles", entry.Name())
		if entry.IsDir() {
			_ = os.RemoveAll(path)
			continue
		}
		_ = os.Remove(path)
	}
	state, err := json.Marshal(guiState{SkipBuiltinRoles: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gui-state.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}
	seedBuiltinRoles()

	entries, err = os.ReadDir(filepath.Join(dir, "roles"))
	if err != nil {
		t.Fatalf("读角色目录: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			t.Errorf("开关打开后不应补种角色，却出现了 %s", entry.Name())
		}
	}
	if len(loadRoles()) != 0 {
		t.Errorf("角色列表应当为空，实际 %d 个", len(loadRoles()))
	}
}

// 开关只影响"补种"，不影响读取已有角色：用户自己放进来的角色照常加载。
func TestSkipBuiltinRolesKeepsUserRoles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	state, _ := json.Marshal(guiState{SkipBuiltinRoles: true})
	if err := os.WriteFile(filepath.Join(dir, "gui-state.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "roles", "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	saveRoleForTest(t, &PiService{}, Role{ID: "my-role", Name: "我的角色"}, "你是我的助手。")

	seedBuiltinRoles()
	views := loadRoles()
	if len(views) != 1 || views[0].Role.ID != "my-role" {
		t.Fatalf("应当只有用户自己的角色，实际 %+v", views)
	}
}

// ★ 回归：每一个被补种的内置角色都必须在保护名单里（不可删除、UI 保存不掉内置标记）。
//
// 名单从种子派生（不再手写第二份）：种子里有什么，保护名单就有什么。
// 现在只内置 zeroa 一个——方向性角色不随程序分发，由用户按需导入。
func TestEverySeededRoleIsProtected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	service := &PiService{}
	seedRoles(t, service) // ListRoles 顺带补种

	if len(builtinRoleIDs) != 1 || !builtinRoleIDs["zeroa"] {
		t.Fatalf("内置保护名单应当只有 zeroa，实际 %+v", builtinRoleIDs)
	}
	for id := range builtinRoleIDs {
		if _, err := service.DeleteRole(id); err == nil {
			t.Errorf("内置角色 %s 不应能删除", id)
		}
	}
	// 方向性角色不再内置：不该出现在补种结果里
	for _, retired := range []string{"ctf", "ir", "forensics", "general", "toolkeeper"} {
		if _, ok := findRole(retired); ok {
			t.Errorf("%s 不该再被内置补种（方向性角色不随程序分发）", retired)
		}
	}

	// 通过界面路径保存一次 zeroa：内置标记必须保住（SaveRole 不接受界面传值）
	view, ok := findRole("zeroa")
	if !ok {
		t.Fatal("zeroa 应当已被补种")
	}
	saveRoleForTest(t, service, view.Role, "改过的提示词")
	after, ok := findRole("zeroa")
	if !ok || !after.Role.Builtin {
		t.Error("界面保存后 zeroa 的 builtin 标记丢了")
	}
	if _, err := service.ResetRole("zeroa"); err != nil {
		t.Errorf("内置角色应当能恢复默认: %v", err)
	}
}

// ★ zeroa 的定义必须保持"空白起点"：不声明技能、本机工具清单（capabilities）、
// 工具白名单、权限、委派——只用 Pi 自带的能力。这是用户的明确要求，
// 谁给它加上配置，谁就要过这条测试。
func TestZeroaSeedIsMinimal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	service := &PiService{}
	seedRoles(t, service)

	view, ok := findRole("zeroa")
	if !ok {
		t.Fatal("zeroa 应当已被补种")
	}
	if view.Role.Tools != nil || view.Role.Skills != nil || view.Role.Capabilities != nil {
		t.Errorf("zeroa 不该声明工具/技能/能力清单: %+v", view.Role)
	}
	if view.Role.Delegation != nil {
		t.Errorf("zeroa 不该能派子智能体: %+v", view.Role.Delegation)
	}
	if len(view.Role.Permissions) != 0 {
		t.Errorf("zeroa 不该有权限覆盖: %+v", view.Role.Permissions)
	}
	if view.Role.Model != "" || view.Role.ThinkingLevel != "" {
		t.Errorf("zeroa 不该锁定模型/思考级别: %+v", view.Role)
	}
}

// ★ 升级迁移：老版本补种过的内置角色（文件里 builtin=true），在新版本里
// 不再是内置——补种时要把它们的标记翻成 false（内容保留），否则界面按文件
// 标记不给删、后端 Delete 却放行，两边说法不一致。
func TestRetiredBuiltinRolesAreDemoted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	// 模拟老版本留下的文件：toolkeeper 还是 builtin=true
	rolesDir := filepath.Join(dir, "roles")
	if err := os.MkdirAll(filepath.Join(rolesDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(map[string]any{
		"id": "toolkeeper", "name": "环境管理员", "builtin": true,
		"tools": []string{"read", "bash"},
	})
	if err := os.WriteFile(filepath.Join(rolesDir, "toolkeeper.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolesDir, "prompts", "toolkeeper.md"), []byte("我自己的提示词"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := &PiService{}
	seedRoles(t, service)

	view, ok := findRole("toolkeeper")
	if !ok {
		t.Fatal("老文件应当原样保留")
	}
	if view.Role.Builtin {
		t.Error("退役的内置角色应当被降级成普通角色（builtin=false）")
	}
	// 内容与工具白名单必须保留，只许翻标记
	if len(view.Role.Tools) != 2 || view.Prompt != "我自己的提示词" {
		t.Errorf("降级不能动用户内容: %+v", view.Role)
	}
	// 降级后就是普通角色：可以删
	if _, err := service.DeleteRole("toolkeeper"); err != nil {
		t.Errorf("降级后的角色应当能删除: %v", err)
	}
	// 再补种一次：幂等，不该把已删的 toolkeeper 又种回来
	seedRoles(t, service)
	if _, ok := findRole("toolkeeper"); ok {
		t.Error("toolkeeper 已不在种子里，不该被重新补种")
	}
}
