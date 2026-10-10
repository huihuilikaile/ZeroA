package bridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zeroa/internal/pi"
)

// Pi 扩展包（已安装的包）。
//
// 两层来源，优先用权威的那个：
//
//  1. **`pi list`**——Pi 自己的命令，认所有安装方式（npm / git / 本地路径）、
//     认个人级与项目级作用域，并给出解析后的真实路径。这是"装了哪些包"的权威答案。
//  2. 扫 `<数据目录>/npm/node_modules`——`pi list` 不可用时的兜底（例如 Pi 还没解析到）。
//
// 识别一个目录是不是 Pi 包的规则照官方文档（docs/packages.md）来，不是我们自己猜的：
//   - package.json 里有 `pi` manifest（显式声明 extensions/skills/prompts/themes）；
//   - **或者**按约定目录 discovery：存在 extensions/ / skills/ / prompts/ / themes/；
//   - **或者** keywords 里有 "pi-package"（gallery 收录标记）。
//
// 第 2 条很关键：很多包不写 manifest，靠约定目录就被 Pi 加载了。
// 只认 manifest 会漏报——用户会看到"装了这个包，界面里却没有"。

// piPackage 是一个已安装的 Pi 包。
type piPackage struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope,omitempty"` // user / project（来自 pi list 的分段）
	Path        string `json:"path,omitempty"`
	Source      string `json:"source,omitempty"` // npm:xxx / git:... / 本地路径
	Explicit    bool   `json:"explicit"`         // 是否在 settings.json 的 packages 里显式声明
	// Resources 是这个包提供的东西（extensions / skills / prompts / themes）
	Resources []string `json:"resources,omitempty"`
	// Tools 是"这个包提供哪些工具"的最佳努力归属（扫包内容找工具名）
	Tools []string `json:"tools,omitempty"`
}

// piPackageReport 是给界面的完整结果。
type piPackageReport struct {
	Source string      `json:"source"` // "pi list" 或 "scan"
	Note   string      `json:"note,omitempty"`
	Items  []piPackage `json:"items"`
}

type piPackageCache struct {
	mu      sync.Mutex
	key     string
	report  piPackageReport
	items   []piPackage
	fetched time.Time
}

var piPackagesCache piPackageCache

// installedPiPackages 返回已装包（带工具归属）。toolNames 是要归属的工具名。
func (s *PiService) installedPiPackages(toolNames []string) piPackageReport {
	report := s.packageReport()
	items := make([]piPackage, len(report.Items))
	copy(items, report.Items)
	for index := range items {
		if items[index].Path == "" {
			continue
		}
		for _, tool := range toolNames {
			// 只归属"扩展注册的工具"：Pi 内置名（read/bash/edit…）是通用词，
			// 扫包内容必然命中一堆无关文件，界面上会变成"每个包都提供 bash"。
			if tool == "" || tool == "mcp" || strings.HasPrefix(tool, "mcp__") || isBuiltinPiTool(strings.ToLower(tool)) {
				continue
			}
			if packageMentionsTool(items[index].Path, tool) {
				items[index].Tools = append(items[index].Tools, tool)
			}
		}
		sort.Strings(items[index].Tools)
	}
	report.Items = items
	return report
}

// packageReport 取"已装包"清单（带缓存，`pi list` 每次调用要起一个 node 进程）。
func (s *PiService) packageReport() piPackageReport {
	piPackagesCache.mu.Lock()
	if !piPackagesCache.fetched.IsZero() && time.Since(piPackagesCache.fetched) < 30*time.Second {
		cached := piPackagesCache.report
		piPackagesCache.mu.Unlock()
		return cached
	}
	piPackagesCache.mu.Unlock()

	report := piPackageReport{Source: "scan", Note: "pi list 不可用，改扫 npm 目录（可能漏掉 git / 本地路径安装的包）"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	out, listErr := pi.ListPackages(ctx, agentDir())
	cancel()
	if listErr == nil {
		if items := parsePiListOutput(out); len(items) > 0 {
			report = piPackageReport{Source: "pi list", Items: items}
		}
	}
	if len(report.Items) == 0 {
		report.Items = scanNpmPackages()
	}
	for index := range report.Items {
		enrichPackage(&report.Items[index])
	}
	sort.SliceStable(report.Items, func(i, j int) bool { return report.Items[i].Name < report.Items[j].Name })

	piPackagesCache.mu.Lock()
	piPackagesCache.report = report
	piPackagesCache.fetched = time.Now()
	piPackagesCache.mu.Unlock()
	return report
}

/**
 * parsePiListOutput 解析 `pi list` 的输出。
 *
 * 实际形状（缩进即层级，分段即作用域）：
 *
 *	User packages:
 *	  npm:pi-mcp-adapter
 *	    E:\...\.pi\npm\node_modules\pi-mcp-adapter
 *	  npm:pi-web-access
 *	    E:\...\.pi\npm\node_modules\pi-web-access
 */
func parsePiListOutput(text string) []piPackage {
	var out []piPackage
	scope := ""
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		line := strings.TrimSpace(raw)
		switch {
		case indent == 0:
			// 分段标题：User packages: / Project packages: / Errors: …
			lower := strings.ToLower(line)
			switch {
			case strings.Contains(lower, "project"):
				scope = "project"
			case strings.Contains(lower, "user"):
				scope = "user"
			default:
				scope = ""
			}
		case indent <= 2:
			item := piPackage{Source: line, Scope: scope, Explicit: true}
			out = append(out, item)
		default:
			// 更深的一层是解析后的路径：贴到上一条上
			if len(out) > 0 && out[len(out)-1].Path == "" {
				out[len(out)-1].Path = line
			}
		}
	}
	return out
}

// scanNpmPackages 扫 npm 目录作为兜底（识别规则与 Pi 的官方发现规则一致）。
func scanNpmPackages() []piPackage {
	root := npmRoot()
	if root == "" {
		return nil
	}
	modules := filepath.Join(root, "node_modules")
	entries, err := os.ReadDir(modules)
	if err != nil {
		return nil
	}
	explicit := explicitDependencies(root)
	var out []piPackage
	add := func(dir, name string) {
		if pkg, ok := readPiPackage(dir, name, explicit); ok {
			out = append(out, pkg)
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "@") {
			scoped, err := os.ReadDir(filepath.Join(modules, name))
			if err != nil {
				continue
			}
			for _, inner := range scoped {
				if inner.IsDir() {
					add(filepath.Join(modules, name, inner.Name()), name+"/"+inner.Name())
				}
			}
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		add(filepath.Join(modules, name), name)
	}
	return out
}

// readPiPackage 读一个包并判断"它是不是 Pi 包"（规则见文件头注释）。
func readPiPackage(dir, name string, explicit map[string]bool) (piPackage, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return piPackage{}, false
	}
	var manifest struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Keywords    []string        `json:"keywords"`
		PI          json.RawMessage `json:"pi"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return piPackage{}, false
	}
	if manifest.Name == "" {
		manifest.Name = name
	}

	var resources []string
	if len(manifest.PI) > 0 {
		var piManifest struct {
			Extensions []string `json:"extensions"`
			Skills     []string `json:"skills"`
			Prompts    []string `json:"prompts"`
			Themes     []string `json:"themes"`
		}
		if json.Unmarshal(manifest.PI, &piManifest) == nil {
			if len(piManifest.Extensions) > 0 {
				resources = append(resources, "extensions")
			}
			if len(piManifest.Skills) > 0 {
				resources = append(resources, "skills")
			}
			if len(piManifest.Prompts) > 0 {
				resources = append(resources, "prompts")
			}
			if len(piManifest.Themes) > 0 {
				resources = append(resources, "themes")
			}
		}
	}
	// 约定目录 discovery：不写 manifest 也能被 Pi 加载的包
	for _, dirName := range []string{"extensions", "skills", "prompts", "themes"} {
		if listHas(resources, dirName) {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, dirName)); err == nil && info.IsDir() {
			resources = append(resources, dirName)
		}
	}
	// gallery 收录标记
	for _, keyword := range manifest.Keywords {
		if strings.EqualFold(strings.TrimSpace(keyword), "pi-package") {
			if len(resources) == 0 {
				resources = append(resources, "pi-package")
			}
			break
		}
	}
	if len(resources) == 0 {
		return piPackage{}, false
	}
	sort.Strings(resources)
	return piPackage{
		Name:        manifest.Name,
		Version:     manifest.Version,
		Description: manifest.Description,
		Path:        dir,
		Explicit:    explicit[manifest.Name],
		Resources:   resources,
	}, true
}

// enrichPackage 补齐 pi list 只给了路径的那些字段。
func enrichPackage(pkg *piPackage) {
	if pkg.Path == "" {
		// pi list 只给了 source（npm:xxx）：尝试在常见位置找到它
		name := strings.TrimPrefix(strings.TrimPrefix(pkg.Source, "npm:"), "npm ")
		if name != "" {
			if dir := packageDir(name); dir != "" {
				pkg.Path = dir
			}
		}
	}
	if pkg.Path == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(pkg.Path, "package.json"))
	if err != nil {
		return
	}
	var manifest struct {
		Name        string          `json:"name"`
		Version     string          `json:"version"`
		Description string          `json:"description"`
		Keywords    []string        `json:"keywords"`
		PI          json.RawMessage `json:"pi"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		return
	}
	// 显式 manifest 声明的资源也要显示（pi-web-access 就是这种：extensions: ["./dist"]）
	if len(manifest.PI) > 0 {
		var piManifest struct {
			Extensions []string `json:"extensions"`
			Skills     []string `json:"skills"`
			Prompts    []string `json:"prompts"`
			Themes     []string `json:"themes"`
		}
		if json.Unmarshal(manifest.PI, &piManifest) == nil {
			if len(piManifest.Extensions) > 0 {
				pkg.Resources = append(pkg.Resources, "extensions")
			}
			if len(piManifest.Skills) > 0 {
				pkg.Resources = append(pkg.Resources, "skills")
			}
			if len(piManifest.Prompts) > 0 {
				pkg.Resources = append(pkg.Resources, "prompts")
			}
			if len(piManifest.Themes) > 0 {
				pkg.Resources = append(pkg.Resources, "themes")
			}
		}
	}
	if manifest.Name != "" && strings.HasPrefix(pkg.Name, "npm:") {
		pkg.Name = manifest.Name
	}
	if pkg.Name == "" {
		pkg.Name = manifest.Name
	}
	pkg.Version = manifest.Version
	pkg.Description = manifest.Description
	for _, dirName := range []string{"extensions", "skills", "prompts", "themes"} {
		if info, err := os.Stat(filepath.Join(pkg.Path, dirName)); err == nil && info.IsDir() {
			pkg.Resources = append(pkg.Resources, dirName)
		}
	}
	sort.Strings(pkg.Resources)
}

func explicitDependencies(root string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return out
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(raw, &manifest) != nil {
		return out
	}
	for name := range manifest.Dependencies {
		out[name] = true
	}
	return out
}

func npmRoot() string {
	dir := agentDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "npm")
}

func packageDir(name string) string {
	root := npmRoot()
	if root == "" {
		return ""
	}
	dir := filepath.Join(root, "node_modules", filepath.FromSlash(name))
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// packageMentionsTool 在包目录里找工具名（只扫文本文件，带总量上限）。
func packageMentionsTool(dir, tool string) bool {
	const maxScanBytes = 4 << 20
	scanned := int64(0)
	found := false
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", "test", "tests", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".js", ".mjs", ".cjs", ".ts", ".json", ".md":
		default:
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		scanned += info.Size()
		if scanned > maxScanBytes {
			return filepath.SkipAll
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(raw), tool) {
			found = true
		}
		return nil
	})
	return found
}

func listHas(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
