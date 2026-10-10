// Package skills 负责技能（Skill）域：扫描 <agentDir>/skills 下含 SKILL.md 的目录、
// 解析 frontmatter 与体积、把角色里的技能 id 解析成 --skill 路径、以及整包导入。
//
// 技能 = 一个含 SKILL.md 的目录，Pi 只在系统提示里放它的名称与描述，
// 等真要用了才读全文（见 pi 的 docs/skills.md）。所以技能是"按需知识"，
// 很适合放方向性的资料：CTF 的 crypto/web/pwn 手册就属于这一类。
//
// 这里是纯文件 IO，不知道 PiService 的存在；主包 skills.go 只剩薄封装
// （skillsRoot 解析根目录、guiLog 日志、Wails 的 Result 形状）。
package skills

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Info 是给界面看的技能卡片。
type Info struct {
	// ID 是 frontmatter 里的 name（没有就退回目录名），角色里存的就是它
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Dir 是技能的绝对路径（--skill 用它）
	Dir string `json:"dir"`
	// Category 是技能根目录下的一级目录名（crypto/web/pwn…），界面上分组用
	Category     string   `json:"category"`
	License      string   `json:"license,omitempty"`
	AllowedTools []string `json:"allowedTools,omitempty"`
	// Files / Bytes 让用户知道选中它会带上多少资料（技能是目录，可能很大）
	Files         int   `json:"files"`
	Bytes         int64 `json:"bytes"`
	HasReferences bool  `json:"hasReferences"`
	UserInvocable bool  `json:"userInvocable"`
	// Broken 非空表示这个技能的 SKILL.md 有问题（缺 name/描述等），界面要标出来
	Broken string `json:"broken,omitempty"`
}

// categories 是有意义的目录名：只有这些一级目录下的技能会被当成"方向"。
// 放别的目录也不算错（仍会被列出），只是 category 会显示成目录名本身。
var categories = map[string]string{
	"crypto": "密码学", "web": "Web", "pwn": "Pwn", "reverse": "逆向",
	"forensics": "取证", "misc": "杂项", "cve": "漏洞", "ir": "应急",
}

// Load 扫描技能目录，返回所有含 SKILL.md 的技能。
//
// 用递归扫描而不是"只认一层目录"：Agent Skills 规范只要求"目录里有 SKILL.md"，
// 用户完全可能按 `skills/<作者>/<技能>/SKILL.md` 组织。
func Load(root string) []Info {
	if root == "" {
		return nil
	}
	skills := make([]Info, 0, 8)
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // 读不了的目录跳过，不影响其他技能
		}
		if !entry.IsDir() {
			return nil
		}
		// 别扫进 references/ scripts/ 这类资源目录深处：它们里面不会有第二个技能，
		// 但可能有成百上千个文件（CTF 技能的引用资料就是这种结构）。
		if nested := filepath.Base(path); nested == "references" || nested == "scripts" || nested == "assets" {
			return filepath.SkipDir
		}
		manifest := filepath.Join(path, "SKILL.md")
		info, statErr := os.Stat(manifest)
		if statErr != nil || info.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(manifest)
		if readErr != nil {
			return nil
		}
		skill := parseSkill(root, path, string(raw))
		skills = append(skills, skill)
		return filepath.SkipDir // 技能内部不再嵌套技能
	})

	sort.SliceStable(skills, func(i, j int) bool {
		if skills[i].Category != skills[j].Category {
			return skills[i].Category < skills[j].Category
		}
		return skills[i].ID < skills[j].ID
	})
	return skills
}

// parseSkill 解析 SKILL.md 的 frontmatter 并统计目录体积。
func parseSkill(root, dir, raw string) Info {
	meta := parseFrontmatter(raw)
	skill := Info{
		ID:          strings.TrimSpace(meta["name"]),
		Name:        strings.TrimSpace(meta["name"]),
		Description: strings.TrimSpace(meta["description"]),
		Dir:         dir,
		License:     strings.TrimSpace(meta["license"]),
		Category:    categoryOf(root, dir),
	}
	if skill.ID == "" {
		skill.ID = filepath.Base(dir)
		skill.Name = skill.ID
		skill.Broken = "SKILL.md 缺少 name（Pi 会退回目录名，但建议补上）"
	}
	if skill.Description == "" {
		skill.Broken = strings.TrimSpace(skill.Broken + "；缺少 description：模型无法判断什么时候该用它")
	}
	if tools := strings.TrimSpace(meta["allowed-tools"]); tools != "" {
		skill.AllowedTools = strings.Fields(strings.ReplaceAll(tools, ",", " "))
	}
	if strings.EqualFold(strings.TrimSpace(meta["user-invocable"]), "true") {
		skill.UserInvocable = true
	}
	// 统计体积：界面上要能看出"选中这个会带上多少资料"
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil {
			skill.Files++
			skill.Bytes += info.Size()
		}
		if filepath.Base(filepath.Dir(path)) == "references" {
			skill.HasReferences = true
		}
		return nil
	})
	return skill
}

// categoryOf 取技能根目录下的第一级目录名作为方向。
func categoryOf(root, dir string) string {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) <= 1 {
		return "" // 直接躺在 skills/ 下的技能没有方向
	}
	return parts[0]
}

// parseFrontmatter 从 SKILL.md 里取出 frontmatter 的顶层键值。
//
// 刻意不引入 YAML 依赖：我们只要几个顶层标量（name/description/license/
// allowed-tools/user-invocable），而技能文件是用户可随手编辑的，
// 写坏了也应该是"这个技能标记为有问题"，不能让整个列表读不出来。
func parseFrontmatter(raw string) map[string]string {
	meta := map[string]string{}
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(text, "---") {
		return meta
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines[1:] {
		trimmed := strings.TrimRight(line, " \t")
		if strings.TrimSpace(trimmed) == "---" {
			break
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			// 缩进行是上一级的子键（如 metadata.user-invocable）：
			// 取出来单独存，避免和顶层同名字段打架
			if key, value, ok := splitYAMLPair(strings.TrimSpace(trimmed)); ok {
				if _, exists := meta[key]; !exists {
					meta[key] = value
				}
			}
			continue
		}
		if key, value, ok := splitYAMLPair(trimmed); ok {
			meta[key] = value
		}
	}
	return meta
}

// splitYAMLPair 拆 "key: value"，并去掉值两端的引号。
func splitYAMLPair(line string) (key, value string, ok bool) {
	index := strings.Index(line, ":")
	if index <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:index])
	value = strings.TrimSpace(line[index+1:])
	value = strings.Trim(value, `"'`)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

// Resolve 把角色里的技能 id 解析成 --skill 需要的路径。
//
// 三种写法都接受，顺序即优先级：
//  1. frontmatter 的 name（推荐：跨机器稳定）
//  2. 技能目录名
//  3. 直接写路径（技能目录或 SKILL.md 文件本身）
//
// 解析不出来的返回在 missing 里，由调用方决定是报错（保存时）还是跳过（启动时）。
func Resolve(root string, ids []string) (paths []string, missing []string) {
	if len(ids) == 0 {
		return nil, nil
	}
	known := Load(root)
	for _, id := range ids {
		value := strings.TrimSpace(id)
		if value == "" {
			continue
		}
		matched := false
		for _, skill := range known {
			if strings.EqualFold(skill.ID, value) || strings.EqualFold(filepath.Base(skill.Dir), value) {
				paths = append(paths, skill.Dir)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		// 退一步：当成路径。技能是用户自己的文件，允许绕过扫描直接指目录。
		if _, err := os.Stat(value); err == nil {
			paths = append(paths, value)
			continue
		}
		missing = append(missing, value)
	}
	return paths, missing
}

// Suggestions 给"找不到技能"的报错列出候选，用户才知道该填什么。
func Suggestions(root string, limit int) []string {
	known := Load(root)
	names := make([]string, 0, len(known))
	for _, skill := range known {
		names = append(names, skill.ID)
	}
	if len(names) > limit {
		names = names[:limit]
	}
	return names
}

// fileLimit 限制单个技能的文件数：导入时防止用户误选一个巨大的目录
// （比如整个 CTF 工具包），把技能根目录塞爆。
const fileLimit = 4000

// Import 把一个目录下的技能复制进技能根目录。
//
// 用途：技能包通常以"作者/技能/SKILL.md"的形式分发（CTF-BTFly 的 defaults/skills
// 就是这样）。导入 = 找到里面所有含 SKILL.md 的目录，逐个复制到 root。
// 已存在的同名目录不覆盖（返回 skipped），避免把用户改过的技能冲掉。
func Import(src, root string) (imported, skipped []string, err error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil, errors.New("请选择要导入的目录")
	}
	info, statErr := os.Stat(src)
	if statErr != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("目录不存在: %s", src)
	}
	if root == "" {
		return nil, nil, errors.New("找不到配置目录")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, err
	}

	candidates := findSkillDirs(src)
	if len(candidates) == 0 {
		return nil, nil, errors.New("这个目录里没有找到 SKILL.md（技能目录必须包含 SKILL.md）")
	}
	// 技能包常见的分发形态是"外面套一层目录"（defaults/skills/agentskills/crypto/…）。
	// 如果所有技能都在同一个包装目录下，就把这一层去掉：否则复制过去会变成
	// skills/agentskills/crypto，方向（category）也丢了。
	wrapper := commonWrapper(src, candidates)
	for _, dir := range candidates {
		// 目标是"技能根目录 + 相对路径"，保留方向分组（crypto/web…）
		rel, relErr := filepath.Rel(src, dir)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(dir)
		}
		slashRel := filepath.ToSlash(rel)
		if wrapper != "" && strings.HasPrefix(slashRel, wrapper+"/") {
			slashRel = strings.TrimPrefix(slashRel, wrapper+"/")
		}
		rel = filepath.FromSlash(slashRel)
		target := filepath.Join(root, rel)
		if DirExists(target) {
			skipped = append(skipped, slashRel)
			continue
		}
		if copyErr := copyTree(dir, target); copyErr != nil {
			return nil, nil, fmt.Errorf("复制 %s 失败: %w", rel, copyErr)
		}
		imported = append(imported, slashRel)
	}
	return imported, skipped, nil
}

// commonWrapper 找出"所有技能共有的那层包装目录"（没有就返回空串）。
//
// 判定条件要严格，否则会把真正的方向目录当成包装层剥掉：
//   - 所有候选都必须有相同的首段目录；
//   - 该首段不能是已知方向名（crypto/web/pwn…）；
//   - 剥掉之后每个候选还得至少还剩一段（否则会把技能自己剥没）。
func commonWrapper(src string, dirs []string) string {
	if len(dirs) < 2 {
		return ""
	}
	wrapper := ""
	for _, dir := range dirs {
		rel, err := filepath.Rel(src, dir)
		if err != nil {
			return ""
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 {
			return "" // 有技能直接躺在根下：不剥，避免路径不一致
		}
		if wrapper == "" {
			wrapper = parts[0]
			continue
		}
		if parts[0] != wrapper {
			return ""
		}
	}
	if wrapper == "" || categories[wrapper] != "" {
		return ""
	}
	return wrapper
}

// findSkillDirs 找出一个目录下所有直接的技能目录（含 SKILL.md），不再往下钻。
func findSkillDirs(src string) []string {
	var found []string
	_ = filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if path != src {
			if _, statErr := os.Stat(filepath.Join(path, "SKILL.md")); statErr == nil {
				found = append(found, path)
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Strings(found)
	return found
}

// copyTree 递归复制目录，带文件数上限。
func copyTree(src, dst string) error {
	files := 0
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		files++
		if files > fileLimit {
			return fmt.Errorf("技能文件数超过上限 %d: %s", fileLimit, rel)
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if info.Size() > 32<<20 {
			return fmt.Errorf("文件过大（>32MB）: %s", rel)
		}
		source, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer source.Close()
		file, createErr := os.Create(target)
		if createErr != nil {
			return createErr
		}
		defer file.Close()
		if _, copyErr := io.Copy(file, source); copyErr != nil {
			return copyErr
		}
		return nil
	})
}

// DirExists 判断路径是不是一个存在的目录。
func DirExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
