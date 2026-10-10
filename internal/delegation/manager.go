// Package delegation 是"子智能体"域：角色声明委派策略 → Go 起独立子进程 →
// 子智能体跑完 → 结果合并后投递回父会话。
//
// 设计边界（方案 docs/05-子智能体方案.md）：
//
//	· 一个子智能体 = 一个独立 Pi 进程，Go 是它的主人（能单独 kill / 限额 / 观察）；
//	· 第一版只允许一层：子智能体不能再派子智能体；
//	· 子会话只回**摘要**（8KB 硬上限），完整过程留在它自己的会话目录里。
//
// 本包不 import 主包：服务侧的事实（当前会话、进程句柄、授权门禁、角色配置……）
// 全部经 Deps 回调注入，Manager 只拥有委派域自己的状态（supervisor / router /
// bridge / 审计证据计数）。
package delegation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"zeroa/internal/agents"
	"zeroa/internal/pi"
	"zeroa/internal/roles"
)

const (
	// MainAgentID 是父（主）会话在 Supervisor 里的 id
	MainAgentID = "main"
	// agentIDPattern 是子智能体 id 的形状，用于防路径穿越（id 会进会话目录名）
	agentIDPattern = `^agent-\d+$`
)

var agentIDRegexp = regexp.MustCompile(agentIDPattern)

// Deps 是 Manager 需要的服务侧事实。全部做成回调：主包（PiService）在创建
// Manager 时把"怎么回答这些问题"注入进来，委派域自己不碰 PiService 的字段。
type Deps struct {
	// Log 写诊断日志（guiLog）。
	Log func(format string, args ...any)
	// Mode 返回父会话的启动模式（mock / real），子智能体沿用。
	Mode func() string
	// CLIPath 返回父会话用的 Pi 入口，子智能体沿用同一个。
	//
	// 为什么必须沿用：父是用 node + <包路径>/cli.js 起的（便携模式），
	// 子智能体如果按默认逻辑去找全局 `pi` 命令，在没装全局 Pi 的机器上
	// 会直接起不来——而错误信息（"找不到 pi"）看起来跟角色配置毫无关系。
	CLIPath func() string
	// Workspace 返回子智能体的工作目录（当前工作区，含兜底解析）。
	Workspace func() string
	// ParentModel 返回父会话启动时用的模型（角色没指定模型时沿用）。
	ParentModel func() string
	// LoadResources 返回本次启动是否加载扩展/技能/提示模板。
	LoadResources func() bool
	// RoleForParent 返回**发起委派的那条会话**启动时用的角色 id（空 = 无角色）。
	//
	// 为什么按会话解析而不是读一个全局"当前角色"：一进程一会话之后，
	// A 会话用角色 X、B 会话用角色 Y 是常态。委派闸门（enabled / 白名单 /
	// 审计 / deliverOn / stopOnFlag）必须按"派活的那条会话"的角色判定——
	// 读全局值会出现"会话 B 被角色 X 的策略放行/拦截"的张冠李戴。
	RoleForParent func(parentID string) string
	// ParentClient 找到"某条会话的进程句柄 + 它此刻忙不忙"。
	//
	// 一进程一会话之后，投递必须发给**派活的那条会话**：
	//   · 发给"当前会话"的话，用户在 A 派活、切到 B 之后，结论会落进 B 的对话；
	//   · 忙不忙也要按那条会话判断（决定用 prompt 还是 follow_up）。
	// 找不到那个 runtime（已经被回收）时退回当前会话：宁可投到"眼下这条"，
	// 也不要让结论凭空消失——用户至少能看到它。
	ParentClient func(parentID string) (*pi.Client, bool)
	// RuntimeBusy 返回某条会话此刻是否在跑回合。
	RuntimeBusy func(parentID string) bool
	// RuntimeAlive 判断某条会话的进程是否还活着（找不到 = 已经不在了）。
	RuntimeAlive func(parentID string) bool
	// CurrentParentID 返回"当前会话"的 id（老版本扩展/手工调用的兼容兜底）。
	CurrentParentID func() string
	// EmitEvent 把子智能体的状态变化推给前端（Agents 列表实时刷新）。
	EmitEvent func(kind string, inst *agents.Instance)
	// EmitNotice 推一条带载荷的管理事件（不是某个子智能体的状态）。
	EmitNotice func(kind string, payload map[string]any)
	// Trace 把子进程的原始 JSONL 也送进观察器，但打上 agentId——
	// 否则界面分不清谁在说话。
	Trace func(agentID string, trace pi.Trace)
}

// Manager 拥有委派域的全部状态。零值不可用：必须 NewManager。
type Manager struct {
	deps Deps

	mu         sync.Mutex
	supervisor *agents.Supervisor
	router     *agents.Router
	bridge     *bridge
	// parentToolCalls：各会话的父 Agent 调过多少次工具（按会话隔离）。
	// 它是"先审计再委派"的**硬代理条件**（Go 判定不了语义，只能判定这个）。
	parentToolCalls map[string]int
}

// NewManager 创建一个委派管理器。deps 里每个回调都不能为 nil（主包负责接好）。
func NewManager(deps Deps) *Manager {
	return &Manager{deps: deps}
}

func (m *Manager) log(format string, args ...any) {
	if m.deps.Log != nil {
		m.deps.Log(format, args...)
	}
}

// Supervisor 返回（必要时创建）子智能体管理器——暴露它是给 Abort/测试用的，
// 常规路径请用 Manager 自己的方法。
func (m *Manager) Supervisor() *agents.Supervisor {
	return m.supervisorFor()
}

// SupervisorIfAny 只在已经创建过时返回（Stop 这类"有就停"的路径用它，不为停而建）。
func (m *Manager) SupervisorIfAny() *agents.Supervisor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.supervisor
}

// Router 返回结果投递路由器（nil = 还没派过任何子智能体）。
func (m *Manager) Router() *agents.Router {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.router
}

// supervisorFor 返回（必要时创建）子智能体管理器。
//
// 懒创建：绝大多数会话不会派子智能体，没必要为它们先建一堆结构。
func (m *Manager) supervisorFor() *agents.Supervisor {
	m.mu.Lock()
	if m.supervisor != nil {
		sup := m.supervisor
		m.mu.Unlock()
		return sup
	}
	sup := agents.NewSupervisor(agents.Options{
		MaxParallel: 2,
		MaxChildren: 4,
		MaxDepth:    1,
		// 可继续子智能体是驻留的（空闲也占一份进程），单独设上限；
		// 满了直接拒绝不排队（照 DSH：占用者可能正是那个在等结果的父级）。
		MaxContinuable: 8,
		OnChange: func(inst *agents.Instance) {
			// 状态变化推给界面：Agents 列表要实时刷新
			m.deps.EmitEvent("agent:state", inst)
		},
	}, m.launchChildAgent)
	sup.SetPrompt(m.promptChildAgent)
	sup.SetTraceSink(func(agentID string, trace pi.Trace) {
		// 子进程的原始 JSONL 也进观察器，但打上 agentId——否则界面分不清谁在说话
		if m.deps.Trace != nil {
			m.deps.Trace(agentID, trace)
		}
	})
	sup.SetResultSink(func(inst *agents.Instance) {
		if router := m.Router(); router != nil {
			router.Submit(inst)
		}
	})
	/**
	 * 每一轮结束也投一次（可继续子智能体）。
	 *
	 * 不接这个的话，一个驻留 10 分钟、跑了好几轮的子智能体，父级只有在它被回收的那一刻
	 * 才收到"第一轮就该看到的结论"——期间父级会以为它还在忙。
	 */
	sup.SetTurnSink(func(inst *agents.Instance) {
		if router := m.Router(); router != nil {
			router.Submit(inst)
		}
	})
	// 投递实现：把一条文本按 kind 送进子进程（prompt/steer/follow_up 都是标准 RPC 命令）。
	sup.SetDeliver(func(ctx context.Context, client *pi.Client, inst *agents.Instance, kind, text string) error {
		_, err := client.Request(ctx, kind, map[string]any{"message": text})
		if err != nil {
			m.log("agent_deliver_failed id=%s kind=%s err=%v", inst.ID, kind, err)
		}
		return err
	})
	m.supervisor = sup
	m.router = agents.NewRouter(agents.RouterOptions{
		Coalesce: 1500 * time.Millisecond,
		Deliver:  m.deliverToParent,
		/**
		 * ParentBusy 现在按**那条会话说**：一进程一会话之后，"父在忙吗"这个问题
		 * 必须问具体的会话进程，而不是问整个应用。
		 *
		 * 用全局标记的后果很具体：A 会话在跑回合时，B 会话派的子智能体结果会被无限期
		 * "攒着等 A 闲下来"——而 A 可能还要跑十分钟。
		 */
		ParentBusy: m.deps.RuntimeBusy,
		ShouldHold: m.shouldHold,
		SessionClosed: func(parentID string) bool {
			// 那条会话已经没有活着的进程：投递不进去，如实丢弃并说明
			return !m.deps.RuntimeAlive(parentID)
		},
		OnDelivered: func(insts []*agents.Instance, _ string) {
			m.log("delegation_delivered agents=%d", len(insts))
			m.deps.EmitEvent("delegation:result", nil)
		},
		OnHeld: func(insts []*agents.Instance) {
			m.log("delegation_held agents=%d（父正在跑回合，等它结束再投）", len(insts))
		},
		OnDrop: func(insts []*agents.Instance, reason string) {
			m.log("delegation_dropped agents=%d reason=%s", len(insts), reason)
			/**
			 * 孤儿守卫：结果没人收的时候，**立刻停掉还在跑的子智能体**。
			 *
			 * 用户报的场景就卡在这里：父 Agent 已经解完并收工，子 Agent 还在跑，
			 * 烧 token 还占内存——而它的结论永远投不进任何人手里。
			 * "没人收"是确定的事实（父进程没了 / 会话已结束），所以这里不用犹豫：
			 * 立刻停，并且把原因写进它的记录，界面上能看出是被谁停的。
			 */
			sup := m.Supervisor()
			if sup == nil {
				return
			}
			for _, inst := range insts {
				if inst == nil || inst.Status.Finished() {
					continue
				}
				if err := sup.Kill(inst.ID, "结果已无人接收（"+reason+"），自动停止"); err != nil {
					m.log("delegation_orphan_kill_failed id=%s err=%v", inst.ID, err)
				} else {
					m.log("delegation_orphan_killed id=%s reason=%s", inst.ID, reason)
				}
			}
		},
	})
	m.mu.Unlock()
	return sup
}

// launchChildAgent 是"真的把子智能体起起来"。
//
// 复用主会话那套编译链路：角色 → CompiledRole → pi.Options。
// 子进程与父进程唯一的区别是**参数**：提示词、工具白名单、模型、会话目录。
func (m *Manager) launchChildAgent(ctx context.Context, inst *agents.Instance, onTrace func(pi.Trace)) (*pi.Client, error) {
	view, ok := roles.Find(inst.RoleID)
	if !ok {
		return nil, fmt.Errorf("没有这个子角色: %s", inst.RoleID)
	}

	workspace := m.deps.Workspace()
	if workspace == "" {
		return nil, errors.New("没有可用的工作目录，子智能体起不来")
	}

	// 子智能体的会话目录：一个实例一个目录，事后可以打开看它的完整过程
	sessionDir := filepath.Join(pi.AgentDir(), "agents", inst.ID, "sessions")
	if !agentIDRegexp.MatchString(inst.ID) {
		return nil, fmt.Errorf("非法子智能体 id: %s", inst.ID)
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, err
	}
	// 走 setter 而不是直接赋值：Launcher 跑在另一个 goroutine 上，
	// 而界面随时可能在别处取快照读这个字段（-race 抓到过）。
	m.supervisorFor().SetSessionDir(inst.ID, sessionDir)

	options := pi.Options{
		// 沿用父会话的模式：mock 会话的子智能体也走 mock。
		// 写死 real 会让"在 mock 下试委派"变成一次真实的模型调用（要花钱，
		// 而且没配 key 时直接失败）——测试时踩到过这个。
		Mode:     m.deps.Mode(),
		CLIPath:  m.deps.CLIPath(),
		Cwd:      workspace,
		Model:    m.deps.ParentModel(), // 角色没指定模型时沿用父的模型；指定了就覆盖（见 roles.Compile）
		AgentDir: pi.AgentDir(),
		// 子智能体也要能加载扩展/技能：没有联网工具它查不了资料。
		LoadResources: m.deps.LoadResources(),
		SessionDir:    sessionDir,
	}
	compiled, err := roles.Compile(view.Role, options.LoadResources)
	if err != nil {
		return nil, err
	}
	compiled.ApplyToOptions(&options)
	if len(compiled.Warnings) > 0 {
		m.log("child_role_warning role=%s %v", inst.RoleID, compiled.Warnings)
	}

	cmd, err := pi.Command(options)
	if err != nil {
		return nil, err
	}
	m.log("agent_spawn id=%s role=%s cwd=%s session_dir=%s", inst.ID, inst.RoleID, workspace, sessionDir)
	client, err := pi.Start(cmd, onTrace)
	if err != nil {
		return nil, err
	}
	inst.ExitCode = -1
	return client, nil
}

// promptChildAgent 把任务发给子智能体。
//
// 起进程 ≠ 开始干活：任务是一条 prompt。这里只发任务本身——
// 上下文隔离是这套设计的核心，**不要**把父会话的对话历史复制过来。
func (m *Manager) promptChildAgent(ctx context.Context, client *pi.Client, inst *agents.Instance) error {
	_, err := client.Request(ctx, "prompt", map[string]any{"message": inst.Task})
	return err
}

// deliverToParent 把合并后的结果投递回父会话。
//
// 投递方法的选择在 deliveryMethodFor：默认 follow_up（父忙）/ prompt（父闲），
// 父角色声明 deliverOn: checkpoint 时改 steer 立刻插话。
func (m *Manager) deliverToParent(parentID, message string) error {
	client, busy := m.deps.ParentClient(parentID)
	if client == nil {
		return errors.New("父会话的 ZeroA 进程不在，投递不进去")
	}

	/**
	 * 实测踩到的坑：一直用 `follow_up` 时，若父会话**已经空闲**（派完活、这一轮
	 * 早就结束了），消息只会排进 Pi 的队列——界面上表现为输入框上方的"队列 1"
	 * 一直挂着，父 Agent 不会被唤醒，用户要自己再敲一条消息才会处理。
	 * 子智能体的结论就这么躺在队列里，"后台委派"等于白做。
	 */
	method := m.deliveryMethodFor(parentID, busy)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Request(ctx, method, map[string]any{"message": message})
	if err != nil {
		m.log("delegation_deliver_failed method=%s parent=%s err=%v", method, parentID, err)
	}
	return err
}

// deliveryMethodFor 决定给这条会话投结果用哪个 RPC 方法（纯判定，便于测试）。
//
//	· 父在跑回合 → follow_up：不打断当前回合，等它结束再注入（设计意图）；
//	· 父已空闲   → prompt：直接开始新一轮，这正是"结论到了自动合并进来"；
//	· 父角色声明 deliverOn: checkpoint → steer：立刻插进最近的检查点
//	 （docs/05：急事用 checkpoint；以前漏了这条路，checkpoint 配了也不生效）。
func (m *Manager) deliveryMethodFor(parentID string, busy bool) string {
	if busy && m.deliverOnCheckpoint(parentID) {
		return "steer"
	}
	return DeliveryMethod(busy)
}

// DeliveryMethod 选投递用的 RPC 方法（纯函数，便于测试）。
//
// 单独抽出来是因为它是"后台委派的结论能不能自动回来"的唯一开关：
// 选错（父空闲时用 follow_up）会让结论永远停在队列里。
func DeliveryMethod(parentBusy bool) string {
	if parentBusy {
		return "follow_up"
	}
	return "prompt"
}

// Delegate 派一个子智能体（父 Agent 通过 delegate_task 工具请求、或界面直接调）。
//
// 返回得**很快**：父 Agent 拿到句柄就继续干活，结果稍后由投递回到它的会话。
// 返回的 payload 是给模型/界面看的受理凭证。
//
// parentID 指名"这条子智能体属于哪条会话"：一进程一会话之后，应用里同时有好几条
// 会话在跑。如果不记归属（以前只有一条会话，所以写死 "main" 也看不出问题），
// A 会话派出去的活，结果会被投到用户当时正在看的 B 会话里——用户会看到
// "我没问过这个，它自己冒出来一段结论"。
func (m *Manager) Delegate(parentID, roleID, task string) (map[string]any, error) {
	if strings.TrimSpace(parentID) == "" {
		parentID = MainAgentID
	}
	sup := m.supervisorFor()

	// 闸门：父角色必须允许委派，且子角色要在它的白名单里。
	// 策略按**派活的那条会话**的角色解析（见 Policy 的注释）。
	policy, err := m.Policy(parentID)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, errors.New("当前角色不允许派子智能体（角色定义里 delegation.enabled = false 或未设置）")
	}
	/**
	 * 空白名单 = 不允许派任何角色（角色定义的原文语义）。
	 *
	 * 这里必须自己拦：Supervisor 只在白名单非空时校验（空切片对它意味着"不限制"），
	 * 直接把空切片传下去会变成"开了委派、没填白名单 → 什么角色都能派"——
	 * 与配置语义正好相反，是个安全闸门的反转（实测审出来的）。
	 */
	if len(policy.AllowedRoles) == 0 {
		return nil, errors.New("这个角色的委派白名单是空的：delegation.allowedRoles 至少写一个子角色 id（或者把 delegation.enabled 关掉）")
	}
	if policy.RequireAudit {
		if err := m.RequireAuditEvidence(parentID); err != nil {
			return nil, err
		}
	}
	/**
	 * kind 闸门：只有单独定义的子智能体（kind=agent）才能被派。
	 *
	 * 普通角色不再兼任子智能体——兼任时"能派谁"和"人能选谁"是同一份名单，
	 * 专为委派调的窄角色会混进输入框、给人用的角色也能被模型派出去。
	 * 保存时 roles.Save 已经挡过一道（引用已存在的普通角色直接不让存），
	 * 这里是真正的执行点，再兜一次：白名单是父角色写的，子角色的定义
	 * 之后可能被改坏（kind 被去掉、文件被删），不能以保存时的情况为准。
	 */
	child, ok := roles.Find(roleID)
	if !ok {
		return nil, fmt.Errorf("子智能体不存在或配置损坏: %s", roleID)
	}
	if !child.Role.IsAgent() {
		return nil, fmt.Errorf("「%s」（%s）是普通角色，不能作为子智能体派出：在角色管理里右键它选「复制为子智能体」，或单独新建一个子智能体", child.Role.Name, roleID)
	}
	inst, err := sup.Spawn(agents.SpawnRequest{
		ParentID:     parentID,
		RoleID:       strings.ToLower(strings.TrimSpace(roleID)),
		Task:         task,
		ParentDepth:  0, // 第一版只有父能派，所以发起者深度恒为 0
		MaxDuration:  policy.MaxDuration,
		AllowedRoles: policy.AllowedRoles,
		Mode:         policy.Lifecycle,
		IdleTimeout:  policy.IdleTimeout,
		// 角色的预算要真的生效：不传下去的话 Supervisor 只会用建 supervisor 时
		// 写死的默认值，而提示词里告诉模型的却是角色配的数字——两边对不上。
		MaxChildren: policy.MaxChildren,
		MaxParallel: policy.MaxParallel,
	})
	if err != nil {
		return nil, err
	}

	// 派活时就把"能不能继续跟它说话"告诉模型：不然它会以为子智能体是一次性的，
	// 结果拿到了也不会再追问（这正是仿 DSH 的那条体验差异）。
	hint := "它在后台工作；完成后结果会自动出现在你的会话里，你现在可以继续手上的事。"
	if inst.Mode == agents.ModeContinuable {
		hint = fmt.Sprintf("它在后台工作；完成后结果会自动出现在你的会话里。"+
			"它是**可继续**的：需要追问或追加要求时用 send_to_agent 给它发消息（id=%s），它会接着原来的上下文继续做。", inst.ID)
	}
	return map[string]any{
		"agentId": inst.ID,
		"roleId":  inst.RoleID,
		"name":    inst.Name,
		"mode":    inst.Mode,
		"status":  inst.Status,
		"message": fmt.Sprintf("已派出子智能体 %s（角色 %s，形态 %s）。%s", inst.Name, inst.RoleID, inst.Mode, hint),
	}, nil
}

/**
 * Send 给一个（可继续的）子智能体发消息 —— 仿 DSH 的 send_message。
 *
 * delivery 决定投递方式（照 DSH 的分工）：
 *   - "steer"：赶最近的检查点插进去（模型的追问走这条，语义是"别停，把这点也考虑进去"）；
 *   - "queue"（默认）：排队等当前回合结束（人从界面发消息走这条，不打断它手上的工作）。
 *
 * 返回值只有受理凭证，**没有回复**：回复要等它自己那一轮跑完，
 * 然后像往常一样由结果通道回流到父会话（阶段成果）。
 */
func (m *Manager) Send(agentID, message, delivery string) (map[string]any, error) {
	trimmed := strings.TrimSpace(agentID)
	if !agentIDRegexp.MatchString(trimmed) {
		return nil, fmt.Errorf("子智能体 id 不合法: %q", agentID)
	}
	if strings.TrimSpace(message) == "" {
		return nil, errors.New("消息不能为空")
	}
	sup := m.supervisorFor()
	kind := agents.Delivery(strings.ToLower(strings.TrimSpace(delivery)))
	if kind != agents.DeliverySteer {
		kind = agents.DeliveryQueue
	}
	messageID, err := sup.Send(trimmed, message, kind)
	if err != nil {
		return nil, err
	}
	m.log("agent_message_sent id=%s delivery=%s", trimmed, kind)
	return map[string]any{
		"agentId":   trimmed,
		"messageId": messageID,
		"delivery":  kind,
		// 照 DSH：受理 ≠ 完成，回复会以阶段成果的形式回到父会话
		"note": "已受理。它的回复不会作为本次调用的返回值，而是等它那一轮跑完后作为阶段结果回到会话里。",
	}, nil
}

/**
 * Interrupt 只中断子智能体**当前这一轮**，不销毁它 —— 仿 DSH 的 interrupt_agent。
 *
 * 与 Kill 的区别要记牢：
 *   - Interrupt：停这一轮，进程与会话都留着，可以继续给它发消息（可继续形态才有意义）；
 *   - Kill：结束它、释放进程，之后不能再说话。
 */
func (m *Manager) Interrupt(agentID string) error {
	trimmed := strings.TrimSpace(agentID)
	if !agentIDRegexp.MatchString(trimmed) {
		return fmt.Errorf("子智能体 id 不合法: %q", agentID)
	}
	sup := m.supervisorFor()
	if err := sup.Interrupt(trimmed); err != nil {
		return err
	}
	m.log("agent_interrupt_requested id=%s", trimmed)
	return nil
}

// RespondUI 把一条 extension_ui_response 记录投递给某个子智能体的进程，
// 用来应答它弹出的 confirm / select / input 请求。
//
// record 的形状校验在 bridge 层（与主会话 RespondUI 共用同一套规则）；
// 这里只做 id 合法性校验 + 找到进程投递。
func (m *Manager) RespondUI(agentID string, record map[string]any) error {
	trimmed := strings.TrimSpace(agentID)
	if !agentIDRegexp.MatchString(trimmed) {
		return fmt.Errorf("子智能体 id 不合法: %q", agentID)
	}
	sup := m.supervisorFor()
	if err := sup.Notify(trimmed, record); err != nil {
		return err
	}
	m.log("agent_ui_response id=%s req=%v", trimmed, record["id"])
	return nil
}

// List 列出所有子智能体（Agents 页 / 父 Agent 查状态用）。
func (m *Manager) List() (map[string]any, error) {
	return m.ListFor("")
}

// ListFor 列出**某条会话**名下的子智能体；空 parentID = 全部。
//
// 桥上的 delegation_list / delegation_stop 走的是按会话过滤的这条路：
// 一个会话的模型只该看到（只该能停）自己派出的子智能体。
func (m *Manager) ListFor(parentID string) (map[string]any, error) {
	sup := m.supervisorFor()
	list := sup.All()
	if strings.TrimSpace(parentID) != "" {
		filtered := make([]*agents.Instance, 0, len(list))
		for _, inst := range list {
			if inst.ParentID == parentID {
				filtered = append(filtered, inst)
			}
		}
		list = filtered
	}
	return map[string]any{
		"agents":  list,
		"running": sup.RunningCount(),
	}, nil
}

// AgentBelongsTo 判断一个子智能体归不归这条会话管（桥的跨会话隔离用）。
func (m *Manager) AgentBelongsTo(agentID, parentID string) bool {
	sup := m.SupervisorIfAny()
	if sup == nil {
		return false
	}
	inst, ok := sup.Get(agentID)
	if !ok {
		return false
	}
	return inst.ParentID == parentID
}

// Kill 停掉一个子智能体并记录原因。
//
// 原因不是装饰：停止可能来自四个地方（用户按钮 / 父会话停止 / 父会话中止 /
// 模型自己收工 / 孤儿守卫），事后回看"它为什么被停"只能靠这句话。
func (m *Manager) Kill(agentID, reason string) error {
	trimmed := strings.TrimSpace(agentID)
	if !agentIDRegexp.MatchString(trimmed) {
		return fmt.Errorf("子智能体 id 不合法: %q", agentID)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "已停止"
	}
	sup := m.supervisorFor()
	if err := sup.Kill(trimmed, reason); err != nil {
		return err
	}
	m.log("agent_killed id=%s reason=%s", trimmed, reason)
	return nil
}

// Policy 是"当前角色允许怎么派子智能体"的读取结果。
type Policy struct {
	Enabled      bool
	RequireAudit bool
	/** 主 Agent 给出候选 flag 时是否自动停掉子智能体（nil 视为 true） */
	StopOnFlag   bool
	DeliverOn    string
	AllowedRoles []string
	MaxChildren  int
	MaxParallel  int
	MaxDuration  time.Duration
	// Lifecycle 是子智能体形态（continuable / one-shot），空 = 取默认（可继续）。
	Lifecycle agents.Mode
	// IdleTimeout 是可继续子智能体的空闲回收时长（0 = 用 Supervisor 默认）。
	IdleTimeout time.Duration
}

// Policy 读出"派活的那条会话的角色允许怎么派子智能体"。
//
// 必须按 parentID 解析角色：一进程一会话之后，应用里同时有好几条会话，
// 各自可能用不同角色。读全局"当前角色"会把 A 会话的策略套到 B 会话的
// 委派请求上（放行不该放的、或拦住本该放的）。
func (m *Manager) Policy(parentID string) (Policy, error) {
	roleID := m.deps.RoleForParent(parentID)
	if roleID == "" {
		return Policy{}, errors.New("当前没有启用角色，无法派子智能体（先在输入框左下角选一个角色）")
	}
	view, ok := roles.Find(roleID)
	if !ok {
		return Policy{}, fmt.Errorf("角色不存在: %s", roleID)
	}
	if view.Role.Delegation == nil {
		return Policy{Enabled: false}, nil
	}
	config := view.Role.Delegation
	policy := Policy{
		Enabled:      config.Enabled,
		RequireAudit: config.RequireAudit,
		StopOnFlag:   config.StopOnFlag == nil || *config.StopOnFlag,
		DeliverOn:    config.DeliverOn,
		AllowedRoles: config.AllowedRoles,
		MaxChildren:  config.MaxChildren,
		MaxParallel:  config.MaxParallel,
		Lifecycle:    agents.ModeOneShot,
	}
	// 形态默认取**可继续**（仿 DSH 的体验）：派出去的子智能体能继续追问，
	// 而不是"一次问答、拿完结论进程就没了"。角色显式写 one-shot 才退回老行为。
	if !strings.EqualFold(strings.TrimSpace(config.Lifecycle), "one-shot") {
		policy.Lifecycle = agents.ModeContinuable
	}
	if config.MaxDurationSeconds > 0 {
		policy.MaxDuration = time.Duration(config.MaxDurationSeconds) * time.Second
	}
	if config.IdleTimeoutSeconds > 0 {
		policy.IdleTimeout = time.Duration(config.IdleTimeoutSeconds) * time.Second
	}
	return policy, nil
}

// deliverOnCheckpoint 返回"这条会话的父角色是否声明了 deliverOn: checkpoint"。
// ShouldHold 与投递方法选择共用这一个判据，免得两处各写一遍再悄悄漂移。
func (m *Manager) deliverOnCheckpoint(parentID string) bool {
	roleID := m.deps.RoleForParent(parentID)
	if roleID == "" {
		return false
	}
	view, ok := roles.Find(roleID)
	if !ok || view.Role.Delegation == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(view.Role.Delegation.DeliverOn), "checkpoint")
}

// shouldHold 是 Router 的"父忙时要不要等它回合结束"。
//
// 注意看的是**父角色**（派活的那条会话的角色）：deliverOn 是"这个角色允许怎么
// 派子智能体"的策略字段，子角色自己的 Delegation（通常是 nil）跟它无关——
// 以前读成 inst.RoleID（子角色），于是 checkpoint 永远触发不了。
func (m *Manager) shouldHold(inst *agents.Instance) bool {
	if inst == nil {
		return true
	}
	return !m.deliverOnCheckpoint(inst.ParentID)
}

// RequireAuditEvidence 是"先审计再委派"的**硬代理条件**。
//
// 说明白一点：Go 判定不了"审计完成"这个语义，只能判定代理条件——
// **这条会话**的父 Agent 至少已经动过工具（读过文件 / 跑过命令）。
// 按会话计数：A 会话动过手不能替 B 会话的委派放行。真正的语义约束在角色提示词里，
// 那是软的。这里不假装能理解语义，只挡住"一句话没说就派人"的情况。
func (m *Manager) RequireAuditEvidence(parentID string) error {
	m.mu.Lock()
	toolCalls := m.parentToolCalls[parentID]
	m.mu.Unlock()
	if toolCalls == 0 {
		return errors.New("还没有审计过题目/附件（本次会话里父 Agent 一次工具都没调用）。先看题目、读附件，再决定要不要派人。")
	}
	return nil
}

// NoteParentToolCall 由事件观察者调用：某条会话的父 Agent 每发起一次工具调用记一次。
func (m *Manager) NoteParentToolCall(parentID string) {
	if strings.TrimSpace(parentID) == "" {
		parentID = MainAgentID
	}
	m.mu.Lock()
	if m.parentToolCalls == nil {
		m.parentToolCalls = map[string]int{}
	}
	m.parentToolCalls[parentID]++
	m.mu.Unlock()
}

// ResetState 在新会话/新回合开始时清空审计证据与挂起结果。
func (m *Manager) ResetState() {
	m.mu.Lock()
	m.parentToolCalls = nil
	m.mu.Unlock()
}
