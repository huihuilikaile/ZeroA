// Package roles 是角色域：磁盘上的角色 CRUD、内置角色补种、导入导出，
// 以及"角色 → Pi 启动参数"的编译（Compiler）。
//
// 这个包**不 import 应用层**：它需要的外部能力（配置目录、日志、技能解析、
// 权限引擎、包清单）全部通过 Host 注入，由 main 在启动时装配（见 roles_alias.go）。
// 这样做是为了让角色域可以独立测试、独立复用——它就是"方案第五节"里的
// RoleLoader / RoleValidator / RoleCompiler，而不是 PiService 的附属品。
package roles

import (
	"time"

	"zeroa/internal/permission"
)

// Host 是应用层注入给角色域的能力集合。
//
// 为什么用注入而不是让包自己去读：配置目录怎么解析（便携模式 / 环境变量 /
// 用户目录）、日志写到哪、技能目录在哪、当前权限策略是什么——这些都是
// **应用层**的决定。角色域只管"给定这些事实，角色该怎么校验、编译、落盘"。
type Host struct {
	// AgentDir 返回 Pi 配置目录（角色文件就在它的 roles/ 子目录下）。
	AgentDir func() string
	// Logf 是应用的诊断日志（gui-log.txt）。
	Logf func(format string, args ...any)
	// SkipSeeding 为 true 时不补种内置角色（"无角色包"分发）。
	SkipSeeding func() bool
	// ResolveSkills 把技能 id/路径解析成目录路径，并报告找不到的项。
	ResolveSkills func(ids []string) (paths []string, missing []string)
	// SkillSuggestions 给"找不到技能"的报错列出候选名。
	SkillSuggestions func(limit int) []string
	// Engine 组装"此刻"的权限引擎（全局/用户策略 + 未过期临时授权 + 角色策略）。
	Engine func(role permission.Policy) permission.Engine
	// Now 返回当前时间（测试可以塞固定时间）。
	Now func() time.Time
	// ReadPackages 读出 settings.json 里声明的包（环境清单用）。
	ReadPackages func() []string
	/**
	 * CompileSalt 是编译缓存的"应用侧失效键"：返回一个随权限策略
	 * （全局/用户/临时授权）变化而变化的指纹。
	 *
	 * 为什么需要它：Compile 的输出里有权限引擎的评估结果，而策略存在应用层、
	 * 角色域看不见它的变化。没有这枚盐，用户改完权限后角色编译会沿用旧结论。
	 * 工具检测 / MCP / 会话工具的失效键由 internal/tools 自己提供（DetectVersion
	 * 与 CapabilityInputsFingerprint），不在这里。
	 */
	CompileSalt func() string
}

// host 是装配好的能力集；默认值让"没装配"的包在单测里也能跑（不 panic），
// 但技能解析默认**全部算缺失**——没接上应用的技能目录时，静默放行一个
// 写错的技能名才是最坏的结果。
var host = Host{
	AgentDir:    func() string { return "" },
	Logf:        func(string, ...any) {},
	SkipSeeding: func() bool { return false },
	ResolveSkills: func(ids []string) ([]string, []string) {
		return nil, append([]string(nil), ids...)
	},
	SkillSuggestions: func(int) []string { return nil },
	Engine:           func(role permission.Policy) permission.Engine { return permission.Engine{Role: role} },
	Now:              time.Now,
	ReadPackages:     func() []string { return nil },
	CompileSalt:      func() string { return "" },
}

// Configure 由应用层（main）在启动时调用一次；nil 字段保留默认值。
func Configure(h Host) {
	if h.AgentDir != nil {
		host.AgentDir = h.AgentDir
	}
	if h.Logf != nil {
		host.Logf = h.Logf
	}
	if h.SkipSeeding != nil {
		host.SkipSeeding = h.SkipSeeding
	}
	if h.ResolveSkills != nil {
		host.ResolveSkills = h.ResolveSkills
	}
	if h.SkillSuggestions != nil {
		host.SkillSuggestions = h.SkillSuggestions
	}
	if h.Engine != nil {
		host.Engine = h.Engine
	}
	if h.Now != nil {
		host.Now = h.Now
	}
	if h.ReadPackages != nil {
		host.ReadPackages = h.ReadPackages
	}
	if h.CompileSalt != nil {
		host.CompileSalt = h.CompileSalt
	}
}

// skillPaths 是角色 → --skill 参数的桥梁：解析 id，顺便把缺失的记进日志。
func skillPaths(ids []string) []string {
	paths, missing := host.ResolveSkills(ids)
	for _, name := range missing {
		host.Logf("role_skill_missing id=%s", name)
	}
	return paths
}
