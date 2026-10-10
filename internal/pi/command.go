package pi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Options 决定“启动谁、带什么参数、在哪个目录”。
// 这些字段与界面「设置」里的选项一一对应：会话启动时就按用户选好的
// 目录、模型、思考级别、工具白名单拉起 Pi 子进程。
type Options struct {
	Mode     string // mock 或 real
	CLIPath  string // 可选：Pi 的 cli.js / 可执行文件路径
	Cwd      string // 工作目录：决定 Pi 从哪里看文件系统
	Model    string // 可选："provider/id" 或 "id"，空 = 用 Pi 配置里的默认模型
	Thinking string // 可选：low/medium/high/max，空 = 用默认
	// AgentDir 可选：Pi 的配置目录（settings.json / models.json / sessions 都在这）。
	// 空 = 不干预，让 Pi 用自己默认的 ~/.pi/agent。
	AgentDir string
	// ExtraEnv 是要额外注入子进程的环境变量（键值对）。
	//
	// 用途：把"只监听本机、每会话随机"的委派通道地址与令牌交给 Pi 里的扩展，
	// 让扩展能回调 Go 请求子智能体。**不走文件**是刻意的——落盘的密钥迟早会被
	// 拷进备份、日志或截图里；随进程环境传，进程一死就没了。
	ExtraEnv map[string]string
	// LoadResources 为 true 时加载用户资源（扩展 / 技能 / 提示模板 / 主题）。
	//
	// 为什么默认要开：界面的斜杠命令面板靠 get_commands，而它返回的就是这三类东西；
	// 全关掉时列表永远是空的（用户会以为"输入 / 没反应"）。代价是启动稍慢，
	// 且会加载用户装的所有扩展——所以设置里给了开关。
	LoadResources bool
	// SessionDir 可选：会话文件目录。空 = 用 Pi 自己的默认
	// （~/.pi/agent/sessions/<按工作目录分组>/）
	SessionDir string
	// NoSession 为 true 时不写会话文件（临时对话）。
	// 默认 false：会话要落盘，界面左侧才能列出历史并与 Pi 的 switch_session 对上。
	NoSession bool
	// Tools 是工具白名单：
	//   nil      → 不加参数，用 Pi 默认的 read/bash/edit/write
	//   非空     → --tools a,b,c（例如去掉 bash 变成只读改文件）
	//   空切片   → --no-tools（完全禁用工具）
	Tools []string
	// ExcludeTools 是在白名单之外再排除几个（--exclude-tools）。nil = 不排除。
	ExcludeTools []string

	// ---- 以下字段由「角色」编译而来（见 roles.go），空值 = 不干预 ----

	// SystemPrompt 直接替换 Pi 的系统提示（--system-prompt，只吃文本）。
	// 只有角色声明 replaceSystemPrompt 时才用；默认走 AppendSystemPromptFiles。
	SystemPrompt string
	// AppendSystemPromptFiles 是追加到系统提示的文件路径（--append-system-prompt，
	// 可以重复）。用**文件路径**而不是把提示词内联进命令行：角色提示词可以很长，
	// Windows 命令行有长度上限，而且内联会让提示词出现在进程列表里。
	AppendSystemPromptFiles []string
	// Skills 是技能白名单：
	//   nil    → 不干预（Pi 加载它发现的全部技能）
	//   非空   → --no-skills + 逐个 --skill
	//   空切片 → --no-skills（一个都不加载）
	Skills []string
	// Extensions / PromptTemplates 与 Skills 同构（nil / 空 / 非空）。
	Extensions      []string
	PromptTemplates []string
	// Models 是作用域模型列表（--models）：cycle_model 会在这个集合里循环。
	Models []string
	// ContextFiles 为 nil 时不干预；显式 false 时加 --no-context-files
	// （不加载 AGENTS.md / CLAUDE.md 这类目录上下文）。
	ContextFiles *bool
}

// mockArgs 把角色相关的启动参数原样传给模拟器。
//
// 只传模拟器真正用得上的那几个：它要用它们构造首条 system 消息
// （sections 里的 preamble/tools/skills + toolsAdded），供"角色已生效"的断言使用。
func mockArgs(opts Options) []string {
	var args []string
	if model := strings.TrimSpace(opts.Model); model != "" {
		args = append(args, "--model", model)
	}
	if level := strings.TrimSpace(opts.Thinking); level != "" {
		args = append(args, "--thinking", level)
	}
	args = append(args, resourceArgs("--skill", opts.Skills)...)
	for _, file := range opts.AppendSystemPromptFiles {
		if file = strings.TrimSpace(file); file != "" {
			args = append(args, "--append-system-prompt", file)
		}
	}
	switch {
	case opts.Tools == nil:
	case len(opts.Tools) == 0:
		args = append(args, "--no-tools")
	default:
		args = append(args, "--tools", strings.Join(trimAll(opts.Tools), ","))
	}
	return args
}

// trimAll 去掉空白项，避免 "a, ,b" 这种输入进到命令行里。
func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// resourceArgs 把一类资源的白名单编译成 Pi 的参数。
//
// 三种语义（nil / 空 / 非空）必须分开，否则"角色不带任何技能"与
// "角色不限制技能"会变成同一种行为——那是很难查的配置问题。
func resourceArgs(flag string, values []string) []string {
	switch {
	case values == nil:
		return nil
	default:
		args := []string{flagNo(flag)}
		for _, value := range trimAll(values) {
			args = append(args, flag, value)
		}
		return args
	}
}

// flagNo 把 --skill 映射成 --no-skills（Pi 的命名规则）。
func flagNo(flag string) string {
	switch flag {
	case "--skill":
		return "--no-skills"
	case "--extension":
		return "--no-extensions"
	case "--prompt-template":
		return "--no-prompt-templates"
	}
	return flag + "-off"
}

// realArgs 把 Options 编译成真实 Pi 的命令行参数。
//
// 单独一个纯函数（不碰进程、不做路径解析）是为了能对它做 golden 断言：
// 角色系统全靠这串参数落地，参数错了表现是"角色看起来生效了其实没有"，
// 那种问题在界面上几乎看不出来。
//
// 这些开关决定 Pi 子进程的能力范围：
//   - --mode rpc   用 JSONL RPC 而不是交互式 TUI
//   - --no-approve 不加载需要项目信任的本地资源（安全边界，保留）
//
// **资源开关由 opts.LoadResources 决定**（设置里可关）：
//
//	true  → 不传 --no-extensions/--no-skills/--no-prompt-templates/--no-themes，
//	        加载扩展、技能、提示模板与主题。这是"输入 / 能列出命令"的前提：
//	        get_commands 返回的正是这三类东西，全关掉时它必然是空列表。
//	false → 四个 --no-* 全传：启动快、不加载任何用户资源。
//
// ⚠️ 默认**不禁用工具**：Pi 默认启用 read/bash/edit/write 四个内置工具，
// 所以模型可以直接读写文件、执行命令——这正是“让它帮我建 main.py”能work的原因。
// 代价是它拥有当前用户的全部权限，cwd 不是权限边界。
// 界面上把工具收窄成白名单时，这里就加 --tools；全部关掉用 --no-tools。
// 版本不同参数可能变化：请对照你本机 pi --help，而不是照抄别的版本说明。
func realArgs(opts Options) []string {
	args := []string{"--mode", "rpc", "--no-approve"}
	if !opts.LoadResources {
		// 用户在设置里关掉了"加载扩展/技能/模板"：四个 --no-* 一起传
		args = append(args, "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes")
	}

	// ---- 角色参数 ----
	// 系统提示：默认"追加"，把角色提示词叠在 Pi 的编码助手提示之后；
	// 角色显式要求替换时才用 --system-prompt（只吃文本，文件内容由上层读好传进来）。
	if prompt := strings.TrimSpace(opts.SystemPrompt); prompt != "" {
		args = append(args, "--system-prompt", prompt)
	}
	for _, file := range opts.AppendSystemPromptFiles {
		if file = strings.TrimSpace(file); file != "" {
			args = append(args, "--append-system-prompt", file)
		}
	}
	// 作用域模型：cycle_model 在这几个里循环（角色限定"用哪几个模型"）。
	if models := trimAll(opts.Models); len(models) > 0 {
		args = append(args, "--models", strings.Join(models, ","))
	}
	// 技能 / 扩展 / 提示模板三者的白名单语义完全一样：nil = 不干预、
	// 空 = 全禁、非空 = "--no-xxx" + 逐个点名。
	//
	// ⚠️ LoadResources=false 时一律不传：上面已经用 --no-* 关掉了资源加载，
	// 再点名 --skill 只会让 Pi 因为"找不到这个技能"直接启动失败。
	if opts.LoadResources {
		args = append(args, resourceArgs("--skill", opts.Skills)...)
		args = append(args, resourceArgs("--extension", opts.Extensions)...)
		args = append(args, resourceArgs("--prompt-template", opts.PromptTemplates)...)
	}
	if opts.ContextFiles != nil && !*opts.ContextFiles {
		args = append(args, "--no-context-files")
	}

	// 会话：默认落盘（界面要列出历史会话），需要临时对话时显式开 NoSession。
	if opts.NoSession {
		args = append(args, "--no-session")
	} else if dir := strings.TrimSpace(opts.SessionDir); dir != "" {
		args = append(args, "--session-dir", dir)
	}

	// 模型与思考级别：Pi 的 --model 支持 "provider/id"，还支持用冒号追加思考级别
	// （--model deepseek/deepseek-flash:high）。两者都留空时不加参数，用 Pi 自己的默认。
	if model := strings.TrimSpace(opts.Model); model != "" {
		if level := strings.TrimSpace(opts.Thinking); level != "" && !strings.Contains(model, ":") {
			model += ":" + level
		}
		args = append(args, "--model", model)
	}

	// 工具白名单：nil = 不干预（Pi 默认四个工具）；空切片 = 全关；非空 = 只留列出的。
	switch {
	case opts.Tools == nil:
		// 不传参数
	case len(opts.Tools) == 0:
		args = append(args, "--no-tools")
	default:
		args = append(args, "--tools", strings.Join(trimAll(opts.Tools), ","))
	}
	if excluded := trimAll(opts.ExcludeTools); len(excluded) > 0 {
		args = append(args, "--exclude-tools", strings.Join(excluded, ","))
	}
	return args
}

// Command 按 Options 组装一个还没 Start 的 exec.Cmd。
//
// 关键设计：连模拟模式也真的启动一个子进程（就是本程序自己加 --mock-pi），
// 走完全相同的 stdin/stdout 管道。这样学到的分帧、id 关联、退出处理
// 在真实 Pi 上同样成立，而不是被一个“进程内假实现”绕过去。
func Command(opts Options) (*exec.Cmd, error) {
	mode := opts.Mode
	cliPath := opts.CLIPath
	cwd := opts.Cwd
	var cmd *exec.Cmd
	if mode == "mock" {
		// os.Executable() 是当前可执行文件（CLI 或桌面程序），main 里识别 --mock-pi。
		// 模拟器不认模型/工具这些参数，所以这里只传 --mock-pi。
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		// 但**角色参数要传下去**：模拟器的职责是复现 Pi 的形状，包括
		// "首条 system 消息里带着本次启动的提示词分段与工具清单"。
		// 不传的话，"角色到底加载上没有"在模拟模式下就永远断言不了。
		cmd = exec.Command(self, append([]string{"--mock-pi"}, mockArgs(opts)...)...)
	} else if mode == "real" {
		resolved, err := ResolveCLIPath(cliPath)
		if err != nil {
			return nil, err
		}
		path := resolved.Path
		// 参数全部由 realArgs 编译（它有一组 golden 测试，见 command_test.go）
		args := realArgs(opts)

		// 配置目录通过 env 传给子进程：便携模式下是 <exe目录>/.pi，默认 ~/.pi/agent。
		// 显式传比让子进程自己猜可靠——"界面读 A 目录、Pi 读 B 目录"这种不一致
		// 会表现成"改了配置没生效"。
		//
		// ⚠️ 这里只准备 env，赋值必须在 cmd 创建之后（cmd 此时还是 nil，写它的字段会 panic）。
		var env []string
		if dir := strings.TrimSpace(opts.AgentDir); dir != "" {
			env = append(os.Environ(), "PI_CODING_AGENT_DIR="+dir)
		}
		/**
		 * 把配置里那个 shell 的目录前置到 PATH。
		 *
		 * 为什么非做不可（实测踩到的坑）：本机 settings.json 里写着
		 * `shellPath: E:\Git\bin\bash.exe`，但 RPC 模式下 Pi 的 bash 工具**没有**
		 * 采用它——实测（把 shellPath 改成不存在的路径，Pi 既不报错也不改变行为）
		 * 说明这个值根本没被消费。于是 Pi 回退到"在 PATH 里找 bash"，
		 * 而 PATH 上第一个 `bash` 是 `C:\Windows\System32\bash.exe`（WSL 启动器），
		 * 模型的所有命令实际跑在 WSL 里：
		 *     uname -s → Linux，python 不存在（只有 python3）
		 * 而环境清单告诉模型"本机有 python"——清单与真实执行环境对不上，
		 * 模型就照着清单写命令、然后一条条失败。
		 *
		 * 前置 PATH 是**两条路都覆盖**的修法：Pi 若读了 shellPath，什么都不会变；
		 * 若没读，回退解析也会先找到 Git Bash。
		 */
		if shellDir := shellDirFor(opts.AgentDir); shellDir != "" {
			env = prependPath(env, shellDir)
		}
		/**
		 * 自带的工具目录也要前置：分发包把 Python 与命令行工具放在 <exe目录>/tools 下，
		 * 但"放在那儿"不等于"模型敲得到"——它执行 `python`/`ffuf` 时靠 PATH 解析。
		 * 这一步让环境清单与执行环境保持一致（清单说可用 = 真能敲到）。
		 */
		for _, dir := range BundledToolDirs() {
			env = prependPath(env, dir)
		}
		for key, value := range opts.ExtraEnv {
			env = append(env, key+"="+value)
		}

		if strings.EqualFold(filepath.Ext(path), ".js") || strings.EqualFold(filepath.Ext(path), ".mjs") {
			// 走 node 时要给绝对路径，否则子进程的工作目录一变就可能找不到入口。
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			// ⚠️ 这里以前是裸的 exec.Command("node", …)：PATH 里没有 Node 的机器直接起不来。
			// 分发目标是"只要一把 key 就能用"，所以必须先找自带的 Node（<exe目录>/tools/node/）。
			cmd = exec.Command(nodeExecutable(), append([]string{absolute}, args...)...)
		} else {
			cmd = exec.Command(path, args...)
		}
		if env != nil {
			cmd.Env = env
		}
	} else {
		return nil, fmt.Errorf("未知模式 %q", mode)
	}
	if cwd != "" {
		// 工作目录必须是已存在的目录：它决定 Pi 从哪里看文件系统。
		absolute, err := filepath.Abs(cwd)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("工作目录不是文件夹: %s", absolute)
		}
		cmd.Dir = absolute
	}
	configureProcess(cmd) // 平台差异：Windows 上隐藏子进程的控制台窗口
	return cmd, nil
}

/**
 * RunCLI 跑一次 Pi 的命令行并返回 stdout。
 *
 * 用途：`pi list` 是"装了哪些包"的权威来源——它认所有安装方式（npm / git / 本地路径）、
 * 认个人级与项目级的作用域，并给出解析后的真实路径。我们自己扫 node_modules
 * 只能猜到一部分（而且官方发现规则还包含"没有 manifest、按约定目录 discovery"的包）。
 *
 * 参数只传查询类子命令（list / --version 之类），不要用它启动会话：
 * 会话走 Command + RPC 管道，那是另一条路。
 */
func RunCLI(ctx context.Context, agentDir string, args ...string) (string, error) {
	// ⚠️ 必须带上 AgentDir：不带的话 Pi 会读**用户全局**配置（~/.pi/agent），
	// 列出来的是另一份安装的包——实测踩过：便携目录里显示的是全局配置里的包名。
	cmd, err := Command(Options{Mode: "real", AgentDir: agentDir})
	if err != nil {
		return "", err
	}
	// Command 已经把"可执行文件 + cli.js"拼好了，这里只替换子命令与参数：
	// 保留可执行文件与脚本路径（前两个参数），其余换成调用方要跑的。
	base := cmd.Args
	if len(base) >= 2 {
		base = base[:2]
	} else {
		base = base[:1]
	}
	cmd.Args = append(base, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// ListPackages 跑 `pi list`，返回原始输出（解析在调用方，便于单独测试）。
func ListPackages(ctx context.Context, agentDir string) (string, error) {
	return RunCLI(ctx, agentDir, "list")
}

/**
 * nodeExecutable 找 Node 解释器：自带优先，其次 PATH。
 *
 * 顺序：<exe目录>/tools/node/node.exe → <exe目录>/node.exe → PATH 里的 node。
 * 为什么自带优先：便携包必须"解压即用"——用户机器上没有 Node 时，
 * 用 PATH 里的 node 会直接失败（这正是第一版分发漏掉的东西）。
 */
func nodeExecutable() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, candidate := range []string{
			filepath.Join(dir, "tools", "node", "node.exe"),
			filepath.Join(dir, "tools", "node", "node"),
			filepath.Join(dir, "node.exe"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return "node"
}
