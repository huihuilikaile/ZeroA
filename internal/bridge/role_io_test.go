package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zeroa/internal/permission"
)

// ★ 导出 → 导入必须完整往返：**提示词也要跟着走**。
//
// 这是这个功能最容易做残的地方：角色是"定义 + 提示词"两个文件，
// 只导出 <id>.json 的话，导入方拿到的是一个没有灵魂的空壳角色
// （能选中、能启动，但模型没有任何角色约束），而且不会报错。
func TestRoleExportImportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	service := &PiService{}

	saveRoleForTest(t, service, Role{ID: "alpha", Name: "阿尔法", ThinkingLevel: "high",
		Tools: []string{"read", "grep"}, SessionNamePrefix: "[A]"}, "你是阿尔法。\n第一条纪律：先侦察。")
	beta := Role{ID: "beta", Name: "贝塔"}
	beta.Permissions = permission.Policy{"shell.execute": permission.Deny}
	saveRoleForTest(t, service, beta, "你是贝塔。")

	out := filepath.Join(t.TempDir(), "pack.json")
	payload, _ := json.Marshal(map[string]any{"path": out})
	result, err := service.ExportRoles(string(payload))
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !result.Success {
		t.Fatalf("导出未成功: %s", result.Data)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("导出文件没落盘: %v", err)
	}
	var exported struct {
		Path  string   `json:"path"`
		Count int      `json:"count"`
		IDs   []string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(result.Data), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Count != 2 {
		t.Fatalf("应当导出 2 个角色，实际 %d（%v）", exported.Count, exported.IDs)
	}

	// 清空角色（模拟换一台机器 / 另一份安装）
	rolesDirPath := filepath.Join(dir, "roles")
	for _, name := range []string{"alpha.json", "beta.json"} {
		if err := os.Remove(filepath.Join(rolesDirPath, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alpha.md", "beta.md"} {
		_ = os.Remove(filepath.Join(rolesDirPath, "prompts", name))
	}
	if len(loadRoles()) != 0 {
		t.Fatal("前置条件不成立：角色目录应当已清空")
	}

	// 导入回来
	result, err = service.ImportRoles(out)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	var summary struct {
		Created     []string            `json:"created"`
		Overwritten []string            `json:"overwritten"`
		Failed      []map[string]string `json:"failed"`
	}
	if err := json.Unmarshal([]byte(result.Data), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Created) != 2 || len(summary.Failed) != 0 {
		t.Fatalf("应当新增 2 个、失败 0 个，实际 created=%v failed=%v", summary.Created, summary.Failed)
	}

	views := loadRoles()
	if len(views) != 2 {
		t.Fatalf("导入后应当有 2 个角色，实际 %d", len(views))
	}
	byID := map[string]roleView{}
	for _, view := range views {
		byID[view.Role.ID] = view
	}
	if got := byID["alpha"].Prompt; !strings.Contains(got, "先侦察") {
		t.Errorf("提示词没跟着回来（这是本测试的核心）：%q", got)
	}
	if byID["alpha"].Role.Name != "阿尔法" || byID["alpha"].Role.ThinkingLevel != "high" || len(byID["alpha"].Role.Tools) != 2 {
		t.Errorf("角色定义字段丢内容: %+v", byID["alpha"].Role)
	}
	if byID["beta"].Role.Permissions["shell.execute"] != permission.Deny {
		t.Errorf("权限字段没带回来: %+v", byID["beta"].Role.Permissions)
	}
}

// 目录形式（社区包 / .pi/roles 的形状）：*.json + prompts/<id>.md
func TestRoleImportFromDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	service := &PiService{}

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	role := Role{ID: "recon", Name: "检索"}
	raw, _ := json.Marshal(role)
	if err := os.WriteFile(filepath.Join(src, "recon.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "prompts", "recon.md"), []byte("只做检索，不要动手。"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 混一个不是角色的 JSON：应当被跳过，而不是让整次导入失败
	if err := os.WriteFile(filepath.Join(src, "mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := service.ImportRoles(src)
	if err != nil {
		t.Fatalf("导入目录失败: %v", err)
	}
	var summary struct {
		Created []string            `json:"created"`
		Failed  []map[string]string `json:"failed"`
	}
	_ = json.Unmarshal([]byte(result.Data), &summary)
	if len(summary.Created) != 1 || summary.Created[0] != "recon" {
		t.Fatalf("应当只导入 recon，实际 %v（失败 %v）", summary.Created, summary.Failed)
	}
	views := loadRoles()
	if len(views) != 1 || !strings.Contains(views[0].Prompt, "只做检索") {
		t.Fatalf("目录里的提示词没被带上: %+v", views)
	}
}

// 非法 id 必须被挡在门外：导入的是别人给的文件，不能让它写到角色目录之外。
func TestRoleImportRejectsInvalidID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	service := &PiService{}

	pack := roleBundle{
		Format: roleBundleFormat, Version: roleBundleVersion,
		Roles: []roleBundleEntry{
			{Role: Role{ID: "../escape", Name: "越界"}, Prompt: "x"},
			{Role: Role{ID: "ok-role", Name: "正常"}, Prompt: "y"},
		},
	}
	raw, _ := json.Marshal(pack)
	src := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(src, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := service.ImportRoles(src)
	if err != nil {
		t.Fatalf("导入不应整体失败: %v", err)
	}
	var summary struct {
		Created []string            `json:"created"`
		Failed  []map[string]string `json:"failed"`
	}
	_ = json.Unmarshal([]byte(result.Data), &summary)
	if len(summary.Created) != 1 || summary.Created[0] != "ok-role" {
		t.Fatalf("只应导入合法的那一个，实际 %v", summary.Created)
	}
	if len(summary.Failed) != 1 || !strings.Contains(summary.Failed[0]["error"], "角色 ID") {
		t.Fatalf("越界 id 应当被拒绝并说明原因，实际 %v", summary.Failed)
	}
	// 关键：不能真的写到角色目录外面去
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.json")); err == nil {
		t.Error("非法 id 竟然写到了角色目录之外")
	}
}

// 默认导出路径落在 <配置目录>/exports/ 下，不污染别的目录。
func TestRoleExportDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	service := &PiService{}
	saveRoleForTest(t, service, Role{ID: "solo", Name: "独自"}, "提示")

	result, err := service.ExportRoles("")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	var payload struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(result.Data), &payload)
	if !strings.HasPrefix(payload.Path, filepath.Join(dir, "exports")) {
		t.Errorf("默认导出路径应当在 <配置目录>/exports 下，实际 %s", payload.Path)
	}
	if _, err := os.Stat(payload.Path); err != nil {
		t.Errorf("默认路径的文件不存在: %v", err)
	}
}

// 覆盖导入：同 id 已存在时报告为"覆盖"，而不是当成新增。
func TestRoleImportOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	service := &PiService{}
	saveRoleForTest(t, service, Role{ID: "dup", Name: "旧名字"}, "旧提示")

	pack := roleBundle{Format: roleBundleFormat, Version: roleBundleVersion,
		Roles: []roleBundleEntry{{Role: Role{ID: "dup", Name: "新名字"}, Prompt: "新提示"}}}
	raw, _ := json.Marshal(pack)
	src := filepath.Join(t.TempDir(), "pack.json")
	_ = os.WriteFile(src, raw, 0o644)

	result, err := service.ImportRoles(src)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	var summary struct {
		Created     []string `json:"created"`
		Overwritten []string `json:"overwritten"`
	}
	_ = json.Unmarshal([]byte(result.Data), &summary)
	if len(summary.Overwritten) != 1 || len(summary.Created) != 0 {
		t.Fatalf("应当报告覆盖 1 个，实际 created=%v overwritten=%v", summary.Created, summary.Overwritten)
	}
	views := loadRoles()
	if len(views) != 1 || views[0].Role.Name != "新名字" || !strings.Contains(views[0].Prompt, "新提示") {
		t.Fatalf("覆盖后内容不对: %+v", views)
	}
}
