// Package tools 是方案的 P7 + P8：工具注册表（Tool Registry）与能力解析
// （Capability Resolver）。
//
// 两条设计原则，来自方案第八、十一节：
//
//  1. 角色不写"用 vol.exe"，角色写能力 `forensics.memory`；由注册表把它解析成
//     本机**实际存在**的工具。用户没装 Volatility 但有 SleuthKit，角色不该整个不可用。
//  2. 分清"Agent 可调用工具"与"环境工具"：read/bash/web_search 是前者（Pi 的工具，
//     能用 --tools 开关）；Volatility、PyCryptodome 是后者——它们**不该**注册成
//     LLM 工具，而应该告诉模型"这台机器上有这些"，由模型用 bash 去调用。
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kind 是工具类型（方案第九节的建议集合）。
type Kind string

const (
	KindBuiltin       Kind = "builtin"        // Pi 自带（read/bash/edit…）
	KindCommand       Kind = "command"        // 本机命令行程序
	KindPythonPackage Kind = "python-package" // Python 包（环境工具，不注册成 LLM 工具）
	KindNodePackage   Kind = "node-package"   // Node 包
	KindApplication   Kind = "application"    // 桌面程序（Ghidra、Burp…）
	KindExtension     Kind = "extension"      // Pi 扩展
	KindService       Kind = "service"        // 本地服务
	KindMCP           Kind = "mcp"            // MCP 服务器
)

// Risk 描述工具的能力面（方案第九节）。
type Risk struct {
	Shell      bool   `json:"shell"`
	Filesystem string `json:"filesystem"` // none / read / write
	Network    bool   `json:"network"`
}

// Detect 是"怎么知道它装没装"。
type Detect struct {
	// Commands：在 PATH 里找这些可执行文件（任一个命中即算可用）
	Commands []string `json:"commands,omitempty"`
	// PythonImport：用 python -c 试 import（Python 包）
	PythonImport string `json:"pythonImport,omitempty"`
	// NodeModule：用 node -e 试 require.resolve
	NodeModule string `json:"nodeModule,omitempty"`
	// Paths：直接探测这些路径（支持环境变量展开；桌面程序用）
	Paths []string `json:"paths,omitempty"`
	/**
	 * SettingsKey：从 Pi 的 settings.json 里读一个路径当结果（如 shellPath）。
	 *
	 * 为什么需要：有些工具的位置**用户已经告诉过 Pi 了**——settings.json 的
	 * `shellPath` 就是 bash 的真实路径。再去猜 Program Files 是不必要的重复，
	 * 而且必错（实测：用户把 Git 装在 E:\Git，猜的两个位置都不对）。
	 */
	SettingsKey string `json:"settingsKey,omitempty"`
}

// Definition 是注册表里的一条工具定义。
type Definition struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Type         Kind     `json:"type"`
	Detect       Detect   `json:"detect"`
	Capabilities []string `json:"capabilities"`
	Risk         Risk     `json:"risk"`
	Note         string   `json:"note,omitempty"`
	// Provider 是"谁提供它"：扩展工具写包名，本机工具留空。
	// 界面据此说明"装了哪个扩展才有这个工具"。
	Provider string `json:"provider,omitempty"`
	// AgentTool 为 true 表示这是**模型直接调用的工具**（扩展注册的），
	// 而不是"环境里能用 bash 调用的程序"。界面上这两类必须分开显示，
	// 否则用户会以为 PyCryptodome 也能像 web_search 那样被模型直接调用。
	AgentTool bool `json:"agentTool,omitempty"`
	// Direction 是方向标签（web/pwn/reverse/crypto/forensics/ir/base），界面分组用
	Direction string `json:"direction,omitempty"`
	// Builtin 为 true 表示"这台机器一定有"（例如 Windows 自带 netstat），
	// 检测仍然跑，但缺了不算问题。
	AlwaysPresent bool `json:"alwaysPresent,omitempty"`
	/**
	 * VersionArgs 是"取版本号该用什么参数"，按顺序试。
	 *
	 * 为什么必须可配：默认是 `--version` 再 `-v`，但 ExifTool 这两种都给不出
	 * 版本号（`--version` 打的是用法标题，实测抓回来一个 "NAME"），要用 `-ver`。
	 * 写死在代码里就只能等下一个工具再踩一次。
	 */
	VersionArgs []string `json:"versionArgs,omitempty"`
	/**
	 * Path 是"上一次确认存在的真实路径"，由检测结果写回清单。
	 *
	 * 为什么要记：① 下次检测直接命中，不用再扫一遍 PATH；
	 * ② 工具装在 PATH 之外的目录时（工具箱、便携包），这是唯一能记住它的地方；
	 * ③ AI 找到/装好一个工具后可以把它登记进来（见 catalog_add 工具）。
	 */
	Path string `json:"path,omitempty"`
	/**
	 * AddedBy 说明这条是谁加进来的：user（人改的 JSON）/ ai（模型登记的）/
	 * detect（检测结果写回）。排查"这条哪来的"时有用。
	 */
	AddedBy string `json:"addedBy,omitempty"`
}

// Status 是定义 + 检测结果。
type Status struct {
	Definition
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	// Detail 说明"为什么不可用"或"用什么路径找到的"，界面直接显示
	Detail string `json:"detail,omitempty"`
	// CheckedAt 让界面知道这个结果是刚才测的还是缓存的
	CheckedAt time.Time `json:"checkedAt"`
}

// environmentPackages 是"当前装了哪些 Pi 包"，由主程序在检测前注入。
//
// 为什么不让本包自己去读 settings.json：那样它就得知道配置目录在哪、文件叫什么
// ——那是主程序的职责。留一个注入口，本包才能单独测试。
var (
	envMu       sync.Mutex
	envPackages []string
)

// SetInstalledPackages 告诉注册表"这台机器装了哪些 Pi 包"。
// 列表变了要顺手清缓存，否则扩展工具的可用性会停留在上一次的结论。
func SetInstalledPackages(packages []string) {
	envMu.Lock()
	changed := len(packages) != len(envPackages)
	if !changed {
		for i := range packages {
			if packages[i] != envPackages[i] {
				changed = true
				break
			}
		}
	}
	envPackages = append([]string(nil), packages...)
	envMu.Unlock()
	if changed {
		ResetCache()
	}
}

func installedPackages() []string {
	envMu.Lock()
	defer envMu.Unlock()
	return append([]string(nil), envPackages...)
}

// envExtensionsDisabled 记录"本次启动是否关掉了扩展/技能/模板"。
//
// 为什么要单独记：设置里关掉扩展之后，包还装在磁盘上、注册表也还能列出
// web_search——但 Pi 是拿 --no-extensions 启动的，这个工具**本次会话根本不存在**。
// 环境清单如果照旧写"你可以直接调用 web_search"，模型会发现自己手上没有这个接口，
// 只能拒绝配合（实际发生过）。所以"装了"和"这次能用"必须分开表达。
var envExtensionsDisabled bool

// SetExtensionsDisabled 告诉注册表"扩展这次没加载"。
func SetExtensionsDisabled(disabled bool) {
	envMu.Lock()
	changed := envExtensionsDisabled != disabled
	envExtensionsDisabled = disabled
	envMu.Unlock()
	if changed {
		ResetCache()
	}
}

// ExtensionsDisabled 返回扩展是否被整体关闭。
func ExtensionsDisabled() bool {
	envMu.Lock()
	defer envMu.Unlock()
	return envExtensionsDisabled
}

// packageInstalled 判断某个包片段是否装了（与主程序 hasPackage 同口径）。
func packageInstalled(needle string) bool {
	for _, name := range installedPackages() {
		if strings.Contains(strings.ToLower(name), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// extensionToolDefinitions 把"已装扩展提供的工具"变成注册表条目。
//
// 同一个工具名可能被多个扩展提供（web_search 就有两个来源）：只保留一条，
// Provider 记第一个命中的，避免界面上出现两行同名工具。
// ⚠️ 去重**不能掩盖冲突**：Pi 遇到两个扩展注册同名工具时，会**整个拒绝后加载的那个**。
// 实测过：装了 @ollama/pi-web-search 之后，pi-web-access 因为 web_search 冲突
// 一个字都没加载进去——而界面上看起来两个都装好了。所以这里同时算出冲突清单。
// ToolConflict 描述"两个已装扩展抢同一个工具名"。
//
// 保留类型是为了不破坏已有接口；检测逻辑随"写死的扩展表"一起删除了——
// 现在扩展工具来自运行中的会话（sections.tools），Pi 自己会拒绝加载冲突的扩展，
// 清单如实显示结果即可，不再由我们猜测。
type ToolConflict struct {
	Tool   string `json:"tool"`
	First  string `json:"first"`
	Second string `json:"second"`
	Effect string `json:"effect"`
}

// ToolConflicts 当前恒为空（见 ToolConflict 的说明）。
func ToolConflicts() []ToolConflict { return nil }

func Definitions() []Definition {
	catalog := ActiveCatalog()
	out := make([]Definition, len(catalog.Tools))
	copy(out, catalog.Tools)
	return out
}

const waitDelay = 2 * time.Second

// runDetection 跑一次检测命令，带超时、静默、管道兜底。
//
// 统一走这一个入口而不是到处 exec：上面那条 WaitDelay 的坑，
// 只有"所有子进程都从同一个地方出去"才可能被一次性修好。
func runDetection(name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), detectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	cmd.WaitDelay = waitDelay
	// 不接 stdin：Windows 上有些程序（含应用商店的 python 别名）会等输入，
	// 一等就永远不返回。
	cmd.Stdin = nil
	var buffer bytes.Buffer
	cmd.Stdout = &buffer
	cmd.Stderr = &buffer
	err := cmd.Run()
	return buffer.String(), err == nil
}

// detectTimeout 是单个检测命令的超时：检测要快，卡住的工具当作不可用。
const detectTimeout = 3 * time.Second

var (
	cacheMu   sync.Mutex
	cache     = map[string]Status{}
	cacheTime time.Time
	// detectVersion 每次真正重扫（或清缓存）时 +1：角色编译缓存拿它当失效键，
	// 环境没变就不用重编译（详见 internal/roles/compile_cache.go）。
	detectVersion int64
)

// cacheTTL 是检测结果的有效期：工具是用户自己装的，不需要每次请求都重扫磁盘。
const cacheTTL = 2 * time.Minute

// DetectVersion 返回检测结果的版本号：重扫与清缓存都会让它递增。
func DetectVersion() int64 {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return detectVersion
}

// DetectAll 检测所有工具（带缓存）。force=true 时忽略缓存重新检测。
func DetectAll(force bool) []Status {
	cacheMu.Lock()
	// 注意有效性判断不能用 len(cache) > 0：零工具的目录（replace 模式清空）
	// 扫出来就是空的，但它同样是"扫过了"——否则每次调用都重扫，
	// 检测版本号每次都变，下游的编译缓存永远命不中。
	if !force && !cacheTime.IsZero() && time.Since(cacheTime) < cacheTTL {
		out := make([]Status, 0, len(cache))
		for _, status := range cache {
			out = append(out, status)
		}
		cacheMu.Unlock()
		sortStatuses(out)
		return out
	}
	cacheMu.Unlock()

	// 并发检测：几十个工具串行跑会明显卡住界面
	defs := Definitions()
	results := make(chan Status, len(defs))
	var wait sync.WaitGroup
	for _, def := range defs {
		wait.Add(1)
		go func(def Definition) {
			defer wait.Done()
			results <- DetectOne(def)
		}(def)
	}
	wait.Wait()
	close(results)

	fresh := map[string]Status{}
	out := make([]Status, 0, len(defs))
	for status := range results {
		fresh[status.ID] = status
		out = append(out, status)
	}
	cacheMu.Lock()
	cache = fresh
	cacheTime = time.Now()
	detectVersion++
	cacheMu.Unlock()
	sortStatuses(out)
	return out
}

func sortStatuses(out []Status) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Direction != out[j].Direction {
			return out[i].Direction < out[j].Direction
		}
		return out[i].ID < out[j].ID
	})
}

// DetectOne 检测单个工具。
func DetectOne(def Definition) Status {
	status := Status{Definition: def, CheckedAt: time.Now()}
	// nil 切片在 JSON 里是 null，前端按数组读会直接崩（"Cannot read properties of null"）：
	// 所有出边界的地方都保证是 [] 而不是 null。
	if status.Capabilities == nil {
		status.Capabilities = []string{}
	}
	// 扩展提供的工具不需要跑任何检测：可用性 = 提供它的包装没装。
	// 单独判断是因为它没有 command / import 可探测（见 extensionProviders）。
	if def.Type == KindExtension {
		if def.Provider == "" {
			status.Detail = "没有记录提供它的扩展"
			return status
		}
		if ExtensionsDisabled() {
			// 包装着，但这次没加载：必须说清楚，否则清单会变成一句空头承诺
			status.Available = false
			status.Detail = "扩展未加载：设置里关闭了「加载扩展 / 技能 / 模板」"
			status.Path = def.Provider
			return status
		}
		status.Available = true
		status.Detail = "由扩展 " + def.Provider + " 提供"
		status.Path = def.Provider
		return status
	}
	/**
	 * 0) 清单里已记录的路径：文件还在就直接算可用。
	 *
	 * 这条是"可写清单"的基础：AI 或上一次检测把真实路径记进 JSON 之后，
	 * 无论那个目录在不在 PATH，下次都能直接命中（省一次全盘查找）。
	 * 文件没了就继续往下探测——记录过期不该导致误判为可用。
	 */
	if recorded := strings.TrimSpace(def.Path); recorded != "" {
		recorded = expandPortable(recorded)
		if info, err := os.Stat(recorded); err == nil && !info.IsDir() {
			status.Available = true
			status.Path = recorded
			status.Detail = "清单记录的路径"
			status.Version = commandVersion(recorded, def.VersionArgs)
			return status
		}
	}

	/**
	 * 1) 可执行文件：PATH → 清单里的 searchPaths → 无扩展名脚本。
	 *
	 * 三种情况都实测遇到过：
	 *   · 普通命令（在 PATH 里）；
	 *   · 工具箱自带目录（如 E:\...\Code4Hack\tools\bin\ffuf，不在 PATH）；
	 *   · pip 装出来的**无扩展名脚本**（Windows 的 LookPath 只认 PATHEXT 里的
	 *     .exe/.cmd/.bat/.com，`binwalk` 这种没有后缀的文件它找不到）。
	 */
	for _, name := range def.Detect.Commands {
		if path := lookPathExtended(name, ActiveCatalog().SearchPaths); path != "" {
			status.Available = true
			status.Path = path
			status.Detail = "在 PATH 中找到"
			if !strings.EqualFold(filepath.Dir(path), "") && !inPathDirs(path) {
				status.Detail = "在清单配置的目录中找到"
			}
			status.Version = commandVersion(name, def.VersionArgs)
			return status
		}
	}
	// 1.5) 用户已经在 Pi 配置里写明的路径（如 shellPath → bash）
	if key := strings.TrimSpace(def.Detect.SettingsKey); key != "" {
		if path := pathFromPiSettings(key); path != "" {
			status.Available = true
			status.Path = path
			status.Detail = "取自 ZeroA 配置 " + key
			status.Version = commandVersion(path, def.VersionArgs)
			return status
		}
	}
	// 2) 桌面程序的常见路径（支持通配与环境变量）
	for _, pattern := range def.Detect.Paths {
		if path := expandPath(pattern); path != "" {
			status.Available = true
			status.Path = path
			status.Detail = "在已知安装路径找到"
			return status
		}
	}
	// 3) Python 包：用 python -c 试 import
	if def.Detect.PythonImport != "" {
		if python, ok := pythonCommand(); ok {
			if version, ok := pythonImport(python, def.Detect.PythonImport); ok {
				status.Available = true
				status.Path = python
				status.Version = version
				status.Detail = "python import 成功"
				return status
			}
			status.Detail = "python 里 import " + def.Detect.PythonImport + " 失败（未安装）"
			return status
		}
		status.Detail = "找不到 python，无法判断这个包"
		return status
	}
	// 4) Node 包
	if def.Detect.NodeModule != "" {
		if _, err := exec.LookPath("node"); err == nil {
			if out, ok := runDetection("node", "-e", "require.resolve('"+def.Detect.NodeModule+"')"); ok {
				status.Available = true
				status.Detail = strings.TrimSpace(out)
				return status
			}
		}
		status.Detail = "node 里找不到模块 " + def.Detect.NodeModule
		return status
	}
	if def.AlwaysPresent {
		status.Detail = "系统自带命令，但本次没在 PATH 找到"
	} else {
		status.Detail = "本机没找到"
	}
	return status
}

// commandVersion 尽力取版本号：拿不到就留空（不猜、不编）。
func commandVersion(name string, versionArgs []string) string {
	if name == "" {
		return ""
	}
	/**
	 * 取版本的参数：先在清单里配的（版本参数因工具而异——ExifTool 只认 -ver，
	 * 用 --version 会打出一整页用法，实测抓回来一个 "NAME"），再退到通用的两种。
	 * 每一项都要单独计时：一个卡住的 --version 不该把另一个也拖死。
	 */
	attempts := append([]string(nil), versionArgs...)
	attempts = append(attempts, "--version", "-v")
	for _, flag := range attempts {
		out, ok := runDetection(name, flag)
		if !ok && strings.TrimSpace(out) == "" {
			continue
		}
		if line := firstLine(out); line != "" {
			return trimVersion(line)
		}
	}
	return ""
}

/**
 * sanitizeConsoleText 把控制台输出变成"能显示"的文本。
 *
 * 起因：WSL 的 bash 在此机上把本地化消息按 GBK 打到 stdout，我们按 UTF-8 解出来
 * 是一串乱码（界面上表现为 `wS□□□□hKmOR□□...`）。真正的编码转换要引 x/text，
 * 为一个版本号不值得；这里只做两件事——丢掉非法字节，并把控制字符换成空格。
 * 宁可少显示几个字，也不要糊一屏乱码。
 */
func sanitizeConsoleText(text string) string {
	if utf8.ValidString(text) {
		return strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' {
				return ' '
			}
			return r
		}, text)
	}
	cleaned := strings.ToValidUTF8(text, "")
	cleaned = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return ' '
		}
		return r
	}, cleaned)
	return strings.TrimSpace(cleaned)
}

func firstLine(text string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// trimVersion 把版本输出裁短：很多工具会打印整段版权声明。
func trimVersion(line string) string {
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return line
}

func pythonCommand() (string, bool) {
	for _, name := range pythonCandidates() {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

func pythonCandidates() []string {
	// 自带 Python 优先（分发包里 tools/python/python.exe），其次才是 PATH
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, candidate := range []string{
			filepath.Join(dir, "tools", "python", "python.exe"),
			filepath.Join(dir, "tools", "python", "python"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return []string{candidate, "python", "python3", "py"}
			}
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"python", "python3", "py"}
	}
	return []string{"python3", "python"}
}

// pythonImport 试 import 一个包并取它的 __version__（取不到也算成功）。
func pythonImport(python, module string) (string, bool) {
	script := "import " + module + " as m;print(getattr(m,'__version__',''))"
	out, ok := runDetection(python, "-c", script)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// expandPath 展开 %VAR% / $VAR 并支持最后一段的 * 通配。
/**
 * lookPathExtended 找可执行文件：先走标准 LookPath，再在附加目录里找，
 * 最后容忍"没有扩展名的脚本"与常见脚本后缀。
 */
func lookPathExtended(name string, extraDirs []string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	/**
	 * 名字本身可以是**便携路径**（清单里写 tools/python/python.exe）。
	 *
	 * 分发包要让"用户不装东西也能用"，清单里就会出现这种相对 exe 的写法；
	 * 它不走 PATH，必须先按便携规则解析再判存在。
	 */
	if strings.ContainsAny(name, "/\\") {
		if candidate := expandPortable(name); candidate != "" {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	dirs := append([]string{}, extraDirs...)
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	suffixes := []string{"", ".exe", ".cmd", ".bat", ".com", ".ps1", ".py", ".sh"}
	for _, dir := range dirs {
		trimmed := strings.TrimSpace(dir)
		if trimmed == "" {
			continue
		}
		base := expandDir(trimmed)
		if base == "" {
			continue
		}
		for _, suffix := range suffixes {
			candidate := filepath.Join(base, name+suffix)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

/**
 * expandDir 展开一个**目录**（支持 %VAR%、$VAR 与 * 通配）。
 *
 * 为什么单独写一个：expandPath 是给"可执行文件模式"用的，它要求命中项**不是目录**
 * （否则 `%ProgramFiles%\Ghidra\*` 这种模式会命中一堆目录名）。拿它来展开搜索目录
 * 会永远返回空——查找目录全部失效，表现就是"工具明明在那个目录里却显示没有"。
 */
/**
 * expandPortable 解析"便携路径"：
 *   · ${TOOLS} → <exe目录>/tools（分发时工具与 Python 都放这儿）
 *   · ${EXE}   → <exe目录>
 *   · 相对路径 → 相对 <exe目录>（清单里写 tools/python/python.exe 就能跨机器用）
 *   · 环境变量 → $VAR / ${VAR} / %VAR% 三种写法都认
 *
 * 为什么需要：分发要求"用户不用装 Python 和工具"，那些东西跟着 exe 走，
 * 清单里的路径就必须是相对 exe 的——绝对路径在别人机器上必然失效。
 *
 * 顺序铁律：环境变量必须在"相对→exe目录"之前展开完。
 * 踩过的坑：%VAR% 展开曾放在返回之后（expandPath/expandDir 里补做），
 * 于是 %ProgramFiles%\Git\bin\bash.exe 先被判成相对路径、拼成
 * <exe目录>\%ProgramFiles%\...，后面再替换 %VAR% 已经救不回来——
 * 内置清单里的 bash 检测永远报「本机没找到」。
 */
func expandPortable(pattern string) string {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return ""
	}
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	trimmed = strings.ReplaceAll(trimmed, "${TOOLS}", filepath.Join(exeDir, "tools"))
	trimmed = strings.ReplaceAll(trimmed, "${EXE}", exeDir)
	expanded := expandPercentEnv(os.ExpandEnv(trimmed))
	if exeDir != "" && !filepath.IsAbs(expanded) && !strings.Contains(expanded, ":") {
		expanded = filepath.Join(exeDir, expanded)
	}
	// 清单里允许用正斜杠写路径（跨平台写法），Windows 下统一成正斜杠→反斜杠
	return filepath.FromSlash(expanded)
}

/**
 * expandPercentEnv 展开 Windows 风格的 %VAR%。
 *
 * os.ExpandEnv 只认 $VAR / ${VAR}，而清单与文档都承诺支持 %VAR%（Windows 用户
 * 最熟的写法），所以在这里补齐。任意变量名都认（%APPDATA%、%ProgramFiles(x86)%…），
 * 不再维护一份写死的变量清单——以前的清单 expandPath 与 expandDir 各有一份、
 * 内容还不一样，漏掉的变量就静默失效。
 *
 * 未设置的变量**原样保留**：替换成空串会把路径悄悄改写掉（%NOPE%\x → \x），
 * 保留原样至少能让日志/报错里看到用户到底写了什么。
 */
func expandPercentEnv(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			b.WriteString(s)
			break
		}
		j := strings.IndexByte(s[i+1:], '%')
		if j < 0 {
			b.WriteString(s)
			break
		}
		j += i + 1
		name := s[i+1 : j]
		if name == "" || strings.ContainsAny(name, "/\\:;") {
			// 不是变量名（路径里的孤立 %，如 "100%"）：保留这个 %，从它后面继续扫
			b.WriteString(s[:i+1])
			s = s[i+1:]
			continue
		}
		if val, ok := os.LookupEnv(name); ok {
			b.WriteString(val)
		} else {
			b.WriteString(s[i : j+1])
		}
		s = s[j+1:]
	}
	return b.String()
}

func expandDir(pattern string) string {
	expanded := expandPortable(pattern)
	if strings.Contains(expanded, "*") {
		matches, err := filepath.Glob(expanded)
		if err != nil || len(matches) == 0 {
			return ""
		}
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && info.IsDir() {
				return match
			}
		}
		return ""
	}
	if info, err := os.Stat(expanded); err == nil && info.IsDir() {
		return expanded
	}
	return ""
}

// inPathDirs 判断某个路径是否位于 PATH 的某个目录里（用于给出准确来源说明）。
func inPathDirs(path string) bool {
	dir := strings.ToLower(filepath.Dir(path))
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.ToLower(strings.TrimSpace(entry)) == dir {
			return true
		}
	}
	return false
}

// pathFromPiSettings 从 <数据目录>/settings.json 里读一个字符串字段当路径。
func pathFromPiSettings(key string) string {
	dir := agentDirEnv()
	if dir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		return ""
	}
	value, _ := settings[key].(string)
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if info, err := os.Stat(trimmed); err == nil && !info.IsDir() {
		return trimmed
	}
	return ""
}

// agentDirEnv 读配置目录（环境变量优先；不引 internal/pi 以免依赖方向反了）。
func agentDirEnv() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return dir
	}
	return ""
}

func expandPath(pattern string) string {
	// %VAR% / $VAR / ${VAR} 都已在 expandPortable 里展开完（顺序见那里的注释）
	expanded := expandPortable(pattern)
	if strings.Contains(expanded, "*") {
		matches, err := filepath.Glob(expanded)
		if err != nil || len(matches) == 0 {
			return ""
		}
		return matches[0]
	}
	if info, err := os.Stat(expanded); err == nil && !info.IsDir() {
		return expanded
	}
	return ""
}

// ResetCache 清掉检测缓存（界面上的"重新检测"用它）。
func ResetCache() {
	cacheMu.Lock()
	cacheTime = time.Time{}
	cache = map[string]Status{}
	detectVersion++
	cacheMu.Unlock()
}
