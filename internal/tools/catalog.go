package tools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 工具目录（tool catalog）：把"本机有哪些工具、它们对应哪些能力、分在哪个方向"
// 从写死的 Go 代码搬进 JSON。
//
// 三层来源（优先级从低到高）：
//  1. 内置默认：//go:embed tool-catalog.default.json，随程序分发，永远兜底；
//  2. 用户级：<数据目录>/tool-catalog.json；
//  3. 项目级：<工作区>/.pi/tool-catalog.json（团队共享，可进 git）。
//
// 合并语义（4 条，必须唯一且可预期）：
//   - 同 id：字段级覆盖（只写要改的字段，其余继承）；
//   - 新 id：追加；
//   - "disabled": true：删掉该条；
//   - "mode": "replace"：整份替换（企业统一定制用）。
//
// 坏文件绝不静默变空清单：解析失败记 Issue 并跳过，最终仍有内置默认可用——
// 否则角色会莫名其妙"没有能力"，而且查不出原因。

//go:embed tool-catalog.default.json
var defaultCatalogJSON []byte

/**
 * 下面三个别名把"文件形态"导出给写入方（主程序 / AI 工具）使用。
 *
 * 为什么不把内部结构直接导出：合并逻辑需要指针字段来表达"这次写了没有"，
 * 那是实现细节；外部（写清单的那一侧）只需要能构造/读一份文件结构。
 */
type (
	// FileTool 是清单文件里的一条工具
	FileTool = fileTool
	// FileDetect 是文件里的 detect 段
	FileDetect = fileDetect
	// CatalogFile 是清单文件结构
	CatalogFile = catalogFile
)

// StripJSONComments 去掉 // 注释与 BOM（写回时也要先把旧文件读成合法 JSON）。
func StripJSONComments(text string) string { return stripJSONComments(text) }

// NormalizeCatalogID 校验并规范化工具 id（写入侧必须用同一套规则）。
func NormalizeCatalogID(id string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(id))
	if trimmed == "" {
		return "", errors.New("缺少 id")
	}
	if len(trimmed) > 64 {
		return "", errors.New("id 太长（上限 64）")
	}
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_', char == '.':
		default:
			return "", fmt.Errorf("id 只能用字母数字与 - _ . ：%q", id)
		}
	}
	return trimmed, nil
}

// CatalogDirection 是一个方向（分组）及其命令行线索。
type CatalogDirection struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Hints 是"这个方向常用的命令行工具名"，只用于提示与「插入提示词」。
	// 它是纯展示信息：真正能被探测的是 tools[]。
	Hints []string `json:"hints,omitempty"`
}

// CapabilityMeta 给能力一个中文名/说明（界面显示用，可选）。
type CapabilityMeta struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// CapabilityRule 是"工具名 → 能力"的通配规则。
//
// 用途：给自动发现的工具（扩展注册的、MCP 提供的）挂能力，而不必把它们写进清单。
// 例如 {"match": "web_search*", "capabilities": ["web.search"]}。
type CapabilityRule struct {
	Match        string   `json:"match"`
	Capabilities []string `json:"capabilities"`
}

// Catalog 是合并后的最终目录。
type Catalog struct {
	Version int
	// SearchPaths 是"额外搜索目录"：工具箱自带目录、pip 的 Scripts 目录等。
	// 只查 PATH 必然漏掉这些（实测：E:\...\Code4Hack\tools\bin\ffuf 就在 PATH 外）。
	SearchPaths  []string
	Directions   []CatalogDirection
	Tools        []Definition
	Capabilities []CapabilityMeta
	Rules        []CapabilityRule
	// Paths 是真正加载成功的文件（诊断用）
	Paths []string
	// Issues 是跳过或降级处理的地方（界面要能说清"我改了为什么没生效"）
	Issues []Issue
}

// Issue 是一条加载问题。
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

/* ---------- 文件形态：指针字段表示"这次写了没有"，才能做字段级覆盖 ---------- */

type fileDetect struct {
	Commands     *[]string `json:"commands,omitempty"`
	PythonImport *string   `json:"pythonImport,omitempty"`
	NodeModule   *string   `json:"nodeModule,omitempty"`
	Paths        *[]string `json:"paths,omitempty"`
}

type fileRisk struct {
	Shell      *bool   `json:"shell,omitempty"`
	Filesystem *string `json:"filesystem,omitempty"`
	Network    *bool   `json:"network,omitempty"`
}

type fileTool struct {
	ID            string      `json:"id"`
	Disabled      bool        `json:"disabled,omitempty"`
	Name          *string     `json:"name,omitempty"`
	Type          *string     `json:"type,omitempty"`
	Direction     *string     `json:"direction,omitempty"`
	Capabilities  *[]string   `json:"capabilities,omitempty"`
	Detect        *fileDetect `json:"detect,omitempty"`
	VersionArgs   *[]string   `json:"versionArgs,omitempty"`
	Note          *string     `json:"note,omitempty"`
	Risk          *fileRisk   `json:"risk,omitempty"`
	AlwaysPresent *bool       `json:"alwaysPresent,omitempty"`
	// Path：检测确认过的真实路径（写回清单，下次直接命中；PATH 外的工具只能靠它记住）
	Path *string `json:"path,omitempty"`
	// AddedBy：这条是谁加进来的：user / ai / detect
	AddedBy *string `json:"addedBy,omitempty"`
}

type catalogFile struct {
	Version         int                `json:"version"`
	SearchPaths     []string           `json:"searchPaths,omitempty"`
	Mode            string             `json:"mode,omitempty"`
	Directions      []CatalogDirection `json:"directions,omitempty"`
	Tools           []fileTool         `json:"tools,omitempty"`
	Capabilities    []CapabilityMeta   `json:"capabilities,omitempty"`
	CapabilityRules []CapabilityRule   `json:"capabilityRules,omitempty"`
}

/* ------------------------------- 加载与合并 ------------------------------- */

var (
	catalogMu     sync.RWMutex
	activeCatalog = mustDefaultCatalog()
)

// DefaultCatalog 返回内置默认目录（解析失败时是空目录：程序仍要能起来）。
func DefaultCatalog() Catalog {
	return mustDefaultCatalog()
}

func mustDefaultCatalog() Catalog {
	var file catalogFile
	if err := json.Unmarshal(defaultCatalogJSON, &file); err != nil {
		return Catalog{Version: 1}
	}
	return catalogFromFile(file, Catalog{Version: file.Version})
}

// LoadCatalog 读入若干文件并按优先级合并（paths 从低到高），返回最终目录。
// 文件不存在不算错误（零配置可用）；解析失败记 Issue 后跳过该文件。
func LoadCatalog(paths ...string) Catalog {
	catalog := DefaultCatalog()
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		raw, err := os.ReadFile(trimmed)
		if err != nil {
			if !os.IsNotExist(err) {
				catalog.Issues = append(catalog.Issues, Issue{Path: trimmed, Message: "读不了: " + err.Error()})
			}
			continue
		}
		var file catalogFile
		// 我们自己导出的清单带 // 注释（给用户讲解合并规则），而 JSON 标准不允许注释。
		// 所以先剥掉注释再解析——否则"导出 → 改 → 重新加载"这条最自然的用法会直接失败。
		cleaned := stripJSONComments(string(raw))
		decoder := json.NewDecoder(strings.NewReader(cleaned))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&file); err != nil {
			// 未知字段也当成错误报出来：写错字段名（比如 "command" 写成 "commands"）
			// 是最常见的一类"改了没生效"，静默忽略会让人查半天。
			catalog.Issues = append(catalog.Issues, Issue{Path: trimmed, Message: "JSON 无效: " + err.Error()})
			continue
		}
		base := catalog
		if strings.EqualFold(strings.TrimSpace(file.Mode), "replace") {
			// 整份替换：只保留这个文件里的内容（内置默认不再参与）
			base = Catalog{Version: file.Version}
		}
		catalog = catalogFromFile(file, base)
		catalog.Paths = append(catalog.Paths, trimmed)
	}
	catalog.Issues = append(catalog.Issues, validateCatalog(&catalog)...)
	return catalog
}

/**
 * stripJSONComments 去掉 // 行注释与 UTF-8 BOM。
 *
 * 只处理整行注释与行尾注释，够用且不会误伤字符串里的 "//"（例如 URL）：
 * 判断方式是"// 之前没有未闭合的引号"。单个文件的注释量很小，不值得上完整词法分析。
 */
func stripJSONComments(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if at := indexOfComment(line); at >= 0 {
			line = line[:at]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// indexOfComment 找行尾 // 注释的位置（引号内的不算）。
func indexOfComment(line string) int {
	inString := false
	escaped := false
	for index := 0; index+1 < len(line); index++ {
		char := line[index]
		if escaped {
			escaped = false
			continue
		}
		switch char {
		case '\\':
			if inString {
				escaped = true
			}
		case '"':
			inString = !inString
		case '/':
			if !inString && line[index+1] == '/' {
				return index
			}
		}
	}
	return -1
}

// SetActiveCatalog 装上一份已加载的目录（进程内全局，界面与角色编译都读它）。
func SetActiveCatalog(catalog Catalog) {
	catalogMu.Lock()
	activeCatalog = catalog
	catalogMu.Unlock()
	// 目录变了，检测缓存必须失效：否则新加的条目会拿着旧状态
	ResetCache()
}

// ActiveCatalog 返回当前生效的目录。
func ActiveCatalog() Catalog {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return activeCatalog
}

// DirectionLabel 返回方向中文名（未知方向原样返回 id）。
func DirectionLabel(id string) string {
	for _, direction := range ActiveCatalog().Directions {
		if direction.ID == id {
			return direction.Label
		}
	}
	return id
}

// DirectionHints 返回方向的命令行线索。
func DirectionHints(id string) []string {
	for _, direction := range ActiveCatalog().Directions {
		if direction.ID == id {
			return direction.Hints
		}
	}
	return nil
}

func catalogFromFile(file catalogFile, base Catalog) Catalog {
	out := Catalog{
		Version:      base.Version,
		SearchPaths:  append([]string(nil), base.SearchPaths...),
		Directions:   append([]CatalogDirection(nil), base.Directions...),
		Tools:        append([]Definition(nil), base.Tools...),
		Capabilities: append([]CapabilityMeta(nil), base.Capabilities...),
		Rules:        append([]CapabilityRule(nil), base.Rules...),
		Paths:        base.Paths,
		Issues:       base.Issues,
	}
	if file.Version > 0 {
		out.Version = file.Version
	}
	if file.SearchPaths != nil {
		// 追加而不是替换：内置默认可能带公共目录，用户再加自己的
		out.SearchPaths = append(out.SearchPaths, file.SearchPaths...)
	}

	// 方向：同 id 覆盖 label/hints，新 id 追加
	for _, direction := range file.Directions {
		id := strings.TrimSpace(direction.ID)
		if id == "" {
			continue
		}
		replaced := false
		for index := range out.Directions {
			if out.Directions[index].ID == id {
				if strings.TrimSpace(direction.Label) != "" {
					out.Directions[index].Label = direction.Label
				}
				if direction.Hints != nil {
					out.Directions[index].Hints = direction.Hints
				}
				replaced = true
				break
			}
		}
		if !replaced {
			out.Directions = append(out.Directions, CatalogDirection{ID: id, Label: direction.Label, Hints: direction.Hints})
		}
	}

	// 工具：同 id 字段级覆盖 / 新 id 追加 / disabled 删除
	for _, entry := range file.Tools {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		index := -1
		for i := range out.Tools {
			if out.Tools[i].ID == id {
				index = i
				break
			}
		}
		if entry.Disabled {
			if index >= 0 {
				out.Tools = append(out.Tools[:index], out.Tools[index+1:]...)
			}
			continue
		}
		definition := Definition{ID: id}
		if index >= 0 {
			definition = out.Tools[index]
		}
		applyFileTool(&definition, entry)
		if index >= 0 {
			out.Tools[index] = definition
		} else {
			out.Tools = append(out.Tools, definition)
		}
	}

	// 能力中文名：同 id 覆盖
	for _, meta := range file.Capabilities {
		id := strings.TrimSpace(meta.ID)
		if id == "" {
			continue
		}
		replaced := false
		for index := range out.Capabilities {
			if out.Capabilities[index].ID == id {
				if strings.TrimSpace(meta.Label) != "" {
					out.Capabilities[index].Label = meta.Label
				}
				replaced = true
				break
			}
		}
		if !replaced {
			out.Capabilities = append(out.Capabilities, CapabilityMeta{ID: id, Label: meta.Label})
		}
	}

	// 规则：追加（同 match 后写的生效，靠遍历顺序）
	for _, rule := range file.CapabilityRules {
		if strings.TrimSpace(rule.Match) == "" || len(rule.Capabilities) == 0 {
			continue
		}
		out.Rules = append(out.Rules, rule)
	}

	sort.SliceStable(out.Tools, func(i, j int) bool {
		if out.Tools[i].Direction != out.Tools[j].Direction {
			return out.Tools[i].Direction < out.Tools[j].Direction
		}
		return out.Tools[i].ID < out.Tools[j].ID
	})
	return out
}

func applyFileTool(definition *Definition, entry fileTool) {
	if entry.Name != nil {
		definition.Name = *entry.Name
	}
	if entry.Type != nil {
		definition.Type = Kind(strings.TrimSpace(*entry.Type))
	}
	if entry.Direction != nil {
		definition.Direction = strings.TrimSpace(*entry.Direction)
	}
	if entry.Capabilities != nil {
		definition.Capabilities = *entry.Capabilities
	}
	if entry.VersionArgs != nil {
		definition.VersionArgs = *entry.VersionArgs
	}
	if entry.Note != nil {
		definition.Note = *entry.Note
	}
	if entry.AlwaysPresent != nil {
		definition.AlwaysPresent = *entry.AlwaysPresent
	}
	if entry.Path != nil {
		definition.Path = strings.TrimSpace(*entry.Path)
	}
	if entry.AddedBy != nil {
		definition.AddedBy = strings.TrimSpace(*entry.AddedBy)
	}
	if entry.Detect != nil {
		if entry.Detect.Commands != nil {
			definition.Detect.Commands = *entry.Detect.Commands
		}
		if entry.Detect.PythonImport != nil {
			definition.Detect.PythonImport = *entry.Detect.PythonImport
		}
		if entry.Detect.NodeModule != nil {
			definition.Detect.NodeModule = *entry.Detect.NodeModule
		}
		if entry.Detect.Paths != nil {
			definition.Detect.Paths = *entry.Detect.Paths
		}
	}
	if entry.Risk != nil {
		if entry.Risk.Shell != nil {
			definition.Risk.Shell = *entry.Risk.Shell
		}
		if entry.Risk.Filesystem != nil {
			definition.Risk.Filesystem = *entry.Risk.Filesystem
		}
		if entry.Risk.Network != nil {
			definition.Risk.Network = *entry.Risk.Network
		}
	}
	// 名字缺省用 id：少了必填字段不该让整条失效
	if strings.TrimSpace(definition.Name) == "" {
		definition.Name = definition.ID
	}
	if definition.Type == "" {
		definition.Type = KindCommand
	}
}

// validateCatalog 做"能救则救、不能救就报"的校验，并返回问题列表。
func validateCatalog(catalog *Catalog) []Issue {
	var issues []Issue
	known := map[string]bool{
		string(KindBuiltin): true, string(KindCommand): true, string(KindPythonPackage): true,
		string(KindNodePackage): true, string(KindApplication): true, string(KindExtension): true,
		string(KindService): true, string(KindMCP): true,
	}
	directions := map[string]bool{}
	for _, direction := range catalog.Directions {
		directions[direction.ID] = true
	}
	for index := range catalog.Tools {
		tool := &catalog.Tools[index]
		if !known[string(tool.Type)] {
			issues = append(issues, Issue{Path: "tool:" + tool.ID, Message: fmt.Sprintf("未知类型 %q，已按 command 处理", tool.Type)})
			tool.Type = KindCommand
		}
		if tool.Direction == "" {
			tool.Direction = "base"
		}
		if !directions[tool.Direction] {
			// 方向没声明就自动补一个（否则界面分组会丢条目）
			catalog.Directions = append(catalog.Directions, CatalogDirection{ID: tool.Direction, Label: tool.Direction})
			directions[tool.Direction] = true
		}
		for _, capability := range tool.Capabilities {
			if !strings.Contains(capability, ".") {
				issues = append(issues, Issue{Path: "tool:" + tool.ID, Message: fmt.Sprintf("能力名 %q 建议用 段.名 形式（如 crypto.rsa）", capability)})
			}
		}
		if len(tool.Detect.Commands) == 0 && tool.Detect.PythonImport == "" && tool.Detect.NodeModule == "" && len(tool.Detect.Paths) == 0 {
			issues = append(issues, Issue{Path: "tool:" + tool.ID, Message: "没有写 detect：它永远不会被判定为可用"})
		}
	}
	// 引用不存在能力的规则：提示但不拦（能力可以是纯声明）
	return issues
}

// MatchRules 返回适用于某个工具名的能力（通配 * 与 ?）。
func MatchRules(name string) []string {
	var out []string
	for _, rule := range ActiveCatalog().Rules {
		if wildcardMatch(rule.Match, name) {
			out = append(out, rule.Capabilities...)
		}
	}
	return out
}

func wildcardMatch(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	rest := value[len(parts[0]):]
	for index := 1; index < len(parts); index++ {
		part := parts[index]
		if part == "" {
			continue
		}
		at := strings.Index(rest, part)
		if at < 0 {
			return false
		}
		rest = rest[at+len(part):]
	}
	return strings.HasSuffix(value, parts[len(parts)-1])
}

// ExportCatalog 把当前目录导出成 JSON 文本（界面「导出当前清单」用）。
func ExportCatalog() (string, error) {
	catalog := ActiveCatalog()
	file := catalogFile{
		Version:         1,
		Directions:      catalog.Directions,
		Capabilities:    catalog.Capabilities,
		CapabilityRules: catalog.Rules,
	}
	for _, tool := range catalog.Tools {
		file.Tools = append(file.Tools, fileToolFromDefinition(tool))
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", err
	}
	header := "// ZeroA 工具清单：改完保存，点「重新加载」即时生效（无需重启）。\n" +
		"// 合并规则：同 id 覆盖字段 / 新 id 追加 / \"disabled\": true 删除 / \"mode\": \"replace\" 整份替换。\n" +
		"// 各字段含义见 docs/user/02-工具配置.md。\n\n"
	return header + string(data) + "\n", nil
}

func fileToolFromDefinition(tool Definition) fileTool {
	commands := tool.Detect.Commands
	paths := tool.Detect.Paths
	capabilities := tool.Capabilities
	versionArgs := tool.VersionArgs
	name, typeName, direction, note := tool.Name, string(tool.Type), tool.Direction, tool.Note
	alwaysPresent := tool.AlwaysPresent
	shell, network, filesystem := tool.Risk.Shell, tool.Risk.Network, tool.Risk.Filesystem
	addedBy := tool.AddedBy
	recordedPath := tool.Path
	entry := fileTool{
		ID: tool.ID, Name: &name, Type: &typeName, Direction: &direction,
		AddedBy: &addedBy, Path: &recordedPath,
		Capabilities: &capabilities, Note: &note, AlwaysPresent: &alwaysPresent,
		VersionArgs: &versionArgs,
		Detect:      &fileDetect{Commands: &commands, Paths: &paths},
		Risk:        &fileRisk{Shell: &shell, Network: &network, Filesystem: &filesystem},
	}
	if tool.Detect.PythonImport != "" {
		entry.Detect.PythonImport = &tool.Detect.PythonImport
	}
	if tool.Detect.NodeModule != "" {
		entry.Detect.NodeModule = &tool.Detect.NodeModule
	}
	return entry
}

// DefaultCatalogPath 是内置默认清单在磁盘上的相对位置（文档与界面提示用）。
func DefaultCatalogPath(agentDir string) string {
	if strings.TrimSpace(agentDir) == "" {
		return "tool-catalog.json"
	}
	return filepath.Join(agentDir, "tool-catalog.json")
}
