package bridge

import (
	"encoding/json"
	"path/filepath"

	"zeroa/internal/pi"
	"zeroa/internal/roles"
	"zeroa/internal/skills"
)

// 本文件是技能域的薄封装：实现已经迁到 internal/skills（扫描 / 解析 / 导入，
// 纯文件 IO），这里只剩三件事——
//
//	① skillsRoot 解析技能根目录（<agentDir>/skills，这正是 Pi 的用户级技能目录，
//	   见 pi 的 package-manager：<globalBaseDir>/skills，放这里不需要任何额外配置）；
//	② 供主包其它文件（capabilities_api / roles_alias / 测试）使用的薄封装函数；
//	③ Wails 绑定入口（Result 形状、Logf 日志）。
//
// 与角色的关系：Pi 的 `--skill <path>` 收的是**路径**（也接受文件），可以重复，
// 配合 `--no-skills` 就是一份白名单。角色里存的是技能 id，编译时解析成路径——
// 存在 id 而不是路径，是为了让角色配置在换机器/换目录后仍然有效。

// skillInfo 是给界面看的技能卡片（实现见 internal/skills.Info）。
type skillInfo = skills.Info

// skillsRoot 是技能根目录：<agentDir>/skills。
func skillsRoot() string {
	dir := agentDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "skills")
}

// loadSkills 扫描技能目录，返回所有含 SKILL.md 的技能。
func loadSkills() []skillInfo {
	return skills.Load(skillsRoot())
}

// resolveSkillPaths 把角色里的技能 id 解析成 --skill 需要的路径。
func resolveSkillPaths(ids []string) (paths []string, missing []string) {
	return skills.Resolve(skillsRoot(), ids)
}

// skillSuggestions 给"找不到技能"的报错列出候选，用户才知道该填什么。
func skillSuggestions(limit int) []string {
	return skills.Suggestions(skillsRoot(), limit)
}

// dirExists 判断路径是不是一个存在的目录。
func dirExists(path string) bool {
	return skills.DirExists(path)
}

// ListSkills 列出技能卡片（含目录、体积、方向）。
func (s *PiService) ListSkills() (Result, error) {
	list := loadSkills()
	root := skillsRoot()
	payload, err := json.Marshal(map[string]any{
		"root":   root,
		"skills": list,
		"total":  len(list),
		// 让界面能提示"技能目录还不存在，要不要导入一批"
		"exists": skills.DirExists(root),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_skills", Success: true, Data: string(payload)}, nil
}

// ImportSkills 把一个目录下的技能复制进技能根目录。
func (s *PiService) ImportSkills(sourceDir string) (Result, error) {
	root := skillsRoot()
	imported, skipped, err := skills.Import(sourceDir, root)
	if err != nil {
		return Result{}, err
	}
	Logf("import_skills source=%s imported=%d skipped=%d", sourceDir, len(imported), len(skipped))
	if len(imported) > 0 {
		// 技能目录变了而角色定义没变：编译缓存的失效键盖不住它，必须手动作废
		roles.InvalidateCompileCache()
	}
	payload, _ := json.Marshal(map[string]any{
		"imported": imported,
		"skipped":  skipped,
		"root":     root,
		"total":    len(loadSkills()),
	})
	return Result{Command: "import_skills", Success: true, Data: string(payload)}, nil
}

// roleSkillPaths 是角色 → --skill 参数的桥梁：解析 id，顺便把缺失的记进日志。
func roleSkillPaths(skillIDs []string) []string {
	paths, missing := resolveSkillPaths(skillIDs)
	for _, name := range missing {
		Logf("role_skill_missing id=%s", name)
	}
	return paths
}

// 编译期断言：技能路径最终进的是 pi.Options.Skills（白名单，nil=不限制）。
var _ = func(opts *pi.Options, skills []string) { opts.Skills = skills }
