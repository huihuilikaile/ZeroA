package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 角色存储：补种、往返、校验、损坏容错、路径穿越、拒删内置。
// 这些都是"配置类功能"最容易漏的地方——写坏了往往表现为"某个角色悄悄不见了"。
func TestRoleStoreRoundTripAndSeed(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	// 首次列出时补种内置角色（现在只内置 zeroa 一个，方向性角色由用户按需导入）
	service := &PiService{}
	result, err := service.ListRoles()
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	roles := decodeRoles(t, result)
	if len(roles) != 1 {
		t.Fatalf("应该补种 1 个内置角色（ZeroA 助手），实际 %d：%+v", len(roles), roles)
	}
	for _, role := range roles {
		if !role.Builtin {
			t.Errorf("内置角色应标记 builtin: %+v", role.Role)
		}
		if role.Broken != "" {
			t.Errorf("刚补种的角色不该是损坏状态: %+v", role)
		}
		if strings.TrimSpace(role.Prompt) == "" {
			t.Errorf("内置角色应该有提示词: %s", role.ID)
		}
	}

	// 再列一次不能重复补种（文件已存在）
	if again := decodeRoles(t, mustList(t, service)); len(again) != 1 {
		t.Errorf("重复补种了：%d", len(again))
	}

	// 用户改过提示词后，再次启动不能把改动冲掉
	edited := rolePayload{
		Role:   Role{ID: "zeroa", Name: "ZeroA（我改过）", Builtin: true, Tools: []string{"read", "bash"}},
		Prompt: "我的自定义提示词",
	}
	raw, _ := json.Marshal(edited)
	if _, err := service.SaveRole(string(raw)); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}
	mustList(t, service)
	for _, role := range decodeRoles(t, mustList(t, service)) {
		if role.ID != "zeroa" {
			continue
		}
		if role.Name != "ZeroA（我改过）" || role.Prompt != "我的自定义提示词" {
			t.Errorf("补种覆盖了用户的改动: %+v", role)
		}
		if len(role.Tools) != 2 {
			t.Errorf("工具白名单没存下来: %+v", role.Tools)
		}
	}
}

func TestRoleValidation(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}

	cases := []struct {
		name    string
		payload rolePayload
		wantErr string
	}{
		{
			name:    "id 不能带路径分隔符（路径穿越）",
			payload: rolePayload{Role: Role{ID: "../evil", Name: "x"}},
			wantErr: "角色 ID",
		},
		{
			name:    "id 不能是大写或空格",
			payload: rolePayload{Role: Role{ID: "My Role", Name: "x"}},
			wantErr: "角色 ID",
		},
		{
			name:    "名称不能为空",
			payload: rolePayload{Role: Role{ID: "ok", Name: "  "}},
			wantErr: "名称",
		},
		{
			name:    "思考级别必须是已知值",
			payload: rolePayload{Role: Role{ID: "ok", Name: "ok", ThinkingLevel: "turbo"}},
			wantErr: "思考级别",
		},
		{
			name:    "角色类型必须是已知值",
			payload: rolePayload{Role: Role{ID: "ok", Name: "ok", Kind: "robot"}},
			wantErr: "角色类型",
		},
		{
			name:    "工具白名单不能有空项",
			payload: rolePayload{Role: Role{ID: "ok", Name: "ok", Tools: []string{"read", "  "}}},
			wantErr: "工具",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.payload)
			_, err := service.SaveRole(string(raw))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("应该报错（含 %q），实际 err=%v", tc.wantErr, err)
			}
		})
	}

	// 提示词超长要挡在保存这一步，而不是等 Pi 启动失败
	long := rolePayload{Role: Role{ID: "long", Name: "长提示词"}, Prompt: strings.Repeat("啊", maxRolePromptBytes)}
	raw, _ := json.Marshal(long)
	if _, err := service.SaveRole(string(raw)); err == nil || !strings.Contains(err.Error(), "提示词太长") {
		t.Fatalf("超长提示词应该被拒绝，实际 err=%v", err)
	}
}

func TestRoleBrokenFileDoesNotBreakOthers(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}
	mustList(t, service) // 先补种

	// 手工放一个坏文件进去：不能因为它让整个角色列表失效
	broken := filepath.Join(agentDirPath, "roles", "broken.json")
	if err := os.WriteFile(broken, []byte("{ 这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	roles := decodeRoles(t, mustList(t, service))
	var brokenView *roleView
	for i := range roles {
		if roles[i].ID == "broken" {
			brokenView = &roles[i]
		}
	}
	if brokenView == nil {
		t.Fatal("损坏的角色应该仍然出现在列表里（标成损坏），而不是被静默丢掉")
	}
	if brokenView.Broken == "" {
		t.Error("损坏的角色必须带 Broken 说明，界面才能提示用户")
	}
	if len(roles) != 2 {
		t.Errorf("其他角色应不受影响（1 个内置 + 1 个损坏文件），实际 %d", len(roles))
	}
	// 损坏的角色不能被选中
	if _, err := service.SetActiveRole("broken"); err == nil {
		t.Error("损坏的角色不该能被设为当前角色")
	}
}

func TestRoleDeleteRules(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	mustList(t, service)

	// 内置角色不可删
	if _, err := service.DeleteRole("zeroa"); err == nil || !strings.Contains(err.Error(), "内置") {
		t.Fatalf("内置角色应拒删，实际 err=%v", err)
	}

	// 自建角色可以删
	raw, _ := json.Marshal(rolePayload{Role: Role{ID: "myrole", Name: "我的角色"}, Prompt: "提示词"})
	if _, err := service.SaveRole(string(raw)); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}
	if _, err := service.DeleteRole("myrole"); err != nil {
		t.Fatalf("自建角色应该能删: %v", err)
	}
	for _, role := range decodeRoles(t, mustList(t, service)) {
		if role.ID == "myrole" {
			t.Error("删掉的角色不该还在列表里")
		}
	}

	// 正在被工作区使用的角色不能删
	if _, err := service.SaveRole(string(raw)); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}
	service.mu.Lock()
	service.currentWorkspace = t.TempDir()
	service.mu.Unlock()
	if _, err := service.SetActiveRole("myrole"); err != nil {
		t.Fatalf("SetActiveRole: %v", err)
	}
	if _, err := service.DeleteRole("myrole"); err == nil || !strings.Contains(err.Error(), "使用") {
		t.Fatalf("正在使用的角色应拒删，实际 err=%v", err)
	}
}

// 角色 → 启动参数的编译：promptFile 是相对路径时落到角色目录内，
// 且必须挡住 ../ 这类越界写法（否则"任意文件当提示词"）。
//
// 环境清单的契约（2026-10 起）：**只有角色声明了能力才生成 env 清单文件**——
// 能力是授权语义，没勾选的一律不给（默认角色 zeroa 因此没有全机工具清单，
// 只用 Pi 自带能力）。声明了能力的角色才会拿到"这些能力在本机对应什么"的清单。
func TestRoleOptionsPromptPath(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	role := Role{ID: "ctf", Name: "CTF"}
	opts, err := roleOptions(role, true)
	if err != nil {
		t.Fatalf("roleOptions: %v", err)
	}
	// 没写提示词文件、也没声明能力：一个提示词文件都不该传——
	// 传读不到的文件会让 Pi 启动失败，而没声明能力的角色按约定没有环境清单。
	if len(opts.AppendSystemPromptFiles) != 0 {
		t.Errorf("无提示词、无能力的角色不该带任何追加提示词文件: %v", opts.AppendSystemPromptFiles)
	}

	// 正常路径：写进角色目录
	if err := saveRole(role, "你是 CTF 选手"); err != nil {
		t.Fatalf("saveRole: %v", err)
	}
	opts, err = roleOptions(role, true)
	if err != nil {
		t.Fatalf("roleOptions: %v", err)
	}
	want := filepath.Clean(filepath.Join(agentDirPath, "roles", "prompts", "ctf.md"))
	found := false
	for _, file := range opts.AppendSystemPromptFiles {
		if filepath.Clean(file) == want {
			found = true
		}
		if strings.HasSuffix(filepath.ToSlash(file), ".env.md") {
			t.Errorf("没声明能力的角色不该有环境清单文件: %v", opts.AppendSystemPromptFiles)
		}
	}
	if !found {
		t.Errorf("提示词路径不对：%v 里没有 %s", opts.AppendSystemPromptFiles, want)
	}

	// 越界路径必须被拒绝
	evil := Role{ID: "evil", Name: "evil", PromptFile: "../../../windows/win.ini"}
	if path := rolePromptFile(evil); path != "" {
		t.Errorf("越界提示词路径应该被拒绝，实际 %s", path)
	}
	// 越界的 promptFile 不能被读成提示词；这个角色同样没声明能力，不该有任何追加文件
	evilOpts, err := roleOptions(evil, true)
	if err != nil {
		t.Fatalf("roleOptions(越界角色): %v", err)
	}
	for _, file := range evilOpts.AppendSystemPromptFiles {
		lower := strings.ToLower(filepath.ToSlash(file))
		if strings.Contains(lower, "windows/win.ini") || strings.Contains(lower, "..") {
			t.Errorf("越界的提示词路径不该出现在启动参数里: %s", file)
		}
		if !strings.Contains(lower, "roles/prompts/") {
			t.Errorf("提示词文件只应该来自角色目录: %s", file)
		}
	}
}

// 能力页崩溃回归：角色声明的能力没人认领时，ResolveRole 的 JSON 里
// available/missing 必须是 [] 而不是 null——前端按数组读，null 会让整个页面崩掉
// （"Cannot read properties of null (reading 'length')"）。
func TestResolveRoleMarshalsEmptySlices(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	role := Role{ID: "ctf", Name: "CTF", Capabilities: []string{"nonexistent.capability"}}
	saveRoleForTest(t, &PiService{}, role, "你是 CTF 选手")

	result, err := (&PiService{}).ResolveRole("ctf")
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if !result.Success {
		t.Fatalf("ResolveRole 失败: %s", result.Error)
	}
	if strings.Contains(result.Data, `"available":null`) || strings.Contains(result.Data, `"missing":null`) {
		t.Errorf("编译预览里不能有 null 数组: %s", result.Data)
	}
	if !strings.Contains(result.Data, `"available":[]`) {
		t.Errorf("没人认领的能力要给出空数组: %s", result.Data)
	}
}

// 资源关闭（设置里关掉扩展/技能/模板）时，角色点名的技能与扩展必须被丢弃：
// 命令行上已经是 --no-skills，再点名技能会让 Pi 直接启动失败。
func TestRoleOptionsRespectsResourceSwitch(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	// 技能要真实存在：--skill 收的是路径，roleOptions 会把 id 解析成路径
	writeSkill(t, filepath.Join(agentDirPath, "skills"), "web", "web", "ctf-web", "Web 手册")
	role := Role{ID: "ctf", Name: "CTF", Skills: []string{"ctf-web"}, Extensions: []string{"web-tools"}}

	on, err := roleOptions(role, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(on.Skills) != 1 || len(on.Extensions) != 1 {
		t.Errorf("资源开启时应保留技能与扩展: %+v", on)
	}

	off, err := roleOptions(role, false)
	if err != nil {
		t.Fatal(err)
	}
	if off.Skills != nil || off.Extensions != nil {
		t.Errorf("资源关闭时不该传技能与扩展: %+v", off)
	}
}

// 白名单的"空数组 = 全禁"必须能活过一次写盘再读回来。
//
// 这里踩过一次真实的坑：给这几个字段加 omitempty 之后，`tools: []` 在保存时
// 被丢掉，读回来变成 nil（不限制）——用户看到的现象是"我明明关了工具，它还在用"。
func TestRoleEmptyWhitelistSurvivesRoundTrip(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	mustList(t, service)

	raw, _ := json.Marshal(rolePayload{
		Role:   Role{ID: "readonly", Name: "只读角色", Tools: []string{}, Skills: []string{}},
		Prompt: "不许改文件",
	})
	if _, err := service.SaveRole(string(raw)); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}

	view, ok := findRole("readonly")
	if !ok {
		t.Fatal("刚保存的角色应该能读回来")
	}
	if view.Tools == nil {
		t.Error("tools: [] 读回来不能变成 nil（那会让 --no-tools 失效）")
	}
	if len(view.Tools) != 0 {
		t.Errorf("tools 应该是空数组，实际 %v", view.Tools)
	}
	if view.Skills == nil || len(view.Skills) != 0 {
		t.Errorf("skills 也应该保持空数组，实际 %v", view.Skills)
	}

	// 反之：不写 tools 字段的角色读回来必须是 nil（不限制），不能被当成全禁
	plain, _ := json.Marshal(rolePayload{Role: Role{ID: "plain", Name: "默认角色"}})
	if _, err := service.SaveRole(string(plain)); err != nil {
		t.Fatalf("SaveRole: %v", err)
	}
	view, _ = findRole("plain")
	if view.Tools != nil {
		t.Errorf("没有声明 tools 的角色应该是不限制（nil），实际 %v", view.Tools)
	}

	// 线缆形状也要钉住：前端按 JSON 里的 null / [] 区分"不限制"与"全禁"。
	// 这里曾经出过一次线上事故——Go 发的是 null，前端只判了 undefined，
	// 于是 `null.length` 抛异常、整个界面变空白。
	listRaw := mustList(t, service).Data
	if !strings.Contains(listRaw, `"tools":[]`) {
		t.Errorf("全禁角色应该以 \"tools\":[] 出现在列表里: %s", truncate(listRaw, 400))
	}
	if !strings.Contains(listRaw, `"tools":null`) {
		t.Errorf("不限制的角色应该以 \"tools\":null 出现在列表里: %s", truncate(listRaw, 400))
	}
}

func mustList(t *testing.T, service *PiService) Result {
	t.Helper()
	result, err := service.ListRoles()
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	return result
}

func decodeRoles(t *testing.T, result Result) []roleView {
	t.Helper()
	var payload struct {
		Roles []roleView `json:"roles"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析角色列表失败: %v (%s)", err, result.Data)
	}
	return payload.Roles
}
