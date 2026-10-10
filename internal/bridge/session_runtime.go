package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"zeroa/internal/agents"
	"zeroa/internal/delegation"
	"zeroa/internal/pi"
)

/**
 * session_runtime.go：一条会话一个 Pi 进程。
 *
 * ## 为什么要这样
 *
 * Pi 的进程模型是"一个进程 + 当前会话文件"，`switch_session` 让这个进程改绑另一份文件
 * （`rpc-mode.js:474` → `runtimeHost.switchSession` → `rebindSession()`：把 RPC 模式里的
 * `session` 换成另一个会话对象）。于是"在回合运行中切换"= 把正在跑的那一轮连根拔掉：
 * 用户看到的是"我点了一下另一个会话，手上的任务就没了"。
 *
 * 以前的补法是**排队**（等回合结束再切）。那是治标：切换本身仍然要改动那个进程，
 * 所以"等着"这件事永远存在，还会在忙碌状态判断出错时变成"点了没反应"。
 *
 * 真正的修法是**一进程一会话**：
 *   · 切换 = 换一个进程说话（`activeRuntime`），不向任何正在干活的进程发
 *     `switch_session` / `new_session` / `abort`；
 *   · 已经开着的会话**复用原进程**：切换是零 RPC 的（不再 `switch_session`，
 *     也不再搬运 `get_messages` 的历史）；
 *   · 冷启动（这条会话还没有进程）才新建进程并在**那个新进程**里 `switch_session`，
 *     失败就把它关掉，当前会话完全不受影响。
 *
 * ## 与本项目其它部分的关系
 *
 * · 忙碌状态：`busy` 是**每个 runtime 自己的**（由它自己的 `agent_start`/`agent_settled` 维护）。
 *   全局那种"一个布尔代表整个应用在忙"的做法在多进程下必然出错。
 * · 进程上限与空闲回收：见 `maxRuntimes` / `idleRuntimeTTL`。
 */

// maxRuntimes 同时活着的会话进程上限。
//
// 每个 Pi 进程百 MB 级，而这个应用是"开着好几个会话来回看"的用法，
// 没有上限就等于"开过的每条会话都永远占着一份内存"。
// 到上限时按"最久没用过、且不在跑回合"的顺序回收（见 reapRuntimesLocked）。
const maxRuntimes = 6

// idleRuntimeTTL 是空闲会话进程的存活时长。
//
// 只回收"既不是当前会话、也没有在跑回合"的进程——回收不等于丢东西：
// 会话内容一直在 .jsonl 里，下次点开是冷启动（重新加载历史），功能不受影响。
const idleRuntimeTTL = 15 * time.Minute

// sessionRuntime 是一条会话的进程。
type sessionRuntime struct {
	id string
	// client 是这个进程的 RPC 句柄。
	client *pi.Client
	// cwd 是它的工作目录（会话头里的 cwd；冷启动时按它来起进程）。
	cwd string
	// role 是这个进程启动时用的角色 id（空 = 无角色）。
	//
	// 必须按 runtime 记：一进程一会话之后，A 会话用角色 X、B 会话用角色 Y 是常态，
	// 而委派闸门（能不能派、白名单、审计、deliverOn）要按"派活的那条会话"的角色
	// 判定——读全局 activeRole 会把 X 的策略套到 B 的委派请求上。
	role string
	// sessionID / sessionFile 由这个进程自己的响应（get_state 等）学到。
	sessionID   string
	sessionFile string
	// lastUsed 用于空闲回收与"同一会话有多个进程时挑最新的那个"。
	lastUsed time.Time
	/**
	 * busy 表示这个进程此刻**正在跑一轮**（agent_start → true，agent_settled → false）。
	 *
	 * 它必须按 runtime 存：多进程下"应用在忙"是个没有意义的说法，
	 * 而回收进程、判断"能不能安全关掉它"都要看**这一个**进程的状态。
	 */
	busy bool
	// closed 保证 Close 只做一次（回调与回收协程可能同时发现它没了）。
	closed atomic.Bool
}

// remember 从这个进程的响应里学 sessionID / sessionFile。
//
// 为什么要"学"而不是各自记：冷启动时的 sessionId/sessionFile 只有进程自己知道
// （`switch_session` 之后 Pi 才确定绑在哪份文件上；`new_session` 则换了一份新文件）。
// 学不到就保持原值——这里绝不能让缓存影响命令本身的成败。
func (r *sessionRuntime) remember(data json.RawMessage) {
	var state struct {
		SessionID   string  `json:"sessionId"`
		SessionFile *string `json:"sessionFile"`
	}
	if json.Unmarshal(data, &state) != nil || state.SessionID == "" {
		return
	}
	// 换了会话 id（new_session / switch_session）就把旧的文件路径丢掉，
	// 否则会把"上一条会话的文件"错认成这一条的。
	if state.SessionID != r.sessionID {
		r.sessionFile = ""
	}
	r.sessionID = state.SessionID
	if state.SessionFile != nil && *state.SessionFile != "" {
		r.sessionFile = *state.SessionFile
	}
}

// startRuntime 新建一个会话进程。
func (s *PiService) startRuntime(opts pi.Options, roleID string) (*sessionRuntime, error) {
	s.mu.Lock()
	s.nextRuntime++
	r := &sessionRuntime{
		id:       fmt.Sprintf("runtime-%d", s.nextRuntime),
		cwd:      opts.Cwd,
		role:     strings.ToLower(strings.TrimSpace(roleID)),
		lastUsed: time.Now(),
	}
	callback := s.OnEvent
	s.mu.Unlock()

	opts.ExtraEnv = mergeExtraEnv(opts.ExtraEnv, map[string]string{runtimeIDEnv: r.id})
	cmd, err := pi.Command(opts)
	if err != nil {
		return nil, err
	}
	client, err := pi.Start(cmd, func(trace pi.Trace) {
		// 每一行都盖上"这是哪个会话进程"的戳：界面靠它把后台会话的输出留在后台。
		trace.RuntimeID = r.id
		if trace.Direction == "stdout" {
			// 顺手学 sessionId/sessionFile：调用方不一定每次都刷 get_state。
			var response pi.Response
			if json.Unmarshal([]byte(trace.Raw), &response) == nil && response.Type == "response" {
				s.mu.Lock()
				r.remember(response.Data)
				if s.activeRuntime == r {
					s.sessionID, s.sessionFile = r.sessionID, r.sessionFile
				}
				s.mu.Unlock()
			}
			s.noteRuntimeEvent(r, trace.Raw)
		}
		// 进程退出（正常或崩溃都算）：立刻清登记、把当前会话的文件交还给
		// pending，别让死句柄继续装"当前会话"（详见 noteRuntimeExit）。
		if trace.Direction == "system" && strings.HasPrefix(trace.Raw, "process_exit") {
			s.noteRuntimeExit(r)
		}
		if callback != nil {
			callback(trace)
		}
	})
	if err != nil {
		return nil, err
	}
	r.client = client

	s.mu.Lock()
	if s.runtimes == nil {
		s.runtimes = make(map[string]*sessionRuntime)
	}
	s.runtimes[r.id] = r
	// 起进程是慢操作，期间可能又有人点了别的会话：这里只登记，不抢 active。
	if s.activeRuntime == nil {
		s.activeRuntime = r
		s.client = client
	}
	s.mu.Unlock()
	Logf("session_runtime_started id=%s cwd=%s total=%d", r.id, r.cwd, s.runtimeCount())
	return r, nil
}

/**
 * ensureActiveRuntime 保证"当前会话"有一个活着的进程。
 *
 * 它是**懒启动的唯一落点**：新建会话、打开一条历史会话都不再立刻起进程
 * （见 sessions.go 的 NewSession / SwitchSession），于是"当前会话没有进程"
 * 成了一种正常状态。真正需要进程的是 prompt —— 用户按下发送的那一刻会话才
 * 第一次有内容，这时候再花 1–3.5 秒（实测的冷启动耗时）建进程是可以接受的
 * （人已经在等回答了），而在"新建/打开"那一刻花掉它不可以。
 *
 * 为什么只给 prompt / promptWithFiles 用：其它命令（SetModel / Bash / GetMessages…）
 * 在没有进程时本来就无从谈起，保持原样报"请先启动 Pi"。顺手把它们全都改成懒启动
 * 只会把"谁负责起进程"这件事扩散到十几个入口，每个入口都要重新想一遍
 * "会话/角色/工作目录对不对"，改动面与出错面都成倍增长。
 */
func (s *PiService) ensureActiveRuntime(command string) error {
	s.navigation.Lock()
	defer s.navigation.Unlock()

	s.mu.Lock()
	current := s.activeRuntime
	ready := s.optionsReady
	opts := s.options
	roleID := s.activeRole
	pending := s.pendingSessionFile
	s.mu.Unlock()

	// 已经有活着的进程：什么都不用做（热路径）。
	if runtimeAlive(current) {
		return nil
	}
	if !ready {
		/**
		 * 还没跑过一次 Start：没有启动参数模板（模型/工具/角色/工作目录都不知道），
		 * 这与旧实现的"请先启动 Pi"是同一种情况，如实报同一句话。
		 */
		return errors.New("请先启动 ZeroA")
	}
	if current != nil {
		// 句柄还挂着但进程已经死了：先摘掉它，否则 startRuntime 不会接管 active。
		// 死掉的那条会话的文件要交还给 pending：noteRuntimeExit 一般会先做好，
		// 这里是"退出 trace 与这条命令赛跑、它还没跑"的兜底——不做的话新进程
		// 会绑一份空会话，用户这句话就写进另一份历史里（实测踩到）。
		s.mu.Lock()
		if current.sessionFile != "" && s.pendingSessionFile == "" {
			s.pendingSessionFile = current.sessionFile
		}
		pending = s.pendingSessionFile
		s.mu.Unlock()
		s.closeRuntime(current, "进程已退出")
	}
	/**
	 * 委派桥（父 Pi 里的扩展 → Go 的本地控制口）与它的令牌是**随进程**的：
	 * Stop 会把它拆掉，`Stop` 之后的第一条消息要在这里补建一次。
	 * 漏掉这一步的后果很隐蔽：进程能起来、对话能用，只是"模型不能主动派子智能体"，
	 * 而且日志里只有一行 delegate_bridge 的旧记录。
	 */
	if err := s.ensureBridge(); err != nil {
		Logf("delegate_bridge_failed err=%v", err)
	}
	opts.ExtraEnv = s.bridgeEnvFor()
	/**
	 * 这个新进程要带**哪条会话的角色**。
	 *
	 * 角色是启动参数（提示词/工具集/权限），只能随进程出生；一条历史会话当初是
	 * 在角色 X 下做的，重新打开它就得按 X 起进程。以前这件事发生在"冷启动打开会话"
	 * 那条路径上（navigate → optionsForSession），懒启动之后"冷启动"挪到了这里，
	 * 判据也得跟着挪——漏了它表现是"打开老会话，角色没跟回来"。
	 */
	if pending != "" {
		opts = s.optionsForSession(opts, pending)
	}
	/**
	 * 这个进程实际会用哪个角色：与 optionsForSession 的判定同源——
	 * 会话有角色记忆用记忆的，否则用参数模板的（Start 时的角色）。
	 * 记到 runtime 上，委派闸门按"派活的那条会话"的角色判定（见 sessionRuntime.role）。
	 */
	effectiveRole := roleID
	if pending != "" {
		if sessionRole := s.roleForSession(pending); sessionRole != "" {
			effectiveRole = sessionRole
		}
	}
	// 起新进程之前先看看是不是到上限了：到上限就回收一个（绝不回收当前会话或正在跑的那条）。
	s.reapOneRuntime()

	runtime, err := s.startRuntime(opts, effectiveRole)
	if err != nil {
		return err
	}
	if pending != "" {
		/**
		 * 关键顺序：**先 switch_session，再发 prompt**。
		 *
		 * 用户看的是历史会话 A，而新进程刚出生时绑的是一份**空的新会话**。
		 * 不先切过去的话，他这句话会写进那份空会话里：界面看起来完全正常
		 * （时间线上有他说的话、模型也在回答），但历史已经断成了两条，
		 * A 的 .jsonl 里永远不会有这一句。这就是"打开老会话继续聊"最隐蔽的坏法。
		 *
		 * 失败（文件被删、被扩展的 session_before_switch 钩子拦下）时**绝不能发**：
		 * 把半成品进程收掉并如实报错，用户手里的历史一个字都不会变。
		 */
		result, err := runtimeRequest(runtime, "switch_session", map[string]any{"sessionPath": pending})
		if err != nil {
			s.closeRuntime(runtime, "懒启动切换会话失败")
			return err
		}
		var data struct {
			Cancelled bool `json:"cancelled"`
		}
		_ = json.Unmarshal([]byte(result.Data), &data)
		if data.Cancelled {
			s.closeRuntime(runtime, "懒启动切换会话被取消")
			return errors.New("打开这条会话被扩展取消了（session_before_switch）")
		}
	}
	if err := s.adoptRuntime(runtime); err != nil {
		// 半成品进程不能留：它没有主人，也没人会去关它。
		s.closeRuntime(runtime, "懒启动失败")
		return err
	}
	// 进程接手了这条会话：pending 的使命结束（"在看它"现在由 activeRuntime 表达）。
	s.mu.Lock()
	s.pendingSessionFile = ""
	s.mu.Unlock()
	/**
	 * 角色的"运行时可改"部分（自动压缩）是进程起来之后补发的 RPC。
	 * Start 里不再发（那时还没有进程），这里补一次是为了让**新起的这个进程**
	 * 也带着角色的压缩策略——否则懒启动的进程会"角色只生效一半"。
	 */
	s.applyRoleAutoCompaction(roleID)
	Logf("session_runtime_lazy_started id=%s command=%s session=%s", runtime.id, command, pending)
	return nil
}

/**
 * adoptRuntime 把一个**刚创建**的进程登记成当前会话（懒启动专用）。
 *
 * 与 activateRuntime 的区别只有一个，但很关键：它**不发** onSessionSwitch。
 * 那条推送的语义是"后端在后台替你换了当前会话"，前端收到会按一次完整切换处理
 * （存旧视图、立新视图、必要时重建时间线）——而懒启动发生在用户刚发出一条消息之后，
 * 重建时间线会把那条刚发出去的消息抹掉一瞬间（用户会看到自己的话闪一下不见了）。
 * 所以这里只做登记：新进程的 id 由 prompt 的返回值带给前端，由前端"认领"
 * （见前端 rpc/router.ts 的 adoptPendingRuntime）。
 *
 * 也**不重载历史**：这是一条刚出生的会话，本来就什么都没有。
 */
func (s *PiService) adoptRuntime(r *sessionRuntime) error {
	// 一次 get_state：既能证明进程真的能应答，也能把 sessionId/sessionFile 学回来
	// （新建的会话可能还没落盘，那时学不到，保持空）。
	state, err := runtimeRequest(r, "get_state", nil)
	if err != nil {
		return err
	}
	s.mu.Lock()
	r.remember(json.RawMessage(state.Data))
	r.lastUsed = time.Now()
	s.activeRuntime, s.client = r, r.client
	s.sessionID, s.sessionFile = r.sessionID, r.sessionFile
	s.mu.Unlock()
	// 新进程 = 新会话：这个会话上不可能跑着"上一条会话的回合"。
	s.clearTurnState("runtime_adopted")
	// 换了会话就换了工具集（不同角色/不同扩展装配）：让"模型可调用工具"的缓存失效。
	invalidateSessionTools()
	return nil
}

// runtimeIDEnv 把 runtime id 注入 Pi 进程的环境（扩展/排查用得到）。
const runtimeIDEnv = "PI_GUI_RUNTIME_ID"

// mergeExtraEnv 把 overrides 合进 base（不改动入参）。
func mergeExtraEnv(base, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}

// noteRuntimeEvent 从事件流里维护"这个进程此刻在不在跑回合"。
//
// 只认 agent_start / agent_settled 两个事件：它们是 Pi 对"这一轮开始了/结束了"
// 的权威表达。用它来判断能不能回收进程，比定时器猜要可靠得多。
func (s *PiService) noteRuntimeEvent(r *sessionRuntime, raw string) {
	if r == nil {
		return
	}
	if !hasEventType(raw, "agent_start") && !hasEventType(raw, "agent_settled") {
		return
	}
	busy := hasEventType(raw, "agent_start")
	s.mu.Lock()
	wasBusy := r.busy
	r.busy = busy
	r.lastUsed = time.Now()
	manager := s.delegation
	s.mu.Unlock()
	router := (*agents.Router)(nil)
	if manager != nil {
		router = manager.Router()
	}
	if wasBusy && !busy && router != nil {
		/**
		 * 这条会话的回合结束了：把攒着的子智能体结果投出去。
		 *
		 * **必须开 goroutine**：本函数跑在 internal/pi 的读取 goroutine 上，
		 * 而投递要发 prompt/follow_up 并等响应——由这条 goroutine 自己等自己会死锁
		 *（响应永远读不到，最后白等满 10 秒超时，结果被当成投递失败丢掉）。
		 */
		go router.FlushParent(r.id)
	}
}

// hasEventType 判断一行 JSONL 是不是某个类型的事件（避免为每次事件做完整解析）。
func hasEventType(raw, want string) bool {
	return len(raw) > 0 && containsJSONType(raw, want)
}

// containsJSONType 是"够用就好"的匹配：`"type":"agent_start"`（允许空格）。
func containsJSONType(raw, want string) bool {
	needle := `"type":"` + want + `"`
	if indexOf(raw, needle) >= 0 {
		return true
	}
	spaced := `"type": "` + want + `"`
	return indexOf(raw, spaced) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// runtimeCount 返回活着的会话进程数（界面与日志用）。
func (s *PiService) runtimeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runtimes)
}

// runtimeRequest 给**指定**会话进程发一条命令（不动其它会话）。
func runtimeRequest(r *sessionRuntime, command string, fields map[string]any) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	response, err := r.client.Request(ctx, command, fields)
	result := newResult(response)
	result.RuntimeID = r.id
	return result, err
}

// runtimeAlive 判断进程还在不在。
func runtimeAlive(r *sessionRuntime) bool {
	if r == nil || r.client == nil {
		return false
	}
	select {
	case <-r.client.Done():
		return false
	default:
		return true
	}
}

/**
 * parentID 是"当前会话在 Supervisor 里的父 id"。
 *
 * 子智能体是按父 id 归属的：A 会话派的活，结果必须回到 A（哪怕用户此刻正在看 B）。
 * 没有会话进程时退回 mainAgentID —— 单测里常常直接构造 PiService、不起进程。
 */
func (s *PiService) parentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRuntime != nil {
		return s.activeRuntime.id
	}
	return delegation.MainAgentID
}

// runtimeByID 按 id 找会话进程。
func (s *PiService) runtimeByID(id string) *sessionRuntime {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimes[id]
}

// runtimeBusy 返回某条会话此刻是否在跑回合。
//
// 找不到那个 runtime（可能已被回收）时退回全局标记：宁可多等一会儿，
// 也不要把结果插进一个正在生成的对话里。
func (s *PiService) runtimeBusy(parentID string) bool {
	if parentID == "" || parentID == delegation.MainAgentID {
		return s.agentParentBusy()
	}
	r := s.runtimeByID(parentID)
	if r == nil {
		return s.agentParentBusy()
	}
	s.mu.Lock()
	busy := r.busy
	s.mu.Unlock()
	return busy
}

// runtimeAliveByID 判断某条会话的进程是否还活着（找不到 = 已经不在了）。
func (s *PiService) runtimeAliveByID(parentID string) bool {
	if parentID == "" || parentID == delegation.MainAgentID {
		s.mu.Lock()
		client := s.client
		s.mu.Unlock()
		return client != nil
	}
	return runtimeAlive(s.runtimeByID(parentID))
}

/**
 * eventBelongsToActiveRuntime 判断一条事件是不是"当前正在看的这条会话"发的。
 *
 * 空 runtimeID 视为属于当前会话：单测与旧路径（直接构造 PiService、手工发事件）
 * 都不带这个字段，不能让它们的事件被静默丢掉。
 */
func (s *PiService) eventBelongsToActiveRuntime(runtimeID string) bool {
	if runtimeID == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRuntime == nil {
		return true
	}
	return s.activeRuntime.id == runtimeID
}

// closeRuntime 关掉一个会话进程并把它从登记表里摘掉。
//
// 注意它**不碰** activeRuntime：调用方自己决定要不要换当前会话
// （回收后台进程时当然不该换）。
func (s *PiService) closeRuntime(r *sessionRuntime, reason string) {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	delete(s.runtimes, r.id)
	// 当前会话被关掉（例如它的会话文件被删了）：把 active 清干净，
	// 否则后续 request 会往一个已死的进程里发命令。
	if s.activeRuntime == r {
		s.activeRuntime = nil
		s.client = nil
	}
	s.mu.Unlock()
	// 手工构造的 runtime（单测）可能没有 client；进程已死时 Close 也只是收尾。
	if r.client != nil {
		_ = r.client.Close()
	}
	Logf("session_runtime_closed id=%s reason=%s left=%d", r.id, reason, s.runtimeCount())
}

/**
 * noteRuntimeExit 在一个会话进程退出时收尾它的登记。
 *
 * 不做这件事的后果（实测故障链）：进程崩溃 → runtimes 里留着死句柄 →
 * s.client 仍指向它 → abort/steer 全部打到死进程上，报"Pi 在响应前退出"，
 * 界面还以为回合在跑，只能重启应用。
 *
 * 两件事：
 *  1. 清忙碌标记并关掉登记（closeRuntime 会把死句柄从 runtimes 摘掉，
 *     是当前会话就一并清掉 activeRuntime/client，下一条消息走懒重启）；
 *  2. 死的是**当前会话**时，把它绑定的会话文件交还给 pendingSessionFile：
 *     下一条消息懒启动会先 switch_session 回这份文件，对话在原来的历史里
 *     继续，而不是悄悄写进一份刚出生的空会话（历史被劈成两条）。
 *
 * 主动 Stop 不收编：Stop 自己清了 pendingSessionFile 并先把 activeRuntime
 * 置空，这里看到的 activeRuntime != r，自然跳过；r.closed 双保险。
 *
 * closeRuntime 会等 Wait goroutine 收尾（最多几秒），而本函数跑在 trace
 * 回调上（正是那个 Wait goroutine 在发 trace），所以必须开 goroutine，
 * 否则就是自己等自己。
 */
func (s *PiService) noteRuntimeExit(r *sessionRuntime) {
	if r == nil {
		return
	}
	s.mu.Lock()
	r.busy = false // 进程都没了，不可能还有回合在跑（回收器也不该再跳过它）
	if !r.closed.Load() && s.activeRuntime == r && r.sessionFile != "" && s.pendingSessionFile == "" {
		s.pendingSessionFile = r.sessionFile
		Logf("session_runtime_exit_pending id=%s session=%s", r.id, r.sessionFile)
	}
	s.mu.Unlock()
	go s.closeRuntime(r, "进程已退出")
}

/**
 * reapRuntimesLocked 在超过上限时挑一个"可以安全回收"的进程。
 *
 * 挑选顺序（都不满足就返回 nil = 这次不回收）：
 *  1. 排除当前会话（用户正在看的那个）；
 *  2. 排除正在跑回合的（回收 = 把用户的后台任务掐掉，绝不能做）；
 *  3. 选 lastUsed 最早的那个。
 */
func (s *PiService) reapRuntimesLocked() *sessionRuntime {
	if len(s.runtimes) < maxRuntimes {
		return nil
	}
	var candidate *sessionRuntime
	for _, r := range s.runtimes {
		if r == s.activeRuntime || r.busy || !runtimeAlive(r) {
			continue
		}
		if candidate == nil || r.lastUsed.Before(candidate.lastUsed) {
			candidate = r
		}
	}
	return candidate
}

// reapOneRuntime 在超过上限时回收一个会话进程（见 reapRuntimesLocked）。
//
// 单独一个函数是因为调用点（懒启动起进程之前）自己**不持** mu：
// reapRuntimesLocked 的"Locked"说的是"调用方持锁"，这里负责把那把锁拿对。
func (s *PiService) reapOneRuntime() {
	s.mu.Lock()
	victim := s.reapRuntimesLocked()
	s.mu.Unlock()
	if victim != nil {
		s.closeRuntime(victim, "超出会话进程上限")
	}
}

// reapIdleRuntimes 回收空闲太久的会话进程（后台会话不会永远占着内存）。
func (s *PiService) reapIdleRuntimes(now time.Time) int {
	s.mu.Lock()
	var victims []*sessionRuntime
	for _, r := range s.runtimes {
		if r == s.activeRuntime || r.busy || !runtimeAlive(r) {
			continue
		}
		if now.Sub(r.lastUsed) >= idleRuntimeTTL {
			victims = append(victims, r)
		}
	}
	s.mu.Unlock()
	for _, r := range victims {
		s.closeRuntime(r, "空闲回收")
	}
	return len(victims)
}

/**
 * roleForSession 找出"这条会话当初是用哪个角色做的"。
 *
 * 为什么要它：角色是 Pi 的**启动参数**（提示词/工具集/权限），每一条会话进程
 * 启动时就得定下来。以前的做法是"打开会话前先把全局角色切过去再重启 Pi"——
 * 那会把**所有**会话进程一起杀掉（用户报的"点一下会话，正在跑的回合就没了"）。
 * 现在改成冷启动这条会话时，**只给它的进程**带上它自己的角色：
 *   · 已经开着的会话 → 进程本来就是那个角色起的，什么都不用做；
 *   · 没开着的会话 → 起进程时按这里查到的角色编译参数，不影响别的会话。
 *
 * 找不到记忆（新会话、或旧数据）时返回空串 = 用界面当前的启动参数。
 */
func (s *PiService) roleForSession(sessionPath string) string {
	if strings.TrimSpace(sessionPath) == "" {
		return ""
	}
	// loadGUIState 的约定是"调用方持有 configMu"（与配置写入互斥）。
	// 这里本来没持锁——saveGUIState 是 tmp+rename 原子写所以实际上读不出半截文件，
	// 但约定就是约定：与其依赖"写入方永远保持原子"，不如把锁补上。
	s.configMu.Lock()
	state := loadGUIState()
	s.configMu.Unlock()
	if len(state.RoleBySession) == 0 {
		return ""
	}
	want := normalizeDir(sessionPath)
	for path, roleID := range state.RoleBySession {
		if normalizeDir(path) == want {
			return strings.ToLower(strings.TrimSpace(roleID))
		}
	}
	return ""
}

/**
 * optionsForSession 在基础启动参数上叠加"这条会话自己的角色"。
 *
 * 刻意**不**动全局状态（activeRole / compiledRole / 任务循环策略）：
 * 那些是"当前会话"的东西，而后台冷启动一条老会话时不该把它们改掉——
 * 改了就会出现"我点开一条老会话看一眼，当前会话的循环策略被换了"。
 */
func (s *PiService) optionsForSession(base pi.Options, sessionPath string) pi.Options {
	roleID := s.roleForSession(sessionPath)
	if roleID == "" {
		return base
	}
	view, ok := findRole(roleID)
	if !ok {
		Logf("session_role_not_found session=%s role=%s", sessionPath, roleID)
		return base
	}
	compiled, err := compileRole(view.Role, base.LoadResources)
	if err != nil {
		Logf("session_role_compile_failed session=%s role=%s err=%v", sessionPath, roleID, err)
		return base
	}
	compiled.ApplyToOptions(&base)
	Logf("session_role_applied session=%s role=%s", sessionPath, roleID)
	return base
}

/**
 * navigate 是"打开一条会话 / 切到一个工作区"的唯一入口。
 *
 * 懒启动之后它只剩两件事：
 *  1. 目标**已经开着** → 换成跟它说话（零 RPC、不重载历史、不打断任何东西）；
 *  2. 目标**还没有进程** → 进入"待创建"状态（记住用户在看谁，进程留到第一条消息）。
 *
 * 冷启动进程这件事整体搬进了 ensureActiveRuntime：**只有用户真的发出消息**
 * 才值得花那 1–3.5 秒（见 session_history.go 开头的说明）。
 *
 * 任何一步失败都不会改变当前会话（用户点了打不开的会话，手上这一轮不受影响）。
 */
func (s *PiService) navigate(path, cwd string, fresh bool, command string) (Result, error) {
	s.navigation.Lock()
	defer s.navigation.Unlock()
	return s.navigateLocked(path, cwd, fresh, command)
}

func (s *PiService) navigateLocked(path, cwd string, fresh bool, command string) (Result, error) {
	s.mu.Lock()
	/**
	 * 还没跑过一次 Start（没有启动参数模板）：连"待创建"都建立不起来——
	 * 那时第一条消息也无从起进程，如实报"请先启动 Pi"。
	 *
	 * 这里判的**不是**"有没有活着的进程"：懒启动之后"刚进应用、一个进程都没有"
	 * 是正常状态，按进程数判会把打开历史会话误报成"请先启动 Pi"。
	 */
	ready := s.optionsReady
	base := s.options.Cwd
	s.mu.Unlock()
	if !ready {
		return Result{}, errors.New("请先启动 ZeroA")
	}
	if cwd == "" {
		cwd = base
	} else {
		cwd = resolveCwd(cwd)
	}

	if !fresh {
		if target := s.residentRuntime(path, cwd); target != nil {
			// 热切换：这条会话的进程还活着，直接换成跟它说话。
			return s.activateRuntime(target, command)
		}
	}
	// 目标还没有进程：只记住"用户在看谁"，进程等第一条消息。
	return s.enterPendingLocked(command, cwd, path)
}

/**
 * residentRuntime 找出"目标会话已经开着的那个进程"（没有就返回 nil）。
 *
 * 两种目标，判据不同：
 *   · path 非空（用户点了某条会话）→ 在所有活着的进程里按 **sessionFile** 找，
 *     找到就复用它（这就是"切回去还是原来那一轮"）；
 *   · path 为空（切到一个工作区）→ 只认**当前**进程已经在这个工作区里的情况
 *     （那等于"什么都没变"）。绝不去别的进程里挑一个"cwd 相同"的：
 *     工作区级切换没有指名道姓要哪条会话，随便认领一个进程会让用户
 *     莫名其妙地落进另一条会话。
 */
func (s *PiService) residentRuntime(path, cwd string) *sessionRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRuntime != nil && runtimeAlive(s.activeRuntime) {
		if path == "" {
			if normalizeDir(s.activeRuntime.cwd) == normalizeDir(cwd) {
				return s.activeRuntime
			}
		} else if s.activeRuntime.sessionFile != "" &&
			normalizeDir(s.activeRuntime.sessionFile) == normalizeDir(path) {
			return s.activeRuntime
		}
	}
	if path == "" {
		return nil
	}
	var target *sessionRuntime
	for _, candidate := range s.runtimes {
		if !runtimeAlive(candidate) || candidate.sessionFile == "" {
			continue
		}
		if normalizeDir(candidate.sessionFile) != normalizeDir(path) {
			continue
		}
		if target == nil || candidate.lastUsed.After(target.lastUsed) {
			target = candidate
		}
	}
	return target
}

/**
 * enterPendingLocked 把"当前会话"置成**还没有进程**的状态。
 *
 * 这是懒启动的另一半：用户点了新建/打开，后端只记下这件事（见 PiService 的
 * pendingSessionFile），一个进程都不起。返回的 payload 与 activateRuntime 的
 * **字段兼容**（前端 finishSessionSwitch 解析同一份 data），只是
 * state/messages/runtimeId 全为空，并多一个 `pending: true`：
 *
 *	{state: null, messages: null, cwd: <工作目录>, sessionPath: <会话文件或空>,
 *	 runtimeId: "", pending: true}
 *
 * 为什么必须清掉 active 指针：上一条会话的进程**不能关**（它可能正跑着一轮，
 * 切回去还要看），但它也**不能再是"当前"**——不清的话，紧接着的 prompt 会被
 * 发到那条会话的进程里，用户的新消息就落进了旧对话（界面还看不出来）。
 */
func (s *PiService) enterPendingLocked(command, cwd, path string) (Result, error) {
	var header sessionHeader
	if path != "" {
		header = readSessionHeader(path)
		if header.ID == "" {
			return Result{}, errors.New("无法读取会话文件")
		}
	}
	s.mu.Lock()
	s.activeRuntime = nil
	s.client = nil
	s.pendingSessionFile = path
	// sessionID/sessionFile 表示"界面此刻在看哪条会话"：待创建的那条也要如实写，
	// 否则 GetState 会把状态栏说成"什么都没有"，侧栏高亮也会丢。
	s.sessionID = header.ID
	s.sessionFile = path
	s.sessionName = header.Name
	if cwd != "" {
		// 下一条消息的进程要在这个目录里起：工作区切换也是从这里生效的。
		s.options.Cwd = cwd
		s.currentWorkspace = cwd
	}
	s.mu.Unlock()
	/**
	 * 换了会话就没有"上一条会话的回合"这回事了（同 activateRuntime 里的清理）：
	 * 不清的话关窗保护会以为前台还在跑，而那个回合属于已经切走的那条会话。
	 */
	s.clearTurnState("session_pending")
	payload, err := json.Marshal(map[string]any{
		"state":       nil,
		"messages":    nil,
		"cwd":         cwd,
		"sessionPath": path,
		"runtimeId":   "",
		"pending":     true,
	})
	if err != nil {
		return Result{}, err
	}
	Logf("session_pending command=%s path=%s cwd=%s", command, path, cwd)
	return Result{Command: command, Success: true, Data: string(payload)}, nil
}

/**
 * activateRuntime 把某个**已经开着的**进程设成当前会话（热切换）。
 *
 * 它**不重载历史**：那条会话的时间线早就在前端手里，这里只回一个 get_state
 * 让状态栏对得上；把 MB 级的 get_messages 再搬一遍是纯浪费（而且会让切换变慢——
 * 这正是旧实现的毛病之一）。
 *
 * 注意这里没有"冷启动"分支了：**冷启动已经完全不存在**——打开一条还没有进程的
 * 会话走 enterPendingLocked（只记住"用户在看它"），历史由 LoadSessionHistory
 * 直接从会话文件读，进程留到第一条消息（见 ensureActiveRuntime）。
 * 以前那个 cold 参数就是干这件事的，留着它只会让人以为打开会话还会起进程。
 */
func (s *PiService) activateRuntime(r *sessionRuntime, command string) (Result, error) {
	// 换了会话就换了工具集（不同角色/不同扩展装配）：让"模型可调用工具"的缓存失效。
	defer invalidateSessionTools()

	state, err := runtimeRequest(r, "get_state", nil)
	if err != nil {
		return state, err
	}
	s.mu.Lock()
	r.lastUsed = time.Now()
	s.activeRuntime, s.client = r, r.client
	s.sessionID, s.sessionFile = r.sessionID, r.sessionFile
	// 热切换之后"在看它"由 activeRuntime 表达，pending 必须清掉：
	// 留着旧值的话，这个进程一旦死掉，懒重启会把新进程切到那条**早就没在看**
	// 的 pending 会话上，用户这句话就落进了别的历史。
	s.pendingSessionFile = ""
	s.options.Cwd = r.cwd
	s.currentWorkspace = r.cwd
	path := r.sessionFile
	s.mu.Unlock()
	/**
	 * 换了会话：这个新会话上不可能跑着"上一个会话的回合"。
	 *
	 * 这个标记不再用于"能不能切换"（那件事已经不需要判断了），但关窗保护与
	 * 子智能体结果的投递时机仍然读它——不清掉就会表现成"明明没人跑，关窗还拦我"。
	 */
	s.clearTurnState("session_switched")
	/**
	 * 返回值与"后台换会话"事件**共用同一份 payload**。
	 *
	 * 形状是前端契约的一部分（见 router.ts 的 finishSessionSwitch）：
	 *   · `state`    必须是**一个 Result**（前端会直接 `routeResult(data.state)`）；
	 *   · `messages` 必须是 **Result 或 null**——热切换恒为 null（不重载历史），
	 *     前端据此一个字都不动它手上的时间线。
	 *
	 * 踩过的坑：事件里曾经塞的是"原始状态 JSON"而不是 Result，前端
	 * `routeResult(原始JSON)` 读不到 `success` → 判定失败 → 报错气泡显示
	 * **"undefined 失败"**（新建会话、切换会话各弹一次）。
	 * 两处各写一份 payload 迟早漂移，所以这里只 marshal 一次、两边共用。
	 */
	payload := map[string]any{
		"state":       state,
		"messages":    nil,
		"cwd":         r.cwd,
		"sessionPath": path,
		"runtimeId":   r.id,
	}
	data, _ := json.Marshal(payload)
	if s.OnSessionSwitch != nil {
		// 覆盖"由后端发起的激活"（任务循环换会话、将来别处主动切）；
		// 前端对它是幂等的（同一个 runtime 再切一次不做事）。
		s.OnSessionSwitch(string(data))
	}
	return Result{Command: command, Success: true, RuntimeID: r.id, Data: string(data)}, nil
}

/**
 * SwitchWorkspace 切换工作区：**不打断**其它工作区里正在跑的会话。
 *
 * 旧实现是"停进程再按新目录启动"，等于把甲工作区正在跑的任务杀掉；
 * 现在连"为新工作区冷启动一个进程"也省了——只把启动参数里的工作目录换过去，
 * 界面接着会打开这个工作区里记住的那条会话（同样不起进程，历史读文件）。
 * 进程统一留到"用户真的发出第一条消息"那一刻（见 ensureActiveRuntime）。
 */
func (s *PiService) SwitchWorkspace(cwd string) (Result, error) {
	resolved := resolveCwd(cwd)
	info, err := os.Stat(resolved)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, errors.New("工作区路径不是文件夹")
	}
	return s.navigate("", resolved, false, "switch_workspace")
}

// StartRuntimeReaper 在后台定期回收空闲会话进程（由 main.go 拉起）。
func (s *PiService) StartRuntimeReaper(ctx context.Context) {
	go s.reapLoop(ctx, time.Minute)
}

// reapLoop 定期回收空闲会话进程。
func (s *PiService) reapLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if reaped := s.reapIdleRuntimes(now); reaped > 0 {
				Logf("session_runtime_reaped count=%d", reaped)
			}
		}
	}
}
