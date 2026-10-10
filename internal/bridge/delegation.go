package bridge

// 本文件是委派域的**接线层**：实现已经迁到 internal/delegation（Manager），
// 这里只剩三件事——
//
//	① 懒创建 Manager，把"服务侧的事实"（当前会话、进程句柄、授权门禁……）以回调注入；
//	② Wails 绑定入口（Result 形状，前端/测试调的就是这些）；
//	③ 会话回合状态（parentBusy/runningWork）——它属于会话域（sessions.go、
//	   service.go 都要问它），不跟 Manager 走，Manager 只在回合结束时被叫来 Flush。

import (
	"encoding/json"
	"strings"

	"zeroa/internal/agent"
	"zeroa/internal/agents"
	"zeroa/internal/delegation"
	"zeroa/internal/pi"
)

// delegationManager 懒创建委派管理器（绝大多数会话不派子智能体，不为它们先建结构）。
//
// 依赖全部做成回调注入：Manager 不 import 主包，它需要的"服务侧事实"都在这里接好。
func (s *PiService) delegationManager() *delegation.Manager {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delegation != nil {
		return s.delegation
	}
	s.delegation = delegation.NewManager(delegation.Deps{
		Log:     Logf,
		Mode:    s.modeFor,
		CLIPath: s.cliPathFor,
		Workspace: func() string {
			s.mu.Lock()
			workspace := s.currentWorkspace
			if workspace == "" {
				workspace = s.workspaceDir
			}
			s.mu.Unlock()
			if workspace == "" {
				workspace = resolveCwd("")
			}
			return workspace
		},
		ParentModel: func() string {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.startModel
		},
		LoadResources:    s.resourceLoading,
		RoleForParent:    s.roleForParentRuntime,
		ParentClient:     s.parentClient,
		RuntimeBusy:      s.runtimeBusy,
		RuntimeAlive:     s.runtimeAliveByID,
		CurrentParentID:  s.parentID,
		EmitEvent:        s.emitAgentEvent,
		EmitNotice:       s.emitAgentNotice,
		Trace: func(agentID string, trace pi.Trace) {
			if s.OnEvent != nil {
				s.OnEvent(pi.Trace{Direction: trace.Direction, Raw: trace.Raw, Time: trace.Time, AgentID: agentID})
			}
		},
	})
	return s.delegation
}

// modeFor 返回父会话的启动模式（mock / real）。
func (s *PiService) modeFor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == "" {
		return "real"
	}
	return s.mode
}

/**
 * roleForParentRuntime 解析"发起委派的那条会话"的角色 id（注入给委派域）。
 *
 * 优先级：runtime 自己记的角色 → 全局 activeRole（兼容没有进程的场景：
 * 单测直接构造 PiService、或老路径写死 "main"）。runtime 找不到时退回全局值，
 * 与改造前的行为一致——那条会话的进程都不在了，它的结果本来也投不出去。
 */
func (s *PiService) roleForParentRuntime(parentID string) string {
	if parentID != "" && parentID != delegation.MainAgentID {
		if r := s.runtimeByID(parentID); r != nil {
			s.mu.Lock()
			role := r.role
			s.mu.Unlock()
			return role
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRole
}

// cliPathFor 返回父会话用的 Pi 入口，子智能体沿用同一个。
func (s *PiService) cliPathFor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cliPath
}

/**
 * parentClient 找到"某条会话的进程句柄 + 它此刻忙不忙"。
 *
 * 一进程一会话之后，投递必须发给**派活的那条会话**：
 *   · 发给 s.client 的话，用户在 A 派活、切到 B 之后，结论会落进 B 的对话；
 *   · 忙不忙也要按那条会话判断（决定用 prompt 还是 follow_up）。
 * 找不到那个 runtime（已经被回收）时退回当前会话：宁可投到"眼下这条"，
 * 也不要让结论凭空消失——用户至少能看到它。
 */
func (s *PiService) parentClient(parentID string) (*pi.Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if parentID != "" && parentID != delegation.MainAgentID {
		if r := s.runtimes[parentID]; r != nil {
			return r.client, r.busy
		}
	}
	return s.client, s.parentBusy
}

// supervisorFor 是测试用的取件口：常规路径请走 Manager 的方法。
func (s *PiService) supervisorFor() *agents.Supervisor {
	return s.delegationManager().Supervisor()
}

// emitAgentEvent 把子智能体的状态变化推给前端。
func (s *PiService) emitAgentEvent(kind string, inst *agents.Instance) {
	if s.OnAgentEvent == nil {
		return
	}
	payload := map[string]any{"kind": kind}
	if inst != nil {
		payload["agent"] = inst
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.OnAgentEvent(kind, string(raw))
}

// emitAgentNotice 推一条带载荷的管理事件（不是某个子智能体的状态）。
func (s *PiService) emitAgentNotice(kind string, payload map[string]any) {
	if s.OnAgentEvent == nil {
		return
	}
	body := map[string]any{"kind": kind}
	for key, value := range payload {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	s.OnAgentEvent(kind, string(raw))
}

// DelegateTask 派一个子智能体（父 Agent 通过 delegate_task 工具请求）。
//
// 返回得**很快**：父 Agent 拿到句柄就继续干活，结果稍后由投递回到它的会话。
func (s *PiService) DelegateTask(roleID, task string) (Result, error) {
	return s.delegateTask(s.parentID(), roleID, task)
}

// delegateTask 是真正的实现：parentID 指名"这条子智能体属于哪条会话"。
//
// 为什么一定要指名：一进程一会话之后，应用里同时有好几条会话在跑。
// 如果不记归属（以前只有一条会话，所以写死 "main" 也看不出问题），
// A 会话派出去的活，结果会被投到用户当时正在看的 B 会话里——
// 用户会看到"我没问过这个，它自己冒出来一段结论"。
func (s *PiService) delegateTask(parentID, roleID, task string) (Result, error) {
	payload, err := s.delegationManager().Delegate(parentID, roleID, task)
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "delegate_task", Success: true, Data: string(raw)}, nil
}

/**
 * SendToAgent 给一个（可继续的）子智能体发消息 —— 仿 DSH 的 send_message。
 *
 * delivery 决定投递方式（照 DSH 的分工）：
 *   - "steer"：赶最近的检查点插进去（模型的追问走这条，语义是"别停，把这点也考虑进去"）；
 *   - "queue"（默认）：排队等当前回合结束（人从界面发消息走这条，不打断它手上的工作）。
 *
 * 返回值只有受理凭证，**没有回复**：回复要等它自己那一轮跑完，
 * 然后像往常一样由结果通道回流到父会话（阶段成果）。
 */
func (s *PiService) SendToAgent(agentID, message, delivery string) (Result, error) {
	payload, err := s.delegationManager().Send(agentID, message, delivery)
	if err != nil {
		return Result{}, err
	}
	raw, _ := json.Marshal(payload)
	return Result{Command: "send_to_agent", Success: true, Data: string(raw)}, nil
}

/**
 * InterruptAgent 只中断子智能体**当前这一轮**，不销毁它 —— 仿 DSH 的 interrupt_agent。
 *
 * 与 KillAgent 的区别要记牢：
 *   - Interrupt：停这一轮，进程与会话都留着，可以继续给它发消息（可继续形态才有意义）；
 *   - Kill：结束它、释放进程，之后不能再说话。
 */
func (s *PiService) InterruptAgent(agentID string) (Result, error) {
	if err := s.delegationManager().Interrupt(agentID); err != nil {
		return Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"agentId": agentID,
		"note":    "已请求中断当前回合；它的队列与后续消息都保留，进程不会退出。",
	})
	return Result{Command: "interrupt_agent", Success: true, Data: string(payload)}, nil
}

// ListAgents 列出所有子智能体（Agents 页 / 父 Agent 查状态用）。
func (s *PiService) ListAgents() (Result, error) {
	payload, err := s.delegationManager().List()
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_agents", Success: true, Data: string(raw)}, nil
}

// KillAgent 停掉一个子智能体（界面按钮走这条路）。
func (s *PiService) KillAgent(agentID string) (Result, error) {
	return s.KillAgentWithReason(agentID, "用户手动停止")
}

// KillAgentWithReason 停掉一个子智能体并记录原因。
//
// 原因不是装饰：停止可能来自四个地方（用户按钮 / 父会话停止 / 父会话中止 /
// 模型自己收工 / 孤儿守卫），事后回看"它为什么被停"只能靠这句话。
func (s *PiService) KillAgentWithReason(agentID, reason string) (Result, error) {
	if err := s.delegationManager().Kill(agentID, reason); err != nil {
		return Result{}, err
	}
	return Result{Command: "kill_agent", Success: true}, nil
}

// delegationPolicy 读出"当前会话的角色允许怎么派子智能体"（测试走它做 round-trip）。
func (s *PiService) delegationPolicy() (delegation.Policy, error) {
	return s.delegationManager().Policy(s.parentID())
}

// noteParentToolCall 由事件观察者调用：某条会话的父 Agent 每发起一次工具调用记一次。
//
// 带 runtimeID：审计证据按会话隔离——A 会话动过手不能替 B 会话的委派放行。
// 空值退回"当前会话"（单测与旧路径不带这个字段）。
func (s *PiService) noteParentToolCall(runtimeID string) {
	id := strings.TrimSpace(runtimeID)
	if id == "" {
		id = s.parentID()
	}
	s.delegationManager().NoteParentToolCall(id)
}

// ResetDelegationState 在新会话/新回合开始时清空审计证据与挂起结果。
func (s *PiService) ResetDelegationState() {
	s.delegationManager().ResetState()
}

// maybeStopChildrenOnFlag 在主 Agent 的回复里发现候选 flag 时停掉子智能体。
func (s *PiService) maybeStopChildrenOnFlag(text string) {
	s.delegationManager().MaybeStopChildrenOnFlag(text)
}

// observeParentMessage 处理主会话的一条 message_end：抽文本、查 flag。
func (s *PiService) observeParentMessage(envelope agent.Envelope) {
	s.delegationManager().ObserveParentMessage(envelope)
}

// agentParentBusy 返回父会话此刻是不是在跑回合。
//
// 由事件流维护：agent_start 置忙、agent_settled 置闲（见 ObserveAgentEvent）。
func (s *PiService) agentParentBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parentBusy
}

// markParentBusy 由事件观察者调用（**当前会话**的回合开始/结束）。
func (s *PiService) markParentBusy(busy bool) {
	s.mu.Lock()
	changed := s.parentBusy != busy
	s.parentBusy = busy
	s.mu.Unlock()
	if changed && !busy {
		// 当前会话的回合结束了：把之前挂着的结果投出去。
		// 开 goroutine 的理由见 noteRuntimeEvent：投递要等 RPC 响应，
		// 而本函数跑在读取 goroutine 上（自己等自己 = 死锁到超时）。
		if router := s.delegationManager().Router(); router != nil {
			parentID := s.parentID()
			go router.FlushParent(parentID)
		}
	}
}

/**
 * clearTurnState 把"有回合在跑"的**两个来源一起**清掉，并说明原因。
 *
 * 为什么需要这么一个函数：忙碌状态来自两处——事件流的 parentBusy（agent_start/agent_settled）
 * 和前端上报的 runningWork。而"回合结束"这个事实**不只由 agent_settled 表达**：
 *
 *   · 进程被停掉或重启（切角色、改设置、换工作区，前端走的都是 stop + start）；
 *   · 进程自己退出（崩溃、被杀）；
 *   · 真的切换了会话（新会话上不可能有回合在跑）。
 *
 * 这三条路上都没有 agent_settled。漏清任何一处，"忙"就永远是 true，
 * 于是之后**每一次点会话都会被排队等一个永远不会结束的回合**——用户看到的就是
 * "切换会话被卡住/被中断"，而唯一的出路是"立即切换"（那会真的打断回合）。
 *
 * 实测日志就是这条线索：连续 4 次 load_session deferred + 2 次 force_idle_timeout，
 * 而中间还夹着两次角色切换。
 */
func (s *PiService) clearTurnState(reason string) {
	s.mu.Lock()
	wasBusy := s.parentBusy || s.runningWork
	s.parentBusy = false
	s.runningWork = false
	s.mu.Unlock()
	if wasBusy {
		Logf("turn_state_cleared reason=%s", reason)
		// 这一轮结束了：攒下的子智能体结果该投了（否则它们一直挂着）。
		// 同样开 goroutine：clearTurnState 可能被读取 goroutine 调到（process_exit、
		// agent_settled 之后），同步投递会自己等自己。
		if router := s.delegationManager().Router(); router != nil {
			parentID := s.parentID()
			go router.FlushParent(parentID)
		}
	}
}

// busySources 同时给出两个忙碌来源，供诊断日志使用。
//
// 分开报是刻意的：只写一句 reason=turn_running 的话，事后无法判断
// 到底是后端事件流漏了 settled，还是前端上报的状态没翻过来。
func (s *PiService) busySources() (parentBusy bool, frontendWork bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parentBusy, s.runningWork
}

// ensureBridge 在第一次启动 Pi 之前把桥准备好（幂等）。
//
// 桥本体在 internal/delegation；这里顺手把主包域的两件事一起做掉：
// 扩展工具默认启用（否则模型根本看不到 delegate_task / catalog_*）、工具清单扩展落盘。
func (s *PiService) ensureBridge() error {
	if err := s.delegationManager().EnsureBridge(s.registerCatalogRoutes); err != nil {
		return err
	}
	// 扩展工具默认是 inactive 的（Pi 的 defaultTools 只含 read/bash/edit/write），
	// 不显式启用的话，我们注册的 delegate_task / catalog_* 模型根本看不到。
	if err := ensurePiDefaultTools(workbenchToolNames); err != nil {
		Logf("pi_default_tools_failed err=%v", err)
	}
	if _, err := ensureCatalogExtension(); err != nil {
		// 写不进去不算致命，只是"模型自己维护清单"这条路暂时不通
		Logf("catalog_extension_failed err=%v", err)
	}
	return nil
}

// bridgeEnvFor 返回启动父 Pi 时要注入的环境变量。
func (s *PiService) bridgeEnvFor() map[string]string {
	return s.delegationManager().BridgeEnv()
}
