package tools

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 能力解析（方案的 P8）：角色声明的是能力，不是工具。
//
// 为什么要有这一层：同一件事在不同机器上能用不同工具完成。
// "内存取证"在装了 Volatility 的机器上是 vol，在只有 SleuthKit 的机器上
// 至少还能做磁盘层面的取证。所以角色写 `forensics.memory`，
// 由解析器告诉它"这台机器上能给你什么"，缺的明确说出来——
// 而不是让角色一启动就失败，或者让模型以为自己有工具。

// Resolution 是一个能力的解析结果。
type Resolution struct {
	Capability string   `json:"capability"`
	Available  []Status `json:"available"`
	Missing    []Status `json:"missing"`
}

// Environment 是"这台机器实际能提供什么"的摘要，会作为环境说明注入提示词。
//
// 方案第十一节的关键点：PyCryptodome、Volatility 这类**不该**注册成 LLM 工具，
// 而应该让模型知道"它存在，你可以用 bash 调用"。这就是那个清单。
type Environment struct {
	// Commands：可用的命令行程序（id → 路径）
	Commands map[string]string `json:"commands"`
	// PythonPackages：可用的 Python 包
	PythonPackages []string `json:"pythonPackages"`
	// NodeModules：可用的 Node 包
	NodeModules []string `json:"nodeModules"`
	// Applications：桌面程序
	Applications []string `json:"applications"`
	// AgentTools：模型**直接调用**的工具（扩展注册的，如 web_search）。
	// 与上面几类必须分开：它们是工具接口，不是"用 bash 调用的程序"。
	//
	// 只收**本次会话真的加载了**的扩展工具：设置里关掉扩展时这里是空的，
	// 清单就不会写"你可以直接调用 web_search"这种手上没有的承诺。
	AgentTools []string `json:"agentTools"`
	// ExtensionsDisabled：本次启动是否整体关掉了扩展/技能/模板
	ExtensionsDisabled bool `json:"extensionsDisabled"`
	// Missing：角色要了但本机没有的能力（界面要提示，提示词里也要说明）
	Missing []string `json:"missing"`
}

// Resolve 把一组能力解析成本机工具。
//
// 返回：每个能力的解析明细 + 环境摘要。解析只做匹配，不下载、不安装
// （方案第二十四节：第一版只检测）。
func Resolve(capabilities []string) ([]Resolution, Environment) {
	statuses := DetectAll(false)
	byCapability := map[string][]Status{}
	for _, status := range statuses {
		for _, capability := range status.Capabilities {
			byCapability[capability] = append(byCapability[capability], status)
		}
	}

	// 外部来源的能力（MCP 等）：它们的可用性来自配置与缓存，不来自 PATH 探测
	if provider := externalCapabilityProvider(); provider != nil {
		for capability, statuses := range provider() {
			byCapability[capability] = append(byCapability[capability], statuses...)
		}
	}

	env := Environment{
		Commands:       map[string]string{},
		PythonPackages: []string{},
		NodeModules:    []string{},
		Applications:   []string{},
		AgentTools:     []string{},
		Missing:        []string{},
	}

	/**
	 * 运行期工具（会话里的真实清单）也要参与能力判定。
	 *
	 * 为什么：web_search 这类工具不在 PATH 里、也不在任何本机目录里，它们是扩展注册的。
	 * 判定依据是"名字 → 能力"的规则（tool-catalog.json 的 capabilityRules）——
	 * 规则本身可配置，所以既不用写死包名，也仍然能回答"角色要 web.search 给不给得起"。
	 */
	agentTools := agentToolsFromProvider()
	for _, agentTool := range agentTools {
		if strings.TrimSpace(agentTool.Name) == "" {
			continue
		}
		for _, capability := range MatchRules(agentTool.Name) {
			byCapability[capability] = append(byCapability[capability], Status{
				Definition: Definition{
					ID:           agentTool.Name,
					Name:         agentTool.Name,
					Type:         KindExtension,
					Direction:    agentTool.Kind,
					Capabilities: MatchRules(agentTool.Name),
					Provider:     agentTool.Provider,
					AgentTool:    true,
				},
				Available: true,
				Detail:    "运行中的会话提供",
				CheckedAt: time.Now(),
			})
		}
	}

	resolutions := make([]Resolution, 0, len(capabilities))
	seen := map[string]bool{}
	for _, capability := range capabilities {
		trimmed := strings.TrimSpace(capability)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		// Available/Missing 必须是空切片而不是 nil：nil 在 JSON 里是 null，
		// 前端按数组读会整个页面崩掉（"Cannot read properties of null (reading 'length')"）。
		resolution := Resolution{Capability: trimmed, Available: []Status{}, Missing: []Status{}}
		for _, status := range byCapability[trimmed] {
			if status.Available {
				resolution.Available = append(resolution.Available, status)
			} else {
				resolution.Missing = append(resolution.Missing, status)
			}
		}
		// 一个能力没有任何工具认领，也要能看出来（多半是能力名写错了）
		resolutions = append(resolutions, resolution)
		if len(resolution.Available) == 0 {
			env.Missing = append(env.Missing, trimmed)
		}
	}

	// 环境摘要：只列**可用**的工具（这是给模型的"你有什么"清单）
	for _, status := range statuses {
		if !status.Available {
			continue
		}
		switch status.Type {
		case KindPythonPackage:
			env.PythonPackages = append(env.PythonPackages, status.Name)
		case KindNodePackage:
			env.NodeModules = append(env.NodeModules, status.Name)
		case KindApplication:
			env.Applications = append(env.Applications, status.Name)
		case KindExtension:
			env.AgentTools = append(env.AgentTools, status.Name)
		default:
			env.Commands[status.ID] = status.Path
		}
	}
	// 运行期工具（会话里的真实清单）并进 AgentTools：模型要知道自己能直接调用什么
	for _, agentTool := range agentTools {
		if strings.TrimSpace(agentTool.Name) != "" {
			env.AgentTools = append(env.AgentTools, agentTool.Name)
		}
	}
	env.AgentTools = dedupeStrings(env.AgentTools)

	// 扩展是否被整体关掉：清单要照实说，否则会变成一句模型无法兑现的承诺
	env.ExtensionsDisabled = ExtensionsDisabled()
	if env.ExtensionsDisabled {
		// 关掉扩展时"可直接调用"的话就不该说：手上没有的东西不能承诺
		env.AgentTools = []string{}
	}
	sort.Strings(env.PythonPackages)
	sort.Strings(env.NodeModules)
	sort.Strings(env.Applications)
	sort.Strings(env.AgentTools)
	sort.Strings(env.Missing)
	return resolutions, env
}

/**
 * ScopedEnvironment 只收"角色声明的能力"解析出来的工具。
 *
 * 用户明确要求：能力清单是**授权语义**——勾选了的才给角色，没勾选的一律不给。
 * 以前 Resolve 返回的全机环境清单原样进提示词，等于角色默认拥有整台机器的工具
 * （默认角色 zeroa 因此也背着一份全量清单，与"只用 Pi 自带能力"的设计相反）。
 * 现在全机清单只出现在「工具」页（ListTools），角色提示词里的环境段由这里收拢。
 *
 * missing 与 extensionsDisabled 照抄 Resolve 的结果：它们本来就只跟声明的能力有关。
 */
func ScopedEnvironment(resolutions []Resolution, missing []string, extensionsDisabled bool) Environment {
	env := Environment{
		Commands:           map[string]string{},
		PythonPackages:     []string{},
		NodeModules:        []string{},
		Applications:       []string{},
		AgentTools:         []string{},
		Missing:            append([]string{}, missing...),
		ExtensionsDisabled: extensionsDisabled,
	}
	seen := map[string]bool{}
	for _, resolution := range resolutions {
		for _, status := range resolution.Available {
			if status.ID == "" || seen[status.ID] {
				continue
			}
			seen[status.ID] = true
			switch status.Type {
			case KindPythonPackage:
				env.PythonPackages = append(env.PythonPackages, status.Name)
			case KindNodePackage:
				env.NodeModules = append(env.NodeModules, status.Name)
			case KindApplication:
				env.Applications = append(env.Applications, status.Name)
			case KindExtension, KindMCP:
				// 扩展/MCP 工具是模型**直接调用**的接口，与环境程序分开列
				env.AgentTools = append(env.AgentTools, status.Name)
			default:
				env.Commands[status.ID] = status.Path
			}
		}
	}
	sort.Strings(env.PythonPackages)
	sort.Strings(env.NodeModules)
	sort.Strings(env.Applications)
	sort.Strings(env.AgentTools)
	if extensionsDisabled {
		// 扩展这次没加载：可直接调用的承诺一句都不能留
		env.AgentTools = []string{}
	}
	return env
}

/**
 * CapabilityInputsFingerprint 指纹化"本机探测之外、会影响能力解析的输入"：
 * 外部能力来源（MCP 配置/缓存）与运行期会话工具（扩展注册的 web_search 等）。
 *
 * 角色编译缓存把它算进失效键：MCP 服务器被停用、会话换了扩展包，
 * 缓存就自动作废——不需要各调用点记得手动清缓存。
 */
func CapabilityInputsFingerprint() string {
	hash := fnv.New64a()
	if provider := externalCapabilityProvider(); provider != nil {
		statuses := provider()
		keys := make([]string, 0, len(statuses))
		for capability := range statuses {
			keys = append(keys, capability)
		}
		sort.Strings(keys)
		for _, capability := range keys {
			hash.Write([]byte(capability))
			for _, status := range statuses[capability] {
				fmt.Fprintf(hash, "|%s=%v", status.Name, status.Available)
			}
		}
	}
	agentTools := agentToolsFromProvider()
	names := make([]string, 0, len(agentTools))
	for _, tool := range agentTools {
		names = append(names, tool.Name+"/"+tool.Kind+"/"+tool.Provider)
	}
	sort.Strings(names)
	for _, name := range names {
		hash.Write([]byte("\x00" + name))
	}
	return strconv.FormatUint(hash.Sum64(), 16)
}

// KnownCapabilities 列出注册表里出现过的全部能力（界面做多选时用）。
//
// 必须走 Definitions() 而不是内部的 registry：后者只有本机命令行工具，
// 扩展提供的联网能力（web.search / web.fetch）不在里面——界面就会少这几项，
// 用户根本没机会声明"我需要联网"。
func KnownCapabilities() []string {
	catalog := ActiveCatalog()
	set := map[string]bool{}
	for _, def := range catalog.Tools {
		for _, capability := range def.Capabilities {
			set[capability] = true
		}
	}
	// 目录里"只声明、暂无人实现"的能力（角色可以先声明需要它），
	// 以及规则里给出的能力（给自动发现的扩展/MCP 工具用），都要出现在界面上，
	// 否则用户根本没机会勾选它们。
	for _, meta := range catalog.Capabilities {
		if id := strings.TrimSpace(meta.ID); id != "" {
			set[id] = true
		}
	}
	for _, rule := range catalog.Rules {
		for _, capability := range rule.Capabilities {
			if trimmed := strings.TrimSpace(capability); trimmed != "" {
				set[trimmed] = true
			}
		}
	}
	// 外部来源（MCP 等）注册进来的能力
	if provider := externalCapabilityProvider(); provider != nil {
		for capability := range provider() {
			set[capability] = true
		}
	}
	out := make([]string, 0, len(set))
	for capability := range set {
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// CapabilityLabels 返回能力的中文名（没有就返回空串）。
func CapabilityLabels() map[string]string {
	out := map[string]string{}
	for _, meta := range ActiveCatalog().Capabilities {
		if strings.TrimSpace(meta.Label) != "" {
			out[meta.ID] = meta.Label
		}
	}
	return out
}

/* ------------------- 外部能力来源（MCP / 运行中的会话） ------------------- */

var (
	externalMu       sync.RWMutex
	externalProvider func() map[string][]Status
)

// SetExternalCapabilityProvider 注册"非本机"的能力来源。
//
// 用来让 tools.Resolve 也能判定 mcp.ida 这类能力：它们的可用性不来自 PATH 探测，
// 而来自 MCP 服务器配置与缓存——那属于主程序的职责，所以用注入而不是反过来依赖。
func SetExternalCapabilityProvider(provider func() map[string][]Status) {
	externalMu.Lock()
	externalProvider = provider
	externalMu.Unlock()
}

func externalCapabilityProvider() func() map[string][]Status {
	externalMu.RLock()
	defer externalMu.RUnlock()
	return externalProvider
}

// AgentTool 是"运行期才知道的、模型可直接调用的工具"（会话里的真实清单）。
type AgentTool struct {
	Name     string
	Kind     string // builtin / extension / mcp
	Provider string // 扩展包名（能认出来时）
}

var (
	agentToolMu       sync.RWMutex
	agentToolProvider func() []AgentTool
)

/**
 * SetAgentToolProvider 注册"运行期才知道的、模型可直接调用的工具"。
 *
 * 这一层以前来自写死的扩展表（PackageMatch → 工具名），现在来自运行中的会话：
 * Pi 把最终工具集写进会话的 sections.tools，内置 + 扩展 + MCP 都在里面。
 * 环境清单必须包含它们，否则模型不知道"你可以直接调用 web_search"。
 */
func SetAgentToolProvider(provider func() []AgentTool) {
	agentToolMu.Lock()
	agentToolProvider = provider
	agentToolMu.Unlock()
}

func agentToolsFromProvider() []AgentTool {
	agentToolMu.RLock()
	provider := agentToolProvider
	agentToolMu.RUnlock()
	if provider == nil {
		return nil
	}
	return provider()
}

// ToolsForCapability 返回某个能力对应工具的定义（界面显示"这个能力会用到什么"）。
func ToolsForCapability(capability string) []Definition {
	var out []Definition
	for _, def := range Definitions() {
		for _, item := range def.Capabilities {
			if item == capability {
				out = append(out, def)
				break
			}
		}
	}
	return out
}

// EnvironmentPrompt 把环境摘要渲染成一段提示词。
//
// 注入方式与方案一致：**不进系统提示的分段结构**（那是 Pi 管的），
// 而是写成文件、用 --append-system-prompt 传过去。
//
// 措辞上有一条硬要求（用户提的）：清单是**线索，不是保证**。
// 工具是用户随时会装/会卸的，版本也可能对不上；如果提示词里写成
// "你可以用 X"，模型会直接假设它可用、参数正确，失败后往往还不承认。
// 所以这里明确要求：**先用一条最便宜的命令验证，再依赖它**。
func EnvironmentPrompt(env Environment, resolutions []Resolution) string {
	var builder strings.Builder
	builder.WriteString("\n\n## 这个角色可用的工具（按声明的能力在本机解析，自动扫描仅供参考）\n\n")

	if len(env.Commands) > 0 {
		keys := make([]string, 0, len(env.Commands))
		for id := range env.Commands {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		builder.WriteString("可用的可执行程序：" + strings.Join(keys, "、") + "\n\n")
	}
	if len(env.PythonPackages) > 0 {
		builder.WriteString("可用的 Python 包：" + strings.Join(env.PythonPackages, "、") + "\n\n")
	}
	if len(env.NodeModules) > 0 {
		builder.WriteString("可用的 Node 模块：" + strings.Join(env.NodeModules, "、") + "\n\n")
	}
	if len(env.Applications) > 0 {
		builder.WriteString("可用的桌面程序：" + strings.Join(env.Applications, "、") + "\n\n")
	}
	if len(env.AgentTools) > 0 {
		builder.WriteString("你可以**直接调用**的工具：" + strings.Join(env.AgentTools, "、") + "\n\n")
	}
	if env.ExtensionsDisabled {
		builder.WriteString("**注意**：本次会话关闭了「加载扩展 / 技能 / 模板」，" +
			"所以扩展提供的工具（web_search、fetch_content 之类）**这次没有加载**，你的工具列表里不会有它们。" +
			"用户需要联网能力时，请让他到设置里打开这个开关。\n\n")
	}
	// 角色明确要的能力 → 本机实际对应什么（带版本，这是最该被信任的一段）
	if len(resolutions) > 0 {
		builder.WriteString("这个角色需要的能力，在本机对应：\n")
		for _, resolution := range resolutions {
			if len(resolution.Available) == 0 {
				builder.WriteString("- `" + resolution.Capability + "`：**本机没有对应工具**（需要时如实说明，并给出替代方案）\n")
				continue
			}
			names := make([]string, 0, len(resolution.Available))
			for _, status := range resolution.Available {
				label := status.Name
				if status.Version != "" {
					label += "(" + trimVersion(status.Version) + ")"
				}
				names = append(names, label)
			}
			builder.WriteString("- `" + resolution.Capability + "`：" + strings.Join(names, "、") + "\n")
		}
		builder.WriteString("\n")
	}

	builder.WriteString("### 这份清单怎么用\n\n")
	builder.WriteString("它是**线索，不是保证**：工具是随时会装会卸的，版本也可能与预期不同。所以：\n\n")
	builder.WriteString("1. **用之前先验证**，一条最便宜的命令就够：`command -v <工具>` / `<工具> --version` / " +
		"`python -c \"import 模块; print(模块.__version__)\"`。\n")
	builder.WriteString("2. 名字对不代表能力对：版本、编译选项、依赖、算法支持都可能不一样，别按记忆里的参数直接上。\n")
	builder.WriteString("3. 清单里**没有的**不要假设它存在；缺什么就明确说出来，并给出替代做法，不要硬凑。\n")
	builder.WriteString("4. 验证失败时如实报告（例如\"本机没有 X\"），不要假装用过。\n\n")
	builder.WriteString("另外：除「可以直接调用的工具」之外，其余都是环境里的程序，不是你的工具接口——用 `bash` 调用它们。\n")
	return builder.String()
}

// DescribeResolution 给界面/日志用的一句话摘要。
func DescribeResolution(resolutions []Resolution) string {
	var parts []string
	for _, resolution := range resolutions {
		names := make([]string, 0, len(resolution.Available))
		for _, status := range resolution.Available {
			names = append(names, status.Name)
		}
		if len(names) == 0 {
			parts = append(parts, fmt.Sprintf("%s（缺）", resolution.Capability))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s→%s", resolution.Capability, strings.Join(names, "/")))
	}
	return strings.Join(parts, "，")
}
