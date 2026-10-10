package roles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 角色导入导出。
//
// 为什么需要它：角色是"一套工作方式"（提示词 + 工具白名单 + 权限 + 模型 + 委派），
// 用户做出来之后必然想带走、分享、在另一台机器或另一份安装里复用。
// 没有这个功能时只能手拷 .pi/roles 目录——而真实的角色是**两个文件**
// （<id>.json 定义 + prompts/<id>.md 提示词），漏一个就成了空壳角色。
//
// 设计取舍：
//   · 导出成**一个自包含 JSON**（定义 + 提示词都在里面），而不是打包目录：
//     单文件好传、好 diff、好放仓库；目录形式留给"导入"侧兼容（社区包常是目录）。
//   · 导入同时接受**文件与目录**：目录按角色包约定读（*.json + prompts/*.md）。
//   · 写入一律走 Save：id 校验、提示词长度上限、UTF-8 边界这些规则只有一份。

const (
	// BundleFormat 是导出文件的 format 标记。
	BundleFormat  = "zeroa-roles"
	BundleVersion = 1
)

// Bundle 是导出文件的结构。
type Bundle struct {
	Format     string        `json:"format"`
	Version    int           `json:"version"`
	ExportedAt string        `json:"exportedAt"`
	App        string        `json:"app,omitempty"`
	Roles      []BundleEntry `json:"roles"`
}

// BundleEntry 一个角色的全部内容：定义 + 提示词正文。
type BundleEntry struct {
	Role   Role   `json:"role"`
	Prompt string `json:"prompt,omitempty"`
}

// Export 把角色导出成一个 JSON 文件，返回落盘路径与导出的角色 id 列表。
//
//   - roleIDs 为空 = 导出全部角色；
//   - requestedPath 为空 = 写到 <配置目录>/exports/roles-<时间戳>.json；
//   - requestedPath 是目录 = 在目录里放默认文件名；是文件 = 用它（自动补 .json 后缀）。
func Export(roleIDs []string, requestedPath string) (target string, ids []string, err error) {
	wanted := map[string]bool{}
	for _, id := range roleIDs {
		if normalized, idErr := RequireID(id); idErr == nil {
			wanted[normalized] = true
		}
	}

	views := Load()
	entries := make([]BundleEntry, 0, len(views))
	for _, view := range views {
		if len(wanted) > 0 && !wanted[strings.ToLower(view.Role.ID)] {
			continue
		}
		entry := BundleEntry{Role: view.Role}
		// 提示词跟着走：只导定义的话，导入方拿到的角色没有灵魂
		if path := PromptFileOf(view.Role); path != "" {
			if raw, readErr := os.ReadFile(path); readErr == nil {
				entry.Prompt = string(raw)
			}
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return "", nil, errors.New("没有可导出的角色")
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Role.ID < entries[j].Role.ID })

	bundle := Bundle{
		Format:     BundleFormat,
		Version:    BundleVersion,
		ExportedAt: time.Now().Format(time.RFC3339),
		App:        "ZeroA",
		Roles:      entries,
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", nil, err
	}

	target, err = ResolveExportPath(requestedPath)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(target, append(data, '\n'), 0o644); err != nil {
		return "", nil, err
	}
	ids = make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.Role.ID)
	}
	host.Logf("roles_exported path=%s count=%d", target, len(entries))
	return target, ids, nil
}

// ResolveExportPath 决定导出到哪个文件。
func ResolveExportPath(requested string) (string, error) {
	name := "roles-" + time.Now().Format("20060102-150405") + ".json"
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		dir := host.AgentDir()
		if dir == "" {
			return "", errors.New("找不到配置目录")
		}
		return filepath.Join(dir, "exports", name), nil
	}
	// 末尾是分隔符、或本身就是已存在的目录 → 当目录处理
	if strings.HasSuffix(trimmed, string(os.PathSeparator)) || strings.HasSuffix(trimmed, "/") {
		return filepath.Join(trimmed, name), nil
	}
	if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
		return filepath.Join(trimmed, name), nil
	}
	if !strings.EqualFold(filepath.Ext(trimmed), ".json") {
		trimmed += ".json"
	}
	return trimmed, nil
}

// Import 从文件或目录导入角色，返回逐条结果。
//
// 支持三种输入（都判一遍，用户不用记格式）：
//  1. 导出的自包含 JSON 包（format = zeroa-roles）；
//  2. 单个角色定义 JSON（有 id 字段）；
//  3. 目录：读里面的 *.json（可选 prompts/<id>.md）——社区包与 .pi/roles 都是这个形状。
func Import(source string) (created, overwritten []string, failed []map[string]string, err error) {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return nil, nil, nil, errors.New("请填写要导入的文件或目录路径")
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("找不到这个路径: %s", trimmed)
	}

	entries, err := ReadEntries(trimmed, info.IsDir())
	if err != nil {
		return nil, nil, nil, err
	}
	if len(entries) == 0 {
		return nil, nil, nil, errors.New("这个位置没有可导入的角色")
	}

	existing := map[string]bool{}
	for _, view := range Load() {
		existing[strings.ToLower(view.Role.ID)] = true
	}

	created = make([]string, 0, len(entries))
	overwritten = make([]string, 0, len(entries))
	failed = make([]map[string]string, 0)
	for _, entry := range entries {
		id, idErr := RequireID(entry.Role.ID)
		if idErr != nil {
			failed = append(failed, map[string]string{"id": entry.Role.ID, "error": idErr.Error()})
			continue
		}
		entry.Role.ID = id
		// 写入统一走 Save：校验与落盘只有一份实现
		if err := Save(entry.Role, entry.Prompt); err != nil {
			failed = append(failed, map[string]string{"id": id, "error": err.Error()})
			continue
		}
		if existing[id] {
			overwritten = append(overwritten, id)
		} else {
			created = append(created, id)
		}
		existing[id] = true
		host.Logf("role_imported id=%s source=%s", id, trimmed)
	}
	return created, overwritten, failed, nil
}

// ReadEntries 把"文件或目录"统一读成角色条目列表。
func ReadEntries(path string, isDir bool) ([]BundleEntry, error) {
	if !isDir {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return ParseBundle(raw, filepath.Dir(path))
	}

	entries := make([]BundleEntry, 0)
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.IsDir() || !strings.EqualFold(filepath.Ext(file.Name()), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(path, file.Name()))
		if err != nil {
			continue
		}
		parsed, err := ParseBundle(raw, path)
		if err != nil {
			// 目录里可能混着别的 JSON（比如 mcp.json），跳过而不是整体失败
			continue
		}
		entries = append(entries, parsed...)
	}
	return entries, nil
}

// ParseBundle 解析一个 JSON：自包含包 / 单角色 / 角色数组都能认。
// promptDir 用来在缺提示词正文时，按约定去找 prompts/<id>.md。
func ParseBundle(raw []byte, promptDir string) ([]BundleEntry, error) {
	// ① 自包含包
	var bundle Bundle
	if err := json.Unmarshal(raw, &bundle); err == nil && len(bundle.Roles) > 0 {
		for index := range bundle.Roles {
			// 导入的角色一律不是内置角色（内置是随程序分发的）——
			// 包里带 builtin=true 多半是"导出时它还是内置"，导进来不能照旧算
			bundle.Roles[index].Role.Builtin = false
			if bundle.Roles[index].Prompt == "" {
				bundle.Roles[index].Prompt = ReadPromptFromDir(promptDir, bundle.Roles[index].Role.ID)
			}
		}
		return bundle.Roles, nil
	}
	// ② 单个角色定义
	var single Role
	if err := json.Unmarshal(raw, &single); err == nil && strings.TrimSpace(single.ID) != "" {
		single.Builtin = false // 导入的角色一律不是内置角色（内置是随程序分发的）
		return []BundleEntry{{Role: single, Prompt: ReadPromptFromDir(promptDir, single.ID)}}, nil
	}
	// ③ 角色数组
	var list []Role
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 && strings.TrimSpace(list[0].ID) != "" {
		entries := make([]BundleEntry, 0, len(list))
		for _, role := range list {
			role.Builtin = false
			entries = append(entries, BundleEntry{Role: role, Prompt: ReadPromptFromDir(promptDir, role.ID)})
		}
		return entries, nil
	}
	return nil, errors.New("不是角色文件（既不是导出包，也不是角色定义）")
}

// ReadPromptFromDir 按 <dir>/prompts/<id>.md 找提示词（找不到返回空串）。
func ReadPromptFromDir(dir, roleID string) string {
	normalized, err := RequireID(roleID)
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, "prompts", normalized+".md"))
	if err != nil {
		return ""
	}
	return string(raw)
}
