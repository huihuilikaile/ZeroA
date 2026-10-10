package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zeroa/internal/permission"
	toolkit "zeroa/internal/tools"
)

// ---- P7 工具注册表：检测、缓存、能力标签 ----

func TestToolRegistryDetection(t *testing.T) {
	statuses := toolkit.DetectAll(true)
	if len(statuses) < 11 {
		t.Fatalf("注册表至少要有通用打底清单的 11 条工具，实际 %d", len(statuses))
	}
	/**
	 * 每条都必须有 id/name/type；能力标签这条只对**出厂默认清单**要求。
	 *
	 * 为什么放宽：清单现在是可写的——用户或 AI（catalog_add）登记的工具有没有能力标签
	 * 是自由的（能力是可选声明）。对默认清单仍然严格要求，因为那是我们自己的质量线。
	 * 另外这条断言必须用默认清单而不是"当前生效清单"：后者会被别的测试/用户改动。
	 */
	defaultIDs := map[string]bool{}
	for _, tool := range toolkit.DefaultCatalog().Tools {
		defaultIDs[tool.ID] = true
	}
	for _, status := range statuses {
		if status.ID == "" || status.Name == "" || status.Type == "" {
			t.Errorf("工具定义不完整: %+v", status.Definition)
		}
		if len(status.Capabilities) == 0 && defaultIDs[status.ID] {
			t.Errorf("默认清单里的 %s 没有能力标签，能力解析器就永远匹配不到它", status.ID)
		}
		if status.CheckedAt.IsZero() {
			t.Errorf("工具 %s 没有检测时间", status.ID)
		}
		if status.Available && status.Path == "" && status.Type != toolkit.KindPythonPackage {
			t.Errorf("工具 %s 标记可用但没有路径", status.ID)
		}
	}
	// 空声明 = 不放行：注册表里不该出现空能力名
	for _, capability := range toolkit.KnownCapabilities() {
		if strings.TrimSpace(capability) == "" {
			t.Error("能力清单里出现了空字符串")
		}
	}
	// Python 包这类"环境工具"必须能被单独识别出来（它们不该注册成 LLM 工具）
	var pythonPackages int
	for _, status := range statuses {
		if status.Type == toolkit.KindPythonPackage {
			pythonPackages++
		}
	}
	if pythonPackages == 0 {
		t.Error("注册表里应该有 Python 包类型的工具（默认清单里的 requests）")
	}
}

func TestToolDetectionCached(t *testing.T) {
	first := toolkit.DetectAll(true)
	second := toolkit.DetectAll(false)
	if len(first) != len(second) {
		t.Fatalf("缓存前后数量不一致: %d vs %d", len(first), len(second))
	}
	// 缓存命中时 CheckedAt 应该是同一批（不去重跑检测）
	if !first[0].CheckedAt.Equal(second[0].CheckedAt) {
		t.Error("第二次检测应该命中缓存（时间戳一致）")
	}
	toolkit.ResetCache()
	third := toolkit.DetectAll(false)
	if len(third) != len(first) {
		t.Error("清缓存后数量不该变")
	}
}

// ---- P8 能力解析：能力 → 工具，缺的要点名 ----

func TestCapabilityResolution(t *testing.T) {
	resolutions, env := toolkit.Resolve([]string{"filesystem.search", "reverse.static", "不可能存在的能力"})
	if len(resolutions) != 3 {
		t.Fatalf("三个能力都应该有解析结果: %+v", resolutions)
	}
	var unknown, known *toolkit.Resolution
	for i := range resolutions {
		switch resolutions[i].Capability {
		case "不可能存在的能力":
			unknown = &resolutions[i]
		case "filesystem.search":
			known = &resolutions[i]
		}
	}
	if unknown == nil || len(unknown.Available) != 0 {
		t.Errorf("不认识的能力应该解析为空可用: %+v", unknown)
	}
	if env.Missing == nil || !containsString(env.Missing, "不可能存在的能力") {
		t.Errorf("缺失能力要进 Environment.Missing（界面与提示词都要说明）: %+v", env.Missing)
	}
	// ripgrep 不一定装了，所以只断言"解析结构正确"：
	// 有可用就给 available，没有就给 missing，不能两边都空
	if known != nil && len(known.Available) == 0 && len(known.Missing) == 0 {
		t.Errorf("一个注册表里存在的能力，必须落在可用或缺失的一边: %+v", known)
	}
}

func TestEnvironmentPromptMentionsTools(t *testing.T) {
	env := toolkit.Environment{
		Commands:       map[string]string{"python": `C:\Python\python.exe`, "curl": `C:\curl.exe`},
		PythonPackages: []string{"PyCryptodome"},
		Missing:        []string{"forensics.memory"},
	}
	text := toolkit.EnvironmentPrompt(env, nil)
	for _, must := range []string{"python", "curl", "PyCryptodome", "bash"} {
		if !strings.Contains(text, must) {
			t.Errorf("环境说明里应该提到 %q：\n%s", must, text)
		}
	}
	// 关键：要说清"这些不是工具接口，而是用 bash 调用的程序"，
	// 否则模型会以为有个叫 python 的工具
	if !strings.Contains(text, "不是你的工具接口") {
		t.Error("环境说明必须区分'环境工具'与'Agent 可调用工具'（方案第十一节）")
	}
}

// ---- P10 权限引擎：四层取交集、临时授权、硬 deny ----

func TestPermissionIntersection(t *testing.T) {
	engine := permission.Engine{
		Global: permission.Policy{permission.ActionNetworkPublic: permission.Allow},
		User:   permission.Policy{permission.ActionNetworkPublic: permission.Ask},
		Role:   permission.Policy{permission.ActionNetworkPublic: permission.Allow},
	}
	now := time.Now()
	if got := engine.Effective(permission.ActionNetworkPublic, now); got != permission.Ask {
		t.Errorf("四层取最严：global=allow + user=ask 应该是 ask，实际 %s", got)
	}
	// 缺声明 = deny（安全默认）：忘了配就是不允许
	if got := engine.Effective(permission.ActionShellExecute, now); got != permission.Deny {
		t.Errorf("没有声明的动作必须是 deny，实际 %s", got)
	}
	// 任一层 deny 就是 deny
	engine.Session = permission.Policy{permission.ActionNetworkPublic: permission.Deny}
	if got := engine.Effective(permission.ActionNetworkPublic, now); got != permission.Deny {
		t.Errorf("会话层 deny 应该压过其他层，实际 %s", got)
	}
}

// 快照里"没表态的层"必须报成 abstain，不能报成 deny。
//
// 界面上按快照画"角色 X · 用户 Y · 全局 Z"。早前 valueOr 把缺省值填成 Deny，
// 于是出现「角色 禁止 · 用户 禁止 · 全局 允许 → 最终 允许」这种自相矛盾的显示——
// 用户会以为权限算错了，其实只是那两层压根没提这件事（弃权，不参与取严）。
func TestPermissionSnapshotMarksAbstain(t *testing.T) {
	engine := permission.Engine{
		Global: permission.Policy{
			permission.ActionFilesystemRead: permission.Allow,
		},
	}
	now := time.Now()
	var readSnapshot, shellSnapshot *permission.Snapshot
	for _, snapshot := range engine.Evaluate(now) {
		copy := snapshot
		switch snapshot.Action {
		case permission.ActionFilesystemRead:
			readSnapshot = &copy
		case permission.ActionShellExecute:
			shellSnapshot = &copy
		}
	}
	if readSnapshot == nil || shellSnapshot == nil {
		t.Fatal("快照缺少预期动作")
	}
	if readSnapshot.Global != permission.Allow {
		t.Errorf("全局层显式声明应当如实显示：%s", readSnapshot.Global)
	}
	for name, got := range map[string]permission.Decision{
		"角色": readSnapshot.Role,
		"用户": readSnapshot.User,
		"会话": readSnapshot.Session,
	} {
		if got != permission.DecisionAbstain {
			t.Errorf("没表态的%s层应当显示为 abstain（未声明），实际 %s", name, got)
		}
	}
	// 最终值仍按"都没表态 = 安全默认 deny"来
	if readSnapshot.Decision != permission.Allow {
		t.Errorf("只有全局允许时最终应为 allow，实际 %s", readSnapshot.Decision)
	}
	if shellSnapshot.Decision != permission.Deny {
		t.Errorf("无人表态的动作最终应为 deny，实际 %s", shellSnapshot.Decision)
	}
	if shellSnapshot.Global != permission.DecisionAbstain {
		t.Errorf("全局层没提 shell，应显示未声明，实际 %s", shellSnapshot.Global)
	}
}

func TestPermissionTemporaryAuthorization(t *testing.T) {
	now := time.Now()
	engine := permission.Engine{
		Global: permission.Policy{permission.ActionShellExecute: permission.Ask},
	}
	if got := engine.Effective(permission.ActionShellExecute, now); got != permission.Ask {
		t.Fatalf("初始应为 ask，实际 %s", got)
	}
	engine.Authorize(permission.ActionShellExecute, now.Add(10*time.Minute), "比赛授权", "用户")
	if got := engine.Effective(permission.ActionShellExecute, now); got != permission.Allow {
		t.Errorf("临时授权期间应该是 allow，实际 %s", got)
	}
	// 过期后自动回到原策略（不是回到 deny/allow，而是回到那一层的值）
	if got := engine.Effective(permission.ActionShellExecute, now.Add(11*time.Minute)); got != permission.Ask {
		t.Errorf("临时授权过期后应回到 ask，实际 %s", got)
	}
	// 撤销
	engine.Authorize(permission.ActionShellExecute, now.Add(time.Hour), "", "用户")
	engine.Revoke(permission.ActionShellExecute)
	if got := engine.Effective(permission.ActionShellExecute, now); got != permission.Ask {
		t.Errorf("撤销后应回到 ask，实际 %s", got)
	}
}

// deny 是硬边界：临时授权不能绕过它（方案第十七节的安全含义）
func TestPermissionDenyIsHardBoundary(t *testing.T) {
	now := time.Now()
	engine := permission.Engine{
		Global: permission.Policy{permission.ActionNetworkTarget: permission.Deny},
	}
	engine.Authorize(permission.ActionNetworkTarget, now.Add(time.Hour), "试图绕过", "用户")
	if got := engine.Effective(permission.ActionNetworkTarget, now); got != permission.Deny {
		t.Errorf("全局 deny 不该被临时授权突破，实际 %s", got)
	}
}

// 权限 → 禁用哪些 Pi 工具：这是当前架构下唯一真正能强制的地方
func TestPermissionToToolDenyList(t *testing.T) {
	decisions := map[permission.Action]permission.Decision{
		permission.ActionFilesystemRead:  permission.Deny,
		permission.ActionFilesystemWrite: permission.Deny,
		permission.ActionShellExecute:    permission.Deny,
		permission.ActionNetworkPublic:   permission.Allow,
	}
	denied := permission.ToolDenyList(func(action permission.Action) permission.Decision {
		if value, ok := decisions[action]; ok {
			return value
		}
		return permission.Deny
	})
	for _, must := range []string{"read", "write", "edit", "bash", "powershell", "grep"} {
		if !containsString(denied, must) {
			t.Errorf("deny 时应该禁用工具 %s，实际 %v", must, denied)
		}
	}
	if containsString(denied, "web_search") || containsString(denied, "web_fetch") {
		t.Errorf("network.public=allow 时不该禁用联网工具: %v", denied)
	}
	// 未知动作按 deny：返回的应该是尽量收紧的清单，而不是空
	if len(denied) == 0 {
		t.Error("全部未知动作时也该有禁用列表（安全默认）")
	}
}

// ---- 权限策略的落盘与恢复 ----

func TestPermissionPolicyPersistence(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	policies.setGlobal(permission.Policy{
		permission.ActionFilesystemRead: permission.Allow,
		permission.ActionShellExecute:   permission.Deny,
	})
	policies.setUser(permission.Policy{permission.ActionShellExecute: permission.Ask})
	policies.authorize(permission.ActionNetworkPublic, time.Now().Add(time.Hour), "测试", "用户")
	if err := savePoliciesToState(); err != nil {
		t.Fatalf("savePoliciesToState: %v", err)
	}

	// 模拟重启：清空内存再从文件读回来
	policies.setGlobal(permission.DefaultGlobalPolicy())
	policies.setUser(permission.Policy{})
	policies.mu.Lock()
	policies.Temp = nil
	policies.mu.Unlock()
	loadPoliciesFromState()

	global, user, temp := policies.snapshot()
	if global[permission.ActionShellExecute] != permission.Deny {
		t.Errorf("全局策略没恢复: %+v", global)
	}
	if user[permission.ActionShellExecute] != permission.Ask {
		t.Errorf("用户策略没恢复: %+v", user)
	}
	if len(temp) != 1 || temp[0].Action != permission.ActionNetworkPublic {
		t.Errorf("临时授权没恢复: %+v", temp)
	}
}

// ---- P11 任务循环 ----

func TestTaskLoopDefaultsToDoingNothing(t *testing.T) {
	loop := newTaskLoop()
	loop.SetPolicy(LoopPolicy{}) // 角色没配 loop
	loop.Start()
	for i := 0; i < 5; i++ {
		if decision := loop.Settle(time.Now()); decision.Action != "continue" {
			t.Fatalf("没有 loop 策略时永远应该是 continue，实际 %+v", decision)
		}
	}
	if loop.IsPaused() {
		t.Error("没有策略时不该暂停")
	}
}

func TestTaskLoopRoundLimit(t *testing.T) {
	loop := newTaskLoop()
	loop.SetPolicy(LoopPolicy{MaxRounds: 2, OnLimit: "summarize"})
	loop.Start()
	if decision := loop.Settle(time.Now()); decision.Action != "continue" {
		t.Fatalf("第 1 轮应该继续: %+v", decision)
	}
	if decision := loop.Settle(time.Now()); decision.Action != "continue" {
		t.Fatalf("第 2 轮应该继续: %+v", decision)
	}
	decision := loop.Settle(time.Now())
	if decision.Action != "summarize" {
		t.Fatalf("第 3 轮超过上限，应该按 OnLimit=summarize 处理: %+v", decision)
	}
	if !strings.Contains(decision.Message, "总结") {
		t.Errorf("summarize 要带一条让 Pi 总结的消息: %+v", decision)
	}
	if !loop.IsPaused() {
		t.Error("到限之后应该暂停，避免无限继续")
	}
	// 暂停后即使再 settled 也不该有动作（Manual Abort 的语义）
	if decision := loop.Settle(time.Now()); decision.Action != "stop" {
		t.Errorf("暂停后应该保持 stop: %+v", decision)
	}
}

func TestTaskLoopDurationLimit(t *testing.T) {
	loop := newTaskLoop()
	loop.SetPolicy(LoopPolicy{MaxDurationSeconds: 1, OnLimit: "stop"})
	loop.Start()
	// 把开始时间往前挪，模拟"跑了很久"
	loop.mu.Lock()
	loop.started = time.Now().Add(-2 * time.Second)
	loop.mu.Unlock()
	decision := loop.Settle(time.Now())
	if decision.Action != "stop" || !strings.Contains(decision.Reason, "时长上限") {
		t.Fatalf("超过时长上限应该按 OnLimit=stop: %+v", decision)
	}
}

func TestTaskLoopStuckDetection(t *testing.T) {
	loop := newTaskLoop()
	loop.SetPolicy(LoopPolicy{
		MaxRounds: 50,
		Stuck:     &StuckPolicy{Enabled: true, Threshold: 3, Window: 3},
		OnStuck:   "replan",
	})
	loop.Start()
	// 前两次不同：不该判定卡住
	loop.ObserveTool("bash", map[string]any{"command": "ls"})
	loop.ObserveTool("bash", map[string]any{"command": "pwd"})
	if decision := loop.Settle(time.Now()); decision.Action != "continue" {
		t.Fatalf("还在换方法，不该判卡住: %+v", decision)
	}
	// 连着三次同一个命令：该判卡住了
	for i := 0; i < 3; i++ {
		loop.ObserveTool("bash", map[string]any{"command": "python solve.py"})
	}
	decision := loop.Settle(time.Now())
	if decision.Action != "replan" {
		t.Fatalf("重复三次应该 replan: %+v", decision)
	}
	if !strings.Contains(decision.Message, "卡") {
		t.Errorf("replan 消息要让模型解释卡点: %q", decision.Message)
	}
}

func TestTaskLoopStuckCanStop(t *testing.T) {
	loop := newTaskLoop()
	loop.SetPolicy(LoopPolicy{
		Stuck:   &StuckPolicy{Enabled: true, Threshold: 2, Window: 2},
		OnStuck: "stop",
	})
	loop.Start()
	for i := 0; i < 2; i++ {
		loop.ObserveTool("read", map[string]any{"path": "a.txt"})
	}
	decision := loop.Settle(time.Now())
	if decision.Action != "stop" {
		t.Fatalf("OnStuck=stop 时应该停下来: %+v", decision)
	}
	if !loop.IsPaused() {
		t.Error("stop 之后要暂停")
	}
}

// 参数里带时间戳/临时路径时不该误判成"重复"（真实场景里这很常见）
func TestToolSignatureIgnoresTailDifference(t *testing.T) {
	a := toolkitSignature("bash", map[string]any{"command": strings.Repeat("x", 300) + "AAA"})
	b := toolkitSignature("bash", map[string]any{"command": strings.Repeat("x", 300) + "BBB"})
	if a != b {
		t.Error("只差 200 字符之后的内容不该影响签名（否则永远判不出重复）")
	}
	c := toolkitSignature("read", map[string]any{"command": strings.Repeat("x", 300) + "AAA"})
	if a == c {
		t.Error("不同工具的签名必须不同")
	}
}

// ---- 编码器：角色 → 编译产物 ----

func TestCompileRoleProducesEffectiveConfig(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	writeSkill(t, filepath.Join(agentDirPath, "skills"), "web", "web", "ctf-web", "Web 手册")

	// 全局策略：一律要问（用户设的），角色想放开 shell —— 交集应该还是 ask
	policies.setGlobal(permission.Policy{
		permission.ActionFilesystemRead:  permission.Allow,
		permission.ActionShellExecute:    permission.Ask,
		permission.ActionNetworkPublic:   permission.Deny,
		permission.ActionFilesystemWrite: permission.Ask,
	})
	defer policies.setGlobal(permission.DefaultGlobalPolicy())

	role := Role{
		ID: "ctf", Name: "CTF",
		Skills:       []string{"ctf-web"},
		Capabilities: []string{"reverse.static", "web.search"},
		Permissions:  permission.Policy{permission.ActionShellExecute: permission.Allow},
		Loop:         LoopPolicy{MaxRounds: 5, OnLimit: "summarize"},
	}
	// ResolveRole 是从磁盘上的角色开始编译的，所以先把它存下来
	saveRoleForTest(t, &PiService{}, role, "你是逆向分析专家")

	compiled, err := compileRole(role, true)
	if err != nil {
		t.Fatalf("compileRole: %v", err)
	}
	if compiled.RoleID != "ctf" {
		t.Errorf("编译产物要带角色 id: %+v", compiled)
	}
	if len(compiled.Spawn.Skills) != 1 {
		t.Errorf("技能应该解析成路径: %+v", compiled.Spawn.Skills)
	}
	if len(compiled.Spawn.AppendSystemPrompt) == 0 {
		t.Error("角色提示词应该进 AppendSystemPrompt")
	}
	// 网络被全局 deny：必须体现为"禁用联网工具"
	if !containsString(compiled.Spawn.ExcludeTools, "web_search") {
		t.Errorf("global deny network.public 时应该禁用联网工具: %v", compiled.Spawn.ExcludeTools)
	}
	// 交集检查：角色想 allow shell，但全局是 ask → 最终 ask（不禁用 bash）
	decisions := permission.Engine{Global: mustPolicy(t), Role: role.Permissions}
	if got := decisions.Effective(permission.ActionShellExecute, time.Now()); got != permission.Ask {
		t.Errorf("角色 allow 不该突破全局 ask，实际 %s", got)
	}
	if containsString(compiled.Spawn.ExcludeTools, "bash") {
		t.Error("ask 不该禁用 bash（ask 是记录+提示，不是拦截）")
	}
	if compiled.Loop.MaxRounds != 5 {
		t.Errorf("循环策略要传下来: %+v", compiled.Loop)
	}
	// 缺能力要有 warning（web.search 只由扩展提供，注册表里没有）
	if len(compiled.Warnings) == 0 {
		t.Error("角色要了本机没有的能力时，必须给出提醒")
	}
	// 编译产物能被预览（界面上的 Effective Configuration 走这个）
	service := &PiService{}
	result, err := service.ResolveRole("ctf")
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	var payload struct {
		Effective map[string]any `json:"effective"`
		Warnings  []string       `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Effective == nil {
		t.Error("编译预览必须包含 effective 段（实际生效的值，不是角色文件里写的）")
	}
}

func mustPolicy(t *testing.T) permission.Policy {
	t.Helper()
	global, _, _ := policies.snapshot()
	return global
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// toolkitSignature 直接复用生产实现（避免测试里另写一份算法而漂移）
func toolkitSignature(name string, args any) string { return toolSignature(name, args) }

var _ = os.Getenv
