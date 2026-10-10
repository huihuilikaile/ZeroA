package bridge

import (
	"time"

	"zeroa/internal/roles"
)

/**
 * 角色域的兼容层。
 *
 * 角色的数据结构与领域逻辑已经迁到 internal/roles（磁盘 CRUD、校验、
 * 补种、编译、导入导出）。这里用**类型别名 + 函数变量**把旧名字留在主包里，
 * 让既有代码与测试（它们大量引用这些符号）原样编译，不需要为了搬家
 * 改动几十个调用点——别名是零成本的：类型就是同一个类型，函数就是同一个函数。
 *
 * 新代码请直接 import "zeroa/internal/roles"，不要再往这个清单里加东西。
 */

type Role = roles.Role
type Delegation = roles.Delegation
type DelegationInherit = roles.DelegationInherit
type roleView = roles.View
type LoopPolicy = roles.LoopPolicy
type StuckPolicy = roles.StuckPolicy
type PiSpawnConfig = roles.PiSpawnConfig
type PiRuntimeConfig = roles.PiRuntimeConfig
type CompiledRole = roles.CompiledRole
type roleBundle = roles.Bundle
type roleBundleEntry = roles.BundleEntry

const (
	maxRolePromptBytes = roles.MaxPromptBytes
	roleBundleFormat   = roles.BundleFormat
	roleBundleVersion  = roles.BundleVersion
)

var (
	rolesDir           = roles.Dir
	rolePromptPath     = roles.PromptPath
	rolePromptFile     = roles.PromptFileOf
	requireRoleID      = roles.RequireID
	validateRole       = roles.Validate
	validThinkingLevel = roles.ValidThinkingLevel
	loadRoles          = roles.Load
	readRolePrompt     = roles.ReadPrompt
	saveRole           = roles.Save
	deleteRole         = roles.Delete
	builtinRoleIDs     = roles.BuiltinIDs
	seedBuiltinRoles   = roles.SeedBuiltin
	roleOptions        = roles.OptionsFor
	findRole           = roles.Find
	compileRole        = roles.Compile
)

// init 装配角色域需要的应用能力（配置目录、日志、技能解析、权限引擎、包清单）。
// 放在 init 而不是 main()：单测直接构造 PiService、不经过 main，角色域照样要能用。
func init() {
	roles.Configure(roles.Host{
		AgentDir:         agentDir,
		Logf:             Logf,
		SkipSeeding:      func() bool { return loadGUIState().SkipBuiltinRoles },
		ResolveSkills:    resolveSkillPaths,
		SkillSuggestions: skillSuggestions,
		Engine:           policies.engine,
		Now:              func() time.Time { return timeNow() },
		ReadPackages:     readPackages,
		// 权限策略变了，角色编译缓存必须作废（编译产物里有权限评估结果）
		CompileSalt: policies.fingerprint,
	})
}
