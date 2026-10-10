package roles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"zeroa/internal/permission"
	"regexp"
	"sort"
	"strings"

	"zeroa/internal/pi"
)

// 角色（Role）= 一份 Agent Profile：提示词 + 模型 + 工具 + 技能 + 上下文策略。
//
// 为什么是"配置 + 编译"而不是新机制：Pi 的 RPC **没有** set_system_prompt /
// set_tools / set_skills（见 pi 的 docs/rpc-commands.md），提示词与工具集只能在
// 启动时通过命令行参数确定。所以角色不是一个运行时可变的开关，而是
// "重启 Pi 时带哪一套参数"的声明；能热切的只有模型与思考级别。
//
// 由此推出两条必须遵守的规则：
//  1. 切换角色 = 重启 Pi 进程 + 新开会话（当前会话保留在历史里）；
//  2. 角色"是否真的生效"必须可验证——Pi 会把本次启动的提示词分段与工具清单
//     写进会话的首条 system 消息（见 pi 的 docs/session-format.md），
//     我们照抄这个形状，在模拟器里也能断言。

const (
	// 角色 id 同时是文件名，必须严格限制，避免 ../ 之类的路径穿越
	roleIDPattern = `^[a-z0-9][a-z0-9_-]{0,31}$`
	// 提示词上限：Windows 命令行长度有限（--system-prompt 是内联文本），
	// 超过就明确报错，而不是让 Pi 以一个看不懂的启动失败收场。
	MaxPromptBytes = 24 << 10
	// 保存格式版本：将来改结构时用它做迁移
	roleFormatVersion = 1
)

// 角色类型（Role.Kind）。留空等同 KindRole：老配置文件没有 kind 字段，
// 语义必须不变——它们本来就能当主会话角色。
const (
	// KindRole 是普通角色：输入框可选、能当主会话角色
	KindRole = "role"
	// KindAgent 是子智能体：只能被别的角色委派（delegation.allowedRoles），
	// 不出现在输入框的角色选择器里，也不能设成主会话角色。
	//
	// 为什么子智能体要独立成一类而不是让普通角色兼任：兼任时"能派谁"的名单
	// 和"人能选谁"的名单是同一份——专为委派调的子角色（只读、窄提示词）会
	// 混进输入框，人误选它当主角色；反过来给人用的角色也能被模型派出去。
	// 分两类之后两份名单各管各的，互不串味。
	KindAgent = "agent"
)

var roleIDRe = regexp.MustCompile(roleIDPattern)

// Role 是角色配置的磁盘形状（roles/<id>.json）。
//
// 字段分成三类：
//   - 身份：ID / 名称 / 描述 / 图标
//   - 能力：模型、思考级别、作用域模型、工具、技能、扩展、提示模板、上下文文件
//   - 提示词：promptFile（单独一个 .md，便于长文本编辑与版本管理）
type Role struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	// Kind：role（默认，普通角色）| agent（子智能体，只能被委派）。语义见 KindAgent。
	Kind string `json:"kind,omitempty"`
	// Builtin 为 true 表示这是随程序分发的角色（可编辑、不可删）
	Builtin bool `json:"builtin,omitempty"`
	// SeedVersion 用于"只补种、不覆盖"：文件存在时升级不动用户改过的内容
	SeedVersion int `json:"seedVersion,omitempty"`

	PromptFile string `json:"promptFile,omitempty"`
	// ReplaceSystemPrompt 为 true 时用 --system-prompt 整体替换 Pi 的编码助手提示；
	// 默认 false = 用 --append-system-prompt 追加（推荐：保留基础编码能力）。
	ReplaceSystemPrompt bool `json:"replaceSystemPrompt,omitempty"`

	Model         string   `json:"model,omitempty"`
	ThinkingLevel string   `json:"thinkingLevel,omitempty"`
	ScopedModels  []string `json:"scopedModels,omitempty"`

	// AutoCompaction 是"上下文快满时自动压缩"的角色级开关。
	//
	// nil = 跟随全局设置；true/false = 这个角色强制开关。
	// 它走 RPC（set_auto_compaction）而不是启动参数，所以切换角色后**不需要**
	// 额外重启就能改：方向不同对上下文的要求差别很大（取证要留原始证据、
	// CTF 解题可以早压缩），让角色自己说了算。
	AutoCompaction *bool `json:"autoCompaction,omitempty"`
	// CompactionInstructions 是手动压缩时默认带上的一段话（compact 的
	// customInstructions）：比如"保留 flag 与利用链，压缩掉试探过程"。
	CompactionInstructions string `json:"compactionInstructions,omitempty"`
	// McpServers 记录这个角色想用哪些 MCP 服务器（nil = 全部）。
	//
	// ⚠️ 目前只记录与展示，**不参与启动参数**：Pi 的 MCP 服务器来自全局
	// mcp.json，没有"只加载这几个"的命令行开关（见 docs/mcp.md）。
	// 要真正按角色隔离，得等 Pi 侧提供选择能力，或者我们去改写 mcp.json——
	// 后者会在切换角色时动用户的全局配置，代价太大，先不做。
	McpServers []string `json:"mcpServers,omitempty"`

	// ---- 方案第四节 RoleDefinition 的其余几段 ----

	// Category 是角色分组（ctf / forensics / ir），界面按它归组
	Category string `json:"category,omitempty"`
	// Capabilities 是角色要的**能力**，不是工具（方案第八节）。
	// 由 CapabilityResolver 解析成本机实际存在的工具；缺什么会明确告诉用户。
	Capabilities []string `json:"capabilities,omitempty"`
	// Permissions 是这个角色声明的权限策略（方案第十六节）。
	// 与全局/用户/会话策略取交集后才是最终生效值。
	Permissions permission.Policy `json:"permissions,omitempty"`
	// Loop 是任务循环策略（方案第十九节）
	Loop LoopPolicy `json:"loop,omitempty"`

	// nil = 不限制；[] = 全禁；非空 = 白名单（语义见 internal/pi 的 resourceArgs）。
	//
	// ⚠️ 这四个字段**不能加 omitempty**：那样"空数组（全禁）"会在写盘时被丢掉，
	// 读回来变成 nil（不限制）——用户看到的现象是"我明明关掉了工具，它还在用"。
	// 代价是配置文件里会出现 `"tools": null`，这是有意的：null = 不限制、
	// [] = 一个都不要，两种语义都必须能原样活过一次读写。
	Tools             []string `json:"tools"`
	Skills            []string `json:"skills"`
	Extensions        []string `json:"extensions"`
	PromptTemplates   []string `json:"promptTemplates"`
	ExcludeTools      []string `json:"excludeTools,omitempty"`
	ContextFiles      *bool    `json:"contextFiles,omitempty"`
	SessionNamePrefix string   `json:"sessionNamePrefix,omitempty"`
	FormatVersion     int      `json:"formatVersion,omitempty"`
	// Delegation 是这个角色"允许怎么派子智能体"的策略。
	//
	// 为什么放在角色里而不是全局设置：方向不同要求完全不同——
	// CTF 综合角色需要派人查资料/试路线，取证角色可能根本不该派人。
	// nil = 不允许委派（安全默认：没写就是不能派）。
	Delegation *Delegation `json:"delegation,omitempty"`
}

// Delegation 描述"这个角色能派什么样的子智能体"。
//
// 它是**带外策略**：不放进工具参数，模型改不了。真正的上限还要与会话授权取交集
// （见 docs/05-子智能体方案.md 第 9 节）。
type Delegation struct {
	// Enabled=false 时这个角色完全不能派子智能体
	Enabled bool `json:"enabled"`
	// Mode：background（父继续干活，默认）| blocking（等结果）
	//
	// ⚠️ blocking 暂未实现：无论写什么，delegate_task 都是受理后立刻返回。
	// 写 blocking 只会在编译时得到一条警告，别把它当生效配置用。
	Mode string `json:"mode,omitempty"`
	// RequireAudit=true 时，父会话至少调过一次工具才允许派人（硬代理条件）
	RequireAudit bool `json:"requireAudit,omitempty"`
	// DeliverOn：turn_end（回合结束后合并投递，默认）| checkpoint（立刻插话）
	DeliverOn string `json:"deliverOn,omitempty"`
	// AllowedRoles 是白名单；空 = 不允许派任何角色
	AllowedRoles []string `json:"allowedRoles,omitempty"`
	// MaxChildren / MaxParallel 是预算（0 = 用默认值；按"这条会话"计）
	MaxChildren int `json:"maxChildren,omitempty"`
	MaxParallel int `json:"maxParallel,omitempty"`
	/**
	 * StopOnFlag：主 Agent 一旦给出候选 flag，就停掉还在跑的子智能体。
	 *
	 * 为什么用 flag 而不是"回合结束"：Pi 每个回合结束都会发 agent_settled，
	 * 而"派完活"本身就意味着父的回合结束了——按回合结束杀子 Agent 等于
	 * 委派白派。只有"已经拿到答案"才真的不需要它们了。
	 * nil 视为 true（CTF 方向默认就该这样）。
	 */
	StopOnFlag *bool `json:"stopOnFlag,omitempty"`
	// MaxDurationSeconds 是单个子智能体的时限（0 = 不限时）
	MaxDurationSeconds int `json:"maxDurationSeconds,omitempty"`
	// IdleTimeoutSeconds 是可继续子智能体"空闲多久后被回收"（0 = 用默认 10 分钟）
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds,omitempty"`
	/**
	 * Lifecycle 决定子智能体的形态（仿 DSH 的 one-shot / continuable）：
	 *
	 *   - continuable（默认）：跑完一轮**不释放进程**，进入空闲；可以继续给它发消息
	 *     （空闲时开新一轮），也可以只中断它当前那一轮。空闲超过 IdleTimeoutSeconds
	 *     （默认 10 分钟）自动回收。
	 *   - one-shot：跑完一轮就结算并释放进程，之后只读——省进程、省内存，
	 *     适合"派出去查个东西、拿结论就走"的一次性任务。
	 *
	 * 为什么要留一个开关：可继续形态每个子智能体都要占一份进程内存（百 MB 级），
	 * 批量侦察那种"派 4 个各查一件事"的场景用一次性更划算。
	 */
	Lifecycle string `json:"lifecycle,omitempty"`
	// Inherit 声明子智能体继承父的什么（留空 = 继承全部）
	//
	// ⚠️ 暂未实现：目前子智能体完全按**子角色自己的定义**启动（模型缺省沿用父模型），
	// 这个字段写什么都一样。保存时会给编译警告，别把它当生效配置用。
	Inherit *DelegationInherit `json:"inherit,omitempty"`
}

// DelegationInherit 声明继承项。留空 = 继承全部；显式空数组 = 完全不继承。
type DelegationInherit struct {
	Skills     []string `json:"skills,omitempty"`
	Mcp        []string `json:"mcp,omitempty"`
	Extensions string   `json:"extensions,omitempty"` // "all" | "none"
	Model      string   `json:"model,omitempty"`      // "own"（子角色自己的）| "inherit"
}

// LoopPolicy 是角色的循环策略（对应方案 role.yaml 的 loop 段）。
type LoopPolicy struct {
	// MaxRounds 是"最多让 Pi 跑几轮"（一轮 = 一次 agent_settled）。
	// 0 表示不限制。
	MaxRounds int `json:"maxRounds,omitempty"`
	// MaxDurationSeconds 是墙钟上限。0 表示不限制。
	MaxDurationSeconds int `json:"maxDurationSeconds,omitempty"`
	// Stuck 是卡住检测（同一组工具调用反复出现）。
	Stuck *StuckPolicy `json:"stuck,omitempty"`
	// OnStuck：replan（发一条"换个思路"的 follow-up）或 stop（停下来告诉用户）
	OnStuck string `json:"onStuck,omitempty"`
	// OnLimit：summarize（让 Pi 先总结再停）或 stop
	OnLimit string `json:"onLimit,omitempty"`
}

// StuckPolicy 描述"怎么算卡住"。
type StuckPolicy struct {
	Enabled   bool `json:"enabled"`
	Threshold int  `json:"threshold,omitempty"`
	// Window 是参与比较的最近工具调用条数（默认 6）
	Window int `json:"window,omitempty"`
}

// View 是给界面看的角色（附带提示词正文，省一次往返）。
type View struct {
	Role
	Prompt string `json:"prompt"`
	// Broken 非空表示这个角色的文件读不了/不合法，界面要标出来而不是静默丢掉
	Broken string `json:"broken,omitempty"`
}

// Dir 是角色目录：<agentDir>/roles
func Dir() string {
	dir := host.AgentDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "roles")
}

func PromptPath(id string) string {
	return filepath.Join(Dir(), "prompts", id+".md")
}

// RequireID 校验 id 并返回它，挡住路径穿越与大小写混乱。
func RequireID(id string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(id))
	if !roleIDRe.MatchString(trimmed) {
		return "", fmt.Errorf("角色 ID 只能用 1-32 位小写字母、数字、下划线和短横线：%q", id)
	}
	return trimmed, nil
}

// Validate 做保存前的完整校验。
//
// 校验原则：能在保存时挡住的，就不要留给 Pi 在启动时炸——
// "角色保存了但一启动就失败"是最难排查的一类问题。
func Validate(role Role) error {
	if _, err := RequireID(role.ID); err != nil {
		return err
	}
	if strings.TrimSpace(role.Name) == "" {
		return errors.New("角色名称不能为空")
	}
	switch strings.ToLower(strings.TrimSpace(role.Kind)) {
	case "", KindRole, KindAgent:
	default:
		return fmt.Errorf("未知的角色类型 %q（可用：role/agent；留空 = 普通角色）", role.Kind)
	}
	if len(role.Name) > 60 {
		return errors.New("角色名称不能超过 60 个字符")
	}
	if strings.TrimSpace(role.Model) != "" && !strings.Contains(role.Model, "/") {
		// Pi 接受 "id"，但界面里选的总是 provider/id；只给 id 会让用户
		// 以为是别的问题，所以这里提示一下（不是硬错误）。
		host.Logf("role_validate model_without_provider id=%s model=%s", role.ID, role.Model)
	}
	for _, tool := range role.Tools {
		if strings.TrimSpace(tool) == "" {
			return errors.New("工具白名单里有空项")
		}
	}
	// 技能名预检：Pi 只有在启动时才知道技能存不存在，写错了会表现为
	// "角色启动了但技能没加载"——那种问题很难查。所以在保存时就挡下来，
	// 并把候选名一并给出，用户才知道该填什么。
	if _, missing := host.ResolveSkills(role.Skills); len(missing) > 0 {
		candidates := host.SkillSuggestions(12)
		hint := ""
		if len(candidates) > 0 {
			hint = "；当前可用的技能：" + strings.Join(candidates, "、")
		}
		return fmt.Errorf("找不到技能 %s%s", strings.Join(missing, "、"), hint)
	}
	if role.ThinkingLevel != "" && !ValidThinkingLevel(role.ThinkingLevel) {
		return fmt.Errorf("未知的思考级别 %q（可用：low/medium/high/max）", role.ThinkingLevel)
	}
	// 委派字段预检：这几个枚举值拼错会被静默当成默认值（lifecycle 写成 "oneshot"
	// 就变成可继续驻留，多占一份进程），保存时挡住比启动后排查便宜得多。
	if config := role.Delegation; config != nil {
		switch strings.ToLower(strings.TrimSpace(config.Mode)) {
		case "", "background", "blocking":
		default:
			return fmt.Errorf("未知的委派模式 %q（可用：background/blocking）", config.Mode)
		}
		switch strings.ToLower(strings.TrimSpace(config.DeliverOn)) {
		case "", "turn_end", "checkpoint":
		default:
			return fmt.Errorf("未知的投递时机 %q（可用：turn_end/checkpoint）", config.DeliverOn)
		}
		switch strings.ToLower(strings.TrimSpace(config.Lifecycle)) {
		case "", "continuable", "one-shot":
		default:
			return fmt.Errorf("未知的子智能体形态 %q（可用：continuable/one-shot）", config.Lifecycle)
		}
		for _, child := range config.AllowedRoles {
			if _, err := RequireID(child); err != nil {
				return fmt.Errorf("委派白名单里的子角色 id 不合法：%w", err)
			}
		}
		if config.MaxChildren < 0 || config.MaxParallel < 0 ||
			config.MaxDurationSeconds < 0 || config.IdleTimeoutSeconds < 0 {
			return errors.New("委派预算（maxChildren/maxParallel/maxDurationSeconds/idleTimeoutSeconds）不能是负数")
		}
	}
	return nil
}

func ValidThinkingLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low", "medium", "high", "max":
		return true
	}
	return false
}

// IsAgent 报告这份定义是否是子智能体（只能被委派，不能当主会话角色）。
func (role Role) IsAgent() bool {
	return strings.EqualFold(strings.TrimSpace(role.Kind), KindAgent)
}

// validateDelegationTargets 校验委派白名单引用的都是**子智能体**。
//
// 只挡"引用的 id 已存在、但它是个普通角色"这种明确的错；引用的 id 还不存在
// 则放行（用户完全可能先配父角色、后建子智能体）——那种情况由派发时
// Manager.Delegate 的闸门兜底，报错时机一样清楚。
func validateDelegationTargets(role Role) error {
	config := role.Delegation
	if config == nil || len(config.AllowedRoles) == 0 {
		return nil
	}
	kindByID := map[string]string{}
	for _, view := range Load() {
		if view.Broken != "" {
			continue // 损坏的角色判不了类型，留给界面标红，不在这里误伤
		}
		kindByID[strings.ToLower(view.Role.ID)] = view.Role.Kind
	}
	for _, child := range config.AllowedRoles {
		kind, exists := kindByID[strings.ToLower(strings.TrimSpace(child))]
		if exists && !strings.EqualFold(strings.TrimSpace(kind), KindAgent) {
			return fmt.Errorf("委派白名单里的「%s」是普通角色，不是子智能体：子智能体要单独定义（在角色管理里右键它选「复制为子智能体」，或新建一个子智能体）", child)
		}
	}
	return nil
}

// Load 读出全部角色。
//
// 单个角色坏掉不能影响其他角色：读不出来的记成 Broken 一并返回，
// 界面显示"配置损坏"，用户还能进去改或删。
func Load() []View {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	views := make([]View, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		view := View{Role: Role{ID: strings.ToLower(id), Name: id}}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			view.Broken = "读不到文件: " + err.Error()
			views = append(views, view)
			continue
		}
		if err := json.Unmarshal(raw, &view.Role); err != nil {
			view.Broken = "JSON 解析失败: " + err.Error()
			views = append(views, view)
			continue
		}
		if _, err := RequireID(view.Role.ID); err != nil {
			view.Broken = err.Error()
		}
		// 文件名就是角色的寻址方式（读、改、删全按 <id>.json 找）。
		// 内容里的 id 和文件名对不上时：列表里显示的是内容 id，但按它删/改
		// 会落到另一个文件上——必须标坏，让用户看到而不是静默错乱。
		if view.Broken == "" && !strings.EqualFold(strings.ToLower(id), strings.ToLower(view.Role.ID)) {
			view.Broken = fmt.Sprintf("文件名 %q 与配置里的 id %q 不一致（按文件名找不到、按 id 会改到别的文件），请修正后重新保存", entry.Name(), view.Role.ID)
		}
		if view.Role.Name == "" {
			view.Role.Name = view.Role.ID
		}
		view.Prompt = ReadPrompt(view.Role)
		views = append(views, view)
	}
	// 内置角色排前面，其余按名称排：界面列表顺序稳定，切换时不会跳来跳去
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Role.Builtin != views[j].Role.Builtin {
			return views[i].Role.Builtin
		}
		return views[i].Role.Name < views[j].Role.Name
	})
	return views
}

// ReadPrompt 读角色提示词；文件不存在返回空串（提示词是可选的）。
func ReadPrompt(role Role) string {
	path := PromptFileOf(role)
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// PromptFileOf 解析角色提示词的绝对路径。
//
// 只允许角色目录内的相对路径：promptFile 是从磁盘读来的，一旦被改成
// ../../ 之类的值，就会变成"任意文件当提示词"。含绝对路径时一律拒绝。
func PromptFileOf(role Role) string {
	name := strings.TrimSpace(role.PromptFile)
	if name == "" {
		return PromptPath(role.ID)
	}
	if filepath.IsAbs(name) {
		host.Logf("role_prompt_absolute_rejected id=%s path=%s", role.ID, name)
		return ""
	}
	clean := filepath.Clean(name)
	if strings.HasPrefix(clean, "..") {
		host.Logf("role_prompt_escape_rejected id=%s path=%s", role.ID, name)
		return ""
	}
	if !strings.ContainsRune(clean, filepath.Separator) {
		clean = filepath.Join("prompts", clean)
	}
	return filepath.Join(Dir(), clean)
}

// Save 写入角色元数据与提示词。
func Save(role Role, prompt string) error {
	if err := Validate(role); err != nil {
		return err
	}
	if err := validateDelegationTargets(role); err != nil {
		return err
	}
	if len(prompt) > MaxPromptBytes {
		return fmt.Errorf("提示词太长（%d 字节，上限 %d）", len(prompt), MaxPromptBytes)
	}
	dir := Dir()
	if dir == "" {
		return errors.New("找不到配置目录")
	}
	promptsDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		return err
	}
	role.FormatVersion = roleFormatVersion
	if strings.TrimSpace(role.PromptFile) == "" {
		role.PromptFile = filepath.ToSlash(filepath.Join("prompts", role.ID+".md"))
	}
	raw, err := json.MarshalIndent(role, "", "  ")
	if err != nil {
		return err
	}
	// 先写提示词再写元数据：反过来的话，中途失败会留下"角色已存在但没有提示词"的半成品
	path := PromptFileOf(role)
	if path == "" {
		return errors.New("提示词路径不合法（只允许角色目录内的相对路径）")
	}
	if err := os.WriteFile(path, []byte(prompt), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, role.ID+".json"), raw, 0o644)
}

// Delete 删除角色（内置角色不给删：它们是"永远能用的那几套配置"）。
func Delete(id string) error {
	normalized, err := RequireID(id)
	if err != nil {
		return err
	}
	if BuiltinIDs[normalized] {
		return fmt.Errorf("「%s」是内置角色，不能删除（可以编辑或复制成新角色）", normalized)
	}
	path := filepath.Join(Dir(), normalized+".json")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("角色不存在: %s", normalized)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	// 提示词文件跟着删，但失败不算错误：留一个孤儿 .md 不影响使用
	_ = os.Remove(PromptPath(normalized))
	return nil
}

// BuiltinIDs 是随程序分发的角色 id：不可删除。
//
// 从 builtinRoles 派生而不是另写一份清单：两处各写一遍的下场是
// "种子里加了新角色、保护名单忘了加"——toolkeeper 就是这样变成
// "能删掉、删完又被补种回默认内容"的（用户的修改被静默重置）。
var BuiltinIDs = func() map[string]bool {
	ids := make(map[string]bool, len(builtinRoles))
	for _, seed := range builtinRoles {
		ids[seed.role.ID] = true
	}
	return ids
}()

// SeedBuiltin 在**文件不存在时**补种内置角色。
//
// 只补不覆盖：升级程序不会把用户改过的提示词或工具集冲掉。
func SeedBuiltin() {
	dir := Dir()
	if dir == "" {
		return
	}
	/**
	 * 允许"完全不带角色"分发。
	 *
	 * 有些用户要一个空白起点：一个角色都不要，全部自己建。那就必须能关掉补种，
	 * 否则首次启动还是会冒出四个内置角色——"无角色包"名不副实。
	 * 开关放在 gui-state.json（应用自己的状态文件），不动 Pi 的 settings.json：
	 * 那是 Pi 在读的文件，多塞一个键属于替它做决定。
	 */
	if host.SkipSeeding() {
		host.Logf("seed_roles_skipped reason=skipBuiltinRoles")
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		host.Logf("seed_roles_mkdir_failed err=%v", err)
		return
	}
	for _, seed := range builtinRoles {
		path := filepath.Join(dir, seed.role.ID+".json")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := Save(seed.role, seed.prompt); err != nil {
			host.Logf("seed_role_failed id=%s err=%v", seed.role.ID, err)
			continue
		}
		host.Logf("seed_role id=%s", seed.role.ID)
	}
	demoteRetiredBuiltins(dir)
}

// demoteRetiredBuiltins 把"曾经是内置、现在不是了"的角色降级成普通角色。
//
// 升级场景：老版本补种过 ctf/ir/forensics 等（文件里 builtin=true），而新版本
// 的内置只剩 zeroa。不处理的话两边说法不一致：界面按文件里的标记把它们当内置
// （不显示删除按钮），后端 Delete 却按 BuiltinIDs 放行。内容一律保留，只翻标记。
func demoteRetiredBuiltins(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var role Role
		if err := json.Unmarshal(raw, &role); err != nil {
			continue // 坏文件留给 Load 标 Broken，这里不动
		}
		id, err := RequireID(role.ID)
		if err != nil || BuiltinIDs[id] || !role.Builtin {
			continue
		}
		role.Builtin = false
		out, err := json.MarshalIndent(role, "", "  ")
		if err != nil {
			continue
		}
		if err := os.WriteFile(path, out, 0o644); err == nil {
			host.Logf("role_demoted_from_builtin id=%s", id)
		}
	}
}

// OptionsFor 是"角色 → Pi 启动参数"的便捷入口。
//
// 真正的编译在 role_compiler.go 的 Compile 里（产物是 CompiledRole：
// 启动配置 + 运行时配置 + 能力 + 权限 + 循环）。这里只是把它落到 pi.Options，
// 给"只关心启动参数"的调用方（与测试）用。
func OptionsFor(role Role, loadResources bool) (pi.Options, error) {
	compiled, err := Compile(role, loadResources)
	if err != nil {
		return pi.Options{}, err
	}
	opts := pi.Options{
		Tools:        role.Tools,
		ExcludeTools: role.ExcludeTools,
		Model:        role.Model,
		Thinking:     role.ThinkingLevel,
		Models:       role.ScopedModels,
		ContextFiles: role.ContextFiles,
	}
	if loadResources {
		if role.Skills != nil {
			resolved := skillPaths(role.Skills)
			if resolved == nil {
				resolved = []string{}
			}
			opts.Skills = resolved
		}
		opts.Extensions = role.Extensions
		opts.PromptTemplates = role.PromptTemplates
	}
	compiled.ApplyToOptions(&opts)
	return opts, nil
}

// errTooLongPrompt 是提示词超长的统一报错（保存与编译两处用同一句话）。
func errTooLongPrompt(size int) error {
	return fmt.Errorf("提示词太长（%d 字节，上限 %d）", size, MaxPromptBytes)
}

// Find 按 id 取角色（含提示词正文）。
func Find(id string) (View, bool) {
	normalized, err := RequireID(id)
	if err != nil {
		return View{}, false
	}
	for _, view := range Load() {
		if strings.EqualFold(view.Role.ID, normalized) {
			return view, view.Broken == ""
		}
	}
	return View{}, false
}

// ResetBuiltin 把内置角色恢复成随程序分发的默认内容（提示词 + 元数据）。
// 只对内置角色开放：用户自建的角色没有"默认内容"可恢复。
func ResetBuiltin(id string) error {
	normalized, err := RequireID(id)
	if err != nil {
		return err
	}
	for _, seed := range builtinRoles {
		if seed.role.ID != normalized {
			continue
		}
		if err := Save(seed.role, seed.prompt); err != nil {
			return err
		}
		host.Logf("reset_role id=%s", normalized)
		return nil
	}
	return errors.New("只有内置角色可以恢复默认: " + id)
}

// builtinRole 是"内置角色种子"：元数据 + 提示词。
type builtinRole struct {
	role   Role
	prompt string
}

// builtinRoles 是随程序分发的内置角色：**只有 zeroa 一个**。
//
// 设计取舍：默认角色不预设方向、不声明技能与本机工具清单
// （capabilities），只用 Pi 自带的能力——一个干净的空白起点。
// 方向性角色不随仓库分发：在「角色」页按需导入 JSON 或目录即可
// （导入的角色是普通角色，可删可改）。
var builtinRoles = []builtinRole{
	{
		role: Role{
			ID: "zeroa", Name: "ZeroA 助手", Builtin: true, SeedVersion: 1,
			Description: "默认角色：不预设方向，用 Pi 自带的全部能力",
			Icon:        "robot",
			// 故意全部留空：Tools/Skills/Capabilities/Permissions/Delegation
			// 都是 nil —— 不限制也不扩展，Pi 启动时是什么样就是什么样。
		},
		prompt: `你是 ZeroA 助手：一个通用的安全工作台默认角色，不预设方向。

工作方式：

1. 先弄清目标与约束再动手；任务没说清的地方，先问而不是猜。
2. 能验证的就验证（跑一遍、读出来、对结果），验证不了就明说。
3. 交付时说清"做了什么、结果是什么、还差什么"。`,
	},
}
