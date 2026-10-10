package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"zeroa/internal/agent"
	"zeroa/internal/delegation"
	"zeroa/internal/pi"
	toolkit "zeroa/internal/tools"
)

// requestTimeout 是所有命令的超时上限。
//
// 选 15 秒的理由：get_state/abort 这类命令本该毫秒级返回；
// prompt/steer/follow_up 的 response 也只代表“命令被接受”，不会等模型跑完。
// 所以 15 秒足够宽松，又不会让界面在协议卡死时无限转圈。
const requestTimeout = 15 * time.Second

// maxPromptBytes 限制界面输入的消息长度。
// 命令行入口没有这个限制（见 README 的“约束”），这里防的是
// “一个手滑粘贴进来的巨大文本把一条 JSONL 撑爆”这一类误操作。
const maxPromptBytes = 32 * 1024

// Result 是前端唯一需要认识的返回值类型。
//
// 为什么不让前端直接拿 pi.Response？因为那样 Wails 生成的绑定会把
// json.RawMessage 变成一个需要前端自己 JSON.parse 的字节数组，
// 而且 error 字段（协议错误）与 Go 的 error（RPC 失败）会混在一起，
// 界面上分不清“命令被拒绝”和“进程没起来”。
//
// 统一成 Result 之后规则只有两条：
//   - Go 的 error 非空 ⇒ 连结果都没有（没启动、超时、校验失败）；
//   - Result.Success 为 false ⇒ Pi 明确回了失败，原因在 Result.Error。
type Result struct {
	/**
	 * RuntimeID 是"这条结果来自哪个会话进程"。
	 *
	 * 一进程一会话之后，同一时刻可能有多个 Pi 在跑；前端的路由必须能分辨
	 * "这是当前会话的回答"还是"后台会话的回答"（后者要留在它自己的时间线里）。
	 */
	RuntimeID string `json:"runtimeId,omitempty"`
	ID        string `json:"id"`
	Command   string `json:"command"`
	Success   bool   `json:"success"`
	Data      string `json:"data"` // 该命令 data 字段的原始 JSON 文本；无数据为空串
	Error     string `json:"error,omitempty"`
}

// newResult 把协议层的 Response 转成前端契约里的 Result。
// Data 保持“原始 JSON 文本”：结构由命令决定（get_state 是对象、
// get_messages 是数组、abort 可能没有 data），这一层不猜业务结构，
// 所以 Pi 增加字段时 Go 侧不需要跟着改。
func newResult(response pi.Response) Result {
	return Result{
		ID:      response.ID,
		Command: response.Command,
		Success: response.Success,
		Data:    string(response.Data),
		Error:   response.Error,
	}
}

// PiService 是暴露给前端的唯一门面：方法少、参数明确、返回结构化结果。
// 真正的协议实现留在 internal/pi —— 前端不需要知道 JSONL、id、管道这些东西。
//
// 它刻意不提供通用的 Send(command, fields) 之类入口：能从前端发出任意命令，
// 就等于把协议层整个暴露出去，界面代码会长出一堆“拼字段”的调用。
// 每加一个能力就加一个具名方法，参数在 Go 侧校验一次。
//
// 命名约定：首字母大写的方法会被 Wails 生成 TypeScript 绑定；
// 以 Service 开头的方法（ServiceShutdown）是生命周期钩子，不暴露给前端。
type PiService struct {
	/**
	 * navigation 串行化"激活/关闭会话进程"这类结构性操作。
	 *
	 * 为什么单独一把锁，而不是用 mu：切换会话涉及"起进程 → switch_session → get_state"
	 * 好几次可能很慢的 RPC，全程持 mu 会把读取 goroutine 上的回调一起卡死
	 *（回调要拿 mu 缓存 sessionId）。navigation 只保护"换 activeRuntime"这一段。
	 */
	navigation sync.Mutex
	// runtimes 是所有**活着的**会话进程：一条会话一个进程（见 session_runtime.go）。
	//
	// 为什么不再是一个 client：Pi 的 switch_session 是"让这个进程改绑另一份会话文件"，
	// 正在跑的那一轮就长在这个进程里 —— 切走 = 把它连根拔掉。
	// 一进程一会话之后，切换只是"换一个进程说话"，跑着的那个继续跑。
	runtimes      map[string]*sessionRuntime
	activeRuntime *sessionRuntime
	nextRuntime   uint64
	// options 是启动参数模板：冷启动一条新会话时按它建进程（工作目录可按会话头覆盖）。
	options pi.Options
	/**
	 * optionsReady 表示"启动参数模板已经准备好"（跑过一次 Start）。
	 *
	 * 为什么要单独一个标记，而不是判"有没有活着的进程"：新建会话改成**懒创建**之后
	 * （见 sessions.go 的 NewSession），"还没有任何进程"是正常状态——
	 * 用户点一条历史会话、或发出第一条消息时，我们正是要**按这个模板**去起进程。
	 * 而 Start 从没成功跑过（例如授权没过）时，模板是空的，那时才该报"请先启动 Pi"。
	 *
	 * 它**不随 Stop 清掉**：Stop 收的是进程，不是"这套启动参数"。
	 * Stop 之后用户再发一条消息，行为与旧实现一致——旧实现是前端在 prompt 之前
	 * 自动补一次 Start（见 useAgent 的 prompt），现在由 Go 侧按需起进程。
	 */
	optionsReady bool
	// mu 保护下面的所有运行状态：界面可能同时点“启动/关闭/发送”，
	// 而 trace 回调还跑在读取 goroutine 上。
	mu sync.Mutex
	// client 是**当前会话**的进程句柄（= activeRuntime.client）。
	// 保留它是因为大量既有代码按"给当前会话发命令"来写；新代码请用 runtimeRequest。
	client *pi.Client
	// configMu 串行化 models.json / auth.json 的“读-改-写”。单次 rename 虽然原子，
	// 但两个并发设置若都从同一个旧快照开始，后写者仍会覆盖先写者。
	configMu sync.Mutex
	// OnEvent 由 main.go 赋值：把一条 trace 同时转成 "pi:trace" 与 "pi:event"。
	// 它必须立刻返回（跑在读取 goroutine 上），所以实现里只能做非阻塞的 Emit。
	OnEvent func(pi.Trace)
	// loadResources：启动 Pi 时是否加载用户资源（扩展/技能/提示模板/主题）。
	// 默认 true —— 关掉它斜杠命令列表必然是空的（get_commands 只返回这类东西）。
	loadResources bool
	// loadResourcesSet 记录用户是否显式设置过（false 的零值与"没设过"必须区分开）
	loadResourcesSet bool
	// sessionID 缓存最近一次在响应里看到的会话 id。
	// 目前只是内部状态：会话 id 的正规来源仍是 get_state，
	// 这里缓存是为了将来做多会话切换时不用反复问 Pi。
	sessionID string
	// sessionFile 缓存当前会话文件路径（get_state.sessionFile）。
	// 重命名/删除会话时要靠它判断"哪个文件正在使用中"。
	sessionFile string
	/**
	 * pendingSessionFile 是"用户正在看这条会话，但它还没有进程"。
	 *
	 * 为什么要有它：会话历史在 .jsonl 里，读历史**不需要进程**，所以"打开一条会话"
	 * 不再冷启动 Pi（实测 1–3.5 秒，而换来的只是一份磁盘上已有的消息数组）。
	 * 于是"当前会话"必须能表达成两种状态：
	 *   · activeRuntime 非空 → 这条会话有一个活着的进程；
	 *   · activeRuntime 为空、pendingSessionFile 非空 → 用户在**看**它，进程还没建。
	 *
	 * 进程留到用户发出第一条消息时才由 ensureActiveRuntime 现起，而且**必须先
	 * switch_session 到这份文件再发 prompt**——否则新进程会把这句话写进一个
	 * 刚出生的空会话，用户的历史就断在了那一句上（界面还看不出来）。
	 */
	pendingSessionFile string
	// workspaceDir 是本次启动 Pi 的工作区根目录。文件树与文本预览只能访问这里，
	// 避免 WebView 绑定退化成任意本地文件读取接口。
	//
	// ⚠️ 它随进程生死（Stop 会清空），所以**不能**单独拿它当文件浏览的边界：
	// 切换工作区时先 Stop 再 Start，中间有一段时间它是空的，那时文件面板请求
	// 新工作区就会被判成越界（用户看到的就是"只能访问当前工作区内的文件"）。
	workspaceDir string
	// currentWorkspace 是界面当前正在看的工作区：由前端在启动/切换时上报
	// （SetActiveWorkspace），Start 也会写。它不随进程退出清空，是文件浏览的真正边界。
	currentWorkspace string
	// starting 表示“有一次 Start 正在创建进程”。
	//
	// 它的作用只有一个：让并发的第二个 Start 立刻返回，而不是又拉起一个进程
	// （两个进程同时活着，先起来的那个就再也没人管了）。
	// 之所以不干脆在锁里启动，是因为回调会在读取 goroutine 上立刻拿这把锁，
	// 持锁做慢操作会把自己的回调卡住。
	starting bool
	// stopping 记录“启动还没结束就有人要求关闭”。
	//
	// 关窗时 Wails 会调用 ServiceShutdown，而界面可能刚点过启动：
	// 如果没有这个标记，关窗那一刻新建好的进程就变成没人认领的孤儿进程。
	// 启动流程结束时看到它，就把刚建好的进程直接关掉。
	stopping bool
	// runningWork 由前端上报"当前是否有回合在跑"。关窗前要问它：
	// 直接关掉会把正在生成的回合打断，用户至少该被问一次（见 RequestClose）。
	runningWork bool
	// loop 是任务循环状态机（方案 P11）：Pi 管一轮之内的 agent loop，
	// Go 管一轮之后还要不要继续、卡住了怎么办。
	loop taskLoop
	// compiledRole 是当前生效的编译产物（界面显示 Effective Configuration 用）
	compiledRole *CompiledRole

	// activeRole 是当前进程启动时用的角色 id（空 = 无角色）。
	// 它只用于"界面显示的是不是实际生效的角色"这类一致性检查。
	activeRole string
	// cliPath / startModel / mode：本次启动用的 Pi 入口、模型与模式，子智能体沿用
	cliPath    string
	startModel string
	mode       string
	// delegation 是委派域管理器（子智能体的登记/执行/投递/控制桥，懒创建，
	// 见 delegation.go 的 delegationManager）。实现已迁到 internal/delegation。
	delegation *delegation.Manager
	// parentBusy：父会话此刻是否在跑回合（agent_start → true，agent_settled → false）。
	// 子智能体完成时用它决定"立刻投递"还是"等父稳定下来再投"。
	parentBusy bool
	// OnAgentEvent 由 main.go 赋值：把子智能体的状态变化推给前端。
	OnAgentEvent func(kind, payload string)
	// sessionName 是当前会话的名字（NameCurrentSession 写下、get_state 回填）。
	// 缓存它只是为了不必为一个名字多跑一次 RPC。
	sessionName string
	// forceClosing 为真表示用户已经在弹窗里确认强制关闭，之后的关闭请求直接放行。
	forceClosing bool
	// OnCloseRequest 由 main.go 赋值：通知前端"有东西在跑，弹确认框"。
	OnCloseRequest func()
	// Quit 由 main.go 赋值：真正退出应用（含 Worker/ServiceShutdown）。
	Quit func()
	// OnSessionSwitch 由 main.go 注入：把"会话进程切换完成"的 payload 推给前端。
	// 事件名 pi:session（见 main.go 的事件常量）。
	//
	// 一进程一会话之后它只在"后台换当前会话"这类场景用得上（前端主要靠自己调
	// SwitchSession 的返回值刷新）；保留是因为任务循环仍可能在后台换当前会话。
	OnSessionSwitch func(data string)
}

// Start 启动 Pi 子进程。
//
// 参数就是界面「设置」里的选择：工作目录、模型、思考级别、工具白名单。
// 它们不是启动后再补发命令，而是直接进 Pi 的命令行参数——这样连
// “第一个回合用的就是所选模型”这件事都不需要额外往返。
//
// 幂等语义：已经有存活进程时直接返回 nil，不报“请先关闭”——
// 界面在重新挂载时会无脑调一次 Start，用“是否拿到 get_state 的响应”
// 来判断连接状态，比让前端维护“启动过了没”的布尔量可靠得多。
// 旧进程已经退出（比如被用户手动杀掉）时，这里清掉句柄重新启动。
// resourceLoading 返回"本次启动会不会加载扩展/技能/提示模板"。
//
// 零值与"没设过"必须区分：用户没动过这个开关时应当**加载**（默认开），
// 所以 loadResourcesSet 为 false 时返回 true。
func (s *PiService) resourceLoading() bool {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.loadResources || !s.loadResourcesSet
}

// SetLoadResources 设置"启动 Pi 时是否加载扩展/技能/提示模板/主题"。
//
// 为什么要显式设置：Go 的零值是 false，而这里**默认必须是 true**（否则斜杠命令永远是空的）。
// 所以 Start 里用 loadResourcesSet 区分"没设过"与"显式设成 false"。
func (s *PiService) SetLoadResources(enabled bool) (Result, error) {
	s.mu.Lock()
	s.loadResources = enabled
	s.loadResourcesSet = true
	s.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"enabled": enabled})
	return Result{Command: "set_load_resources", Success: true, Data: string(payload)}, nil
}

/**
 * Start 准备**启动参数模板**，不创建任何 Pi 进程。
 *
 * ## 为什么不再起进程
 *
 * 实测的启动时间线里，进入应用要等两次冷启动（各 1–3.5 秒）：
 * 一次是这里给"当前空会话"起的，一次是恢复上次那条会话又起的。
 * 而对齐 DSH 的认识是：**会话历史在 .jsonl 里，读历史不需要进程**——
 * 那两次冷启动换来的东西磁盘上都有。进程只是"干活时按需借来的资源"，
 * 所以它留到用户发出第一条消息时由 ensureActiveRuntime 现起
 * （那时人在等回答，1–3.5 秒花在"等它开始干活"上，而不是"点了没反应"上）。
 *
 * 参数就是界面「设置」里的选择：工作目录、模型、思考级别、工具白名单、角色。
 * 它们不是启动后再补发命令，而是直接进 Pi 的命令行参数——这样连
 * “第一个回合用的就是所选模型”这件事都不需要额外往返。
 *
 * 幂等语义：重复调用只是按新参数把模板重写一遍（界面在重新挂载时会无脑调一次
 * Start，用“是否拿到 get_state 的响应”判断连接状态，比让前端维护“启动过了没”可靠）。
 * 已经活着的进程不受影响——它们带着**自己出生时**的参数继续跑，
 * 这正是"一进程一会话"该有的样子（换参数不该掐掉别人的回合）。
 */
func (s *PiService) Start(mode, cliPath, cwd, model, thinkingLevel, tools, roleID string) error {
	s.mu.Lock()
	if s.starting {
		// 另一次 Start 正在准备模板：同样按幂等处理，等它准备完即可。
		s.mu.Unlock()
		return nil
	}
	s.starting = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.starting = false
		/**
		 * stopping 也一起清掉。
		 *
		 * 它的用途是"启动还没结束就有人要求关闭"时，把**刚建好的进程**收掉；
		 * 现在 Start 不建进程，就没有"启动中的进程"这回事了——留着它会永远为真，
		 * 让下一次启动的收尾逻辑以为有人在关窗。
		 */
		s.stopping = false
		s.mu.Unlock()
	}()

	workspaceDir := resolveCwd(cwd)
	// 边界立刻生效（不等进程起来）：文件面板可能在这之前就来列目录了。
	// workspaceDir 也跟着写：它原来表示"进程的工作目录"，现在表示"这次启动用的工作区"，
	// 否则"工作区正在使用不能删"的判定在懒启动之后会失效（那时一个进程都没有）。
	s.mu.Lock()
	s.currentWorkspace = workspaceDir
	s.workspaceDir = workspaceDir
	// 记住本次启动的 CLI 与模型：子智能体要沿用同一套（否则会出现
	// "父用的 node 版 Pi、子去找全局 pi 命令"这种莫名其妙的失败）。
	s.cliPath = cliPath
	s.startModel = model
	s.mode = mode
	s.mu.Unlock()

	// 委派桥：起一个只服务本机的小控制口，并把地址/令牌注入 Pi 的环境。
	// 失败不算致命——没有它只是"模型不能主动派子智能体"，其它功能照常。
	if err := s.ensureBridge(); err != nil {
		Logf("delegate_bridge_failed err=%v", err)
	}

	// 角色只影响"启动参数长什么样"：Pi 的 RPC 没有 set_system_prompt / set_tools，
	// 提示词与工具集只能是启动时的选择，所以切换角色必然是"换一套启动参数"（见 roles.go）。
	options := pi.Options{
		Mode:    mode,
		CLIPath: cliPath,
		// 工作目录默认 = exe 同目录：双击启动时在程序所在目录干活，而不是系统目录
		Cwd:      workspaceDir,
		Model:    model,
		Thinking: thinkingLevel,
		Tools:    parseTools(tools),
		// 界面读的配置目录和 Pi 用的必须是同一个（便携模式下是 <exe目录>/.pi）
		AgentDir: agentDir(),
		// 是否加载用户资源（扩展 / 技能 / 提示模板 / 主题）：
		// 设置里可关；开着才可能有斜杠命令（get_commands 返回的就是这类东西）。
		// 没被显式设置过时按 true —— Go 零值是 false，不能让"默认放开"变成"默认关闭"。
		LoadResources: s.loadResources || !s.loadResourcesSet,
	}
	// 角色：先编译，再把编译产物落到启动参数上。
	//
	// 编译是角色的唯一出口（方案第五节）：role → CompiledRole → pi.Options。
	// 角色不存在（被删了 / id 写错）时**不阻止启动**：退回无角色启动，
	// 否则一个坏配置会让整个界面用不了。
	if id := strings.TrimSpace(roleID); id != "" {
		if view, ok := findRole(id); ok {
			compiled, err := compileRole(view.Role, options.LoadResources)
			if err != nil {
				return err
			}
			compiled.ApplyToOptions(&options)
			s.loop.SetPolicy(compiled.Loop)
			s.mu.Lock()
			s.compiledRole = &compiled
			s.activeRole = strings.ToLower(id)
			s.mu.Unlock()
			for _, warning := range compiled.Warnings {
				Logf("role_compile_warning role=%s %s", id, warning)
			}
			if summary := toolkit.DescribeResolution(compiled.Resolutions); summary != "" {
				Logf("role_capabilities role=%s %s", id, summary)
			}
		} else {
			Logf("start_role_not_found id=%s", id)
			s.mu.Lock()
			s.activeRole = ""
			s.compiledRole = nil
			s.mu.Unlock()
			s.loop.SetPolicy(LoopPolicy{})
		}
	} else {
		s.mu.Lock()
		s.activeRole = ""
		s.compiledRole = nil
		s.mu.Unlock()
		s.loop.SetPolicy(LoopPolicy{})
	}
	options.ExtraEnv = s.bridgeEnvFor()
	/**
	 * 模板要留住：第一条消息（或切工作区之后的第一条消息）就是按它建进程的
	 * （见 ensureActiveRuntime）。不存的话，进程只能用一堆零值参数启动——
	 * 模型、工具白名单、角色提示词全丢。
	 */
	s.mu.Lock()
	s.options = options
	// 模板准备好了：此后"没有进程"不再等于"没启动过"（见 optionsReady 的说明）。
	s.optionsReady = true
	/**
	 * 一套新的启动参数 = 一条**新会话**。
	 *
	 * 所以这里把"用户在看的那条待创建会话"清掉：旧参数下的那条会话不该被
	 * 新参数悄悄接管（切角色、改设置之后用户预期的是"开一条新的"）。
	 * 想继续看某条历史会话时，前端会再走一次 SwitchSession（打开会话），
	 * 那时 pendingSessionFile 会被重新写上。
	 */
	s.pendingSessionFile = ""
	s.mu.Unlock()

	/**
	 * 新参数 = 没有任何回合在跑。
	 *
	 * 这一句专治"切换角色之后点会话没反应"：前端的角色切换是 stop + start，
	 * 旧进程里那一轮的 agent_settled 永远等不到，忙碌标记会一直为真。
	 */
	s.clearTurnState("process_started")
	/**
	 * 角色的"运行时可改"部分（自动压缩）是**进程起来之后**补发的 RPC，
	 * 这里没有进程可发——它由 ensureActiveRuntime 在懒启动的那个进程上下发
	 * （见该函数末尾的 applyRoleAutoCompaction）。
	 */
	Logf("pi_options_ready mode=%s cwd=%s role=%s", mode, workspaceDir, roleID)
	return nil
}

/**
 * applyRoleAutoCompaction 把角色的自动压缩设置下发给**当前进程**。
 *
 * 为什么单独一个函数：进程现在有两条出生路径——Start（界面点启动/切角色）
 * 与 ensureActiveRuntime（懒启动，见 session_runtime.go）。两边都要下发同一份设置，
 * 各写一遍迟早会漏掉一条（症状是"角色只生效一半"：提示词生效了、压缩没生效，
 * 而界面上完全看不出来）。
 */
func (s *PiService) applyRoleAutoCompaction(roleID string) {
	stringPtr := s.roleAutoCompactionSetting()
	if stringPtr == nil {
		return
	}
	// 异步下发：这是进程起来之后的补充动作，不该让 Start / prompt 等它。
	go func() {
		payload := map[string]any{"enabled": *stringPtr}
		if _, err := s.request("set_auto_compaction", payload); err != nil {
			Logf("apply_role_auto_compaction_failed role=%s enabled=%v err=%v", roleID, *stringPtr, err)
		}
	}()
}

// roleAutoCompaction 取角色的自动压缩设置（空角色/未设置时返回 nil = 不改动）。
func roleAutoCompaction(roleID string) *bool {
	id := strings.TrimSpace(roleID)
	if id == "" {
		return nil
	}
	view, ok := findRole(id)
	if !ok {
		return nil
	}
	return view.Role.AutoCompaction
}

// NameCurrentSession 给**当前**会话改名（Pi 的 set_session_name）。
//
// 与 RenameSession 的区别：那个是"改某一条历史会话的名字"，必须切过去再切回来；
// 这个是"给现在这条改名"，直接调 RPC 即可——切换角色后给新会话打上角色前缀
// 走的就是这条路径。
func (s *PiService) NameCurrentSession(name string) (Result, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return Result{}, errors.New("会话名不能为空")
	}
	if len([]rune(trimmed)) > 80 {
		trimmed = string([]rune(trimmed)[:80])
	}
	result, err := s.request("set_session_name", map[string]any{"name": trimmed})
	if err != nil {
		return Result{}, err
	}
	// 顺手更新缓存：后续 get_state 与界面都用得上
	s.mu.Lock()
	s.sessionName = trimmed
	s.mu.Unlock()
	return result, nil
}

// Stop 关闭 Pi 进程。Close 内部保证“先关 stdin 让 Pi 自己退出，超时才强杀”，
// 所以这里无论成功与否都释放句柄：否则一个半死的 client 会挡住下一次 Start。
func (s *PiService) Stop() error {
	s.clearTurnState("stop")
	s.navigation.Lock()
	defer s.navigation.Unlock()

	s.mu.Lock()
	if s.starting {
		// 进程正在创建：标记一下，让 Start 收尾时把它关掉（见上面的 stopping）。
		s.stopping = true
	}
	/**
	 * 一进程一会话：这里要收掉**全部**会话进程，而不只是当前那个。
	 *
	 * 漏掉后台的会怎样：那些进程没有界面引用它们了，用户既看不到也控制不了，
	 * 只能靠任务管理器杀——而且它们还占着授权名额与内存。
	 * 用 map 去重是因为同一份 client 可能同时挂在 active 与 runtimes 上。
	 */
	clients := make(map[*pi.Client]bool)
	if s.client != nil {
		clients[s.client] = true
	}
	for _, runtime := range s.runtimes {
		if runtime.client != nil {
			clients[runtime.client] = true
		}
	}
	runtimes := make([]*sessionRuntime, 0, len(s.runtimes))
	for _, runtime := range s.runtimes {
		runtimes = append(runtimes, runtime)
	}
	s.runtimes = nil
	s.activeRuntime = nil
	s.client = nil
	// 注意：options / optionsReady **不在这里清**。收掉的是进程，不是"这套启动参数"——
	// Stop 之后用户再发一条消息时，进程要按同一套参数重新起（见 ensureActiveRuntime）。
	s.sessionID = "" // 会话随进程结束，缓存跟着失效
	s.sessionFile = ""
	/**
	 * pendingSessionFile 也一起清掉：Stop 之后这条会话**不再被使用**，
	 * 下一条消息按旧行为开一条新会话（进程按参数模板起，不做 switch_session）。
	 * 留着它会让"停掉再发消息"悄悄接回上一条会话，而界面已经按"要开新会话"提示过了。
	 */
	s.pendingSessionFile = ""
	// 只清"进程用过的目录"：currentWorkspace 是界面正在看的工作区，
	// 进程停了界面还停在原地，文件面板要继续能列目录（它是下一个 Start 的边界）。
	s.workspaceDir = ""
	manager := s.delegation
	s.mu.Unlock()

	for _, runtime := range runtimes {
		runtime.closed.Store(true) // 避免 closeRuntime 再去动已经清空的表
	}
	// 全部会话都要停了，子智能体必须跟着停——不管是哪条会话派的。
	//
	// 这是用户报的问题：父 Agent 已经收工（甚至解出题目了），子 Agent 还在跑——
	// 烧 token、占内存，而且**结果已经没人收**（父进程都没了，投递不进去）。
	// 子智能体的生命周期必须挂在父会话上，不能自己活着。
	if manager != nil {
		if sup := manager.SupervisorIfAny(); sup != nil {
			if stopped := sup.CancelAll("父会话已停止"); stopped > 0 {
				Logf("delegation_cancelled_on_stop count=%d", stopped)
			}
		}
		// 本地控制桥（父 Pi 里的扩展 → Go）也要跟着停：它是"能派子智能体"的入口，
		// 授权失效后继续监听没有意义，而且令牌一旦签发就终身有效（见 internal/delegation）。
		// 下次 Start 会重新建桥并换新令牌。
		if manager.StopBridge() {
			Logf("delegate_bridge_stopped")
		}
	}
	var firstErr error
	for client := range clients {
		if err := client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if len(runtimes) > 0 {
		Logf("session_runtimes_stopped count=%d", len(runtimes))
	}
	return firstErr
}

// mergeRoleOptions 把角色编译出的参数叠加到基础启动参数上。
//
// 只覆盖角色**显式声明**的字段：角色没说的事（比如不给提示词、不管技能）
// 保持基础值，这样"角色"是增量而不是"必须写全的完整配置"。
// 注意 Tools 的 nil 与空切片语义不同（不限制 / 全禁），所以用 nil 判断而不是长度。
func mergeRoleOptions(base *pi.Options, role pi.Options) {
	if role.SystemPrompt != "" {
		base.SystemPrompt = role.SystemPrompt
	}
	if len(role.AppendSystemPromptFiles) > 0 {
		base.AppendSystemPromptFiles = role.AppendSystemPromptFiles
	}
	if role.Tools != nil {
		base.Tools = role.Tools
	}
	if len(role.ExcludeTools) > 0 {
		base.ExcludeTools = role.ExcludeTools
	}
	if role.Skills != nil {
		base.Skills = role.Skills
	}
	if role.Extensions != nil {
		base.Extensions = role.Extensions
	}
	if role.PromptTemplates != nil {
		base.PromptTemplates = role.PromptTemplates
	}
	if len(role.Models) > 0 {
		base.Models = role.Models
	}
	if role.ContextFiles != nil {
		base.ContextFiles = role.ContextFiles
	}
	// 模型与思考级别：角色给了就用角色的，没给保持界面里的选择
	if strings.TrimSpace(role.Model) != "" {
		base.Model = role.Model
	}
	if strings.TrimSpace(role.Thinking) != "" {
		base.Thinking = role.Thinking
	}
}

// roleAutoCompactionSetting 取当前生效角色的自动压缩设置（没角色/没声明时 nil）。
func (s *PiService) roleAutoCompactionSetting() *bool {
	s.mu.Lock()
	compiled := s.compiledRole
	s.mu.Unlock()
	if compiled == nil {
		return nil
	}
	return compiled.Runtime.AutoCompaction
}

// ObserveAgentEvent 让任务循环看到事件流（由 main.go 的事件回调调用）。
//
// 这条回调跑在读取 goroutine 上，所以只能做"不阻塞"的事：
// ObserveTool / Settle 都只改内存里的计数，发消息交给 goroutine。
func (s *PiService) ObserveAgentEvent(envelope agent.Envelope) {
	/**
	 * 后台会话的事件**不能**改前台的回合状态。
	 *
	 * 一进程一会话之后，这条回调会收到所有会话进程的输出。如果不筛，
	 * B 会话（后台）发一个 agent_start，就会被当成"A 会话在跑回合"：
	 *   · 关窗保护会以为前台在忙；
	 *   · 子智能体结果会被"攒着等前台空闲"，而后台那条会话才是该收结果的人。
	 * 每一条会话自己的 busy 状态由 sessionRuntime 单独维护（见 noteRuntimeEvent）。
	 */
	// 审计证据按会话隔离、且后台会话也记账：它在谁的进程里动的手，证据就记在
	// 谁头上（委派闸门按会话查）。所以这一段放在"属于当前会话"的过滤**之前**。
	if envelope.Type == "tool_execution_start" {
		s.noteParentToolCall(envelope.RuntimeID)
	}
	if !s.eventBelongsToActiveRuntime(envelope.RuntimeID) {
		return
	}
	switch envelope.Type {
	case "agent_start":
		// 父会话开始跑回合：子智能体的结果先攒着，等这一轮结束再投
		s.markParentBusy(true)
	case "tool_execution_start":
		var record struct {
			ToolName string `json:"toolName"`
			Args     any    `json:"args"`
		}
		if json.Unmarshal([]byte(envelope.Record), &record) == nil {
			s.loop.ObserveTool(record.ToolName, record.Args)
		}
	case "message_end":
		// 主 Agent 的每条回复都看一眼：出现候选 flag 就停掉还在跑的子智能体
		// （见 delegation_flag.go：只有"已经拿到答案"才真的不需要它们）
		s.observeParentMessage(envelope)
	case "agent_settled":
		// 先跑任务循环（它可能决定再来一轮），再按"父已稳定"投递攒下的子结果
		s.handleSettled()
		s.markParentBusy(false)
	case "process_exit":
		// 进程都没了，不可能还有回合在跑。
		//
		// 这里以前只 Pause 循环、没清忙碌标记，后果很隐蔽：一次崩溃/被 kill 之后
		// parentBusy 永远是 true，于是**之后每一次点会话都被"排队等回合结束"**——
		// 而那个回合早就不存在了，永远不会结束（用户实测：连续 4 次点击都没切成）。
		s.clearTurnState("process_exit")
		s.loop.Pause("进程已退出")
	}
}

// handleSettled 在一轮结束时决定下一步（方案第十八、十九节）。
//
// 默认策略是"什么都不做"：没有 loop 配置的角色判断结果一定是 continue，
// 所以这个函数对普通对话完全透明。
func (s *PiService) handleSettled() {
	decision := s.loop.Settle(time.Now())
	switch decision.Action {
	case "replan", "summarize":
		Logf("task_loop action=%s reason=%s", decision.Action, decision.Reason)
		go func() {
			if err := s.sendLoopMessage(decision.Message); err != nil {
				Logf("task_loop_send_failed action=%s err=%v", decision.Action, err)
			}
		}()
	case "stop":
		Logf("task_loop action=stop reason=%s", decision.Reason)
	}
	/**
	 * 这里原本是"排队中的会话切换"：一轮结束后去执行用户之前点的切换。
	 *
	 * 一进程一会话之后不需要了——用户点会话时就**立刻**切好了（换的是"跟哪个进程说话"，
	 * 不碰正在跑的这个），所以 settled 时没有任何待办。
	 */
}

// sendLoopMessage 用 follow_up 把循环决策交给 Pi。
//
// 为什么用 follow_up 而不是 steer：这时一轮已经结束了，steer 的语义是
// "插进正在跑的回合"，而此刻没有回合在跑；follow_up 正是"干完继续做这个"。
func (s *PiService) sendLoopMessage(message string) error {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return nil
	}
	_, err := s.request("follow_up", map[string]any{"message": trimmed})
	return err
}

// LoopSnapshot 供界面显示循环状态。
func (s *PiService) LoopSnapshot() loopSnapshot { return s.loop.Snapshot(time.Now()) }

// startLoop 在用户发消息时让循环开始计时计数。
func (s *PiService) startLoop() { s.loop.Start() }

// ServiceShutdown 是 Wails 的退出钩子：关窗时自动调用，避免留下孤儿子进程。
func (s *PiService) ServiceShutdown() error { return s.Stop() }

// SetRunningWork 由前端上报"当前是否有回合在跑"。
//
// 关窗前要判断这件事，而"是否在跑"只有前端的状态机知道（status: running/retrying/
// compacting/waiting-ui）。让 Go 去解析事件流也能算出来，但那是把界面状态机的
// 规则复制一份，迟早会漂。
func (s *PiService) SetRunningWork(running bool) {
	s.mu.Lock()
	s.runningWork = running
	s.mu.Unlock()
}

// RequestClose 在窗口即将关闭时被调用（main.go 的 WindowClosing 钩子）。
// 返回 true 表示这次关闭被拦下，前端会弹确认框。
func (s *PiService) RequestClose() bool {
	s.mu.Lock()
	force := s.forceClosing
	running := s.runningWork
	notify := s.OnCloseRequest
	s.mu.Unlock()
	if force || !running {
		return false // 已确认，或本来就没在跑：直接放行
	}
	if notify != nil {
		notify()
	}
	return true
}

// ForceClose 是用户在弹窗里点了"强制关闭"之后走的路径。
//
// 两件事一起做，顺序不能反：先把 Pi 子进程收掉（优雅关闭 stdin，超时才强杀），
// 再退出应用。反过来的话应用先没了，子进程就成了孤儿。
func (s *PiService) ForceClose() error {
	s.mu.Lock()
	s.forceClosing = true
	quit := s.Quit
	s.mu.Unlock()
	_ = s.Stop()
	if quit != nil {
		quit()
	}
	return nil
}

// request 是所有命令的统一入口：加超时、转发给客户端、把结果转成 Result。
//
// 这里只做两件事——拿到 client 和发命令；业务校验放在各个具名方法里，
// 因为校验规则和命令语义绑定（见 Prompt 的长度限制）。
func (s *PiService) request(command string, fields map[string]any) (Result, error) {
	s.mu.Lock()
	client := s.client
	runtimeID := ""
	if s.activeRuntime != nil {
		runtimeID = s.activeRuntime.id
	}
	s.mu.Unlock()
	if client == nil {
		return Result{}, errors.New("请先启动 ZeroA")
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	response, err := client.Request(ctx, command, fields)
	// 会话 id / 会话文件在响应里（get_state）出现，顺手缓存一次。
	s.rememberSession(response.Data)
	// 结果带上归属：前端要能判断"这是当前会话的回答还是后台会话的"
	reply := newResult(response)
	reply.RuntimeID = runtimeID
	if err != nil {
		// 失败时也返回 Result：前端的错误横幅既要知道“失败了”，
		// 也要能看到 Pi 回的原文（Success/Error/Data）。
		// 命令失败（success=false）的描述已经包含在 err 里——
		// pi.Client.Request 会拼成 "命令: 原因"，所以这里不重复包装。
		return reply, err
	}
	return reply, nil
}

// rememberSession 从 data 里顺手取一次 sessionId / sessionFile。
// 取不到就保持原值：这里绝不能让“缓存”影响命令本身的成败。
//
// 缓存 sessionFile 是为了「重命名/删除会话」能判断哪个文件正在使用中：
// 那两件事都要拒绝当前会话，而 get_state 是唯一会给出路径的地方。
func (s *PiService) rememberSession(data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	var payload struct {
		SessionID   string `json:"sessionId"`
		SessionFile string `json:"sessionFile"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	if payload.SessionID == "" && payload.SessionFile == "" {
		return
	}
	s.mu.Lock()
	if payload.SessionID != "" {
		s.sessionID = payload.SessionID
	}
	if payload.SessionFile != "" {
		s.sessionFile = payload.SessionFile
	}
	s.mu.Unlock()
}

/**
 * GetState 查询 Pi 当前状态（模型、会话 id、是否正在生成…）。
 *
 * 界面用它来判断“连接是不是真的可用”：能拿到 response 才算连上。
 *
 * ## 没有进程时为什么不报错
 *
 * 会话历史在 .jsonl 里，读它不需要进程（见 session_history.go），所以
 * "还没起过进程"是一种**正常状态**：刚进应用、刚打开一条历史会话、刚 Stop 过
 * 都属于它。如果这里照旧报"请先启动 Pi"，前端的启动流程（useAgent.start 用它
 * 证明"这套参数能用"）就会把它当成失败，弹出报错条——而实际上一切都好，
 * 只是还没到需要进程的时候。
 *
 * 所以：有活进程就如实去问 Pi；没有进程但**启动参数已经准备好**时，返回一份
 * 与真实 get_state 字段兼容的合成状态（pending=true、connected=false、
 * isStreaming=false）。前端读到的仍是同一套字段，不需要为"没进程"写第二个分支。
 *
 * 而**从没跑过 Start**（例如授权没过）时照旧报错：那时连参数模板都没有，
 * 说"请先启动 Pi"才是对的（service_test.go 钉着这条旧契约）。
 */
func (s *PiService) GetState() (Result, error) {
	if s.liveClient() != nil {
		return s.request("get_state", nil)
	}
	return s.pendingState()
}

// liveClient 取当前会话的进程句柄（没有进程时返回 nil）。
func (s *PiService) liveClient() *pi.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	select {
	case <-s.client.Done():
		// 句柄还挂着但进程已经死了：它已经不能回答任何命令。
		return nil
	default:
		return s.client
	}
}

/**
 * pendingState 是"还没有进程"时给界面的合成 get_state。
 *
 * 字段刻意与真实 get_state 对齐（rpc-mode.js 的 get_state）：
 *   · isStreaming / isCompacting 为 false —— 没有进程就不可能正在跑；
 *   · sessionFile / sessionId 给**用户正在看的那条会话**（pendingSessionFile
 *     与它的会话头），这样状态栏与侧栏高亮说的是同一条会话，不会一打开就显示成空的；
 *   · model 只在启动参数里确实指定了模型时才给（否则省略，让界面保留原值——
 *     报一个空模型会把模型指示器擦掉，看起来像"模型没了"）；
 *   · pending=true、connected=false 是给人和测试看的口径：这不是"连不上"，
 *     而是"进程等第一条消息才创建"。
 */
func (s *PiService) pendingState() (Result, error) {
	s.mu.Lock()
	ready := s.optionsReady
	sessionID := s.sessionID
	sessionFile := s.sessionFile
	sessionName := s.sessionName
	options := s.options
	s.mu.Unlock()

	if !ready {
		// 从没成功跑过 Start：没有参数模板，与旧实现的"请先启动 Pi"是同一种情况。
		return Result{}, errors.New("请先启动 ZeroA")
	}

	state := map[string]any{
		/**
		 * pending=true：这份状态是"当前会话还没有进程"的**合成状态**。
		 *
		 * 它说的是"进程等第一条消息才建"，不是"连不上"——后者由 connected=false
		 * 表达。两者的区别对界面很重要：前者可以照常发消息（会自动启动），
		 * 后者要先解决启动问题。
		 */
		"pending":             true,
		"connected":           false,
		"isStreaming":         false,
		"isCompacting":        false,
		"sessionId":           sessionID,
		"sessionFile":         sessionFile,
		"messageCount":        0,
		"pendingMessageCount": 0,
	}
	if name := strings.TrimSpace(sessionName); name != "" {
		state["sessionName"] = name
	}
	if level := strings.TrimSpace(options.Thinking); level != "" {
		state["thinkingLevel"] = level
	}
	if model := strings.TrimSpace(options.Model); model != "" {
		provider, id := splitModelID(model)
		state["model"] = map[string]any{"id": id, "provider": provider}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "get_state", Success: true, Data: string(data)}, nil
}

// splitModelID 把 "provider/id"（Pi 的 --model 写法）拆成两段。
// 只给 id 时 provider 留空——我们不猜，界面上少显示一项比显示错了强。
func splitModelID(model string) (provider, id string) {
	trimmed := strings.TrimSpace(model)
	// Pi 允许用冒号追加思考级别（provider/id:high），这里只要模型部分
	if index := strings.Index(trimmed, ":"); index > 0 {
		trimmed = trimmed[:index]
	}
	if index := strings.Index(trimmed, "/"); index > 0 {
		return trimmed[:index], trimmed[index+1:]
	}
	return "", trimmed
}

// GetMessages 取回本会话的消息数组（结构由 Pi 决定，前端按 role/content 渲染）。
func (s *PiService) GetMessages() (Result, error) { return s.request("get_messages", nil) }

// GetSessionStats 取回 token / 费用统计，用于界面右下角的用量显示。
func (s *PiService) GetSessionStats() (Result, error) { return s.request("get_session_stats", nil) }

// GetCommands 列出可用命令：扩展命令、提示模板、技能。
//
// 用法（见 pi 的 docs/rpc-commands.md）：拿到的名字前面加 `/`，用 prompt 发出去，
// Pi 会在发送前展开（技能命令 /skill:name、模板 /template 都是这个机制）。
// 界面上就是输入区的斜杠面板。
func (s *PiService) GetCommands() (Result, error) { return s.request("get_commands", nil) }

// GetForkMessages 列出"可以从哪几条用户消息分支"，返回 [{entryId, text}]。
//
// 界面把这句文本与时间线里的用户消息对上，就能在每条消息旁给出「从这里分支」。
// 会话是一棵只追加的树，entryId 是稳定游标（见 docs/rpc-commands.md）。
func (s *PiService) GetForkMessages() (Result, error) { return s.request("get_fork_messages", nil) }

// Fork 从指定的用户消息开一条新分支（新分支从这条消息之后重来），返回这条消息的文本。
// 扩展可以用 session_before_fork 拦下，此时返回 cancelled=true。
func (s *PiService) Fork(entryID string) (Result, error) {
	if strings.TrimSpace(entryID) == "" {
		return Result{}, errors.New("缺少 entryId")
	}
	return s.request("fork", map[string]any{"entryId": entryID})
}

// Clone 把当前分支整体复制成一个新会话（带完整上下文，但之后互不影响）。
func (s *PiService) CloneSession() (Result, error) { return s.request("clone", nil) }

// Compact 手动压缩上下文（长会话 token 快满时的主动操作）。
//
// customInstructions 可以留空：留空时 Pi 用默认的压缩策略；
// 填了就相当于告诉它"压缩时重点保留什么"（例如"保留代码改动"）。
func (s *PiService) Compact(customInstructions string) (Result, error) {
	// 没有进程 = 会话还没有任何内容（懒启动），压缩无从谈起。
	// 返回 Success=false 的 Result 而不是 Go error：前者走前端的 .then，
	// reportCompactResult 认 "nothing to compact" 前缀给温和提示；后者会让
	// promise reject，提示根本不会出现。与 Pi 自己的
	// "Nothing to compact (session too small)" 走同一条降级路径。
	if s.liveClient() == nil {
		return Result{
			Command: "compact",
			Success: false,
			Error:   "nothing to compact: 会话还没有内容，发出第一条消息后才谈得上压缩",
		}, nil
	}
	fields := map[string]any{}
	if trimmed := strings.TrimSpace(customInstructions); trimmed != "" {
		fields["customInstructions"] = trimmed
	}
	return s.request("compact", fields)
}

// SetAutoCompaction 开关"上下文快满时自动压缩"。
//
// 默认由 Pi 自己决定；界面把它做成一个显式开关，用户才能预期长会话的行为。
// CycleModel 切到可用模型列表里的下一个（界面上的"下一个模型"快捷动作）。
func (s *PiService) CycleModel() (Result, error) { return s.request("cycle_model", nil) }

// CycleThinkingLevel 切到下一个推理等级。
func (s *PiService) CycleThinkingLevel() (Result, error) {
	return s.request("cycle_thinking_level", nil)
}

// GetLastAssistantText 取最后一条助手消息的文本。
//
// 用途：界面上的「复制上一条回答」——比让用户手动框选整段回答可靠。
//
// 没有进程时（待创建/待恢复的会话）回答在磁盘的 .jsonl 里：
// pendingSessionFile 就是"用户正在看的这条会话"，直接读文件找最后一条
// 助手消息——与「重新载入消息」同一个数据来源。契约与有进程时一致
// （见 mockpi：data.text，没有回答是空串，同一消息内多个 text 块取最后一个），
// 前端照常提示"还没有可复制的回答"，不用分进程有无两套逻辑。
func (s *PiService) GetLastAssistantText() (Result, error) {
	if s.liveClient() != nil {
		return s.request("get_last_assistant_text", nil)
	}
	s.mu.Lock()
	pending := s.pendingSessionFile
	s.mu.Unlock()
	text := ""
	if pending != "" {
		target, err := s.requireSessionPath(pending)
		if err != nil {
			return Result{}, err
		}
		entries, err := readSessionEntries(target)
		if err != nil {
			return Result{}, err
		}
		messages := sessionMessages(entries)
		for i := len(messages) - 1; i >= 0 && text == ""; i-- {
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(messages[i], &msg) != nil || msg.Role != "assistant" {
				continue
			}
			text = lastTextBlock(msg.Content)
		}
	}
	data, _ := json.Marshal(map[string]any{"text": text})
	return Result{Command: "get_last_assistant_text", Success: true, Data: string(data)}, nil
}

// lastTextBlock 从消息 content 里取文本：块数组形态取最后一个 text 块
// （与 mockpi 的口径一致，覆盖而不是拼接）；兼容 content 是裸字符串的旧形态。
func lastTextBlock(raw json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil && blocks != nil {
		text := ""
		for _, block := range blocks {
			if block.Type == "text" {
				text = block.Text
			}
		}
		return text
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	return ""
}

// Bash 直接执行一条 shell 命令，并把输出**加进会话上下文**。
//
// 与模型回合里的 bash 工具不同：这是用户在界面里主动跑一条命令（调试、看环境都常用），
// 命令与输出会进会话，所以下一次 prompt 时模型能看到。
// 输出会以 bash_execution_update 事件流式到达（router 里已处理），响应里是最终结果。
func (s *PiService) Bash(command string) (Result, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Result{}, errors.New("命令不能为空")
	}
	return s.request("bash", map[string]any{"command": trimmed})
}

// AbortBash 中止正在跑的 bash 命令（不会杀掉整个 Pi 进程）。
func (s *PiService) AbortBash() (Result, error) { return s.request("abort_bash", nil) }

// SetSteeringMode 设置"运行中插入的消息（steer）怎么投递"。
//
//	all            : 当前助手回合的工具调用跑完后，把所有插入消息一起投递
//	one-at-a-time  : 每个完成的助手回合投递一条（Pi 的默认）
//
// 界面把默认值暴露出来，用户才能预期"我插了三句话会发生什么"。
func (s *PiService) SetSteeringMode(mode string) (Result, error) {
	return s.setMode("set_steering_mode", mode)
}

// SetFollowUpMode 设置"排到后面的消息（follow_up）怎么投递"。
//
//	all            : 回合全部结束后一次性投递
//	one-at-a-time  : 每次结束后投递一条（Pi 的默认）
func (s *PiService) SetFollowUpMode(mode string) (Result, error) {
	return s.setMode("set_follow_up_mode", mode)
}

// setMode 是两个模式设置的共用校验：只允许 Pi 文档里写明的两种取值。
// 传错值不如本地拦住——否则 Pi 会回一句英文错误，用户还得去猜。
func (s *PiService) setMode(command, mode string) (Result, error) {
	trimmed := strings.TrimSpace(mode)
	if trimmed != "all" && trimmed != "one-at-a-time" {
		return Result{}, errors.New("模式只能是 all 或 one-at-a-time")
	}
	return s.request(command, map[string]any{"mode": trimmed})
}

// SetAutoRetry 开关"遇到瞬时错误（过载 / 限流 / 5xx）自动重试"。
//
// 之前界面只能被动看着 willRetry 事件滚过去，用户没有任何控制权。
func (s *PiService) SetAutoRetry(enabled bool) (Result, error) {
	return s.request("set_auto_retry", map[string]any{"enabled": enabled})
}

// AbortRetry 中止正在进行的重试（取消等待、不再重试）。
//
// 与 abort 的区别：abort 停的是当前回合，abort_retry 停的是"还要再试一次"这件事。
func (s *PiService) AbortRetry() (Result, error) { return s.request("abort_retry", nil) }

// GetTree 取会话的**树**结构（每个节点 {entry, children, label?}）。
//
// 为什么不是 get_messages：会话是只追加的树，分支之后 get_messages 只给当前分支，
// 看不到"另一条分支也在那儿"。要在界面上画出分支结构、或提供"从某个历史点再分一次"，
// 就得看这棵树。data 里还有 leafId（当前所在节点）。
//
// 没有进程时（待创建/待恢复的会话）树在磁盘的 .jsonl 里：pendingSessionFile
// 就是"用户正在看的这条会话"，离线建树（buildOfflineTree）与「重新载入消息」
// 读的是同一份数据。契约与有进程时一致（{tree, leafId}，空会话给空树），
// 前端照常显示"这个会话还没有条目"，不用分进程有无两套逻辑。
func (s *PiService) GetTree() (Result, error) {
	if s.liveClient() != nil {
		return s.request("get_tree", nil)
	}
	s.mu.Lock()
	pending := s.pendingSessionFile
	s.mu.Unlock()
	tree := []*treeNodeView{}
	leafID := ""
	if pending != "" {
		target, err := s.requireSessionPath(pending)
		if err != nil {
			return Result{}, err
		}
		entries, err := readSessionEntries(target)
		if err != nil {
			return Result{}, err
		}
		tree, leafID = buildOfflineTree(entries)
	}
	data, _ := json.Marshal(map[string]any{"tree": tree, "leafId": leafID})
	return Result{Command: "get_tree", Success: true, Data: string(data)}, nil
}

func (s *PiService) SetAutoCompaction(enabled bool) (Result, error) {
	return s.request("set_auto_compaction", map[string]any{"enabled": enabled})
}

// Prompt 发送一条用户消息。空消息与超长消息都在这里拦下，
// 不让它们变成一条没意义的 RPC 请求。
//
// 会话还没有进程时（刚新建、或刚 Stop 过）由 prompt 里的 ensureActiveRuntime
// 按需起一个，所以界面不需要在发送前先"启动 Pi"。
func (s *PiService) Prompt(message string) (Result, error) {
	s.startLoop()
	if err := validateMessage(message); err != nil {
		return Result{}, err
	}
	return s.prompt(map[string]any{"message": message})
}

// prompt 是 Prompt / PromptWithFiles 共用的出口：把校验过的字段交给协议层。
//
// 它同时是**懒启动**的落点：新建的会话在第一条消息之前没有进程（会话=日志，
// 进程按需创建，见 sessions.go 的 NewSession 与 session_runtime.go 的
// ensureActiveRuntime）。放在这里而不是各个具名方法里，是因为这两个入口
// （Prompt / PromptWithFiles）最终都会走到这一行。
func (s *PiService) prompt(fields map[string]any) (Result, error) {
	if message, ok := fields["message"].(string); ok {
		if err := validateMessage(message); err != nil {
			return Result{}, err
		}
	}
	// 需要进程才发得出去：没有就先按启动参数模板起一个（首次约 1–3.5 秒）。
	if err := s.ensureActiveRuntime("prompt"); err != nil {
		return Result{}, err
	}
	result, err := s.request("prompt", fields)
	if err != nil {
		return result, err
	}
	// prompt 被接受就说明这个会话真的用起来了：立刻把"上次聊到哪"记到它自己的工作区。
	// 不等 agent_settled，是因为用户完全可能在回答中途关掉窗口。
	s.rememberCurrentSession()
	return result, nil
}

// Steer 在不中止当前回合的前提下插一句话：Pi 会在回合中的下一个
// 检查点把它作为新的用户消息接进去（例如“改成用 Go 写”）。
func (s *PiService) Steer(message string) (Result, error) {
	if err := validateMessage(message); err != nil {
		return Result{}, err
	}
	return s.request("steer", map[string]any{"message": message})
}

// FollowUp 排队一条消息：等当前回合彻底结束后再处理，不会打断正在跑的生成。
func (s *PiService) FollowUp(message string) (Result, error) {
	if err := validateMessage(message); err != nil {
		return Result{}, err
	}
	return s.request("follow_up", map[string]any{"message": message})
}

// validateMessage 是 Prompt/Steer/FollowUp 共用的输入校验。
// 三个命令都是“往会话里塞用户文本”，规则必须一致，
// 否则界面会出现“prompt 报错但 steer 悄悄成功”这种不一致行为。
func validateMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("消息不能为空")
	}
	if len(message) > maxPromptBytes {
		return errors.New("学习 Demo 每条消息限制为 32 KB")
	}
	return nil
}

// Abort 中止当前回合。它只影响正在生成的内容，不会杀掉进程，
// 所以中止之后还能继续发下一条 prompt（界面里可以直接试）。
func (s *PiService) Abort() (Result, error) {
	// 先停子智能体再中止父回合：用户按「停止」的意思是"都别干了"。
	// 留着子智能体继续跑，等于按了停止还在扣费，而且它们的结论会投给一个
	// 已经被中止的回合（变成噪音）。
	manager := s.delegationManager()
	sup := manager.SupervisorIfAny()
	router := manager.Router()
	// 只收**当前这条会话**派出去的：用户按「停止」的意思是"别再干我这条会话的活"，
	// 不是"把别的会话里正在跑的子智能体也一起掐掉"。
	parentID := s.parentID()
	if sup != nil {
		if stopped := sup.CancelChildren(parentID, "父会话已中止"); stopped > 0 {
			Logf("delegation_cancelled_on_abort count=%d parent=%s", stopped, parentID)
		}
	}
	if router != nil {
		// 挂起的结果也不用投了：这一轮已经不要了
		router.Reset(parentID)
	}
	return s.request("abort", nil)
}

// ClearQueue 清空排队中的 steer/follow_up 消息。
// 队列留在 Pi 进程里，所以这件事必须问服务端，界面不能自己假装清掉了。
func (s *PiService) ClearQueue() (Result, error) { return s.request("clear_queue", nil) }

// SetModel 切换模型。
//
// 字段名与取值都要精确：真实 Pi 的 set_model 要求 provider 与 modelId **同时匹配**
// 它配置里的某个模型（见 1.0.0 的 dist/rpc-mode.js），provider 为空不会"自动推断"，
// 只会回 Model not found。所以这里两个都要求非空，把错误挡在界面侧、说清楚原因。
func (s *PiService) SetModel(provider, model string) (Result, error) {
	if strings.TrimSpace(provider) == "" {
		return Result{}, errors.New("缺少 provider：ZeroA 要求 provider 与模型同时匹配，请从模型列表里选")
	}
	if strings.TrimSpace(model) == "" {
		return Result{}, errors.New("请选择模型")
	}
	// 还没有进程（新建会话还没发第一条消息）：模型本来就是启动参数，
	// 直接改写启动模板——第一条消息懒启动时就按它起进程。
	// pendingState 的合成 get_state 会把新值汇报给界面，前端不用特殊处理。
	if s.liveClient() == nil {
		s.mu.Lock()
		if !s.optionsReady {
			s.mu.Unlock()
			return Result{}, errors.New("请先启动 ZeroA")
		}
		s.options.Model = provider + "/" + model
		// startModel 是"本次启动用的模型"，子智能体沿用它（见 delegation.go）——同步更新。
		s.startModel = s.options.Model
		s.mu.Unlock()
		data, _ := json.Marshal(map[string]any{
			"pending": true,
			"model":   map[string]any{"id": model, "provider": provider},
		})
		return Result{Command: "set_model", Success: true, Data: string(data)}, nil
	}
	return s.request("set_model", map[string]any{"provider": provider, "modelId": model})
}

// GetAvailableModels 取回可选模型列表，供界面渲染模型选择器。
//
// 有进程时这是唯一的模型来源：Pi 通过 RPC 按自己的配置回答（provider 目录、鉴权状态、
// 内置模型全都算进去了）；成功应答顺手写进磁盘缓存（见 models_cache.go）。
// 没有进程时按缓存回答——新建会话还没发消息时，模型菜单恰恰最该可用。
func (s *PiService) GetAvailableModels() (Result, error) {
	if s.liveClient() == nil {
		if cached := readModelsCache(); cached.Models != "" {
			return Result{Command: "get_available_models", Success: true, Data: cached.Models}, nil
		}
		return Result{}, errors.New("请先启动 ZeroA（还没有模型清单缓存：发过一条消息之后这里就有内容了）")
	}
	result, err := s.request("get_available_models", nil)
	if err == nil && result.Success && result.Data != "" {
		data := result.Data
		s.writeModelsCache(func(cache *modelsCache) { cache.Models = data })
	}
	return result, err
}

// parseTools 把界面传来的工具白名单文本转成 pi.Options.Tools。
//
// 三种输入对应三种行为（见 pi.Options 的注释）：
//
//	""            → nil   不传参数，用 Pi 默认的 read/bash/edit/write
//	"none" / "-"  → 空切片 → --no-tools，完全禁用工具
//	"read,write"  → 白名单 → --tools read,write
func parseTools(tools string) []string {
	trimmed := strings.TrimSpace(tools)
	if trimmed == "" {
		return nil
	}
	if trimmed == "none" || trimmed == "-" {
		return []string{}
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// SetThinkingLevel 切换思考档位（low/medium/high/max 之类，取值由 Pi 决定）。
func (s *PiService) SetThinkingLevel(level string) (Result, error) {
	if strings.TrimSpace(level) == "" {
		return Result{}, errors.New("请选择思考档位")
	}
	// 无进程时与 SetModel 同理：思考档位也是启动参数（--thinking），改写模板即可。
	if s.liveClient() == nil {
		s.mu.Lock()
		if !s.optionsReady {
			s.mu.Unlock()
			return Result{}, errors.New("请先启动 ZeroA")
		}
		s.options.Thinking = level
		s.mu.Unlock()
		data, _ := json.Marshal(map[string]any{"pending": true, "thinkingLevel": level})
		return Result{Command: "set_thinking_level", Success: true, Data: string(data)}, nil
	}
	return s.request("set_thinking_level", map[string]any{"level": level})
}

// GetAvailableThinkingLevels 取回可选思考档位，供界面渲染下拉框。
// 与 GetAvailableModels 共用一份磁盘缓存（无进程时菜单才画得出来）。
func (s *PiService) GetAvailableThinkingLevels() (Result, error) {
	if s.liveClient() == nil {
		if cached := readModelsCache(); cached.ThinkingLevels != "" {
			return Result{Command: "get_available_thinking_levels", Success: true, Data: cached.ThinkingLevels}, nil
		}
		return Result{}, errors.New("请先启动 ZeroA（还没有思考档位缓存）")
	}
	result, err := s.request("get_available_thinking_levels", nil)
	if err == nil && result.Success && result.Data != "" {
		data := result.Data
		s.writeModelsCache(func(cache *modelsCache) { cache.ThinkingLevels = data })
	}
	return result, err
}

// parseExtensionUIResponse 校验并裁剪一条 extension_ui_response 载荷，
// 主会话（RespondUI）与子智能体（RespondAgentUI）共用同一套规则。
//
// response 是完整的 extension_ui_response JSON 文本，由前端原样构造后传进来，例如：
//
//	{"type":"extension_ui_response","id":"uuid-1","confirmed":true}   确认
//	{"type":"extension_ui_response","id":"uuid-1","value":"Allow"}    选择/输入
//	{"type":"extension_ui_response","id":"uuid-1","cancelled":true}   取消
//
// 校验三件事再转发：
//   - 是合法 JSON（否则 Pi 那边只会回一条含糊的解析错误）；
//   - type 必须是 extension_ui_response，防止界面把别的记录误发过去；
//   - id 必须存在——它是唯一能把答案交回某次请求的关联键；
//   - 只透传 confirmed / cancelled / value 三个协议认识的字段：
//     任何字段都透传，等于把界面变成一个可以往协议流里塞任意内容的入口。
//
// 关键：这条记录必须用**扩展请求自己的 id**，而且不能用 Request 发。
//
// 两个坑（都是审计出来后改的）：
//
//	· pi.Client.Request 会把信封 id 改写成 req-N 做请求-响应关联，
//	  而 Pi 是按 id 去 pendingExtensionRequests 里找等待者的，被改写就永远找不到；
//	· Pi 收到 extension_ui_response 后不会回任何 response 记录
//	  （见 1.0.0 的 dist/rpc-mode.js：唤醒等待者后直接 return），
//	  用 Request 会白等到 15 秒超时，还把一个已经成功的操作报成失败。
//
// 所以投递一律走 Notify：自己写整条记录、不登记也不等响应。
func parseExtensionUIResponse(response string) (map[string]any, string, error) {
	if strings.TrimSpace(response) == "" {
		return nil, "", errors.New("response 不能为空")
	}
	var payload struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Confirmed *bool  `json:"confirmed"`
		Cancelled *bool  `json:"cancelled"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal([]byte(response), &payload); err != nil {
		return nil, "", fmt.Errorf("response 不是合法 JSON: %w", err)
	}
	if payload.Type != "extension_ui_response" {
		return nil, "", fmt.Errorf("response 的 type 必须是 extension_ui_response，收到 %q", payload.Type)
	}
	if payload.ID == "" {
		return nil, "", errors.New("response 缺少 id，无法对应到某次扩展请求")
	}
	record := map[string]any{"type": "extension_ui_response", "id": payload.ID}
	switch {
	case payload.Cancelled != nil && *payload.Cancelled:
		// 关掉对话框：扩展会收到 undefined（select/input）或 false（confirm）。
		record["cancelled"] = true
	case payload.Confirmed != nil:
		record["confirmed"] = *payload.Confirmed
	case payload.Value != "":
		record["value"] = payload.Value
	default:
		// 三者都没有的响应无法被扩展理解，提前拦住比让它静默失败好。
		return nil, "", errors.New("response 需要 confirmed、value 或 cancelled 之一")
	}
	return record, payload.ID, nil
}

// RespondUI 应答**主会话**的扩展 UI 请求（confirm / select / input）。
//
// 校验三件事再转发：
//   - 是合法 JSON（否则 Pi 那边只会回一条含糊的解析错误）；
//   - type 必须是 extension_ui_response，防止界面把别的记录误发过去；
//   - id 必须存在——它是唯一能把答案交回某次请求的关联键。
//
// 调用方给的 id 会以 uiId 转发（值就是被回答请求的 id，语义与协议一致）。
// 不能直接用 id 这个名字：请求信封自身也要用 id 做请求-响应关联
// （pi.Client.Request 会写入 req-1、req-2…），同名键会被覆盖——
// 曾经因此把 req-1 当成扩展请求 id 发出去，服务端只能报“没有等待中的扩展请求”。
func (s *PiService) RespondUI(response string) (Result, error) {
	record, reqID, err := parseExtensionUIResponse(response)
	if err != nil {
		return Result{}, err
	}

	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return Result{}, errors.New("请先启动 ZeroA")
	}
	if err := client.Notify(record); err != nil {
		return Result{}, err
	}
	// 没有 response 可等，所以直接返回成功；data 里带上被回答的请求 id 便于排查。
	raw, _ := json.Marshal(map[string]any{"id": reqID, "delivered": true})
	return Result{Command: "extension_ui_response", Success: true, Data: string(raw)}, nil
}

// RespondAgentUI 应答**子智能体**的扩展 UI 请求。
//
// 子智能体是独立 Pi 进程，应答必须发到它自己的 stdin——发给父会话的
// RespondUI 够不着它，这正是界面上"子智能体请求交互（本视图暂不能应答）"
// 的来历。校验规则与主会话完全同一套（parseExtensionUIResponse）。
func (s *PiService) RespondAgentUI(agentID, response string) (Result, error) {
	record, reqID, err := parseExtensionUIResponse(response)
	if err != nil {
		return Result{}, err
	}
	mgr := s.delegationManager()
	if mgr == nil {
		return Result{}, errors.New("子智能体管理器不可用")
	}
	if err := mgr.RespondUI(agentID, record); err != nil {
		return Result{}, err
	}
	raw, _ := json.Marshal(map[string]any{"id": reqID, "agentId": agentID, "delivered": true})
	return Result{Command: "extension_ui_response", Success: true, Data: string(raw)}, nil
}
