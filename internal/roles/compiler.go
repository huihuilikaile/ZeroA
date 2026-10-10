package roles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeroa/internal/permission"
	"zeroa/internal/pi"
	"zeroa/internal/tools"
)

// RoleCompiler（方案的第五节）：角色的唯一出口。
//
//	role.yaml → RoleLoader → RoleValidator → RoleCompiler
//	                                            ├── PiSpawnConfig
//	                                            ├── PiRuntimeConfig
//	                                            ├── CapabilityRequirements
//	                                            └── PermissionPolicy
//
// 为什么要有这一层而不是"RoleManager 直接启动 Pi"：编译是无副作用的纯函数，
// 可以单独测、可以在界面上预览（Effective Configuration），
// 将来换掉 Pi 也只需要换一个 Runtime Adapter。

// PiSpawnConfig 是启动 Pi 时需要的一切（只有启动时能定）。
type PiSpawnConfig struct {
	CWD                string
	SystemPrompt       string
	AppendSystemPrompt []string
	Skills             []string
	Extensions         []string
	Tools              []string
	ExcludeTools       []string
	SessionDir         string
}

// PiRuntimeConfig 是运行时可改的部分（对应 RPC 的标准命令）。
type PiRuntimeConfig struct {
	Provider               string
	ModelID                string
	ThinkingLevel          string
	AutoCompaction         *bool
	AutoRetry              *bool
	SteeringMode           string
	FollowUpMode           string
	CompactionInstructions string
}

// CompiledRole 是编译产物：启动配置 + 运行时配置 + 能力 + 权限 + 循环策略。
type CompiledRole struct {
	RoleID       string
	Spawn        PiSpawnConfig
	Runtime      PiRuntimeConfig
	Capabilities []string
	Permissions  permission.Policy
	Loop         LoopPolicy
	// Environment 是角色声明的能力在本机实际解析到的工具（只含勾选的能力；
	// 全机清单在「工具」页，不进角色提示词）
	Environment tools.Environment
	// Resolutions 是能力 → 工具的解析明细（界面显示"要的能力有没有"）
	Resolutions []tools.Resolution
	// Warnings 是编译期的提醒（缺能力、缺技能…），不阻断启动
	Warnings []string
}

// Compile 把角色定义 + 本机环境编译成一份可执行配置。
//
// loadResources=false（设置里关了扩展/技能/模板）时，技能与扩展一律不参与：
// 那时候命令行上已经是 --no-skills，再点名只会让 Pi 启动失败。
func Compile(role Role, loadResources bool) (CompiledRole, error) {
	// 编译缓存：同一份输入（角色 + 提示词 + 环境 + 策略，见 compile_cache.go）
	// 直接复用产物，切换角色不必每次都重扫全机工具。
	cacheKey := compileCacheKey(role, loadResources)
	if cached, ok := compileCacheLookup(role.ID, cacheKey); ok {
		return cached, nil
	}

	compiled := CompiledRole{
		RoleID:       role.ID,
		Capabilities: append([]string(nil), role.Capabilities...),
		Loop:         role.Loop,
	}
	/**
	 * 工具白名单要在最前面就落位。
	 *
	 * nil = 不限制（用 Pi 默认），[] = 全禁（--no-tools），非空 = 白名单。
	 * 顺序很重要：下面"能力 → Agent 工具"的合并判断的是 `Tools != nil`，
	 * 如果这时它还是零值 nil，合并会被整个跳过——表现就是
	 * "角色声明了 web.search，工具白名单里却没有 web_search"。
	 */
	compiled.Spawn.Tools = role.Tools

	/**
	 * ---- 环境清单：只给角色它声明了的能力 ----
	 *
	 * 用户明确要求：能力清单是授权语义——**勾选了的才给角色，没勾选的不给**。
	 * 以前这里把全机环境清单原样注入提示词（"不管角色有没有声明能力都告诉模型"），
	 * 等于每个角色默认拥有整台机器的工具：默认角色 zeroa 也背着全量清单，
	 * 与"不预设方向、只用 Pi 自带能力"的设计直接冲突。
	 *
	 * 现在的分工：Resolve 仍然做全机探测（能力解析需要知道谁能认领能力），
	 * 但进提示词/预览的 Environment 由 ScopedEnvironment 收拢成"声明的能力
	 * 解析出的工具"。本机完整清单只在「工具」页展示，与角色解耦。
	 */
	tools.SetInstalledPackages(host.ReadPackages())
	// 设置了"不加载扩展"时，扩展工具本次就是不存在的——清单必须照这个口径说
	tools.SetExtensionsDisabled(!loadResources)
	resolutions, detected := tools.Resolve(role.Capabilities)
	compiled.Resolutions = resolutions
	compiled.Environment = tools.ScopedEnvironment(resolutions, detected.Missing, tools.ExtensionsDisabled())

	// ---- 能力解析的收尾：拿解析结果去影响工具白名单与警告 ----
	//
	// 扩展冲突与"缺能力"这两件事都跟角色声明无关（前者是环境问题，
	// 后者要角色声明了能力才有意义），所以冲突检测放在 if 外面。
	for _, conflict := range tools.ToolConflicts() {
		compiled.Warnings = append(compiled.Warnings,
			"扩展工具冲突：`"+conflict.Tool+"` 同时由 "+conflict.First+" 与 "+conflict.Second+
				" 提供。ZeroA 会拒绝加载后一个扩展，联网能力可能整个不可用："+conflict.Effect)
	}
	/**
	 * 白名单写实、角色又开了委派、却没有委派工具 → 说清楚"这个角色派不了人"。
	 *
	 * ⚠️ 只在角色**自己开了委派**时才提醒：子智能体角色（ctf-recon 这类）的白名单里
	 * 天然没有 delegate_task（深度就限 1 层），对它们报警是误报——实测踩过，
	 * 日志里刷了一条"ctf-recon 无法派子智能体"，看着像故障其实是设计。
	 */
	if role.Delegation != nil && role.Delegation.Enabled &&
		compiled.Spawn.Tools != nil && !containsToolName(compiled.Spawn.Tools, "delegate_task") {
		compiled.Warnings = append(compiled.Warnings,
			"这个角色开了委派，但工具白名单里没有 delegate_task：它派不了子智能体（把工具白名单设成「不限制」，或手动加上 delegate_task）")
	}
	// 声明了但还没实现的委派配置必须说出来：静默忽略的结果只有一个——
	// 用户以为配上了，实际按默认行为跑（这类问题曾经审出过一串）。
	if role.Delegation != nil && role.Delegation.Enabled {
		if strings.EqualFold(strings.TrimSpace(role.Delegation.Mode), "blocking") {
			compiled.Warnings = append(compiled.Warnings,
				"委派模式 blocking 暂未实现：delegate_task 仍是受理后立刻返回（按 background 处理），结果照常自动回到会话里")
		}
		if role.Delegation.Inherit != nil {
			compiled.Warnings = append(compiled.Warnings,
				"delegation.inherit 暂未实现：子智能体完全按子角色自己的定义启动（模型缺省沿用父会话的），这个字段当前不生效")
		}
	}
	if len(role.Capabilities) > 0 {
		resolutions := compiled.Resolutions
		for _, capability := range compiled.Environment.Missing {
			compiled.Warnings = append(compiled.Warnings,
				"本机没有能提供能力 "+capability+" 的工具（角色仍可启动，但它可能做不到这件事）")
		}
		/**
		 * 能力要**真的能用上**：把能力解析出来的「Agent 可调用工具」并进工具白名单。
		 *
		 * 少了这一步会出现一个很隐蔽的问题：角色写 `capabilities: [web.search]`，
		 * 而工具白名单是 ["read","bash"] ——能力被声明了，但 web_search 不在白名单里，
		 * Pi 启动后根本没有这个工具。用户看到的是"角色配了联网却搜不了"。
		 *
		 * 只在白名单**非 nil**（角色限制了工具）时合并：nil 表示"不限制"，
		 * 那时扩展工具本来就能用，不需要额外加。
		 */
		/**
		 * 角色开了委派，就保证委派工具在白名单里。
		 *
		 * 实测踩到的坑：角色的工具白名单一旦被写实（界面上点过"全选"、或手改过 JSON），
		 * 很容易漏掉 delegate_task —— 结果"委派配置是开的、提示词里也写了怎么派人，
		 * 但模型一调就 Tool delegate_task not found"，只能自己干（用户实测报的）。
		 * 这里做的是"配置意图 → 必要工具"的补齐，与"能力 → Agent 工具"同一类逻辑。
		 */
		if role.Delegation != nil && role.Delegation.Enabled && compiled.Spawn.Tools != nil {
			merged := append([]string(nil), compiled.Spawn.Tools...)
			for _, name := range workbenchRoleTools {
				if !containsToolName(merged, name) {
					merged = append(merged, name)
				}
			}
			compiled.Spawn.Tools = merged
		}

		if compiled.Spawn.Tools != nil {
			merged := append([]string(nil), compiled.Spawn.Tools...)
			for _, resolution := range resolutions {
				for _, tool := range resolution.Available {
					if tool.AgentTool {
						merged = append(merged, tool.ID)
					}
				}
			}
			compiled.Spawn.Tools = dedupeStrings(merged)
		}
	}

	// ---- 权限（P10）：算出最终策略，并翻译成"禁用哪些工具" ----
	engine := host.Engine(role.Permissions)
	compiled.Permissions = permission.Policy{}
	for _, snapshot := range engine.Evaluate(host.Now()) {
		compiled.Permissions[snapshot.Action] = snapshot.Decision
	}
	// ---- 工具排除项 ----
	// 白名单已在函数开头落位（那里有顺序原因）；这里只处理"再排除几个"。
	compiled.Spawn.ExcludeTools = append([]string(nil), role.ExcludeTools...)
	compiled.Spawn.ExcludeTools = append(compiled.Spawn.ExcludeTools,
		permission.ToolDenyList(func(action permission.Action) permission.Decision {
			return compiled.Permissions[action]
		})...)
	compiled.Spawn.ExcludeTools = dedupeStrings(compiled.Spawn.ExcludeTools)

	/**
	 * 冲突检测：角色声明了联网能力，但权限把联网工具禁掉了。
	 *
	 * 这种情况必须说出来，否则用户会看到"角色配了 web.search，模型却从来不用"——
	 * 两个配置各自都合理，凑在一起互相抵消，界面上完全看不出来。
	 */
	needsNetwork := false
	for _, capability := range compiled.Capabilities {
		if strings.HasPrefix(capability, "web.") || strings.HasPrefix(capability, "net.") {
			needsNetwork = true
			break
		}
	}
	if needsNetwork && compiled.Permissions[permission.ActionNetworkPublic] == permission.Deny {
		compiled.Warnings = append(compiled.Warnings,
			"角色声明了联网能力，但权限「访问公网」是禁止：启动时会把联网工具排除，模型用不了它们。"+
				"要联网就把权限改成允许或询问（可在角色页的权限页签或全局权限页调整）。")
	}

	// ---- 提示词：角色提示词 + 环境清单 ----
	promptPath := PromptFileOf(role)
	if info, err := os.Stat(promptPath); err == nil && !info.IsDir() {
		raw, readErr := os.ReadFile(promptPath)
		if readErr != nil {
			return compiled, readErr
		}
		if len(raw) > MaxPromptBytes {
			return compiled, errTooLongPrompt(len(raw))
		}
		if role.ReplaceSystemPrompt {
			compiled.Spawn.SystemPrompt = string(raw)
		} else {
			compiled.Spawn.AppendSystemPrompt = []string{promptPath}
		}
	}
	// 环境清单单独一个文件：它是程序生成的，不该混进用户编辑的提示词文件
	if envText := environmentSection(compiled.Environment, compiled.Resolutions); envText != "" {
		if path, err := writeEnvironmentPrompt(role.ID, envText); err == nil {
			compiled.Spawn.AppendSystemPrompt = append(compiled.Spawn.AppendSystemPrompt, path)
		} else {
			compiled.Warnings = append(compiled.Warnings, "环境说明写不进去："+err.Error())
		}
	}
	/**
	 * 委派说明也要自动注入。
	 *
	 * 为什么不让用户写进提示词文件：这段内容必须与**实际配置**一致——
	 * 允许派哪些角色、最多几个、并发几、要不要先审计。手写迟早写歪，
	 * 而写歪的后果是模型去派一个不在白名单里的角色（被 Go 拦下来，
	 * 但用户看到的是一次莫名其妙的失败）。
	 */
	if delegationText := delegationSection(role); delegationText != "" {
		if path, err := writeDelegationPrompt(role.ID, delegationText); err == nil {
			compiled.Spawn.AppendSystemPrompt = append(compiled.Spawn.AppendSystemPrompt, path)
		} else {
			compiled.Warnings = append(compiled.Warnings, "委派说明写不进去："+err.Error())
		}
	}

	// ---- 技能 / 扩展 / 提示模板（资源的确定性加载，方案第三十节）----
	if loadResources {
		if role.Skills != nil {
			resolved := skillPaths(role.Skills)
			if resolved == nil {
				resolved = []string{}
			}
			compiled.Spawn.Skills = resolved
		}
		compiled.Spawn.Extensions = role.Extensions
	}

	// ---- 运行时配置 ----
	compiled.Runtime.ModelID = role.Model
	compiled.Runtime.ThinkingLevel = role.ThinkingLevel
	compiled.Runtime.AutoCompaction = role.AutoCompaction
	compiled.Runtime.CompactionInstructions = role.CompactionInstructions
	compileCacheStore(role.ID, cacheKey, compiled)
	return compiled, nil
}

// ApplyToOptions 把编译产物落进 Pi 的启动参数。
//
// 这是"编译"与"启动"之间唯一的接缝：CompiledRole 是数据，
// pi.Options 是进程参数；分开之后编译可以单独测试与预览。
func (c CompiledRole) ApplyToOptions(opts *pi.Options) {
	if c.Spawn.SystemPrompt != "" {
		opts.SystemPrompt = c.Spawn.SystemPrompt
	}
	if len(c.Spawn.AppendSystemPrompt) > 0 {
		opts.AppendSystemPromptFiles = c.Spawn.AppendSystemPrompt
	}
	if c.Spawn.Skills != nil {
		opts.Skills = c.Spawn.Skills
	}
	if c.Spawn.Extensions != nil {
		opts.Extensions = c.Spawn.Extensions
	}
	if len(c.Spawn.ExcludeTools) > 0 {
		opts.ExcludeTools = c.Spawn.ExcludeTools
	}
	// nil 与空切片语义不同：nil = 不干预，[] = --no-tools
	if c.Spawn.Tools != nil {
		opts.Tools = c.Spawn.Tools
	}
	if strings.TrimSpace(c.Runtime.ModelID) != "" {
		opts.Model = c.Runtime.ModelID
	}
	if strings.TrimSpace(c.Runtime.ThinkingLevel) != "" {
		opts.Thinking = c.Runtime.ThinkingLevel
	}
}

// delegationSection 生成"什么时候该派子智能体"的说明。
//
// 只有当角色的 delegation.enabled 为真时才生成——没开委派的角色拿到这段文字
// 只会让它尝试去调一个不存在的工具。
func delegationSection(role Role) string {
	if role.Delegation == nil || !role.Delegation.Enabled {
		return ""
	}
	config := role.Delegation

	var builder strings.Builder
	builder.WriteString("\n\n## 子智能体（你可以派人替你干活）\n\n")
	builder.WriteString("你有一个工具 `delegate_task(role, task)`：它启动一个**独立进程**的子智能体，")
	builder.WriteString("有自己的上下文窗口。调用后**立刻返回**，你继续手上的工作；")
	builder.WriteString("子智能体完成后，它的结论会自动出现在本会话里（不需要你去等或去查）。\n\n")
	/**
	 * 子智能体是**可继续**的：这一点必须写进提示词。
	 * 不写的话，模型会把它当成"一次性任务"——需要补充要求时又去派一个新的，
	 * 于是重复劳动、多起一个进程，还把已经建立起来的上下文丢掉（仿 DSH 的体验差异就在这）。
	 */
	builder.WriteString("子智能体是**可继续**的（除非角色里配了 one-shot）：\n")
	builder.WriteString("- `delegation_list` 能看到它们的 id 与状态；`idle` 表示它空闲着、**还能继续派活**（不是失败、也还没结束）；\n")
	builder.WriteString("- `send_to_agent(agentId, message)` 给它追加要求或追问：它正在跑就插到最近的检查点，它空闲就开新一轮（上下文还在）；\n")
	builder.WriteString("- `agent_interrupt(agentId)` 只停它**当前这一轮**（它不会消失，之后还能继续派活）；\n")
	builder.WriteString("- 要彻底结束它、释放进程才用 `delegation_stop`。\n\n")

	// 白名单里的角色：名字与说明都从角色定义来，保证与实际配置一致
	if len(config.AllowedRoles) > 0 {
		builder.WriteString("你能派的角色：\n")
		for _, id := range config.AllowedRoles {
			view, ok := Find(id)
			if !ok {
				builder.WriteString("- `" + id + "`（**这个角色不存在**，派了会被拒——改用别的角色或自己做）\n")
				continue
			}
			description := strings.TrimSpace(view.Role.Description)
			if description == "" {
				description = strings.TrimSpace(view.Role.Name)
			}
			builder.WriteString("- `" + id + "`：" + description + "\n")
		}
		builder.WriteString("\n")
	}

	builder.WriteString("**该派人**：查 writeup / 资料 / 工具用法；独立复核一个结论或候选 flag；")
	builder.WriteString("有明显多条解法、需要同时试不同路线。\n")
	builder.WriteString("**不该派人**：线性链条中的下一步（定位判断 → 逆算法 → 写脚本，拆开会丢线索）；")
	builder.WriteString("几分钟就能做完的小事（起一个子进程要一两秒和一份内存）。\n")

	if config.RequireAudit {
		builder.WriteString("\n**硬要求**：先自己动手看清楚题目与附件（至少读文件或跑一条命令），")
		builder.WriteString("在那之前派人是会被拒绝的。\n")
	}
	limits := make([]string, 0, 3)
	if config.MaxChildren > 0 {
		limits = append(limits, fmt.Sprintf("最多派 %d 个", config.MaxChildren))
	}
	if config.MaxParallel > 0 {
		limits = append(limits, fmt.Sprintf("同时最多 %d 个在跑", config.MaxParallel))
	}
	if config.MaxDurationSeconds > 0 {
		limits = append(limits, fmt.Sprintf("单个最长 %d 秒", config.MaxDurationSeconds))
	}
	if len(limits) > 0 {
		builder.WriteString("\n预算：" + strings.Join(limits, "、") + "。超了会被拒绝——那时自己做，或等一个跑完再派。\n")
	}

	// 结果怎么回来：说清楚，模型才不会去轮询或空等
	if strings.EqualFold(config.DeliverOn, "checkpoint") {
		builder.WriteString("\n子智能体的结论会在下一个检查点插进对话；在那之前继续做你的事。\n")
	} else {
		builder.WriteString("\n子智能体的结论会在你**这一轮结束后**合并成一条消息交给你；在那之前先完成手上正在做的事。\n")
	}
	builder.WriteString("收到结论后，说明你采纳了什么、忽略了什么——不要只是复述它。\n")
	builder.WriteString("\n派完活**立刻继续你自己的主线**：不要空等，也不要把最终判断交给子智能体。\n")
	builder.WriteString("题目已经解出来、或不再需要某个结果时，用 `delegation_stop` 停掉还在跑的子智能体")
	builder.WriteString("（留着它们只会烧预算，而且结论已经没人要了）。\n")
	return builder.String()
}

// writeDelegationPrompt 把委派说明写进 <agentDir>/roles/prompts/<id>.delegation.md。
func writeDelegationPrompt(roleID, text string) (string, error) {
	path := filepath.Join(Dir(), "prompts", roleID+".delegation.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// environmentSection 把环境摘要渲染成"给模型看的一节"。
//
// resolutions 也传进来：角色明确要的能力对应的工具（带版本）是清单里
// 最该被信任的一段，值得单独列出来。
func environmentSection(env tools.Environment, resolutions []tools.Resolution) string {
	if len(env.Commands) == 0 && len(env.PythonPackages) == 0 &&
		len(env.NodeModules) == 0 && len(env.Applications) == 0 &&
		len(env.AgentTools) == 0 && len(env.Missing) == 0 {
		return ""
	}
	return tools.EnvironmentPrompt(env, resolutions) + shellSection()
}

/**
 * shellSection 说明 bash 工具**实际上**跑在哪个 shell 里。
 *
 * 为什么值得单独写一段：清单里的"本机可执行"是 Go 进程在 **Windows PATH** 上检测的，
 * 而模型的命令跑在 Pi 的 bash 工具里——两者的 PATH 可以完全不同。实测踩过：
 * bash 落到 C:\Windows\System32\bash.exe（WSL），uname 是 Linux、python 不存在，
 * 而清单写着"有 python"，模型就照着清单写命令然后一条条失败。
 * 现在启动时会把配置的 shell 目录前置到 PATH（见 internal/pi/shellenv.go），
 * 这里再把结果如实告诉模型，让它能自己判断"该用 python 还是 python3"。
 */
func shellSection() string {
	dir := pi.ShellDirFor(host.AgentDir())
	if dir == "" {
		return "\n\n## 命令执行环境\n\n" +
			"bash 工具用的是系统默认 shell（未能从设置里确定具体路径）。" +
			"写命令前先用 `uname -s` / `command -v <工具名>` 确认一遍，不要假设上面的清单在这个 shell 里同样成立。\n"
	}
	shellPath := filepath.Join(dir, "bash.exe")
	if _, err := os.Stat(shellPath); err != nil {
		shellPath = dir
	}
	return "\n\n## 命令执行环境\n\n" +
		"bash 工具运行在 **Git Bash**（" + shellPath + "，MINGW64）。" +
		"它是 Windows 上的 POSIX 环境：路径写作 /c/Users/... 或 C:/Users/...，" +
		"而不是 WSL/Linux。" +
		"上面的清单就是在这个 shell 的 PATH 上检测的；" +
		"如果某个工具找不到，用 `command -v <名字>` 确认，不要想当然。\n"
}

// writeEnvironmentPrompt 把环境说明写进 <agentDir>/roles/prompts/<id>.env.md。
//
// 每次启动重写：工具是用户随时会装的，"这台机器有什么"必须反映此刻的实际情况。
func writeEnvironmentPrompt(roleID, text string) (string, error) {
	path := filepath.Join(Dir(), "prompts", roleID+".env.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// workbenchRoleTools 是"工作台自带的、按需要补进白名单"的工具。
var workbenchRoleTools = []string{
	"delegate_task", "delegation_list", "delegation_stop",
	// 可继续子智能体的追问与中断：白名单里没有它们，模型就只能"派完不管"。
	"send_to_agent", "agent_interrupt",
}

// containsToolName 忽略大小写判断工具名是否在白名单里。
func containsToolName(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}
