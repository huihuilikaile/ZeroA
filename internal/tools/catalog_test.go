package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★ 内置默认目录是"通用打底"：只装任何机器上都可能有、且无方向偏向的工具。
//
// 方向性（CTF/取证/应急…）的目录不随仓库分发，由用户用 tool-catalog.json 覆盖/追加
// （见 TestCatalogUserOverrideAndAdd）。这里断言的是打底表本身不丢条目、不改能力标签。
func TestDefaultCatalogMatchesOldRegistry(t *testing.T) {
	catalog := DefaultCatalog()
	if len(catalog.Tools) != 11 {
		t.Errorf("默认目录应当有 11 条通用本机工具，实际 %d", len(catalog.Tools))
	}
	if len(catalog.Directions) != 1 {
		t.Errorf("默认目录应当只有 1 个通用方向，实际 %d", len(catalog.Directions))
	}
	byID := map[string]Definition{}
	for _, tool := range catalog.Tools {
		byID[tool.ID] = tool
	}
	for id, wantCapability := range map[string]string{
		"jq":     "data.json",
		"python": "script.run",
		"bash":   "shell.unix",
		"curl":   "net.download",
		"git":    "repo.clone",
	} {
		tool, ok := byID[id]
		if !ok {
			t.Errorf("默认目录缺少 %s", id)
			continue
		}
		if !contains(tool.Capabilities, wantCapability) {
			t.Errorf("%s 应当带能力 %s，实际 %v", id, wantCapability, tool.Capabilities)
		}
	}
	// 方向中文名不能丢（界面分组直接显示它）
	labels := map[string]string{}
	for _, direction := range catalog.Directions {
		labels[direction.ID] = direction.Label
	}
	if labels["base"] != "通用" {
		t.Errorf("方向中文名不对: %+v", labels)
	}
}

// ★ 导出 → 改 → 重新加载：用户能加一条自己的工具，不用改代码。
func TestCatalogUserOverrideAndAdd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{
	  "version": 1,
	  "directions": [ { "id": "mytools", "label": "我的工具" } ],
	  "tools": [
	    { "id": "jq", "note": "我自己改的说明", "capabilities": ["data.json", "my.json"] },
	    { "id": "myhelper", "name": "我的助手", "type": "command", "direction": "mytools",
	      "capabilities": ["my.helper"], "detect": { "commands": ["myhelper"] } }
	  ]
	}`)
	catalog := LoadCatalog(path)
	if len(catalog.Issues) != 0 {
		t.Fatalf("不该有问题: %+v", catalog.Issues)
	}
	if len(catalog.Tools) != 12 {
		t.Errorf("同 id 覆盖 + 新 id 追加后应当 12 条，实际 %d", len(catalog.Tools))
	}
	byID := map[string]Definition{}
	for _, tool := range catalog.Tools {
		byID[tool.ID] = tool
	}
	// ① 字段级覆盖：只写了 note 与 capabilities，其他字段继承默认
	if byID["jq"].Note != "我自己改的说明" {
		t.Errorf("note 没被覆盖: %q", byID["jq"].Note)
	}
	if len(byID["jq"].Detect.Commands) == 0 || byID["jq"].Detect.Commands[0] != "jq" {
		t.Errorf("未写的字段必须继承默认（detect 丢了）: %+v", byID["jq"].Detect)
	}
	if byID["jq"].Type != KindCommand {
		t.Errorf("未写的字段必须继承默认（type 丢了）: %+v", byID["jq"].Type)
	}
	// ② 新条目
	if byID["myhelper"].Name != "我的助手" || byID["myhelper"].Direction != "mytools" {
		t.Errorf("新条目不对: %+v", byID["myhelper"])
	}
	// ③ 能力清单要跟着变
	SetActiveCatalog(catalog)
	defer SetActiveCatalog(DefaultCatalog())
	if !contains(KnownCapabilities(), "my.helper") {
		t.Error("自定义能力应当出现在能力清单里")
	}
	if got := DirectionLabel("mytools"); got != "我的工具" {
		t.Errorf("自定义方向名没生效: %q", got)
	}
}

// 显式禁用比"不写"清楚：用户要能表达"我不想让它出现"。
func TestCatalogDisableBuiltinTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	if err := os.WriteFile(path, []byte(`{
	  "version": 1,
	  "tools": [ { "id": "wget", "disabled": true } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(path)
	for _, tool := range catalog.Tools {
		if tool.ID == "wget" {
			t.Fatal("被禁用的条目不该出现")
		}
	}
	if len(catalog.Tools) != 10 {
		t.Errorf("禁用一条后应当 10 条，实际 %d", len(catalog.Tools))
	}
}

// mode: replace = 整份替换（企业统一定制用）。
func TestCatalogReplaceMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	if err := os.WriteFile(path, []byte(`{
	  "version": 1,
	  "mode": "replace",
	  "directions": [ { "id": "base", "label": "通用" } ],
	  "tools": [ { "id": "only", "name": "仅此一个", "detect": { "commands": ["only"] } } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(path)
	if len(catalog.Tools) != 1 || catalog.Tools[0].ID != "only" {
		t.Errorf("replace 模式应当只剩一份清单，实际 %d 条", len(catalog.Tools))
	}
}

// ★ 坏 JSON 绝不能静默变空清单：要报出来，并且仍然有内置默认可用。
func TestCatalogBadJsonFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	if err := os.WriteFile(path, []byte(`{ "tools": [ { "id": "x", } ] }`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(path)
	if len(catalog.Issues) == 0 {
		t.Fatal("坏文件必须报出来（否则用户改了没生效还查不到原因）")
	}
	if !strings.Contains(catalog.Issues[0].Message, "JSON") {
		t.Errorf("问题信息应当说清是 JSON 无效: %+v", catalog.Issues[0])
	}
	if len(catalog.Tools) != 11 {
		t.Errorf("坏文件应当回退到内置默认（11 条），实际 %d", len(catalog.Tools))
	}
}

// 写错字段名（未知字段）也要报错：这是最常见的"改了没生效"。
func TestCatalogUnknownFieldReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	if err := os.WriteFile(path, []byte(`{
	  "version": 1,
	  "tools": [ { "id": "x", "detect": { "command": "x" } } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(path)
	if len(catalog.Issues) == 0 {
		t.Fatal("未知字段（command 应为 commands）必须被报出来")
	}
}

// 三层的优先级：项目级覆盖用户级。
func TestCatalogLayerPriority(t *testing.T) {
	userDir, projectDir := t.TempDir(), t.TempDir()
	userPath := filepath.Join(userDir, "tool-catalog.json")
	projectPath := filepath.Join(projectDir, "tool-catalog.json")
	if err := os.WriteFile(userPath, []byte(`{"version":1,"tools":[{"id":"jq","note":"用户层"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"version":1,"tools":[{"id":"jq","note":"项目层"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(userPath, projectPath)
	for _, tool := range catalog.Tools {
		if tool.ID == "jq" {
			if tool.Note != "项目层" {
				t.Errorf("项目级应当覆盖用户级，实际 %q", tool.Note)
			}
			return
		}
	}
	t.Fatal("jq 不见了")
}

// 通配规则与导出：规则命中要能挂上能力；导出要能再读回来（往返）。
func TestCatalogRulesAndExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	if err := os.WriteFile(path, []byte(`{
	  "version": 1,
	  "capabilityRules": [ { "match": "web_*", "capabilities": ["web.search"] } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	SetActiveCatalog(LoadCatalog(path))
	defer SetActiveCatalog(DefaultCatalog())

	if !contains(MatchRules("web_search"), "web.search") {
		t.Error("通配规则没命中")
	}
	if contains(MatchRules("bash"), "web.search") {
		t.Error("不该命中无关工具")
	}

	text, err := ExportCatalog()
	if err != nil {
		t.Fatal(err)
	}
	roundTrip := filepath.Join(dir, "exported.json")
	if err := os.WriteFile(roundTrip, []byte(stripExportHeader(text)), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded := LoadCatalog(roundTrip)
	if len(reloaded.Issues) != 0 {
		t.Fatalf("导出的清单应当能被重新读入: %+v", reloaded.Issues)
	}
	if len(reloaded.Tools) != len(ActiveCatalog().Tools) {
		t.Errorf("往返后条数变了: %d → %d", len(ActiveCatalog().Tools), len(reloaded.Tools))
	}
}

// stripExportHeader 去掉导出文件开头的注释行（JSON 本身不含注释）。
func stripExportHeader(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ★ 带注释的导出文件必须能被重新读入。
//
// 我们自己生成的清单头部有 // 注释（讲解合并规则），而 JSON 标准不允许注释。
// 如果解析时不剥掉，用户"导出 → 改 → 重新加载"就会直接报 JSON 无效——
// 这是最自然的用法，不能坏。
func TestCatalogAcceptsOurExportedComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool-catalog.json")
	commented := "// ZeroA 工具清单：改完保存，点「重新加载」即时生效。\n" +
		"// 合并规则：同 id 覆盖字段。\n" +
		"\n" +
		"{\n" +
		"  \"version\": 1,\n" +
		"  \"tools\": [ { \"id\": \"jq\", \"note\": \"注释文件里的说明\" } ]   // 行尾注释\n" +
		"}\n"
	if err := os.WriteFile(path, []byte(commented), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(path)
	if len(catalog.Issues) != 0 {
		t.Fatalf("带注释的清单不该报错: %+v", catalog.Issues)
	}
	for _, tool := range catalog.Tools {
		if tool.ID == "jq" && tool.Note == "注释文件里的说明" {
			return
		}
	}
	t.Error("注释剥离后没能读到 jq 的覆盖内容")
}

// 字符串里的 // 不能被误当注释（URL 里到处都是）。
func TestStripJSONCommentsKeepsUrls(t *testing.T) {
	text := `{ "url": "http://127.0.0.1:13391/mcp" }`
	if got := stripJSONComments(text); !strings.Contains(got, "http://127.0.0.1:13391/mcp") {
		t.Errorf("URL 被截断了: %q", got)
	}
}

// ★ "工具明明装了却显示没有"的三个真实成因，每个都要有测试兜住。
//
// ① 无扩展名脚本：pip 装的 binwalk 在 Scripts 目录里没有后缀，
//
//	Windows 的 exec.LookPath 只认 PATHEXT（.exe/.cmd/.bat/.com）所以找不到；
//
// ② 工具箱自带目录：ffuf 在 E:\...\Code4Hack\tools\bin，不在 PATH 里；
// ③ 用户已经在 Pi 配置里写明路径：settings.json 的 shellPath 就是 bash 的真实位置，
//
//	再去猜 Program Files 必错（实测：Git 装在 E:\Git）。
func TestLookPathExtendedFindsExtensionlessScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "binwalk")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookPathExtended("binwalk", []string{dir}); got != script {
		t.Errorf("无扩展名脚本应当能找到，得到 %q", got)
	}
	if got := lookPathExtended("binwalk", nil); got != "" {
		// 万一开发机 PATH 里真有 binwalk，就跳过这条断言
		if !strings.Contains(strings.ToLower(got), "binwalk") {
			t.Errorf("没给搜索目录时不该找到本机没有的东西: %q", got)
		}
	}
	// 带后缀的也照常
	exePath := filepath.Join(dir, "tool.exe")
	if err := os.WriteFile(exePath, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookPathExtended("tool", []string{dir}); got != exePath {
		t.Errorf("带 .exe 后缀的应当能找到，得到 %q", got)
	}
}

// 清单里的 searchPaths 会参与查找（数据驱动，不是给某台机器打补丁）。
func TestCatalogSearchPathsDriveDetection(t *testing.T) {
	workspaceDir := t.TempDir()
	binDir := filepath.Join(workspaceDir, "toolbox", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(binDir, "my-ffuf")
	if err := os.WriteFile(toolPath, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(workspaceDir, "tool-catalog.json")
	if err := os.WriteFile(catalogPath, []byte(`{
	  "version": 1,
	  "searchPaths": ["`+strings.ReplaceAll(binDir, "\\", "\\\\")+`"],
	  "tools": [ { "id": "my-ffuf", "name": "我的 ffuf", "direction": "web",
	    "detect": { "commands": ["my-ffuf"] }, "capabilities": ["web.fuzz"] } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := LoadCatalog(catalogPath)
	if len(catalog.SearchPaths) == 0 {
		t.Fatal("searchPaths 没读进来")
	}
	SetActiveCatalog(catalog)
	defer SetActiveCatalog(DefaultCatalog())

	// ⚠️ 别用 Tools[0]：清单按"方向 + id"排序，下标 0 是内置的 7z 而不是新加的这条。
	var target Definition
	for _, tool := range catalog.Tools {
		if tool.ID == "my-ffuf" {
			target = tool
		}
	}
	if target.ID == "" {
		t.Fatal("自定义工具没进目录")
	}
	status := DetectOne(target)
	if !status.Available {
		t.Errorf("清单里配了搜索目录，应当能找到 %s（detail=%s）", toolPath, status.Detail)
	}
	if status.Detail != "在清单配置的目录中找到" {
		t.Errorf("来源说明要如实（不是 PATH 里找到的）: detail=%q path=%q searchPaths=%v inPath=%v",
			status.Detail, status.Path, catalog.SearchPaths, inPathDirs(status.Path))
	}
}

// settingsKey：路径直接取自 Pi 的 settings.json，不再靠猜安装位置。
func TestDetectReadsPathFromPiSettings(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	shell := filepath.Join(t.TempDir(), "bash.exe")
	if err := os.WriteFile(shell, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"shellPath": "` + strings.ReplaceAll(shell, "\\", "\\\\") + `"}`
	if err := os.WriteFile(filepath.Join(agentDirPath, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	def := Definition{
		ID: "bash", Name: "Bash", Type: KindCommand, Direction: "base",
		Detect: Detect{Paths: []string{"Z:\\definitely\\not\\here\\bash.exe"}, SettingsKey: "shellPath"},
	}
	status := DetectOne(def)
	if !status.Available || status.Path != shell {
		t.Errorf("应当从 settings.json 的 shellPath 拿到路径: %+v", status)
	}
	if !strings.Contains(status.Detail, "settings") && !strings.Contains(status.Detail, "ZeroA 配置") {
		t.Errorf("来源要说明取自 ZeroA 配置: %q", status.Detail)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
