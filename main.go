package main

import (
	"context"
	"embed"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"zeroa/internal/agent"
	"zeroa/internal/bridge"
	"zeroa/internal/mockpi"
	"zeroa/internal/pi"
)

// 前端构建产物被编译进二进制（all: 连点号开头的文件也一起打包）。
// 所以必须先跑 scripts/build.ps1 生成 frontend/dist，否则 go build 会因为
// 找不到这个目录直接失败——新克隆仓库的第一件事就是跑构建脚本。
//
//go:embed all:frontend/dist
var assets embed.FS

// 发给前端的事件名。集中成常量：名字写错在 JS 侧只会表现为“什么都没收到”，
// 很难排查，所以 Go 侧只允许在这一个地方出现字面量。
const (
	// traceEvent 是协议观察器抽屉用的原始记录流（pi.Trace：direction/raw/time）。
	traceEvent = "pi:trace"
	// agentEvent 是界面主体消费的类型化事件（agent.Envelope）。
	agentEvent = "pi:event"
	// closeRequestEvent 告诉前端"有人要关窗，但还有回合在跑，请弹确认框"。
	closeRequestEvent = "pi:close-request"
	// fileDropEvent 把"从资源管理器拖进来的文件"的真实路径交给前端。
	// 走原生投递而不是 DOM drop：web 层拿不到文件路径（见 main 里 EnableFileDrop 的注释）。
	fileDropEvent = "pi:files-dropped"
	// agentStateEvent 是子智能体的状态通道（谁被派出去、在跑、跑完了）
	agentStateEvent = "pi:agent-state"
	// sessionEvent 把"排队中的会话切换已完成"推给前端（payload 与 load_session 的 Data 同构）。
	// 为什么需要它：切换排队时，前端那次调用早就返回了（deferred），真正的数据只能由后端推。
	sessionEvent = "pi:session"
)

// 桌面程序入口。三件事：启动时准备好“前端产物 + Service”，
// 运行期间把 Pi 的每一行输出转发给前端，退出时清理子进程（见 bridge.PiService.ServiceShutdown）。
func main() {
	// 桌面程序也能当“假 Pi”用：mock 模式下 pi.Command() 会用同一个可执行文件
	// 加 --mock-pi 再启动一份自己，走的仍是真实 stdin/stdout 管道。
	// 这个分支必须放在最前面：它不需要 App，也不该去动 embed 进来的前端产物。
	if len(os.Args) > 1 && os.Args[1] == "--mock-pi" {
		// 启动参数由模拟器自己从 os.Args 里挑（见 mockpi.parseLaunchArgs）：
		// 它要把启动配置（角色提示词文件、工具白名单）如实写进会话的首条
		// system 消息，"角色是否加载上了"才是可断言的。
		mockpi.Serve(os.Stdin, os.Stdout)
		return
	}
	/**
	 * 单实例检查必须在建 App 之前。
	 *
	 * 第二实例不是"启动失败"：它会在 Wails 内部无声挂死（WebView2 用户数据目录
	 * 被第一个实例占用），不弹窗、不写日志、进程变成僵尸——实测"双击了什么都没发生"
	 * 就是这么来的，而且 main 里的 app_run_failed 兜底走不到（错误不经 app.Run 返回）。
	 * 所以在这里用命名互斥锁拦下：把已有窗口翻到前台，本进程安静退出。
	 * 注意它在 --mock-pi 之后：那条路径不开窗口，不该被拦。
	 */
	if !ensureSingleInstance() {
		return
	}
	service := &bridge.PiService{}
	// 只创建一个 App：服务必须注册在同一个 App 上，前端生成的绑定才能调到它。
	// （曾经见过先 New 一次、再 New 一次覆盖变量的写法，那样注册进去的服务会失效。）
	app := application.New(application.Options{
		Name: "ZeroA",
		// Services：前端 → Go 的方向。Wails 会为这里的方法生成 TypeScript 绑定。
		Services: []application.Service{application.NewService(service)},
		// Assets：把上面 embed 进来的前端产物直接提供给 WebView，不需要另起 HTTP 服务。
		Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(assets)},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		Windows: application.WindowsOptions{
			// 调试端口：界面出问题时（比如"某一页空白"）能挂上 DevTools 看真实 DOM。
			//
			// 必须由环境变量开启，不能默认打开：打开等于给本机任意进程一个
			// "读取并操作这个应用界面"的入口。用法：
			//   set PI_GUI_DEBUG_PORT=9222 && bin\zeroa.exe
			// 然后用 http://127.0.0.1:9222/json 取调试目标。
			AdditionalBrowserArgs: debugBrowserArgs(),
			// 每个安装一个独立的 WebView2 用户数据目录（见下）。
			WebviewUserDataPath: webviewProfileDir(),
		},
	})
	// Go → 前端的方向。这里用“赋值”而不是构造参数，是因为必须先有 app 才能 Emit；
	// 而真正的 Pi 子进程要等界面点“启动”才会创建，所以现在挂上完全来得及。
	//
	// 一条 trace 发两个事件，而不是让前端二选一：
	//   pi:trace 原始行 → 协议观察器，学 RPC 就看这个；
	//   pi:event 信封   → 界面逻辑，已经分好类，前端不用再 try/catch JSON.parse。
	// 两条都保留还有一个实际好处：观察器显示的是真实字节，
	// 当 pi:event 的 record 看起来不对时，可以直接和原始行对照。
	//
	// 这个回调跑在 internal/pi 的读取 goroutine 上：两个读取 goroutine
	// （stdout/stderr）会同时调用它，所以它只能做“不能阻塞、可并发”的事。
	// application.EventManager.Emit 正好满足：它内部只是把事件投递给
	// 前端连接，不等待 WebView 处理，所以这里不需要再加队列或互斥锁。
	service.OnEvent = func(trace pi.Trace) {
		app.Event.Emit(traceEvent, trace)
		/**
		 * 诊断：把每个会话进程的 stderr 尾巴攒着（见 internal/bridge/session_runtime_diag.go）。
		 *
		 * 实测踩到的坑：新起的 Pi 进程在回答第一条命令前就退了，日志里只有一句
		 * `冷启动失败`，真正的原因（模块找不到/参数不认/端口被占/杀软拦下）随进程
		 * 一起消失。stderr 是唯一能说明原因的东西，所以在这里（事件出口）收下它。
		 */
		service.RecordRuntimeStderr(trace)
		envelope := agent.Classify(trace.Direction, trace.Raw, traceTime(trace))
		// 会话进程的归属要一路带到前端：一进程一会话之后，后台会话的输出必须留在
		// 它自己的时间线里，不能因为"它此刻不是当前会话"就被丢弃或串台。
		envelope.RuntimeID = trace.RuntimeID
		// 子智能体的事件带着 AgentID 出来：前端靠它分流到"各自的时间线"，
		// 否则子智能体的文本会串进父会话的聊天记录里（用户会看到自己没说过的话）。
		if trace.AgentID != "" {
			envelope.AgentID = trace.AgentID
		}
		app.Event.Emit(agentEvent, envelope)
		// 进程退出的那一刻，把它的 stderr 尾巴写进日志：
		// 下一次"Pi 在响应前退出"就能一眼看出原因，而不是靠猜。
		if envelope.Kind == "system" && envelope.Type == "process_exit" {
			bridge.Logf("pi_process_exit runtime=%s stderr_tail=%s", trace.RuntimeID, bridge.RuntimeStderrSummary(trace.RuntimeID))
			bridge.ForgetRuntimeStderr(trace.RuntimeID)
		}
		// 子智能体的输出也走这条流（带 AgentID），但**不能**用它推导父会话状态：
		// 子智能体的一轮结束会把自己的 agent_settled 发出来，若当成父的开始/结束，
		// 父的状态机与审计计数就被子进程污染了（"我没派活它却说在忙"）。
		if trace.AgentID != "" {
			return
		}
		// 任务循环也要看这条流：它靠工具调用判断"是否在原地打转"，
		// 靠 agent_settled 判断"一轮结束了，要不要继续"。见 internal/bridge/loop.go。
		service.ObserveAgentEvent(envelope)
	}
	// 关窗前的确认通道：WindowClosing 钩子发现"还有回合在跑"时会拦下这次关闭，
	// 并让前端弹窗（前端点"强制关闭"后走 PiService.ForceClose）。
	// 子智能体的状态变化（spawned / state / result）单独一条通道：
	// 界面用它画 Agents 列表与委派卡片，和 Pi 的原始事件流分开。
	service.OnAgentEvent = func(kind, payload string) {
		app.Event.Emit(agentStateEvent, map[string]any{"kind": kind, "payload": payload})
	}
	// 会话进程切换完成（后台换当前会话）：把 payload 推给前端。
	// 前端收到后按"新会话"重建时间线（与它自己调 SwitchSession 成功时同一套处理）。
	service.OnSessionSwitch = func(data string) {
		app.Event.Emit(sessionEvent, json.RawMessage(data))
	}
	service.OnCloseRequest = func() {
		app.Event.Emit(closeRequestEvent, nil)
	}
	service.Quit = func() { app.Quit() }
	// 后台协程共用的 ctx：退出时取消（见下面的 cancelBackground）。
	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	defer cancelBackground()
	/**
	 * 空闲会话进程的回收协程。
	 *
	 * 一进程一会话之后，"开过的每条会话都留着一个 Pi 进程"是这套架构最大的代价
	 *（每个百 MB 级）。这里定期回收"既不是当前会话、也没在跑回合"的那些；
	 * 回收不丢东西：会话内容一直在 .jsonl 里，下次点开是冷启动。
	 */
	service.StartRuntimeReaper(backgroundCtx)
	// 无边框窗口：标题栏由前端自绘（见 frontend/src/features/shell/TitleBar.tsx），
	// 这样标题栏能跟着主题走，两套配色下都不会出现"系统白条 + 暗色内容"的割裂。
	// 代价是窗口拖动与最小化/最大化/关闭都要自己接（拖拽用 CSS --wails-draggable）。
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "ZeroA — Multi-Agent Security Workbench", Width: 1200, Height: 820,
		MinWidth: 800, MinHeight: 600, URL: "/", DevToolsEnabled: devToolsEnabled,
		Frameless: true,
		// 允许把文件从资源管理器拖进窗口（用户要求"拖拽上传附件"）。
		//
		// 必须走 Wails 的原生投递：web 层拿不到拖进来文件的真实路径
		// （浏览器安全模型只给 File 对象，没有路径），而我们的附件机制
		// 统一是"把绝对路径交给模型读"。开了这个开关之后，拖拽不会触发
		// DOM 的 drop 事件，而是由下面那个 WindowFilesDropped 钩子给出真实路径。
		EnableFileDrop: true,
		// 无边框 + 圆角：Windows 11 上让系统负责圆角与阴影，避免自绘出现直角
		Windows: application.WindowsWindow{},
	})
	// 文件投递：把真实路径转发给前端，由输入框决定怎么用（附件 / 提示）。
	// 前端只拿到路径列表与落点信息，不碰文件内容——大文件也不会卡住界面。
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		files := event.Context().DroppedFiles()
		details := event.Context().DropTargetDetails()
		payload := map[string]any{"files": files}
		if details != nil {
			// 落点元素：前端用 data-file-drop-target 标记"希望接收文件"的区域
			payload["target"] = map[string]any{
				"id":    details.ElementID,
				"class": details.ClassList,
				// 坐标为 0 表示"没有具体落点"（例如拖到窗口空白处）
				"x": details.X,
				"y": details.Y,
			}
		}
		app.Event.Emit(fileDropEvent, payload)
	})
	// 拖拽进入/离开**不在这里做**：Wails 的 WindowFileDraggingEntered 只有 macOS 有。
	// Windows/WebView2 上开了 EnableFileDrop 之后，JS 的 dragenter/dragover/drop
	// 会照常触发（见 Wails 源码 webview_window_windows.go 的注释），
	// 所以"松开即可添加"的提示由前端听 DOM 事件实现，不需要原生通道。
	// 关闭窗口前先问一次：还有回合在跑就拦下（Cancel 会跳过默认的销毁监听），
	// 让前端弹"正在运行"的确认框；用户确认后 ForceClose 会把 forceClosing 置真，
	// 这次关闭就能顺利通过。钩子先于监听执行，所以这里是有效的拦截点。
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if service.RequestClose() {
			event.Cancel()
		}
	})
	if err := app.Run(); err != nil {
		/**
		 * 启动失败必须有**看得见**的出口。
		 *
		 * 这里是 GUI 子系统，stderr 没有去处：WebView2 缺失、端口被占、用户数据目录不可写
		 * 这些情况下，原来的 log.Fatal 表现为"双击了，什么都没发生"——最难排查的一种故障。
		 */
		bridge.Logf("app_run_failed err=%v", err)
		bridge.MessageBoxError("ZeroA 启动失败", "应用无法启动：\n"+err.Error()+"\n\n详细信息见日志：\n"+filepath.Join(pi.AgentDir(), "gui-log.txt"))
		log.Fatal(err)
	}
	// 退出前停掉后台协程（defer 也会兜一次，这里显式写出来是为了说明生命周期）。
	cancelBackground()
}

// debugBrowserArgs 按环境变量决定是否给 WebView2 开远程调试端口。
//
// 存在的理由：界面里的问题（"点某一页是空白"）光看代码猜不出来，
// 需要能读真实 DOM。默认关闭是安全边界：端口一开，本机任何进程都能
// 通过 CDP 读这个界面、改这个界面。
func debugBrowserArgs() []string {
	port := strings.TrimSpace(os.Getenv("PI_GUI_DEBUG_PORT"))
	if port == "" {
		return nil
	}
	// 只接受数字，避免把任意字符串当参数塞进浏览器
	if _, err := strconv.Atoi(port); err != nil {
		log.Printf("PI_GUI_DEBUG_PORT 不是端口号，已忽略: %q", port)
		return nil
	}
	log.Printf("WebView2 远程调试端口已开启: %s", port)
	return []string{"--remote-debugging-port=" + port}
}

// traceTime 取出 trace 自带的时刻，让 pi:trace 与 pi:event 上的时间戳完全一致。
//
// internal/pi 为了不让观察者多做一次 Parse，直接把时间格式化成了字符串；
// 这里解析回来是唯一需要它的地方（agent.Classify 要 time.Time）。
// 解析失败只可能是布局被人改过，退回当前时间即可——时间戳不值得让事件丢掉。
func traceTime(trace pi.Trace) time.Time {
	at, err := time.Parse(agent.TimeLayout, trace.Time)
	if err != nil {
		return time.Now()
	}
	return at
}
