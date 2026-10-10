// Package mockpi 是一个确定性的“假 Pi”：没有模型、不需要 API Key，也能把协议练完整。
//
// 它从服务端视角演示一个 JSONL RPC 服务必须具备的结构（客户端视角见 internal/pi）：
//   - 一个读循环，不断从 stdin 读入一行行命令；
//   - 每条命令回一条 id 相同的 response；
//   - 回复内容是异步产生的：先回 response，再持续发 event；
//   - 同一时刻只允许一个回合在跑，abort 会取消它并把收尾事件发完；
//   - 写 stdout 必须串行，否则两段 JSON 会交错成一行。
//
// 相比“只发文本”的最小版本，这里补齐了真实 Pi 单轮里最容易把界面写错的三块：
//
//	思考（thinking_*）→ 工具（tool_execution_*）→ 文本（text_*）→ 结束（agent_settled）
//	外加 extension_ui_request / extension_ui_response 这一对“扩展问界面要答案”的往返。
//
// 仍然有意省略：usage 明细、compaction、自动重试、真实模型推理。
// 它覆盖的是事件骨架，而不是模型行为。
package mockpi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 事件骨架里用到的固定值。集中成常量是为了让“假数据长什么样”一眼可见。
const (
	defaultSessionID = "mock-session"
	providerID       = "mock"
	modelID          = "mock-model"
	// defaultLevel 是启动时的思考档位，取值必须在 levels 里。
	defaultLevel = "medium"
	// uiRequestID 是扩展请求的固定 id：客户端必须原样回传某个 id，
	// 服务端才能把 extension_ui_response 交回正在等待的那次请求。
	uiRequestID = "mock-ui-1"
	// uiTimeout 是等界面回答的最长时间。超时不是错误：
	// 真实扩展也会超时收场，这里按“没确认”处理，让回合能继续走完。
	uiTimeout = 10 * time.Second
	// defaultTextDelay 是一个字符之间的停顿。保留它有两个原因：
	//  1. 界面上能真的看到“逐字出字”，而不是一瞬间刷完；
	//  2. abort 才有可观察的落点（等待期间收到取消）。
	// 测试里可以传更小的值把用例跑快，见 Serve 的 textDelay 可选参数。
	defaultTextDelay = 200 * time.Millisecond
	// delayEnv 让测试进程能在启动子进程时统一调小逐字停顿（单位：毫秒）。
	delayEnv = "PI_MOCK_TEXT_DELAY_MS"
)

// models 是 get_available_models 的假数据：两个 provider、三个模型。
//
// 字段与真实 Pi 的 Model 对象一致（见 pi 的 docs/rpc-commands.md “Model object”）：
// 界面拿它渲染模型选择器，将来切到真实 Pi 时不需要改一行代码。
var models = []map[string]any{
	{
		"id": modelID, "name": "Mock Model", "provider": providerID, "api": "openai-completions",
		"baseUrl": "http://localhost", "reasoning": true, "input": []string{"text"},
		"contextWindow": 200000, "maxTokens": 8192,
		"cost": map[string]any{"input": 0.3, "output": 1.2, "cacheRead": 0.006, "cacheWrite": 0},
	},
	{
		"id": "mock-fast", "name": "Mock Fast", "provider": providerID, "api": "openai-completions",
		"baseUrl": "http://localhost", "reasoning": false, "input": []string{"text"},
		"contextWindow": 32000, "maxTokens": 4096,
		"cost": map[string]any{"input": 0.1, "output": 0.4, "cacheRead": 0.002, "cacheWrite": 0},
	},
	{
		"id": "demo-pro", "name": "Demo Pro", "provider": "demo", "api": "anthropic-messages",
		"baseUrl": "http://localhost", "reasoning": true, "input": []string{"text", "image"},
		"contextWindow": 128000, "maxTokens": 16384,
		"cost": map[string]any{"input": 3.0, "output": 15.0, "cacheRead": 0.3, "cacheWrite": 3.75},
	},
}

// levels 是 get_available_thinking_levels 的返回值，也是 set_thinking_level 的合法取值。
// 真实 Pi 的档位集合是 off/minimal/low/medium/high/xhigh/max（后两个只在模型支持时出现），
// 这里给一个固定子集：够界面把下拉框画出来，又留出“非法档位必须报错”的验证空间。
var levels = []string{"low", "medium", "high", "max"}

// request 是一条命令里我们关心的字段。
// 只解析自己认识的字段：协议允许出现更多字段，不认识就忽略，
// 这比“结构必须完全一致”要健壮得多。
//
// 字段名以真实 Pi 为准（docs/rpc-commands.md）：
// 切换模型传的是 modelId 而不是 model，扩展 UI 的关联键就是 id。
type request struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message"`
	// 下面这些只有部分命令用得上，零值就等于“没传”。
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"` // 真实 Pi 的 set_model 用 modelId，不是 model
	Level    string `json:"level"`
	// Enabled 是 set_auto_compaction 的参数。
	Enabled bool `json:"enabled"`
	// Mode 是 set_steering_mode / set_follow_up_mode 的参数：all 或 one-at-a-time。
	Mode string `json:"mode"`
	// Command 是 bash 的参数：要执行的命令。
	Command string `json:"command"`
	// SessionPath 是 switch_session 的参数：要打开哪个会话文件。
	SessionPath string `json:"sessionPath"`
	// Name 是 set_session_name 的参数（界面给会话起的名字）。
	Name string `json:"name"`
	// OutputPath 是 export_html 的参数：导出到哪个文件（可空）。
	OutputPath string `json:"outputPath"`
	// EntryID 是 fork 的参数：从哪条用户消息开新分支。
	EntryID   string `json:"entryId"`
	Confirmed *bool  `json:"confirmed"` // 指针才能区分“没传”和“明确传了 false”
	Cancelled *bool  `json:"cancelled"` // 对话框被关掉：扩展收到 undefined/false
	Value     string `json:"value"`     // select/input/editor 的答案
	// UIID 是被回答的扩展请求 id。
	//
	// 为什么不能直接用 id？因为请求信封自己也要用 id（req-1、req-2…）做
	// 请求-响应的关联，两个用途会撞在同一个键上。所以这里约定用 uiId，
	// 与 Pi 文档里的“响应 id 与请求 id 相同”在语义上一致，只是占用了不同的键。
	UIID string `json:"uiId"`
}

// toolResult 描述一次工具执行的“账本”：它既进事件流，也进会话消息。
// bash/read 两个工具故意一个成功一个失败，界面必须能同时渲染两种情况。
type toolResult struct {
	toolCallID string
	toolName   string
	args       map[string]any
	result     map[string]any // 工具的返回值；失败时带上错误文本
	isError    bool
}

// sessionMsg 是 mock 会话里的一条消息，原样进 get_messages 和 agent_end。
type sessionMsg struct {
	role    string
	content []map[string]any // 助手消息的内容块；用户消息改用纯文本（见 json）
	text    string
	stop    string // 只有助手消息有：界面靠它区分 stop / aborted / toolUse
	// sections 只有 system 消息有：Pi 把本次启动的提示词分段写在这里
	// （preamble / tools / cwd / skills），见 pi 的 docs/session-format.md。
	// "角色是否真的加载上了"就靠它来断言。
	sections map[string]any
	// toolsAdded 也只有 system 消息有：本次启动声明了哪些工具。
	toolsAdded []map[string]any
	// errorMessage 只有"被中止"的助手消息会有：真实 Pi 在中止时同时给
	// stopReason=aborted 和 errorMessage="This operation was aborted"。
	// 少了它，界面"按停止却弹出报错"这类问题在 mock 下就复现不出来。
	errorMessage string
	// 下面三个只有 toolResult 消息用：真实 Pi 把它们放在**消息级**
	// （不在 content 块里），界面回放历史时靠 toolCallId 把结果配回工具调用。
	toolCallID string
	toolName   string
	isError    bool
}

// json 把消息编码成协议里的形状。
//
// 用户消息的 content 是**字符串**（真实 Pi 就是这样，见 docs/json.md 的事件序列示例），
// 助手消息才是内容块数组：文本块的键是 text、思考块的键是 thinking、
// 工具调用块的键是 toolCallId/toolName/args。时间戳是毫秒。
func (m sessionMsg) json() map[string]any {
	payload := map[string]any{"role": m.role, "timestamp": time.Now().UnixMilli()}
	if m.role == "user" {
		payload["content"] = m.text
		return payload
	}
	blocks := make([]any, 0, len(m.content))
	for _, block := range m.content {
		blocks = append(blocks, block)
	}
	payload["content"] = blocks
	if m.stop != "" {
		payload["stopReason"] = m.stop
	}
	if m.errorMessage != "" {
		payload["errorMessage"] = m.errorMessage
	}
	// system 消息：内容为空，真正的信息在 sections / toolsAdded 里
	if m.role == "system" {
		payload["content"] = ""
		if m.sections != nil {
			payload["sections"] = m.sections
		}
		if m.toolsAdded != nil {
			payload["toolsAdded"] = m.toolsAdded
		}
	}
	// toolResult 的配对信息在消息级（与真实 Pi 一致）
	if m.role == "toolResult" {
		payload["toolCallId"] = m.toolCallID
		payload["toolName"] = m.toolName
		payload["isError"] = m.isError
	}
	return payload
}

// state 是一个 mock 进程的全部可变状态，由 Server.stateMu 保护：
// 命令在主读循环里处理，事件在回合 goroutine 里产生，两者都要读写它。
type state struct {
	cancel     context.CancelFunc
	done       chan struct{} // 当前回合的收尾信号，stop() 靠它等“事件都发完了”
	messages   []sessionMsg
	steerQueue []string // steer：插进当前回合的下一条用户消息
	followUps  []string // follow_up：等下一次 prompt 时一起处理
	model      string
	provider   string
	thinking   string
	// sessionID 会随 new_session / switch_session 变化（真实 Pi 也会换会话文件）
	sessionID string
	// sessionName 是 set_session_name 设的名字（get_state 会带上它）
	sessionName string
	/**
	 * sessionFile 是"这个进程此刻绑在哪份会话文件上"（switch_session 的参数；
	 * new_session 之后清空）。
	 *
	 * 一进程一会话之后，Go 侧靠它判断"这条会话是不是已经有一个活着的进程"：
	 * 有就复用（切换即时、不重载历史），没有才冷启动一个新进程。
	 * 真实 Pi 的 get_state 也带这个字段。
	 */
	sessionFile string
	// roundPrompt 是本轮的用户输入（prompt + steer + follow_up 拼在一起）。
	// 扩展要不要弹确认，靠的是这段文本；不能去 messages 里找最后一条——
	// 那时最后一条已经是思考消息了（见 needsUI 的注释）。
	autoRetry    bool // set_auto_retry // set_auto_compaction：仅记录，供 get_state 之类后续读取
	roundPrompt  string
	roundStart   int // 本轮消息在 messages 里的起点，agent_end 只带本轮
	messageCount int
	toolCalls    int // 累计工具调用次数，get_session_stats 用它填 toolCalls/toolResults
	tokensIn     int
	tokensOut    int
	running      bool
	autoCompact  bool
	// PI_MOCK_RETRY_ONCE：本进程是否已经演过一次"瞬时错误 → 自动重试"
	retrySimulated bool
	// retrying 为真表示正在重试等待中；abort_retry 只在这种情况下取消这一轮
	retrying bool
	// 投递模式：Pi 的取值只有 all / one-at-a-time（默认后者）
	steeringMode string
	followUpMode string
}

// Server 是一个 mock Pi 服务端。它把“读命令 → 回响应 → 发事件”三件事拆开，
// 方便单独看清楚每一块的职责；对外只需要 Serve 一个入口。
type Server struct {
	input  io.Reader
	output io.Writer
	// outputMu 保证“一条 JSON 一次写出”，和客户端的 writeMu 对称：
	// 事件来自回合 goroutine、response 来自读循环，两者都会写 stdout。
	outputMu sync.Mutex
	stateMu  sync.Mutex
	state    state
	// uiWaiters 把“扩展请求 id”映射到等待界面回答的通道：
	// 回合 goroutine 在这里等，读循环收到 extension_ui_response 后投递。
	// 这是整个模拟器里唯一一处“服务端反等客户端”，也是最容易写丢的一环。
	uiMu      sync.Mutex
	uiWaiters map[string]chan map[string]any
	// textDelay 是逐字输出时每个字符之间的停顿，见 defaultTextDelay。
	textDelay time.Duration
	// launch 是本次启动带的角色相关参数（见 parseLaunchArgs）。
	launch launchOptions
}

// Serve 从 input 读命令、往 output 写记录，直到 input 结束（EOF）为止。
// 它不知道自己是不是真的 Pi，只认协议——所以也能被别的客户端拿来测。
//
// textDelay 可选，省略时取环境变量 PI_MOCK_TEXT_DELAY_MS（默认 200ms）。
// 测试用环境变量而不是新参数，是为了不改动任何现有调用方：
// 测试进程启动子进程时设置它即可，正式运行时根本不设。
func Serve(input io.Reader, output io.Writer, textDelay ...time.Duration) {
	delay := defaultTextDelay
	if len(textDelay) > 0 {
		delay = textDelay[0]
	} else {
		delay = delayFromEnv()
	}
	server := &Server{
		input:     input,
		output:    output,
		uiWaiters: make(map[string]chan map[string]any),
		textDelay: delay,
		state:     state{model: modelID, provider: providerID, thinking: defaultLevel, autoCompact: true, sessionID: defaultSessionID},
	}
	// 启动参数（角色编译来的那部分）：把它们如实写进首条 system 消息，
	// 这样"角色到底加载上没有"在模拟模式下也能被断言，而不是只能肉眼看。
	server.launch = parseLaunchArgs(os.Args[1:])
	// 角色给的模型与思考级别就是这次进程的默认值：不跟随的话，
	// "角色的模型生效了吗"在模拟模式下永远是 mock 的固定值，断言不出任何东西。
	if server.launch.model != "" {
		provider, id := splitModel(server.launch.model)
		server.state.provider = provider
		server.state.model = id
	}
	if server.launch.thinking != "" {
		server.state.thinking = server.launch.thinking
	}
	server.state.messages = append(server.state.messages, server.launchSystemMessage())
	server.serve()
}

// splitModel 把 "provider/id" 拆开；只给 id 时沿用当前 provider。
func splitModel(value string) (provider, id string) {
	value = strings.TrimSpace(value)
	// Pi 允许用冒号追加思考级别（provider/id:high），这里只要模型部分
	if index := strings.Index(value, ":"); index > 0 {
		value = value[:index]
	}
	if index := strings.Index(value, "/"); index > 0 {
		return value[:index], value[index+1:]
	}
	return providerID, value
}

// launchOptions 是模拟器关心的启动参数（真机由 Pi 解析，这里只取够用的几个）。
type launchOptions struct {
	model       string
	thinking    string
	tools       string
	noTools     bool
	skills      []string
	noSkills    bool
	promptFiles []string
}

// parseLaunchArgs 从命令行里挑出模拟器需要如实复现的那几个参数。
//
// 故意不引入 flag 包：真机参数是混合形态（--tools a,b 与 --no-tools 并存），
// 这里只要"看到谁就记谁"，多余的参数一律忽略——模拟器不认识的参数不该让它崩。
func parseLaunchArgs(args []string) launchOptions {
	var opts launchOptions
	for i := 0; i < len(args); i++ {
		value := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--model":
			opts.model = value()
		case "--thinking":
			opts.thinking = value()
		case "--tools":
			opts.tools = value()
		case "--no-tools":
			opts.noTools = true
		case "--skill":
			opts.skills = append(opts.skills, value())
		case "--no-skills":
			opts.noSkills = true
		case "--append-system-prompt":
			opts.promptFiles = append(opts.promptFiles, value())
		}
	}
	return opts
}

// launchSystemMessage 构造会话的首条 system 消息。
//
// 形状照抄真实 Pi（docs/session-format.md）：内容为空，信息在 sections 与
// toolsAdded 里；提示词分段用 "prompt#<文件>" 标明来源，工具逐个列出来。
// 有了它，"切换角色后会话里能看到什么"就是可断言的事实。
func (s *Server) launchSystemMessage() sessionMsg {
	sections := map[string]any{
		"preamble": "You are an expert coding assistant.",
	}
	// --append-system-prompt 可以传多次，真实 Pi 会把它们**依次拼接**进系统提示。
	// 所以这里也要拼接（而不是让最后一个覆盖前面的）：工作台会传"角色提示词"+
	// "环境清单"两个文件，只保留最后一个就等于把角色提示词丢了。
	var promptBodies, promptSources []string
	for _, file := range s.launch.promptFiles {
		if body, err := os.ReadFile(file); err == nil {
			promptBodies = append(promptBodies, string(body))
			promptSources = append(promptSources, file)
		} else {
			// 读不到也要留痕：否则"提示词没生效"会变成没有证据的争论
			sections["rolePromptError"] = err.Error()
		}
	}
	if len(promptBodies) > 0 {
		sections["rolePrompt"] = strings.Join(promptBodies, "\n\n")
		sections["rolePromptSources"] = promptSources
	}
	if len(s.launch.skills) > 0 {
		// 真实 Pi 在 sections.skills 里列的是**技能名**（SKILL.md 的 name），
		// 不是路径——命令行上传的是路径，提示词里出现的是名字。
		// 这里照做：读每个技能目录的 SKILL.md，取名。
		names := make([]string, 0, len(s.launch.skills))
		for _, path := range s.launch.skills {
			names = append(names, skillName(path))
		}
		sections["skills"] = "<skills>\n" + strings.Join(names, "\n") + "\n</skills>"
	} else if s.launch.noSkills {
		sections["skills"] = ""
	}
	tools := defaultToolNames
	switch {
	case s.launch.noTools:
		tools = nil
	case strings.TrimSpace(s.launch.tools) != "":
		tools = nil
		for _, name := range strings.Split(s.launch.tools, ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				tools = append(tools, trimmed)
			}
		}
	}
	sections["tools"] = "<tools>\n" + strings.Join(tools, "\n") + "\n</tools>"
	added := make([]map[string]any, 0, len(tools))
	for _, name := range tools {
		added = append(added, map[string]any{"name": name, "description": name + " tool"})
	}
	return sessionMsg{role: "system", sections: sections, toolsAdded: added}
}

// skillName 从 --skill 给的值里取出技能名。
//
// 真实 Pi 收的是路径（可能是目录，也可能是 SKILL.md 本身），进提示词的是
// frontmatter 的 name。这里做最小解析：读得到 SKILL.md 就用它的 name，
// 否则退回目录/文件名——不引入 YAML 依赖，够模拟器用。
func skillName(path string) string {
	manifest := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		manifest = filepath.Join(path, "SKILL.md")
	}
	base := filepath.Base(strings.TrimSuffix(filepath.Dir(manifest), string(filepath.Separator)))
	if raw, err := os.ReadFile(manifest); err == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			if strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "--- ") {
				continue
			}
			if strings.HasPrefix(line, "name:") {
				if name := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "name:")), `"'`); name != "" {
					return name
				}
			}
			if strings.HasPrefix(line, "# ") {
				break // 已经进了正文，说明 frontmatter 里没有 name
			}
		}
	}
	if base == "" || base == "." {
		base = filepath.Base(manifest)
	}
	return base
}

// defaultToolNames 是 Pi 默认启用的四个内置工具。
var defaultToolNames = []string{"read", "bash", "edit", "write"}

// delayFromEnv 读取 PI_MOCK_TEXT_DELAY_MS。
//
// 存在的理由：端到端测试要跑得快，又不该改协议行为。测试进程无法给
// 子进程传 Go 参数，只能传环境变量，所以在服务端入口统一读一次。
// 解析失败一律退回默认值——一个坏环境变量不该让模拟器拒绝启动。
func delayFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(delayEnv))
	if raw == "" {
		return defaultTextDelay
	}
	milliseconds, err := strconv.Atoi(raw)
	// 0 也要拦：emitText 的停顿为 0 时，select 的 ctx.Done 与 time.After(0)
	// 同时就绪、Go 随机挑一个，abort 就变成抽签（见 emitText 的注释）。
	if err != nil || milliseconds < 1 {
		return defaultTextDelay
	}
	return time.Duration(milliseconds) * time.Millisecond
}

// serve 是主读循环：一行一条命令。
func (s *Server) serve() {
	defer s.stop() // stdin 结束时，别把还在跑的回合 goroutine 留下
	scanner := bufio.NewScanner(s.input)
	// 单行上限：坏输入不该把内存吃光。
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var command request
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			// 解析失败也要回一条记录，让客户端知道出了什么事。
			// 注意此时 id 可能根本没解析出来，真实服务同样只能尽力而为。
			s.respond("", "parse", nil, err.Error())
			continue
		}
		data, errText := s.handle(&command)
		// extension_ui_response 在协议里没有 response（真实 Pi 也不回），
		// 所以这里跳过；否则界面上会多出一条没人认领的记录。
		if command.Type == "extension_ui_response" {
			continue
		}
		s.respond(command.ID, command.Type, data, errText)
	}
}

// handle 处理一条命令，返回它的 data 与错误文本。
// 返回错误文本而不是 error 值：这里唯一要做的就是把它放进 response。
func (s *Server) handle(command *request) (any, string) {
	switch command.Type {
	case "get_state":
		// 只读查询：回合跑不跑都能问，所以 UI 用它来判断“连接是否真的可用”。
		return s.stateSnapshot(), ""
	case "get_messages":
		// 真实 Pi 用对象包一层：{"messages":[...]}（见 docs/rpc-commands.md）。
		// 包起来是为了将来加游标、分页这类字段时不破坏现有客户端。
		return map[string]any{"messages": s.messagesSnapshot()}, ""
	case "get_session_stats":
		return s.statsSnapshot(), ""
	case "prompt":
		if errText := s.startRound(command.Message); errText != "" {
			return nil, errText
		}
		// 关键顺序：先把 response 发回去，再异步产生事件。
		// 这就是“response 只代表命令被接受，不代表任务完成”。
		return map[string]string{"disposition": "started"}, ""
	case "steer":
		return s.handleSteer(command.Message)
	case "follow_up":
		return s.handleFollowUp(command.Message)
	case "abort":
		// 先停、并等回合彻底收尾，再回 response：
		// 这样客户端收到 abort 成功时，事件流里该发的结束事件都已经发出。
		s.stop()
		return nil, ""
	case "clear_queue":
		return s.handleClearQueue(), ""
	case "set_model":
		return s.handleSetModel(command.ModelID)
	case "get_available_models":
		return map[string]any{"models": models}, ""
	case "set_thinking_level":
		return s.handleSetThinkingLevel(command.Level)
	case "get_available_thinking_levels":
		// 真实 Pi 只回 {"levels":[...]}]：当前档位要去 get_state.thinkingLevel 拿，
		// 这里不额外编一个 current 字段，免得界面依赖上真实 Pi 没有的东西。
		return map[string]any{"levels": levels}, ""
	case "extension_ui_response":
		// 特殊：这条命令**不回 response 记录**（对齐真实 Pi 的 dist/rpc-mode.js）。
		// 界面侧用的是 Notify（写完就走），这里回一条反而会多出一条没人认领的记录。
		// 失败也不会有人看到，所以只写 stderr 当诊断。
		if _, errText := s.handleUIResponse(command); errText != "" {
			fmt.Fprintf(os.Stderr, "[mockpi] extension_ui_response 未被接受: %s\n", errText)
		}
		return nil, ""
	case "new_session":
		// 真实 Pi 的 new_session 会被扩展的 session_before_switch 钩子拦下，
		// 返回值里可能有 cancelled=true；这里固定不取消，清空消息换一个会话 id。
		s.stateMu.Lock()
		s.state.messages = nil
		s.state.messageCount = 0
		s.state.sessionID = newSessionID()
		s.state.sessionFile = ""
		s.stateMu.Unlock()
		return map[string]any{"cancelled": false}, ""
	case "switch_session":
		// 参数是会话文件路径（sessionPath）。真实 Pi 会去读这个文件；
		// 模拟器只需要把会话 id 换成文件名里的 uuid，界面就能看到切换效果。
		path := strings.TrimSpace(command.SessionPath)
		if path == "" {
			return nil, "sessionPath 不能为空"
		}
		s.stateMu.Lock()
		s.state.messages = nil
		s.state.messageCount = 0
		s.state.sessionID = sessionIDFromPath(path)
		s.state.sessionFile = path
		s.stateMu.Unlock()
		return map[string]any{"cancelled": false}, ""
	case "cycle_model":
		// 契约：切到可用模型列表里的下一个，data 带切换后的模型 id。
		s.stateMu.Lock()
		next := models[0]["id"].(string)
		for i, model := range models {
			if model["id"] == s.state.model {
				next = models[(i+1)%len(models)]["id"].(string)
				break
			}
		}
		s.state.model = next
		s.stateMu.Unlock()
		return map[string]any{"modelId": next, "model": next}, ""
	case "cycle_thinking_level":
		// 契约：切到下一个推理等级，回 data.level。
		s.stateMu.Lock()
		next := levels[0]
		for i, level := range levels {
			if level == s.state.thinking {
				next = levels[(i+1)%len(levels)]
				break
			}
		}
		s.state.thinking = next
		s.stateMu.Unlock()
		return map[string]any{"level": next}, ""
	case "get_last_assistant_text":
		// 契约：data.text = 最后一条助手消息的文本（没有就为空串）。
		// 用途是界面上的「复制上一条回答」，比让用户手动框选可靠。
		s.stateMu.Lock()
		text := ""
		for i := len(s.state.messages) - 1; i >= 0; i-- {
			if s.state.messages[i].role != "assistant" {
				continue
			}
			for _, block := range s.state.messages[i].content {
				if kind, _ := block["type"].(string); kind == "text" {
					if value, ok := block["text"].(string); ok {
						text = value
					}
				}
			}
			if text != "" {
				break
			}
		}
		s.stateMu.Unlock()
		return map[string]any{"text": text}, ""
	case "bash":
		// 契约：输出以 bash_execution_update 流式到达，最终结果在 response 里。
		// 模拟器不真的执行命令（安全与可复现都更重要），回一段可预期的输出，
		// 让"运行命令"这条链路能被测试。
		cmd := strings.TrimSpace(command.Command)
		if cmd == "" {
			return nil, "命令不能为空"
		}
		output := fmt.Sprintf("[模拟执行] %s\nmock: command finished\n", cmd)
		s.event(map[string]any{"type": "bash_execution_update", "output": output, "id": command.ID})
		return map[string]any{"output": output, "exitCode": 0, "cancelled": false, "truncated": false}, ""
	case "abort_bash":
		return map[string]any{"aborted": true}, ""
	case "set_steering_mode", "set_follow_up_mode":
		mode := strings.TrimSpace(command.Mode)
		if mode != "all" && mode != "one-at-a-time" {
			return nil, "mode 只能是 all 或 one-at-a-time"
		}
		s.stateMu.Lock()
		if command.Type == "set_steering_mode" {
			s.state.steeringMode = mode
		} else {
			s.state.followUpMode = mode
		}
		s.stateMu.Unlock()
		return map[string]any{"mode": mode}, ""
	case "set_auto_retry":
		s.stateMu.Lock()
		s.state.autoRetry = command.Enabled
		s.stateMu.Unlock()
		return map[string]any{"enabled": command.Enabled}, ""
	case "abort_retry":
		// 真实 Pi 只取消"重试等待"，不影响正在正常跑的回合。
		// 所以这里只在确实处于重试等待时取消，否则如实回报"没在重试"。
		s.stateMu.Lock()
		retrying := s.state.retrying
		cancel := s.state.cancel
		s.stateMu.Unlock()
		if retrying && cancel != nil {
			cancel()
			return map[string]any{"aborted": true}, ""
		}
		return map[string]any{"aborted": false}, ""
	case "get_tree":
		// 契约：data.tree = [{entry:{type,id,parentId,...}, children, label?}] + leafId。
		// 模拟器把消息拼成一条单链（真实 Pi 在分支后会长出多叉）。
		s.stateMu.Lock()
		type node map[string]any
		var chain []node
		parent := any(nil)
		leaf := ""
		for index, message := range s.state.messages {
			id := fmt.Sprintf("entry-%d", index)
			entry := map[string]any{
				"type":     "message",
				"id":       id,
				"parentId": parent,
				"role":     message.role,
				"text":     message.text,
			}
			chain = append(chain, node{"entry": entry, "children": []any{}})
			parent = id
			leaf = id
		}
		s.stateMu.Unlock()
		// 把链挂成一棵树（每个节点挂在上一个的 children 上）
		for i := len(chain) - 2; i >= 0; i-- {
			chain[i]["children"] = []any{chain[i+1]}
		}
		tree := []any{}
		if len(chain) > 0 {
			tree = append(tree, chain[0])
		}
		return map[string]any{"tree": tree, "leafId": leaf}, ""
	case "compact":
		// 真实 Pi 会先回 response 再发 compaction_start / compaction_end 事件；
		// 模拟器只把状态里的 token 数压小一点，让界面能看到"用量降下来了"。
		s.stateMu.Lock()
		s.state.tokensIn = s.state.tokensIn / 2
		s.state.tokensOut = s.state.tokensOut / 2
		s.stateMu.Unlock()
		s.emit(map[string]any{"type": "compaction_end", "summary": "模拟器压缩完成"})
		return map[string]any{"compacted": true}, ""
	case "set_auto_compaction":
		s.stateMu.Lock()
		// 必须写 autoCompact —— get_state 读的是它。
		// 曾经有两个字段（autoCompaction / autoCompact）各写各的，
		// 结果是"命令成功返回、状态却没变"，界面与测试都看不出来。
		s.state.autoCompact = command.Enabled
		s.stateMu.Unlock()
		return map[string]any{"enabled": command.Enabled}, ""
	case "set_session_name":
		s.stateMu.Lock()
		s.state.sessionName = command.Name
		s.stateMu.Unlock()
		return map[string]any{"name": command.Name}, ""
	case "get_fork_messages":
		// 契约：{messages:[{entryId, text}]}。模拟器用消息序号当 entryId，
		// 只需保证"同一轮里稳定"，用来验证界面的按钮与调用链。
		s.stateMu.Lock()
		out := make([]map[string]any, 0, len(s.state.messages))
		index := 0
		for _, message := range s.state.messages {
			if message.role != "user" {
				continue
			}
			out = append(out, map[string]any{"entryId": fmt.Sprintf("entry-%d", index), "text": message.text})
			index++
		}
		s.stateMu.Unlock()
		return map[string]any{"messages": out}, ""
	case "fork":
		entryID := strings.TrimSpace(command.EntryID)
		if entryID == "" {
			return nil, "entryId 不能为空"
		}
		// 真实 Pi 会新建一条分支并返回被分支消息的文本；模拟器只回报文本与未取消
		s.stateMu.Lock()
		text := ""
		index := 0
		for _, message := range s.state.messages {
			if message.role != "user" {
				continue
			}
			if fmt.Sprintf("entry-%d", index) == entryID {
				text = message.text
				break
			}
			index++
		}
		s.stateMu.Unlock()
		if text == "" {
			return nil, "找不到该 entryId"
		}
		return map[string]any{"text": text, "cancelled": false}, ""
	case "clone":
		return map[string]any{"cancelled": false}, ""
	case "get_commands":
		// 契约见 docs/rpc-commands.md：data.commands = [{name, description, source}]，
		// source ∈ extension | prompt | skill。名字前加 / 用 prompt 发出去即可。
		return map[string]any{"commands": []map[string]any{
			{"name": "fix-tests", "description": "修好失败的测试", "source": "prompt"},
			{"name": "review", "description": "审查当前改动", "source": "skill"},
			{"name": "hello", "description": "演示用的扩展命令", "source": "extension"},
		}}, ""
	case "export_html":
		// 契约见 pi 的 docs/rpc-commands.md：{type:"export_html", outputPath?} →
		// data:{path}。模拟器真的写一份最小 HTML，这样"导出"这条路径能在
		// mock 模式下免费回归（否则它只在真实模式里才被走到，平时没人测）。
		return s.handleExportHTML(command.OutputPath)
	default:
		return nil, "模拟器不支持命令: " + command.Type
	}
}

// handleExportHTML 写出一份能打开的最小 HTML，返回 {"path": ...}。
func (s *Server) handleExportHTML(outputPath string) (map[string]any, string) {
	target := strings.TrimSpace(outputPath)
	if target == "" {
		// 真实 Pi 在不传路径时会自己挑一个位置；模拟器用会话 id 命名，落在临时目录
		s.stateMu.Lock()
		id := s.state.sessionID
		s.stateMu.Unlock()
		if id == "" {
			id = "session"
		}
		target = filepath.Join(os.TempDir(), id+".html")
	}
	html := "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>ZeroA 会话导出</title></head>\n" +
		"<body><h1>ZeroA 会话导出（模拟器）</h1>\n" +
		"<p>本文件由 PI_GUI_MODE=mock 的模拟器生成，用于验证导出路径。</p>\n" +
		"</body></html>\n"
	if err := os.WriteFile(target, []byte(html), 0o644); err != nil {
		return nil, "写文件失败: " + err.Error()
	}
	return map[string]any{"path": target}, ""
}

// emit 是唯一的写出口：一条 JSON + LF，串行写出。
func (s *Server) emit(value any) {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	_ = json.NewEncoder(s.output).Encode(value)
}

// event 发一条事件：Pi 主动产生的记录，没有 id，不属于任何请求。
func (s *Server) event(payload map[string]any) { s.emit(payload) }

// respond 统一生成 response：id 必须原样回传，客户端靠它找到等待者。
// 真实 Pi 会省略空的 data/error 字段（Go 的 json omitempty），这里保持一致，
// 免得学习者以为 error 字段永远存在。
func (s *Server) respond(id, command string, data any, errText string) {
	payload := map[string]any{"type": "response", "id": id, "command": command, "success": errText == ""}
	if data != nil {
		payload["data"] = data
	}
	if errText != "" {
		payload["error"] = errText
	}
	s.emit(payload)
}

// stateSnapshot 是 get_state 的假数据，字段名以真实 Pi 为准（docs/rpc-commands.md）：
// 界面用它一处判断“连接可用、正在跑、用的哪个模型、排了几条消息”。
// sessionFile 也要给：一进程一会话的实现靠它认领"这条会话已经在哪个进程里开着"
// （见 session_runtime.go），没有它每次切换都会冷启动一个新进程。
func (s *Server) stateSnapshot() map[string]any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	payload := map[string]any{
		"model":                 modelByID(s.state.model),
		"thinkingLevel":         s.state.thinking,
		"isStreaming":           s.state.running,
		"isCompacting":          false,
		"steeringMode":          "one-at-a-time",
		"followUpMode":          "one-at-a-time",
		"sessionId":             s.state.sessionID,
		"sessionFile":           s.state.sessionFile,
		"sessionName":           s.state.sessionName,
		"autoCompactionEnabled": s.state.autoCompact,
		"messageCount":          s.state.messageCount,
		"pendingMessageCount":   len(s.state.steerQueue) + len(s.state.followUps),
	}
	// sessionName 只在设置过之后出现（与真实 Pi 一致：没设过就没有这个键）。
	// 注意 stateSnapshot 持着 stateMu，这里读字段是安全的。
	if name := strings.TrimSpace(s.state.sessionName); name != "" {
		payload["sessionName"] = name
	}
	return payload
}

// modelByID 返回完整的 Model 对象。找不到时退回一个最小可用的壳：
// state 里存的只是一个 id，而真实 Pi 的 state.model 是完整对象。
func modelByID(id string) map[string]any {
	for _, model := range models {
		if model["id"] == id {
			return model
		}
	}
	return map[string]any{"id": id, "provider": providerID}
}

// messagesSnapshot 把会话消息编码成协议形状。
// 没有消息时返回空数组而不是 null：前端一律按数组处理，少一个分支。
func (s *Server) messagesSnapshot() []any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	out := make([]any, 0, len(s.state.messages))
	for _, message := range s.state.messages {
		out = append(out, message.json())
	}
	return out
}

// statsSnapshot 是 get_session_stats 的假数据，形状与真实 Pi 一致（docs/rpc-commands.md）：
// 计数字段 + tokens 对象 + cost（美元，数字）+ contextUsage（token 数与百分比）。
// 界面按字段名渲染即可，切到真实 Pi 不需要改代码。
func (s *Server) statsSnapshot() map[string]any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	// 价格是随手编的常量，只为了让“cost 怎么算出来的”在代码里看得见。
	cost := float64(s.state.tokensIn)*3.0/1_000_000 + float64(s.state.tokensOut)*15.0/1_000_000
	users, assistants := 0, 0
	for _, message := range s.state.messages {
		switch message.role {
		case "user":
			users++
		case "assistant":
			assistants++
		}
	}
	const window = 200000
	used := s.state.tokensIn + s.state.tokensOut
	percent := 0
	if window > 0 {
		percent = used * 100 / window
		if percent > 100 {
			percent = 100
		}
	}
	return map[string]any{
		"sessionId":         s.state.sessionID,
		"userMessages":      users,
		"assistantMessages": assistants,
		"toolCalls":         s.state.toolCalls,
		"toolResults":       s.state.toolCalls,
		"totalMessages":     s.state.messageCount,
		"tokens": map[string]any{
			"input": s.state.tokensIn, "output": s.state.tokensOut,
			"cacheRead": 0, "cacheWrite": 0,
			"total": s.state.tokensIn + s.state.tokensOut,
		},
		"cost": cost,
		"contextUsage": map[string]any{
			"tokens": used, "contextWindow": window, "percent": percent,
		},
	}
}

// handleSteer 记录一条“插话”：它会在下一轮开始时作为用户消息发出来。
//
// 真实 Pi 里 steer 由 steering 队列单独管理（处理时机在“当前回合的工具跑完之后、
// 下一次模型调用之前”），follow_up 则要等整个回合结束。
// 模拟器简化成“下一轮开始时按 steer 在前、follow_up 在后依次发出”，
// 但两个队列分开存：clear_queue 要能分别把它们还回去。
func (s *Server) handleSteer(message string) (any, string) {
	if strings.TrimSpace(message) == "" {
		return nil, "消息不能为空"
	}
	s.stateMu.Lock()
	s.state.steerQueue = append(s.state.steerQueue, message)
	s.stateMu.Unlock()
	s.emitQueueUpdate()
	return map[string]any{"disposition": "queued"}, ""
}

// handleFollowUp 记录一条“补话”：等下一次 prompt 时一起带出去。
func (s *Server) handleFollowUp(message string) (any, string) {
	if strings.TrimSpace(message) == "" {
		return nil, "消息不能为空"
	}
	s.stateMu.Lock()
	s.state.followUps = append(s.state.followUps, message)
	s.stateMu.Unlock()
	s.emitQueueUpdate()
	return map[string]any{"disposition": "queued"}, ""
}

// emitQueueUpdate 广播两个队列的原文，形状与真实 Pi 的 queue_update 事件一致：
// {"type":"queue_update","steering":[...],"followUp":[...]}（见真实 Pi 的
// agent-session._emitQueueUpdate）。界面的排队面板靠它显示消息原文——
// 没有它，mock 模式下面板永远是空的，"排队/插入/编辑/删除"这条链路测不到。
func (s *Server) emitQueueUpdate() {
	s.stateMu.Lock()
	steering := append([]string{}, s.state.steerQueue...)
	followUp := append([]string{}, s.state.followUps...)
	s.stateMu.Unlock()
	s.event(map[string]any{"type": "queue_update", "steering": steering, "followUp": followUp})
}

// handleClearQueue 清空两个队列，并把原文还回去。
//
// 真实 Pi 返回的是消息文本本身（不是计数）：界面上按 Esc 清队列时，
// 需要把这些文字放回输入框，所以“还回原文”才是正确的数据形状。
func (s *Server) handleClearQueue() any {
	s.stateMu.Lock()
	steering := append([]string{}, s.state.steerQueue...)
	followUp := append([]string{}, s.state.followUps...)
	s.state.steerQueue, s.state.followUps = nil, nil
	s.stateMu.Unlock()
	s.emitQueueUpdate()
	return map[string]any{"steering": steering, "followUp": followUp}
}

// handleSetModel 切换模型，成功时返回完整的 Model 对象（真实 Pi 就是这样）。
//
// 真实 Pi 的 set_model 只认 modelId（provider 是可选提示），
// 所以这里也按 id 匹配：未知模型返回错误，界面的模型选择器
// 需要能区分“切成功”和“这个名字根本不存在”。
func (s *Server) handleSetModel(modelID string) (any, string) {
	if strings.TrimSpace(modelID) == "" {
		return nil, "modelId 不能为空"
	}
	for _, candidate := range models {
		if candidate["id"] == modelID {
			s.stateMu.Lock()
			s.state.model = modelID
			s.state.provider, _ = candidate["provider"].(string)
			s.stateMu.Unlock()
			return candidate, ""
		}
	}
	return nil, "未知模型: " + modelID
}

// handleSetThinkingLevel 校验档位后写入状态。
//
// 响应不带 data，与真实 Pi 一致：当前档位的权威来源是 get_state.thinkingLevel。
// 界面切完档位应该再查一次状态（或者自己记住刚设的值）。
func (s *Server) handleSetThinkingLevel(level string) (any, string) {
	valid := false
	for _, candidate := range levels {
		if candidate == level {
			valid = true
			break
		}
	}
	if !valid {
		return nil, "未知思考档位: " + level
	}
	s.stateMu.Lock()
	s.state.thinking = level
	s.stateMu.Unlock()
	return nil, ""
}

// handleUIResponse 把界面的回答投递给正在等待的回合 goroutine。
//
// 关联键就是记录**自身的 id**：真实 Pi 的响应形状是
//
//	{"type":"extension_ui_response","id":"<扩展请求 id>","confirmed":true}   确认
//	{"type":"extension_ui_response","id":"<扩展请求 id>","value":"Allow"}    选择/输入
//	{"type":"extension_ui_response","id":"<扩展请求 id>","cancelled":true}   取消
//
// 与普通命令不同，这里的 id 不是 req-N 而是扩展请求 id —— 因为 Pi 是按 id
// 去 pendingExtensionRequests 里找等待者的（见 1.0.0 的 dist/rpc-mode.js）。
// 而且 Pi 收下之后**不会回任何 response 记录**，所以下面返回的 data 用不上，
// 分发处会跳过响应（见 serve 里对这条命令的特殊处理）。
//
// id 对不上就说明那次请求已经超时离场，必须明确报错（进 stderr 之外的诊断），
// 不能让界面以为答成功了。
func (s *Server) handleUIResponse(command *request) (any, string) {
	answer := map[string]any{}
	switch {
	case command.Cancelled != nil && *command.Cancelled:
		answer["cancelled"] = true
	case command.Confirmed != nil:
		answer["confirmed"] = *command.Confirmed
	case command.Value != "":
		answer["value"] = command.Value
	default:
		return nil, "extension_ui_response 需要 confirmed / value / cancelled 之一"
	}
	// 兼容旧字段：早期版本把请求 id 放在 uiId 里
	requestID := command.ID
	if requestID == "" {
		requestID = command.UIID
	}
	s.uiMu.Lock()
	waiter, waiting := s.uiWaiters[requestID]
	if waiting {
		delete(s.uiWaiters, requestID)
	}
	s.uiMu.Unlock()
	if !waiting {
		return nil, "没有等待中的扩展请求: " + requestID
	}
	waiter <- answer // 缓冲为 1，投递不会卡住读循环
	return map[string]any{"accepted": true}, ""
}

// waitUIResponse 注册一个等待位并阻塞，直到界面回答、超时或回合被取消。
// 返回值里的 ok=false 表示“没等到”（超时或取消），调用方按“没确认”处理。
func (s *Server) waitUIResponse(ctx context.Context, id string, timeout time.Duration) (map[string]any, bool) {
	answer := make(chan map[string]any, 1)
	s.uiMu.Lock()
	s.uiWaiters[id] = answer
	s.uiMu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case value := <-answer:
		return value, true
	case <-timer.C:
	case <-ctx.Done():
	}
	// 超时或取消后把等待位摘掉：迟到的 response 会得到明确错误，
	// 而不是被悄悄丢进一个没人再读的通道。
	s.uiMu.Lock()
	delete(s.uiWaiters, id)
	s.uiMu.Unlock()
	return nil, false
}

// stop 取消当前回合并等它收尾（事件都发完）。
// abort 命令、进程退出（serve 的 defer）都走这里，保证不会漏掉正在跑的 goroutine。
func (s *Server) stop() {
	s.stateMu.Lock()
	cancel, done := s.state.cancel, s.state.done
	s.stateMu.Unlock()
	// 先解锁再等：回合 goroutine 收尾时要拿 stateMu，
	// 若这里持锁等待，双方就会互相卡死。
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// startRound 启动一个回合。成功时立刻返回空串，由调用方先回 response；
// 返回非空串表示命令被拒绝（消息为空、已有回合在跑）。
func (s *Server) startRound(message string) string {
	if strings.TrimSpace(message) == "" {
		return "消息不能为空"
	}
	s.stateMu.Lock()
	if s.state.running {
		s.stateMu.Unlock()
		return "已有任务正在运行"
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.state.followUps = append(s.state.followUps, message)
	s.state.cancel, s.state.done, s.state.running = cancel, done, true
	s.stateMu.Unlock()
	go func() {
		// done 一定要关：stop() 会一直等它，漏关就是一次永久卡死。
		defer close(done)
		if err := s.runRound(ctx); err != nil {
			s.event(map[string]any{"type": "agent_error", "error": err.Error()})
		}
		// 顺序很重要：先标记空闲，再发 agent_settled。
		// 客户端收到 agent_settled 后可能立刻发下一条 prompt，
		// 如果此时 running 还是 true，新回合会被误判为“已有任务在跑”。
		s.stateMu.Lock()
		s.state.running = false
		s.event(map[string]any{"type": "agent_settled"})
		s.stateMu.Unlock()
	}()
	return ""
}

// simulateRetryOnce 演一次自动重试（只有 PI_MOCK_RETRY_ONCE=1 且本进程还没演过时）。
//
// 事件序列照真实 Pi：
//
//	agent_end{willRetry:true} → auto_retry_start{attempt,delayMs,errorMessage}
//	→ 等待（可被 abort_retry 打断）→ auto_retry_end{success}
//
// 返回 true 表示用户在等待期间中止了重试，调用方应当结束这一轮。
func (s *Server) simulateRetryOnce(ctx context.Context) bool {
	if os.Getenv("PI_MOCK_RETRY_ONCE") != "1" {
		return false
	}
	s.stateMu.Lock()
	if s.state.retrySimulated {
		s.stateMu.Unlock()
		return false
	}
	s.state.retrySimulated = true
	s.state.retrying = true
	s.stateMu.Unlock()

	s.event(map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": true})
	s.event(map[string]any{
		"type":         "auto_retry_start",
		"attempt":      1,
		"delayMs":      4000,
		"errorMessage": "429 Too Many Requests（模拟的瞬时错误）",
	})

	select {
	case <-time.After(4 * time.Second):
		s.stateMu.Lock()
		s.state.retrying = false
		s.stateMu.Unlock()
		s.event(map[string]any{"type": "auto_retry_end", "success": true})
		return false
	case <-ctx.Done():
		s.stateMu.Lock()
		s.state.retrying = false
		s.stateMu.Unlock()
		s.event(map[string]any{"type": "auto_retry_end", "success": false, "finalError": "用户中止了重试"})
		return true
	}
}

// runRound 产生一轮完整的事件流。它是整个模拟器的“剧本”，
// 每一步都对应界面上一个可观察的状态变化：
//
//	agent_start / turn_start   进入“运行中”
//	thinking_*                 展开思考块（真实 Pi 上是可折叠的推理过程）
//	message_end(toolUse)       助手宣布“我要调用工具”
//	extension_ui_request       扩展要界面点一下确认（只有特定输入才触发）
//	tool_execution_*           工具开始 / 流式输出 / 结束
//	text_*                     最终答复逐字出现
//	agent_settled              回到空闲（由 startRound 的 goroutine 发）
func (s *Server) runRound(ctx context.Context) error {
	s.roundStart()

	// PI_MOCK_RETRY_ONCE=1：先演一次"瞬时错误 → 自动重试"。
	// 真实 Pi 只在遇到 429/5xx 时才重试，没法稳定复现，所以给测试留一个开关，
	// 让"重试中"这个状态与「停止重试」按钮能被免费回归。
	if s.simulateRetryOnce(ctx) {
		return nil // 用户在重试等待中点了停止重试：这一轮到此结束
	}

	s.event(map[string]any{"type": "agent_start"})
	s.event(map[string]any{"type": "turn_start"})

	// 本轮要处理的用户消息：prompt 本身，加上之前排队的 steer/follow_up。
	// 顺手把它们拼成本轮的用户输入，供 needsUI 判断扩展要不要弹确认。
	queued := s.takeQueued()
	s.setRoundPrompt(strings.Join(queued, "\n"))
	for _, message := range queued {
		index := s.startMessage(sessionMsg{role: "user", text: message})
		s.endMessage(index)
	}

	// 第一轮：思考 → 宣布调用工具 → 执行工具。
	// 每条消息都拿到自己的下标：流式过程中要按它回填内容，
	// 而消息表在流式期间是可能变长的（例如 steer 又插了一条用户消息）。
	thinking := sessionMsg{role: "assistant", content: []map[string]any{{"type": "thinking", "thinking": ""}}}
	thinkingIndex := s.startMessage(thinking)
	s.emitThinking(ctx, thinkingIndex, 0, "先想清楚这一步要做什么：确认环境、读一个文件、再给出结论。")
	s.endMessage(thinkingIndex)

	// 宣布调用工具的那条助手消息：stopReason=toolUse 是协议里的信号，
	// 界面据此把这条消息渲染成“工具调用卡片”而不是正文。
	toolMessage := sessionMsg{role: "assistant", content: s.toolCalls(ctx), stop: "toolUse"}
	toolIndex := s.startMessage(toolMessage)
	s.endMessage(toolIndex)
	if ctx.Err() != nil {
		// 被 abort：不再执行工具，直接进入收尾（收尾事件一个都不能少）。
		return nil
	}
	for _, call := range s.toolCallsFor(toolMessage) {
		s.runTool(ctx, call)
		// 把工具结果也作为**一条消息**放进会话，形状与真实 Pi 一致：
		// role=toolResult + 消息级 toolCallId/toolName/isError。
		// 少了这一步，"回放历史时把 toolResult 配回 toolCall"这条路径就永远测不到
		// ——之前界面上那个「工具调用 call_xx 没有对应记录」就是这么漏掉的。
		s.stateMu.Lock()
		// result 是 map（工具的返回值）；取出文本作为这条消息的正文
		resultText := ""
		if text, ok := call.result["text"].(string); ok {
			resultText = text
		} else if encoded, err := json.Marshal(call.result); err == nil {
			resultText = string(encoded)
		}
		s.state.messages = append(s.state.messages, sessionMsg{
			role:       "toolResult",
			content:    []map[string]any{{"type": "text", "text": resultText}},
			toolCallID: call.toolCallID,
			toolName:   call.toolName,
			isError:    call.isError,
		})
		s.state.messageCount = len(s.state.messages)
		s.stateMu.Unlock()
	}
	// turn_end 带上这条助手消息与它产生的工具结果，形状与真实 Pi 一致。
	s.event(map[string]any{"type": "turn_end", "message": toolMessage.json(), "toolResults": s.toolResultsFor(toolMessage)})

	// 第二轮：最终答复。
	s.event(map[string]any{"type": "turn_start"})
	s.turnStart()
	answer := sessionMsg{role: "assistant", content: []map[string]any{{"type": "text", "text": ""}}, stop: "stop"}
	answerIndex := s.startMessage(answer)
	if !s.emitText(ctx, answerIndex, 0, s.answerText()) {
		// 被中止：stopReason 让客户端能区分“正常结束”与“被中止”。
		// 内容已经由 emitText 回填成“已生成的那一段”，这里只改收尾原因。
		//
		// errorMessage 与真实 Pi 对齐（它中止时会 stopReason 与 errorMessage 一起给），
		// 否则"按停止却弹出报错"这类问题在 mock 下永远复现不出来。
		s.updateBlock(answerIndex, -1, "stopReason", "aborted")
		s.updateBlock(answerIndex, -1, "errorMessage", "This operation was aborted")
	}
	s.endMessage(answerIndex)
	// turn_end 必须从 state 里读回消息：abort 时 stopReason 是在流式过程中
	// 回填进 state 的，用本地副本会发出一个还写着 "stop" 的旧版本，
	// 界面就会把被中止的回合显示成正常结束。
	s.event(map[string]any{"type": "turn_end", "message": s.messageAt(answerIndex)})
	// 真实 Pi 会在这里带上整轮所有消息；这里同样只放本轮，
	// 与 get_messages 的全量历史区分开。
	s.event(map[string]any{"type": "agent_end", "messages": s.roundMessages(), "willRetry": false})
	return nil
}

// emitText 逐字发出一条助手文本，返回 false 表示中途被 abort。
// 停顿让界面能真的看到逐字效果，也给 abort 留出可观察的落点。
func (s *Server) emitText(ctx context.Context, message, index int, text string) bool {
	s.messageUpdate(map[string]any{"type": "text_start", "contentIndex": index})
	assembled := ""
	for _, char := range text {
		// select 的语义：ctx 被取消就收尾，否则等一个停顿再发下一个字。
		// 若把停顿改成 0，两个分支会同时就绪，Go 随机挑一个执行，
		// abort 就变成抽签——这就是 README 实验 1 提醒不要改成 0 的原因。
		select {
		case <-ctx.Done():
			// 被中止也要发 text_end 并把已生成的内容落进消息：
			// 界面已经渲染出来的部分需要一个收尾（否则“正在输入”的光标会一直留着），
			// 而且 message_end 是权威版本，内容不一致会让历史重放出现空消息。
			s.updateBlock(message, index, "text", assembled)
			s.messageUpdate(map[string]any{"type": "text_end", "contentIndex": index, "content": assembled})
			return false
		case <-time.After(s.textDelay):
		}
		delta := string(char)
		assembled += delta
		s.messageUpdate(map[string]any{"type": "text_delta", "contentIndex": index, "delta": delta})
	}
	s.updateBlock(message, index, "text", assembled)
	s.messageUpdate(map[string]any{"type": "text_end", "contentIndex": index, "content": assembled})
	return true
}

// emitThinking 发思考块。真实 Pi 的思考是模型推理过程，这里用固定文本，
// 但事件形状完全一致：start / delta×N / end，并且 end 与 message_end
// 里的内容保持一致（中止时是“已经生成的那一段”）。
func (s *Server) emitThinking(ctx context.Context, message, index int, text string) bool {
	s.messageUpdate(map[string]any{"type": "thinking_start", "contentIndex": index})
	assembled := ""
	for _, char := range text {
		select {
		case <-ctx.Done():
			s.updateBlock(message, index, "thinking", assembled)
			s.messageUpdate(map[string]any{"type": "thinking_end", "contentIndex": index, "content": assembled})
			return false
		case <-time.After(s.textDelay):
		}
		delta := string(char)
		assembled += delta
		s.messageUpdate(map[string]any{"type": "thinking_delta", "contentIndex": index, "delta": delta})
	}
	s.updateBlock(message, index, "thinking", assembled)
	s.messageUpdate(map[string]any{"type": "thinking_end", "contentIndex": index, "content": assembled})
	return true
}

// answerText 是最终答复的正文。它把三个学习点写进正文里：
// response 先到、事件后到、agent_settled 才算结束。
// answerText 是模拟回复的正文。
//
// 故意写成 Markdown：一份带标题、列表、表格、行内代码与代码块的回答，
// 刚好用来验证界面的 Markdown 排版（这也是真实模型的常见输出形态）。
func (s *Server) answerText() string {
	return "这是本地模拟回复，没有调用模型。\n\n" +
		"## 这一轮发生了什么\n\n" +
		"事件流分为三段，顺序固定：\n\n" +
		"1. `prompt` 的 **response** 先确认接收（只代表命令被接受）\n" +
		"2. 事件陆续到达：思考（`thinking_*`）→ 工具（`tool_execution_*`）→ 文本（`text_*`）\n" +
		"3. `agent_settled` 表示这一轮**彻底**结束，界面这时才能回到空闲\n\n" +
		"> 注意：`agent_end` 不等于结束——它可能带着 `willRetry` 表示还要重试。\n\n" +
		"| 记录 | 作用 | 是否代表结束 |\n" +
		"| --- | --- | --- |\n" +
		"| `response` | 命令被接受 | 否 |\n" +
		"| `agent_end` | 一次运行结束 | 否（可能重试） |\n" +
		"| `agent_settled` | 不会再有后续 | **是** |\n\n" +
		"想自己看一遍原始通信，可以打开顶栏「更多 → 原始事件日志」：\n\n" +
		"```json\n" +
		"{\"type\":\"prompt\",\"message\":\"你好\"}\n" +
		"{\"type\":\"response\",\"command\":\"prompt\",\"success\":true}\n" +
		"{\"type\":\"agent_settled\"}\n" +
		"```\n"
}

// toolCalls 决定本轮“调用”哪些工具。
//
// 两条固定调用（bash 失败、read 成功）让界面必须同时处理成功与失败两种渲染；
// 另外，如果用户消息里出现“确认/confirm”，就先发一条 extension_ui_request
// 等界面回答：确认了才两个都执行，超时或拒绝就跳过 read——
// 但“模型提出过这个调用”仍然留在消息里，因为那是已经发生的历史。
func (s *Server) toolCalls(ctx context.Context) []map[string]any {
	blocks := []map[string]any{
		{"type": "toolCall", "toolCallId": "call-1", "toolName": "bash", "args": map[string]any{"command": "go test ./..."}},
		{"type": "toolCall", "toolCallId": "call-2", "toolName": "read", "args": map[string]any{"path": "main.go"}},
	}
	if !s.needsUI() {
		return blocks
	}
	// 请求形状与真实 Pi 一致：必须有 type/id/method；
	// 带 timeout 的对话框由 Pi 侧自动兜底，客户端不必自己计时（这里也带上，
	// 顺便让界面知道“这个确认框会在 10 秒后自动关掉”）。
	s.event(map[string]any{
		"type": "extension_ui_request", "id": uiRequestID, "method": "confirm",
		"title": "扩展请求确认", "message": "是否允许执行 rm -rf ./tmp/build？",
		"timeout": uiTimeout.Milliseconds(),
	})
	if ctx.Err() != nil {
		// 回合已被取消：别再等界面了，直接按“没确认”收场。
		return blocks[:1]
	}
	answer, ok := s.waitUIResponse(ctx, uiRequestID, uiTimeout)
	// 没等到（超时/取消）或明确 rejected/cancelled 都按“没确认”处理：
	// 回合必须继续走完，界面不能卡在一个永远不会有答案的确认框上。
	confirmed := ok && answer["cancelled"] == nil
	if value, present := answer["confirmed"].(bool); present {
		confirmed = value
	}
	if !confirmed {
		return blocks[:1]
	}
	return blocks
}

// toolCallsFor 把消息里的工具调用块还原成可执行的账本。
func (s *Server) toolCallsFor(message sessionMsg) []toolResult {
	out := make([]toolResult, 0, len(message.content))
	for _, block := range message.content {
		if block["type"] != "toolCall" {
			continue
		}
		id, _ := block["toolCallId"].(string)
		name, _ := block["toolName"].(string)
		args, _ := block["args"].(map[string]any)
		call := toolResult{toolCallID: id, toolName: name, args: args}
		switch name {
		case "bash":
			// 失败工具：返回码非 0，同时带错误文本。
			// 这样界面既能看到“工具失败了”，也能看到失败原因。
			call.result = toolPayload("FAIL: TestLargeUnicodeRecord (模拟的测试失败)", map[string]any{"exitCode": 1})
			call.isError = true
		case "read":
			call.result = toolPayload("// 这里是模拟的文件内容", map[string]any{"path": "main.go", "lines": 52})
		default:
			call.result = toolPayload("ok", nil)
		}
		out = append(out, call)
	}
	return out
}

// toolResultsFor 生成 turn_end 里的 toolResults 数组。
// 每个结果用 toolCallId 指回它的调用，界面据此把“调用”和“结果”配成一张卡片。
func (s *Server) toolResultsFor(message sessionMsg) []any {
	calls := s.toolCallsFor(message)
	out := make([]any, 0, len(calls))
	for _, call := range calls {
		out = append(out, map[string]any{
			"role": "toolResult", "toolCallId": call.toolCallID, "toolName": call.toolName,
			"content": call.result["content"], "isError": call.isError,
		})
	}
	return out
}

// toolPayload 把工具返回值包装成真实 Pi 的形状：
//
//	{"content":[{"type":"text","text":"..."}],"details":{...}}
//
// 界面主要渲染 content 里的文本，details 放结构化补充信息。
// 按这个形状造数据，界面就不需要为“mock 的纯文本”写一条特殊分支。
func toolPayload(text string, details map[string]any) map[string]any {
	if details == nil {
		details = map[string]any{}
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"details": details,
	}
}

// runTool 发一次完整的工具执行：start → update（1~2 次）→ end。
// 字段与真实 Pi 完全一致（见 pi 的 docs/json.md “Tool execution events”）：
// update 也带 args，result/partialResult 用 content+details 的结构。
// 界面据此渲染“正在执行的工具”，而不是等整轮结束才知道发生过什么。
func (s *Server) runTool(ctx context.Context, call toolResult) {
	s.countToolCall()
	s.event(map[string]any{"type": "tool_execution_start", "toolCallId": call.toolCallID, "toolName": call.toolName, "args": call.args})
	if call.toolName == "bash" {
		// 只给长命令发增量：read 那种一次就返回的工具没有中间过程。
		for _, line := range []string{"=== RUN   TestRPCProcess\n", "--- FAIL: TestLargeUnicodeRecord\n"} {
			if ctx.Err() != nil {
				// 回合被取消：工具不再继续报增量，但仍然要走完 end，
				// 否则界面上那张工具卡片会永远停在“运行中”。
				break
			}
			s.event(map[string]any{
				"type": "tool_execution_update", "toolCallId": call.toolCallID, "toolName": call.toolName,
				"args": call.args, "partialResult": toolPayload(line, nil),
			})
		}
	}
	s.event(map[string]any{
		"type": "tool_execution_end", "toolCallId": call.toolCallID, "toolName": call.toolName,
		"result": call.result, "isError": call.isError,
	})
}

// countToolCall 累加工具调用次数，供 get_session_stats 使用。
// 真实 Pi 的 toolCalls/toolResults 是全会话累计值，这里也跟着累计。
func (s *Server) countToolCall() {
	s.stateMu.Lock()
	s.state.toolCalls++
	s.stateMu.Unlock()
}

// messageUpdate 发一条 message_update。
//
// 结构固定为 {"type":"message_update","usage":{...},"assistantMessageEvent":{...}}：
// usage 是累加用量，界面用它实时显示 token/费用（这是真实 Pi 也带的字段）。
// 客户端只关心 assistantMessageEvent.type、delta 和 contentIndex。
func (s *Server) messageUpdate(assistantEvent map[string]any) {
	s.emit(map[string]any{"type": "message_update", "usage": s.usageSnapshot(), "assistantMessageEvent": assistantEvent})
}

// usageSnapshot 是 message_update 里的累计用量，形状与真实 Pi 一致。
// 调用方不持锁：这里自己加锁，免得将来有人在持锁路径上误用它。
func (s *Server) usageSnapshot() map[string]any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return map[string]any{
		"input": s.state.tokensIn, "output": s.state.tokensOut,
		"cacheRead": 0, "cacheWrite": 0,
		"totalTokens": s.state.tokensIn + s.state.tokensOut,
		"cost":        map[string]any{"input": 0.0, "output": 0.0, "cacheRead": 0.0, "cacheWrite": 0.0, "total": 0.0},
	}
}

// startMessage 记一条 message_start 并把消息追加进会话历史，返回它的下标。
//
// 消息在 start 时就登记（而不是 end 时），这样界面在流式过程中
// 调 get_messages 也能看到“正在生成的那一条”。
func (s *Server) startMessage(message sessionMsg) int {
	s.stateMu.Lock()
	s.state.messageCount++
	s.state.messages = append(s.state.messages, message)
	index := len(s.state.messages) - 1
	s.stateMu.Unlock()
	s.event(map[string]any{"type": "message_start", "message": message.json()})
	return index
}

// endMessage 发 message_end。end 才是权威版本（含 stopReason 这类收尾字段），
// 所以它必须读“流式过程中被回填过”的那份消息，而不是 start 时的空壳。
func (s *Server) endMessage(index int) {
	s.event(map[string]any{"type": "message_end", "message": s.messageAt(index)})
}

// updateBlock 把流式生成的内容写回“状态里的那条消息”。
//
// 为什么必须回填？message_end 才是权威版本，如果它带着空内容发出去，
// 界面在流式结束后重放历史就会看到一条空消息——这是最典型的“边发边丢”。
// 另外本函数也负责写消息级字段（index 传 **negative**，如 stopReason="aborted"），
// 因为消息一旦进了 state，改本地副本就不再生效了。
func (s *Server) updateBlock(message, index int, key, value string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if message < 0 || message >= len(s.state.messages) {
		return
	}
	target := &s.state.messages[message]
	if index < 0 {
		// 消息级字段：stopReason / errorMessage 走这条路。
		switch key {
		case "stopReason":
			target.stop = value
		case "errorMessage":
			target.errorMessage = value
		}
		return
	}
	if index >= len(target.content) {
		return
	}
	target.content[index][key] = value
}

// messageAt 返回某条消息的协议形状（调用方拿它发 turn_end/agent_end）。
func (s *Server) messageAt(index int) map[string]any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if index < 0 || index >= len(s.state.messages) {
		return map[string]any{}
	}
	return s.state.messages[index].json()
}

// roundStart 记下本轮起点，并给统计加一点输入 token。
// get_state / get_session_stats 的数字要跟着回合走，否则界面上的统计是死的。
func (s *Server) roundStart() {
	s.stateMu.Lock()
	s.state.roundStart = len(s.state.messages)
	s.state.tokensIn += 1200
	s.stateMu.Unlock()
}

// turnStart 给统计加一点 token，模拟“每轮都要把历史重新喂给模型”。
func (s *Server) turnStart() {
	s.stateMu.Lock()
	s.state.tokensIn += 900
	s.state.tokensOut += 240
	s.stateMu.Unlock()
}

// setRoundPrompt 记录本轮的用户输入。它必须在发出任何事件之前设好：
// needsUI 会在“宣布调用工具”那一步读它，而那时消息表里最后一条已经是思考消息。
func (s *Server) setRoundPrompt(text string) {
	s.stateMu.Lock()
	s.state.roundPrompt = text
	s.stateMu.Unlock()
}

// takeQueued 取出排队的用户消息，取完即清空。
// 顺序：先 follow_up 再 steer——prompt 本身在 followUps 里排在最前，
// 也就是“先回答主问题，再处理插话与补话”。
func (s *Server) takeQueued() []string {
	s.stateMu.Lock()
	queued := append(append([]string(nil), s.state.followUps...), s.state.steerQueue...)
	s.state.followUps, s.state.steerQueue = nil, nil
	s.stateMu.Unlock()
	// 队列被本轮吃掉了：广播空队列，排队面板才能及时清空（真实 Pi 在
	// 消费每一条时都会 _emitQueueUpdate）。
	if len(queued) > 0 {
		s.emitQueueUpdate()
	}
	return queued
}

// roundMessages 返回本轮新增的消息（agent_end 只带本轮）。
func (s *Server) roundMessages() []any {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	out := make([]any, 0, len(s.state.messages)-s.state.roundStart)
	for _, message := range s.state.messages[s.state.roundStart:] {
		out = append(out, message.json())
	}
	return out
}

// needsUI 判断这一轮是否要求走一次“扩展确认”。
//
// 真实 Pi 里这是扩展自己的判断，模拟器用一个关键词近似：
// 本轮用户输入里出现 confirm / 确认 就发一次 extension_ui_request。
func (s *Server) needsUI() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	text := strings.ToLower(s.state.roundPrompt)
	return strings.Contains(text, "confirm") || strings.Contains(text, "确认")
}

// newSessionID / sessionIDFromPath 只在模拟器里用：真实 Pi 的会话 id 由它自己生成，
// 这里模仿命令行观察到的形态，好让界面上的"会话 id 变了"这件事可见。
func newSessionID() string {
	return "mock-session-" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
}

// sessionIDFromPath 从会话文件名里取 uuid。
// 真实 Pi 的会话文件形如 2026-10-03T04-55-32-796Z_01a1001e-10bc-712e-aae9-cf8fd602e4a0.jsonl，
// 下划线之后就是会话 id；取不到就退回文件名（保证界面上有个稳定标识）。
func sessionIDFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if idx := strings.LastIndex(base, "_"); idx >= 0 && idx+1 < len(base) {
		return base[idx+1:]
	}
	return base
}
