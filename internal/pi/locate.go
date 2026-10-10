package pi

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 本文件负责「用哪一个 Pi」这一个问题。
//
// 解析顺序（越靠前优先级越高）：
//  1. 显式传入的路径（设置页里填的）
//  2. 环境变量 PI_CLI_PATH
//  3. **应用自带**：exe 同目录下的 pi-agent/（见 scripts/fetch-pi-agent.ps1）
//  4. PATH 里的 pi（本机全局安装）
//
// 为什么要让"自带"优先：应用要能整包拷走就用——换一台机器、或者本机全局
// 版本被升级/删掉，行为都不该跟着变。自带的版本还锁定在发布时验证过的那一个。

// CLISource 是解析结果：入口路径 + 它从哪来（设置页要显示，排查时也要看）。
type CLISource struct {
	Path string
	Kind string // explicit | env | local | path
	// Version 只在本地/全局 npm 安装能读出 package.json 时才有值
	Version string
}

// DescribeCLI 供界面显示"现在用的是哪个 Pi"，不启动任何进程。
func DescribeCLI(explicit string) CLISource {
	resolved, err := ResolveCLIPath(explicit)
	if err != nil {
		return CLISource{Kind: "missing"}
	}
	return resolved
}

// localAgentDirCandidates 列出"应用自带 Pi"可能出现的目录，按优先级排列。
//
// PI_AGENT_HOME 可以覆盖这个位置：既是测试注入点（测试二进制没法伪装自己的
// exe 目录），也方便把 Pi 放在别处（例如共享盘）而不用改程序。
func localAgentDirCandidates() []string {
	if override := strings.TrimSpace(os.Getenv("PI_AGENT_HOME")); override != "" {
		return []string{override}
	}
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	base := filepath.Dir(self)
	names := []string{"pi-agent", "pi", filepath.Join("runtime", "pi-agent")}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(base, name))
	}
	return out
}

// localAgentCLI 在应用同目录里找自带 Pi 的入口。
func localAgentCLI() (string, string, bool) {
	for _, dir := range localAgentDirCandidates() {
		// 标准 npm 布局：node_modules/<scope>/pi-coding-agent/<bin 里的相对路径>
		for _, scope := range []string{"@earendil-works", "@mariozechner"} {
			root := filepath.Join(dir, "node_modules", scope, "pi-coding-agent")
			if entry, version, ok := cliFromPackage(root); ok {
				return entry, version, true
			}
		}
		// 也有人把解包后的文件直接放在这里
		for _, name := range []string{"pi-cli.js", "cli.js", "pi.cmd", "pi.exe"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, "", true
			}
		}
	}
	return "", "", false
}

// cliFromPackage 读一个 npm 包的 package.json，把 bin.pi 解析成绝对路径。
func cliFromPackage(root string) (string, string, bool) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", "", false
	}
	var pkg struct {
		Version string            `json:"version"`
		Bin     map[string]string `json:"bin"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Bin["pi"] == "" {
		return "", "", false
	}
	entry := filepath.Join(root, pkg.Bin["pi"])
	if info, err := os.Stat(entry); err != nil || info.IsDir() {
		return "", "", false
	}
	return entry, pkg.Version, true
}

// AgentDir 解析 Pi 的「配置目录」（settings.json / models.json / auth.json / sessions/ 所在处）。
//
// 顺序：
//  1. 环境变量 PI_CODING_AGENT_DIR —— 用户显式指定
//  2. **应用同目录的 .pi** —— 默认位置：配置、会话、粘贴的图片全在程序旁边，
//     整包拷到别的机器上就能继续用（绿色化）。目录不存在会自动创建。
//  3. ~/.pi/agent —— 兜底：应用目录不可写时（例如装在 Program Files）才用它
//
// 放在这个包里而不是 main：桌面端与命令行入口都要用同一套解析，
// 两边算出不同目录的现象是"命令行能用、界面说没有模型"。
func AgentDir() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return dir
	}
	if dir := portableAgentDir(); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// AgentDirKind 说明配置目录来自哪一档：env | portable | home。
func AgentDirKind() string {
	if strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")) != "" {
		return "env"
	}
	if portableAgentDir() != "" {
		return "portable"
	}
	return "home"
}

// portableAgentDir 返回应用同目录的配置目录（<exe目录>/.pi）。
//
// 与早期实现的关键差别：**不存在就创建**。以前是"存在才用"，于是全新安装会悄悄
// 写进 ~/.pi/agent，用户以为配置在程序旁边、其实在用户目录里。
// 创建失败（目录只读）时返回空串，调用方会退回 ~/.pi/agent —— 不能因为
// 想要绿色化就让应用在只读安装目录里直接不可用。
func portableAgentDir() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(self), ".pi")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// ResolveCLIPath 按上面的顺序找出该用哪个入口，并顺手把 .cmd/.ps1 包装解开。
func ResolveCLIPath(explicit string) (CLISource, error) {
	if path := strings.TrimSpace(explicit); path != "" {
		unwrapped, version, err := unwrapLauncher(path)
		if err != nil {
			return CLISource{}, err
		}
		return CLISource{Path: unwrapped, Kind: "explicit", Version: version}, nil
	}
	if path := strings.TrimSpace(os.Getenv("PI_CLI_PATH")); path != "" {
		unwrapped, version, err := unwrapLauncher(path)
		if err != nil {
			return CLISource{}, err
		}
		return CLISource{Path: unwrapped, Kind: "env", Version: version}, nil
	}
	if path, version, ok := localAgentCLI(); ok {
		return CLISource{Path: path, Kind: "local", Version: version}, nil
	}
	found, err := exec.LookPath("pi")
	if err != nil {
		return CLISource{}, fmt.Errorf("找不到 ZeroA 引擎：应用同目录没有自带 pi-agent，PATH 里也没有 pi（可用 scripts/fetch-pi-agent.ps1 拉一份，或在设置里填写路径）")
	}
	unwrapped, version, err := unwrapLauncher(found)
	if err != nil {
		return CLISource{}, err
	}
	return CLISource{Path: unwrapped, Kind: "path", Version: version}, nil
}

// unwrapLauncher 把 npm 生成的 .cmd/.ps1 包装解开成真正的 cli.js。
//
// 为什么不让 exec 直接跑 .cmd：Windows 上会多一层 cmd.exe，路径带空格时的引号
// 处理也容易出错；而且 node 才是真正的解释器。所以读同目录 package.json 里的
// bin 字段，改成 node <cli.js>。
func unwrapLauncher(path string) (string, string, error) {
	if !(runtime.GOOS == "windows" && (strings.EqualFold(filepath.Ext(path), ".cmd") || strings.EqualFold(filepath.Ext(path), ".ps1"))) {
		return path, "", nil
	}
	// 场景一：<prefix>/pi.cmd 旁边就是 node_modules
	for _, scope := range []string{"@earendil-works", "@mariozechner"} {
		root := filepath.Join(filepath.Dir(path), "node_modules", scope, "pi-coding-agent")
		if entry, version, ok := cliFromPackage(root); ok {
			return entry, version, nil
		}
	}
	// 场景二：<prefix>/pi.cmd 只是符号链接式入口，往上一层再找
	for _, scope := range []string{"@earendil-works", "@mariozechner"} {
		root := filepath.Join(filepath.Dir(filepath.Dir(path)), "node_modules", scope, "pi-coding-agent")
		if entry, version, ok := cliFromPackage(root); ok {
			return entry, version, nil
		}
	}
	return "", "", fmt.Errorf("无法解析 %s，请直接填写 ZeroA 引擎的 cli.js 路径", path)
}
