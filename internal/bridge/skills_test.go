package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 造一个假技能：目录 + SKILL.md（frontmatter 与真实技能同形）。
func writeSkill(t *testing.T, root, category, dir, name, description string) string {
	t.Helper()
	path := filepath.Join(root, category, dir)
	if err := os.MkdirAll(filepath.Join(path, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: " + name + "\ndescription: " + description +
		"\nlicense: MIT\nallowed-tools: Bash Read Write\nmetadata:\n  user-invocable: \"false\"\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "references", "notes.md"), []byte("参考资料"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 技能扫描：认 frontmatter、统计体积、按方向分组、坏文件不让整个列表失效。
func TestSkillScanning(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	root := filepath.Join(agentDirPath, "skills")

	writeSkill(t, root, "crypto", "crypto", "ctf-crypto", "RSA / 格 / 哈希攻击手册")
	writeSkill(t, root, "web", "web", "ctf-web", "Web 漏洞利用手册")
	// 坏技能：没有 name/description
	broken := filepath.Join(root, "misc", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte("没有 frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := &PiService{}
	result, err := service.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	var payload struct {
		Root   string      `json:"root"`
		Total  int         `json:"total"`
		Exists bool        `json:"exists"`
		Skills []skillInfo `json:"skills"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 3 {
		t.Fatalf("应该扫到 3 个技能，实际 %d：%+v", payload.Total, payload.Skills)
	}
	if !payload.Exists {
		t.Error("技能目录已存在，exists 应为 true")
	}
	for _, skill := range payload.Skills {
		switch skill.ID {
		case "ctf-crypto":
			if skill.Category != "crypto" {
				t.Errorf("方向应该是 crypto，实际 %q", skill.Category)
			}
			if skill.Files < 2 || skill.Bytes == 0 {
				t.Errorf("应该统计到文件数与体积: %+v", skill)
			}
			if !skill.HasReferences {
				t.Error("有 references 目录的技能应该标记出来")
			}
			if len(skill.AllowedTools) != 3 {
				t.Errorf("allowed-tools 应该解析出 3 个: %+v", skill.AllowedTools)
			}
		case "broken":
			if skill.Broken == "" {
				t.Error("缺少 name/description 的技能要标成有问题，不能静默通过")
			}
		}
	}

	// 技能 id 解析成路径（--skill 要的是路径）
	paths, missing := resolveSkillPaths([]string{"ctf-web", "web", "不存在的技能"})
	if len(paths) != 2 {
		t.Errorf("应该解析出 2 个路径（按 name 与目录名各一个），实际 %v", paths)
	}
	for _, path := range paths {
		if !strings.HasSuffix(filepath.ToSlash(path), "/SKILL.md") && !dirExists(path) {
			t.Errorf("解析结果应该是技能目录: %s", path)
		}
	}
	if len(missing) != 1 || missing[0] != "不存在的技能" {
		t.Errorf("应该报出缺失的技能名，实际 %v", missing)
	}
}

// 保存角色时的技能预检：写错的技能名要挡住，并给出候选项。
func TestRoleSkillPrecheck(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	writeSkill(t, filepath.Join(agentDirPath, "skills"), "web", "web", "ctf-web", "Web 手册")

	service := &PiService{}
	// 存在的技能：保存成功
	raw, _ := json.Marshal(rolePayload{Role: Role{ID: "ok", Name: "ok", Skills: []string{"ctf-web"}}})
	if _, err := service.SaveRole(string(raw)); err != nil {
		t.Fatalf("有效技能应该保存成功: %v", err)
	}
	// 不存在的技能：报错里要带上可用技能名
	raw, _ = json.Marshal(rolePayload{Role: Role{ID: "bad", Name: "bad", Skills: []string{"不存在的技能"}}})
	_, err := service.SaveRole(string(raw))
	if err == nil {
		t.Fatal("无效技能名应该被拒绝")
	}
	if !strings.Contains(err.Error(), "找不到技能") || !strings.Contains(err.Error(), "ctf-web") {
		t.Errorf("错误信息应该说明缺哪个、并给出候选，实际: %v", err)
	}
}

// 角色编译：技能 id → --skill 路径。
func TestRoleOptionsResolveSkillPaths(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	writeSkill(t, filepath.Join(agentDirPath, "skills"), "pwn", "pwn", "ctf-pwn", "Pwn 手册")

	opts, err := roleOptions(Role{ID: "ctf", Name: "CTF", Skills: []string{"ctf-pwn"}}, true)
	if err != nil {
		t.Fatalf("roleOptions: %v", err)
	}
	if len(opts.Skills) != 1 {
		t.Fatalf("应该解析出一个技能路径: %v", opts.Skills)
	}
	if !dirExists(opts.Skills[0]) {
		t.Errorf("--skill 收到的是目录路径: %s", opts.Skills[0])
	}
	// 技能全禁（空切片）与不限制（nil）必须区分开
	off, _ := roleOptions(Role{ID: "x", Name: "x", Skills: []string{}}, true)
	if off.Skills == nil || len(off.Skills) != 0 {
		t.Errorf("空切片应该保持为空的非 nil（--no-skills）: %v", off.Skills)
	}
	none, _ := roleOptions(Role{ID: "y", Name: "y"}, true)
	if none.Skills != nil {
		t.Errorf("没声明技能时应该是 nil（不干预）: %v", none.Skills)
	}
}

// 导入技能目录：复制结构、跳过已存在的、拒绝没有 SKILL.md 的目录。
func TestImportSkills(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	source := filepath.Join(t.TempDir(), "pack")
	writeSkill(t, source, "agentskills/crypto", "crypto", "ctf-crypto", "密码学手册")
	writeSkill(t, source, "agentskills/web", "web", "ctf-web", "Web 手册")

	service := &PiService{}
	result, err := service.ImportSkills(filepath.Join(source, "agentskills"))
	if err != nil {
		t.Fatalf("ImportSkills: %v", err)
	}
	var payload struct {
		Imported []string `json:"imported"`
		Skipped  []string `json:"skipped"`
		Total    int      `json:"total"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Imported) != 2 || payload.Total != 2 {
		t.Fatalf("应该导入 2 个技能: %+v", payload)
	}
	// 目录结构与 references 一起复制过来（技能是目录，不是单个文件）
	for _, rel := range payload.Imported {
		dir := filepath.Join(agentDirPath, "skills", filepath.FromSlash(rel))
		if !fileExists(filepath.Join(dir, "SKILL.md")) || !dirExists(filepath.Join(dir, "references")) {
			t.Errorf("技能目录复制不完整: %s", dir)
		}
	}
	// 再导入一次：已存在的跳过而不是覆盖
	result, err = service.ImportSkills(filepath.Join(source, "agentskills"))
	if err != nil {
		t.Fatalf("ImportSkills(第二次): %v", err)
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Imported) != 0 || len(payload.Skipped) != 2 {
		t.Errorf("第二次导入应该全部跳过: %+v", payload)
	}
	// 没有 SKILL.md 的目录要报错而不是默默成功
	empty := t.TempDir()
	if _, err := service.ImportSkills(empty); err == nil {
		t.Error("没有 SKILL.md 的目录应该报错")
	}

	// 外面套一层包装目录（agentskills/...）时要把那层剥掉：
	// 否则技能会变成 skills/agentskills/crypto，方向也跟着丢了。
	if err := os.RemoveAll(filepath.Join(agentDirPath, "skills")); err != nil {
		t.Fatal(err)
	}
	result, err = service.ImportSkills(source)
	if err != nil {
		t.Fatalf("ImportSkills(带包装目录): %v", err)
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Imported) != 2 {
		t.Fatalf("应该导入 2 个技能: %+v", payload.Imported)
	}
	for _, rel := range payload.Imported {
		if strings.HasPrefix(rel, "agentskills/") {
			t.Errorf("包装目录没被剥掉: %s", rel)
		}
		if !strings.Contains(rel, "/") {
			t.Errorf("方向层应该保留（crypto/web）: %s", rel)
		}
	}
}

// MCP 配置读写：新增/更新/删除，且**不认识的字段不能被抹掉**。
func TestMcpServerRoundTrip(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}

	// 先手写一份带"界面不认识的字段"的配置：toolExposure 与 oauth
	path := filepath.Join(agentDirPath, "mcp.json")
	original := `{
  "mcpServers": {
    "existing": {
      "url": "https://example.com/mcp",
      "toolExposure": { "get_*": "direct", "delete_*": "hidden" },
      "oauth": { "clientId": "abc" }
    }
  },
  "autoEnableCodemode": false
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// 更新已有服务器：只改界面管的字段
	enabled := false
	payload, _ := json.Marshal(mcpServer{
		Name: "existing", URL: "https://example.com/mcp", Description: "文档检索", Enabled: &enabled, Exposure: "direct",
	})
	if _, err := service.SaveMcpServer(string(payload)); err != nil {
		t.Fatalf("SaveMcpServer: %v", err)
	}
	// 新增一条 stdio 服务器
	payload, _ = json.Marshal(mcpServer{
		Name: "search", Command: "npx", Args: []string{"-y", "@example/search-mcp"}, Exposure: "deferred",
	})
	if _, err := service.SaveMcpServer(string(payload)); err != nil {
		t.Fatalf("SaveMcpServer: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, must := range []string{"toolExposure", "oauth", "autoEnableCodemode", "delete_*", `"search"`} {
		if !strings.Contains(text, must) {
			t.Errorf("保存后丢了内容 %q:\n%s", must, text)
		}
	}
	if !strings.Contains(text, `"description": "文档检索"`) {
		t.Errorf("界面写的字段没落盘:\n%s", text)
	}

	servers, err := loadMcpServers()
	if err != nil {
		t.Fatalf("loadMcpServers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("应该有 2 条服务器: %+v", servers)
	}
	for _, server := range servers {
		switch server.Name {
		case "search":
			if server.Transport != "stdio" {
				t.Errorf("有 command 的应该是 stdio: %+v", server)
			}
		case "existing":
			if server.Transport != "http" || server.Enabled == nil || *server.Enabled {
				t.Errorf("http 服务器与 enabled=false 要读对: %+v", server)
			}
		}
	}

	// 校验：名字非法 / 缺命令与 URL / exposure 非法
	bad, _ := json.Marshal(mcpServer{Name: "有中文", Command: "x"})
	if _, err := service.SaveMcpServer(string(bad)); err == nil {
		t.Error("非法服务器名应该被拒绝")
	}
	empty, _ := json.Marshal(mcpServer{Name: "ok"})
	if _, err := service.SaveMcpServer(string(empty)); err == nil {
		t.Error("既没有 command 也没有 url 应该被拒绝")
	}
	weird, _ := json.Marshal(mcpServer{Name: "ok", Command: "x", Exposure: "当官"})
	if _, err := service.SaveMcpServer(string(weird)); err == nil {
		t.Error("非法 exposure 应该被拒绝")
	}

	// 删除
	if _, err := service.DeleteMcpServer("search"); err != nil {
		t.Fatalf("DeleteMcpServer: %v", err)
	}
	servers, _ = loadMcpServers()
	if len(servers) != 1 || servers[0].Name != "existing" {
		t.Errorf("删除后应该只剩 existing: %+v", servers)
	}
	if _, err := service.DeleteMcpServer("不存在"); err == nil {
		t.Error("删除不存在的服务器应该报错")
	}
}

// 工具目录接口：三层信息（本机工具线索 / 自动发现的模型工具 / 目录来源）。
//
// Pi 工具不再写死在代码里：它由会话的 sections.tools 自动加载。
// 没有会话时这里就该是空的，并给出"启动一次会话"的说明——而不是显示一份假清单。
func TestToolCatalog(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	service := &PiService{}
	result, err := service.ListToolCatalog()
	if err != nil {
		t.Fatalf("ListToolCatalog: %v", err)
	}
	var payload struct {
		PiTools    []toolInfo `json:"piTools"`
		PiToolsMsg string     `json:"piToolsNote"`
		ToolHints  []struct {
			Direction string   `json:"direction"`
			Label     string   `json:"label"`
			Tools     []string `json:"tools"`
		} `json:"toolHints"`
		Packages   []string `json:"packages"`
		WebCapable bool     `json:"webCapable"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PiTools) != 0 {
		t.Errorf("没有会话时不该凭空造出工具清单: %+v", payload.PiTools)
	}
	if payload.PiToolsMsg == "" {
		t.Error("没有会话时要告诉用户怎么让清单出现")
	}
	if len(payload.ToolHints) == 0 {
		t.Error("方向的工具线索来自目录，不能为空")
	}
	// 没有装联网扩展时应该明确说"不支持联网"
	if payload.WebCapable {
		t.Error("没装任何联网包时 webCapable 应为 false")
	}

	// 造一条会话：工具清单应当自动出现（这就是"不写死"的核心）
	writeSessionWithTools(t, agentDirPath, []string{"bash", "web_search"})
	invalidateSessionTools()
	result, err = service.ListToolCatalog()
	if err != nil {
		t.Fatalf("ListToolCatalog: %v", err)
	}
	var second struct {
		PiTools []toolInfo `json:"piTools"`
	}
	if err := json.Unmarshal([]byte(result.Data), &second); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, tool := range second.PiTools {
		ids[tool.ID] = tool.Category
	}
	if ids["bash"] != "内置" {
		t.Errorf("bash 应当归到内置: %+v", ids)
	}
	if ids["web_search"] != "扩展" {
		t.Errorf("web_search 应当归到扩展: %+v", ids)
	}
}

// 联网能力判断认的是"装了提供 web/search/fetch 的包"。
func TestWebCapabilityDetection(t *testing.T) {
	cases := []struct {
		packages []string
		want     bool
	}{
		{nil, false},
		{[]string{"npm:pi-mcp-adapter"}, false},
		{[]string{"npm:pi-web-access"}, true},
		{[]string{"npm:@ollama/pi-web-search"}, true},
		{[]string{"npm:pi-web-access@0.36.0", "npm:pi-mcp-adapter"}, true},
	}
	for _, tc := range cases {
		if got := hasWebPackage(tc.packages); got != tc.want {
			t.Errorf("hasWebPackage(%v) = %v，期望 %v", tc.packages, got, tc.want)
		}
	}
}
