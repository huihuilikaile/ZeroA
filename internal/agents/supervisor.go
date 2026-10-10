// Package agents 管理"一个子智能体 = 一个独立 Pi 进程"。
//
// 为什么这一层在 Go 而不是 Pi 扩展里：Pi 的 RPC 没有 spawn 原语
// （rpc-commands.md 里只有 fork/clone 这类会话级操作），而我们要管的东西全在
// 进程外面——权限交集、预算、并发上限、单独 kill、UI 事件、审计日志。
// 把生命周期交给扩展，就等于把这些能力锁进那个进程里，GUI 再也插不上手。
//
// 第一版硬约束：depth ≤ 1（子智能体不能再派子智能体）。否则 A→B→C→D 会同时
// 炸掉成本、进程数与循环边界。
package agents

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"zeroa/internal/pi"
)

// Status 是子智能体的生命周期状态。
type Status string

const (
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	/**
	 * StatusIdle：进程活着、但此刻没有回合在执行（可继续子智能体一轮跑完之后的状态）。
	 *
	 * 这个状态是仿 DSH 的 `inactive` 加的，语义必须写清楚，否则界面一定把它误读成"停下来了"：
	 *   **idle 只描述"有没有轮次在跑"，不描述"任务成没成、失没失败"。**
	 * 一轮正常跑完是 idle，被中断一轮也是 idle；想表达结果请看 LastTurnCompleted / Err。
	 * 它的实际用途是"还能继续说话"——给 idle 的子智能体发消息会开新一轮，而不是重新起一个进程。
	 */
	StatusIdle      Status = "idle"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusKilled    Status = "killed"
)

// Mode 是子智能体的形态（对齐 DSH 的 one-shot / continuable）。
type Mode string

const (
	// ModeOneShot：跑完一轮就结算并释放进程，之后只读。默认形态，也是老行为。
	ModeOneShot Mode = "one-shot"
	// ModeContinuable：保留进程与会话，按顺序接受后续消息；可以只中断当前轮次而不销毁它。
	ModeContinuable Mode = "continuable"
)

// Delivery 是"给正在工作的子智能体发消息"的投递方式（对齐 DSH 的 Steer / Queue）。
type Delivery string

const (
	// DeliverySteer：在最近的检查点插进去（不打断已有推理，但会影响当前轮）。
	DeliverySteer Delivery = "steer"
	// DeliveryQueue：排队等当前回合彻底结束再生效。默认值：不打断。
	DeliveryQueue Delivery = "queue"
)

// Finished 判断是否已结束（终态）。
//
// 注意 idle **不是**终态：它还能继续干活（这正是可继续子智能体存在的意义）。
func (s Status) Finished() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusKilled:
		return true
	}
	return false
}

// Active 判断它此刻是否"活着"（含空闲驻留）。
func (s Status) Active() bool {
	return !s.Finished()
}

/**
 * TurnSettled 表示"它至少已经有一轮得出结论了"。
 *
 * 为什么要这个谓词：调用方（父级投递、界面、测试）真正关心的是"结论到了吗"，
 * 而在可继续形态下"结论到了"与"它结束了"是两件事——它可能还会继续跑好几轮。
 * 少了这层区分，就会写出"等它 Finished 才处理结果"的代码，于是可继续子智能体的
 * 结论永远投不出去（本项目里两个既有用例就是这么失败的）。
 */
func (i *Instance) TurnSettled() bool {
	if i.Status == StatusCompleted {
		return true
	}
	return i.Status == StatusIdle && i.LastTurnCompleted && strings.TrimSpace(i.Result) != ""
}

// Usage 是子智能体的用量（来自 Pi 的 usage 字段）。
type Usage struct {
	Turns       int   `json:"turns"`
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	TotalTokens int64 `json:"totalTokens"`
}

func (u *Usage) merge(other Usage) {
	// 每轮 usage 是"这一轮"的增量，累加才对；totalTokens 取最后一次的窗口占用
	u.Turns += other.Turns
	u.Input += other.Input
	u.Output += other.Output
	u.CacheRead += other.CacheRead
	if other.TotalTokens > 0 {
		u.TotalTokens = other.TotalTokens
	}
}

// Instance 是一个正在跑（或跑过）的子智能体。
type Instance struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parentId"`
	RoleID    string    `json:"roleId"`
	Task      string    `json:"task"`
	Status    Status    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`

	// Result 是归一化后的最终回复（模型可见的那一份，有硬上限）
	Result string `json:"result,omitempty"`
	// Err 是失败原因（进程起不来、超时、无输出…）
	Err string `json:"err,omitempty"`
	// Usage 是这个子智能体花了多少
	Usage Usage `json:"usage"`
	// ExitCode 进程退出码（-1 = 还没退出）。失败排查时第一个要看的东西。
	ExitCode int `json:"exitCode"`
	// SessionDir 是它的会话目录：完整对话在那里，可查看，但不回流父上下文
	SessionDir string `json:"sessionDir"`
	// Phase 是给界面看的一行当前动作（例如 "bash: file challenge.exe"）
	Phase string `json:"phase,omitempty"`

	/**
	 * Mode 决定它是一次性的还是可继续的（对齐 DSH 的 one-shot / continuable）。
	 *
	 * 一次性：跑完一轮就结算、进程释放，之后只读——不能再发消息。
	 * 可继续：进程与会话都留着，空闲时可以继续发消息开新一轮，也可以只中断当前轮次。
	 */
	Mode Mode `json:"mode"`
	// Name 是给人看、也用来寻址的短名（委派侧生成，保证同父下不重名）。
	Name string `json:"name,omitempty"`

	/**
	 * LastTurnCompleted 记录"最近一个**已结束**的轮次是不是正常跑完的"。
	 *
	 * 为什么单独立一个布尔，而不是多一个状态枚举值（照 DSH 的做法）：
	 * "活动态"（在跑 / 空闲）与"结果"（成没成）是两件事，混进同一个枚举必然出现
	 * "失败被显示成完成"或"跑完了被显示成还在忙"。新轮次开始时它会先清成 false，
	 * 直到这一轮真的以 completed 收尾才置真。
	 */
	LastTurnCompleted bool `json:"lastTurnCompleted"`
	// Turns 已正常结束的轮次数（可继续子智能体可以跑很多轮）。
	Turns int `json:"turns"`
	// IdleSince 是进入空闲的时刻（零值 = 不在空闲）。
	IdleSince time.Time `json:"idleSince,omitempty"`

	// rt 放"不能随快照复制"的东西（锁、通道、cancel）。
	//
	// 为什么要拆出来：Instance 会被大量复制成快照（给界面、给结果路由），
	// 而 sync.Once / channel 一旦被复制就是 bug——go vet 的 copylocks 会直接
	// 拦下来（它当时确实拦住了）。拆成指针之后，快照只带数据不带运行时。
	rt *instanceRuntime
}

// instanceRuntime 是实例的运行时状态（只有真正跑着的那个对象持有）。
type instanceRuntime struct {
	cancel context.CancelFunc
	done   chan struct{}
	closed sync.Once
	events chan pi.Trace

	/**
	 * client 是子进程的 RPC 句柄，给"继续说话"用（Send / Interrupt）。
	 *
	 * 为什么要存下来：可继续子智能体的消息不是从 watch 循环里发出的——
	 * 用户或父模型可以在任意时刻发消息，而那一刻只有这个句柄能把它送进子进程。
	 */
	clientMu sync.Mutex
	client   *pi.Client
	// msgSeq 给受理凭证编号（照 DSH：send_message 只返回 messageId，不返回回复）
	msgSeq int
	/**
	 * turnSettled 在"这一轮得出结论"（可继续形态的 idle，或终态）时被关闭。
	 *
	 * 用途是 WaitTurn：调用方真正想等的是"结论到了吗"，而不是"进程没了吗"——
	 * 在可继续形态下后者可能要等到空闲回收（好几分钟）。
	 * 新的一轮开始时它会被换成新通道（见 beginTurn）。
	 */
	turnSettled chan struct{}

	// stderr 尾巴：子进程失败时这是**唯一**能说明原因的东西。
	//
	// 官方示例就在这里翻过车（issue #8129）：它把子进程的 error 事件丢掉，
	// 结果工具只回一句 "Agent error: (no output)"，真正的原因（模块找不到、
	// 参数不认、鉴权失败）全被吞了，排查时只能靠猜。
	stderrMu   sync.Mutex
	stderrTail []string
}

const maxStderrLines = 20

func (i *Instance) recordStderr(line string) {
	if i.rt == nil || strings.TrimSpace(line) == "" {
		return
	}
	i.rt.stderrMu.Lock()
	defer i.rt.stderrMu.Unlock()
	i.rt.stderrTail = append(i.rt.stderrTail, strings.TrimSpace(line))
	if len(i.rt.stderrTail) > maxStderrLines {
		i.rt.stderrTail = i.rt.stderrTail[len(i.rt.stderrTail)-maxStderrLines:]
	}
}

// stderrSummary 把 stderr 尾巴拼成一行（最多 500 字），给失败原因用。
func (i *Instance) stderrSummary() string {
	if i.rt == nil {
		return ""
	}
	i.rt.stderrMu.Lock()
	defer i.rt.stderrMu.Unlock()
	if len(i.rt.stderrTail) == 0 {
		return ""
	}
	joined := strings.Join(i.rt.stderrTail, " | ")
	if len(joined) > 500 {
		joined = joined[:500] + "…"
	}
	return joined
}

// Done 在子智能体结束时关闭。
func (i *Instance) Done() <-chan struct{} {
	if i.rt == nil {
		return nil
	}
	return i.rt.done
}

// snapshot 复制一份给外部用（不带运行时）。
func (i *Instance) snapshot() *Instance {
	copy := *i
	copy.rt = nil
	return &copy
}

// SpawnRequest 是"派一个子智能体"的请求。
type SpawnRequest struct {
	ParentID string
	RoleID   string
	Task     string
	// ParentDepth 是发起者的深度：0 = 主智能体。用于执行 depth ≤ 1。
	ParentDepth int
	// MaxDuration 为零表示不限时（由上层预算决定）
	MaxDuration time.Duration
	// AllowedRoles 非空时只允许派这些角色（角色策略 ∩ 会话授权）
	AllowedRoles []string
	/**
	 * MaxChildren / MaxParallel 是**这次派生的父级**自带的预算（来自角色策略）。
	 *
	 * 零值 = 用 Supervisor 的全局默认。为什么按请求带而不是只认全局默认：
	 * 角色的 delegation.maxChildren/maxParallel 写进了给模型看的提示词
	 * （"最多派 N 个"），如果执行层只认全局默认，模型按角色预算行动、
	 * Go 按另一套数字拦截——"没超预算却被拒 / 超了预算没人管"。
	 */
	MaxChildren int
	MaxParallel int
	// Mode 为空时按一次性处理（保持老行为）。
	Mode Mode
	// Name 是寻址用的短名；为空时用 agent-<n>。
	Name string
	/**
	 * IdleTimeout 是可继续子智能体"空闲多久后被回收"，零值取默认。
	 *
	 * 这是我们对 DSH 的一处**有意偏离**：DSH 的驻留子智能体没有超时（它同进程、几乎不花钱），
	 * 而我们每个子智能体都是一个独立 Pi 进程（~100MB 级）。没有回收就等于"每聊过一个子智能体
	 * 就永久多一个进程"，所以给一个默认 10 分钟的空闲回收——回收后状态是 completed（会话目录仍在，
	 * 冷恢复是后续工作）。
	 */
	IdleTimeout time.Duration
	// Idle 标记这是"从空闲被唤醒的一轮"（内部用，避免重复占用容量）。
	Idle bool
}

// Launcher 负责"真的把进程起起来"。抽出来是为了测试能注入假进程。
//
// onTrace 交给 pi.Start：它拿到子进程的每一行输出。
type Launcher func(ctx context.Context, inst *Instance, onTrace func(pi.Trace)) (*pi.Client, error)

// TraceSink 把子进程的事件抛给上层（界面 / 原始事件日志）。必须立刻返回。
type TraceSink func(agentID string, trace pi.Trace)

// ResultSink 在子智能体结束时被调用（终态 + 结果）。
//
// 由上层决定"怎么把结果送回父会话"——合并、择时、格式化都是上层的事，
// Supervisor 只保证结束时一定调一次。
type ResultSink func(inst *Instance)

/**
 * TurnSink 在**一轮正常结束**时被调用（可继续子智能体的阶段成果）。
 *
 * 为什么与 ResultSink 分开：可继续子智能体可能跑十轮、并且长时间空闲驻留。
 * 只在"终结"时通知父级，父级会以为它还在干活、却半天收不到任何结论；
 * 每一轮结束都送一次阶段结果，父级才跟得上（对应 DSH 的 settlement notice）。
 */
type TurnSink func(inst *Instance)

// Options 是 Supervisor 的配置。
type Options struct {
	// MaxParallel 一个父级同时运行的子智能体上限（默认 2；可被 SpawnRequest 的角色预算覆盖）
	MaxParallel int
	// MaxChildren 一个父智能体累计能派出的子智能体数（默认 4；可被 SpawnRequest 的角色预算覆盖）
	MaxChildren int
	// MaxDepth 允许的最大深度（默认 1，即"子不能再派子"）
	MaxDepth int
	/**
	 * MaxContinuable 是**可继续**子智能体的存活上限（默认 8，照 DSH 的 maxActiveSubagents）。
	 *
	 * 为什么单独一个上限：可继续子智能体是**驻留**的（空闲也占着进程），
	 * 而 MaxParallel 只管"同时在跑的回合数"——空闲的驻留实例不占它。
	 * 没有这个上限，用户挨个聊过去的每个子智能体都会留一个进程。
	 *
	 * 超限时的行为照 DSH：**直接拒绝，不排队**。
	 * 理由也一样：占用者可能正是"正在等子智能体结果"的父级，排队会变成自己等自己。
	 */
	MaxContinuable int
	// OnChange 状态变化回调（界面刷新用）。必须立刻返回。
	OnChange func(inst *Instance)
}

// Supervisor 是所有子智能体的登记处与执行器。
type Supervisor struct {
	mu        sync.Mutex
	instances map[string]*Instance
	order     []string
	seq       int
	options   Options
	launch    Launcher
	sink      TraceSink
	results   ResultSink
	turnSink  TurnSink
	prompt    func(ctx context.Context, client *pi.Client, inst *Instance) error
	// deliver 把一条文本按 kind（prompt/steer/follow_up）投给子进程，供"继续说话"用。
	deliver DeliverFunc
}

// DeliverFunc 是"把一条文本投进子进程"的动作。
//
// kind 直接用 Pi 的 RPC 命令名：
//   - prompt    ：开一轮新的（子进程空闲时用）
//   - steer     ：插到最近的检查点（模型发来的消息走它，照 DSH"模型一律 Steer"）
//   - follow_up ：排队等当前回合彻底结束（人发来的消息默认走它，不打断）
type DeliverFunc func(ctx context.Context, client *pi.Client, inst *Instance, kind, text string) error

// SetDeliver 挂上投递实现（由 delegate 侧提供，内部就是一个 RPC 请求）。
func (s *Supervisor) SetDeliver(fn DeliverFunc) { s.deliver = fn }

// maxResultBytes 是模型可见结果的硬上限。
//
// 子智能体可能跑 20 轮、几万 token；把整段塞回父上下文会把父的窗口撑爆，
// 而这正是"多智能体"最常见的翻车方式。超出的部分留在子会话文件里，
// 结果末尾写明去哪里看全文。
const maxResultBytes = 8 << 10

// NewSupervisor 建一个 Supervisor。
func NewSupervisor(options Options, launch Launcher) *Supervisor {
	if options.MaxParallel <= 0 {
		options.MaxParallel = 2
	}
	if options.MaxChildren <= 0 {
		options.MaxChildren = 4
	}
	if options.MaxDepth <= 0 {
		options.MaxDepth = 1
	}
	if options.MaxContinuable <= 0 {
		options.MaxContinuable = 8
	}
	return &Supervisor{
		instances: map[string]*Instance{},
		options:   options,
		launch:    launch,
	}
}

// SetTraceSink 挂上事件出口（界面 / 原始日志）。
func (s *Supervisor) SetTraceSink(sink TraceSink) { s.sink = sink }

// SetResultSink 挂上"结果该送去哪"的处理。
func (s *Supervisor) SetResultSink(sink ResultSink) { s.results = sink }

// SetTurnSink 挂上"每一轮结束后阶段成果该送去哪"的处理（可继续子智能体）。
func (s *Supervisor) SetTurnSink(sink TurnSink) { s.turnSink = sink }

// SetPrompt 挂上"怎么把任务发给子进程"（通常是 RPC prompt）。
func (s *Supervisor) SetPrompt(send func(ctx context.Context, client *pi.Client, inst *Instance) error) {
	s.prompt = send
}

var (
	// ErrDepthExceeded：子智能体不能再派子智能体
	ErrDepthExceeded = errors.New("子智能体不能再派子智能体（第一版只允许一层）")
	// ErrTooManyChildren：这个父智能体派得太多了
	ErrTooManyChildren = errors.New("这个会话派出的子智能体已达上限")
	// ErrTooManyParallel：同时跑的子智能体太多了
	ErrTooManyParallel = errors.New("同时运行的子智能体已达上限")
	// ErrTooManyContinuable：驻留的可继续子智能体太多了（满了直接拒绝，不排队）
	ErrTooManyContinuable = errors.New("可继续子智能体已达上限")
	// ErrNotContinuable：一次性子智能体不能再收消息
	ErrNotContinuable = errors.New("这个子智能体是一次性的，不能继续发消息（派它的时候用 continuable 形态才能继续对话）")
	// ErrRoleNotAllowed：角色不在允许清单里
	ErrRoleNotAllowed = errors.New("这个角色不允许派出该子智能体角色")
)

// Spawn 派一个子智能体。它**立刻返回**——父智能体不等它做完；
// 结果由 ResultSink 在结束时投递回父会话。
func (s *Supervisor) Spawn(req SpawnRequest) (*Instance, error) {
	if req.ParentDepth >= s.options.MaxDepth {
		return nil, ErrDepthExceeded
	}
	if strings.TrimSpace(req.RoleID) == "" {
		return nil, errors.New("缺少子角色")
	}
	if strings.TrimSpace(req.Task) == "" {
		return nil, errors.New("缺少任务描述")
	}
	if len(req.AllowedRoles) > 0 && !containsString(req.AllowedRoles, req.RoleID) {
		return nil, fmt.Errorf("%w：%s（允许：%s）", ErrRoleNotAllowed, req.RoleID, strings.Join(req.AllowedRoles, "、"))
	}

	s.mu.Lock()
	// 预算优先用这次派生自带的（角色策略），零值退回 Supervisor 默认。
	// 两个口径都按**这个父级**计：A 会话派了几个、在跑几个，与 B 会话无关。
	maxChildren := s.options.MaxChildren
	if req.MaxChildren > 0 {
		maxChildren = req.MaxChildren
	}
	if s.countLocked(req.ParentID) >= maxChildren {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w（这个会话已派 %d 个，上限 %d）",
			ErrTooManyChildren, s.countLocked(req.ParentID), maxChildren)
	}
	maxParallel := s.options.MaxParallel
	if req.MaxParallel > 0 {
		maxParallel = req.MaxParallel
	}
	if s.runningChildrenLocked(req.ParentID) >= maxParallel {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w（这个会话有 %d 个在跑，上限 %d）",
			ErrTooManyParallel, s.runningChildrenLocked(req.ParentID), maxParallel)
	}
	/**
	 * 可继续子智能体的驻留上限：超了直接拒绝，不排队（照 DSH）。
	 *
	 * 排队在这里是**有害**的：占着名额的很可能正是"派了活正在等结果"的那个父级，
	 * 让它的新请求排队 = 父级等子级、子级等父级空出名额，谁也不动。
	 */
	if req.Mode == ModeContinuable && s.continuableLocked() >= s.options.MaxContinuable {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w（可继续子智能体上限 %d：先结束/关闭一个，或等它空闲回收）",
			ErrTooManyContinuable, s.options.MaxContinuable)
	}
	s.seq++
	if req.Mode == "" {
		req.Mode = ModeOneShot
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		// 默认短名带上角色：界面上"ctf-recon-2"比"agent-2"有用得多（照 DSH 的具名子智能体）。
		name = fmt.Sprintf("%s-%d", req.RoleID, s.seq)
	}
	inst := &Instance{
		ID:        fmt.Sprintf("agent-%d", s.seq),
		ParentID:  req.ParentID,
		RoleID:    req.RoleID,
		Task:      strings.TrimSpace(req.Task),
		Status:    StatusStarting,
		Mode:      req.Mode,
		Name:      name,
		StartedAt: time.Now(),
		ExitCode:  -1,
		rt:        &instanceRuntime{done: make(chan struct{}), events: make(chan pi.Trace, 512)},
	}
	s.instances[inst.ID] = inst
	s.order = append(s.order, inst.ID)
	s.mu.Unlock()

	go s.run(inst, req)
	s.notify(inst)
	return inst, nil
}

func (s *Supervisor) run(inst *Instance, req SpawnRequest) {
	defer func() {
		inst.rt.closed.Do(func() { close(inst.rt.done) })
		if inst.EndedAt.IsZero() {
			inst.EndedAt = time.Now()
		}
		if !inst.Status.Finished() {
			inst.Status = StatusFailed
			if inst.Err == "" {
				inst.Err = "子智能体异常结束"
			}
		}
		s.notify(inst)
		/**
		 * 终态结果只投一次，而且**不重复投**已经投过的最后一轮。
		 *
		 * 可继续子智能体在空闲回收（或被杀）时进终态，而它最后一轮的结论
		 * 早在 markIdle 时就通过 TurnSink 投给父级了；这里再投一遍，
		 * 父会话里就会出现两条一模一样的"子智能体结果"。真正还有信息量的是
		 * "它是被停的 / 是失败"的那些情况——那些照投不误。
		 */
		alreadyDelivered := inst.Mode == ModeContinuable && inst.Status == StatusCompleted && inst.LastTurnCompleted
		if s.results != nil && !alreadyDelivered {
			if snapshot, ok := s.Get(inst.ID); ok {
				s.results(snapshot)
			}
		}
	}()

	ctx := context.Background()
	if req.MaxDuration > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, req.MaxDuration)
		defer timeoutCancel()
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	/**
	 * rt.cancel 要在锁里写：Kill() 会在另一个 goroutine 上读它（用户点停止、
	 * 父会话中止都可能发生在任意时刻）。裸赋值就是数据竞争（-race 抓到过）。
	 */
	s.mu.Lock()
	inst.rt.cancel = cancel
	s.mu.Unlock()

	client, err := s.launch(ctx, inst, func(trace pi.Trace) {
		if trace.Direction == "stderr" {
			inst.recordStderr(trace.Raw)
		}
		if s.sink != nil {
			s.sink(inst.ID, trace)
		}
		select {
		case inst.rt.events <- trace:
		default:
			// 观察队列满了：宁可丢事件也不能阻塞读取 goroutine（那会卡住整个子进程）
		}
	})
	if err != nil {
		inst.Status = StatusFailed
		inst.Err = "启动子智能体失败：" + err.Error()
		return
	}
	/**
	 * 记下句柄：可继续子智能体的后续消息（Send / Interrupt）要靠它送进去。
	 *
	 * 消息不是从 watch 循环里发出的——用户或父模型可以在任意时刻说话，
	 * 而那一刻唯一的通道就是这个 client。
	 */
	inst.rt.clientMu.Lock()
	inst.rt.client = client
	inst.rt.clientMu.Unlock()

	s.beginTurn(inst)

	// 起进程 ≠ 开始干活：任务是一条 prompt。
	if s.prompt != nil {
		if err := s.prompt(ctx, client, inst); err != nil {
			s.finish(inst, StatusFailed, "发送任务失败："+err.Error(), "", Usage{})
			_ = client.Close()
			return
		}
	}

	s.watch(inst, client, req)
}

// defaultIdleTimeout 是"可继续子智能体空闲多久后被回收"。
//
// 与 DSH 的一处有意偏离：DSH 的子智能体同进程驻留、几乎不占资源，所以它不设超时；
// 我们每个子智能体是一个独立 Pi 进程（百 MB 级），不回收就等于"每聊过一个就永久多一个进程"。
const defaultIdleTimeout = 10 * time.Minute

/**
 * watch 消费子进程的输出，直到它结束。
 *
 * 结果抽取只用两个信号：assistant 的 message_end（拿最终文本）与 agent_settled
 * （确认这一轮真的结束）。工具调用细节不在这里解析——那是界面的事，
 * 已经通过 sink 原样抛给上层了。
 *
 * **可继续形态的关键差别**：`agent_settled` 不再等于"结束"，只等于"这一轮结束了"。
 * 循环继续跑，等三种事情之一：
 *   - 又有消息进来（Send 已经把它变成一轮新的 prompt）→ 继续消费事件；
 *   - 空闲超过 IdleTimeout → 结算并释放进程；
 *   - 被 Kill / 父会话停止 / 超时 → 结算。
 * 一次性形态保持老行为：settled 即结算、随即释放进程。
 */
func (s *Supervisor) watch(inst *Instance, client *pi.Client, req SpawnRequest) {
	defer func() { _ = client.Close() }()

	final := ""
	usage := Usage{}
	// 这一轮是不是被中断的（aborted）。它决定"最近一个已结束的轮次是否正常完成"。
	// 不区分的话，中断一轮会被显示成绿点"完成"——那正是 DSH 用 lastTurnCompleted 单独表达的东西。
	turnAborted := false

	var deadline <-chan time.Time
	if req.MaxDuration > 0 {
		timer := time.NewTimer(req.MaxDuration)
		defer timer.Stop()
		deadline = timer.C
	}

	idleTimeout := req.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTimeout
	}
	idleTimer := time.NewTimer(idleTimeout)
	if !idleTimer.Stop() {
		<-idleTimer.C
	}
	var idle <-chan time.Time

	for {
		select {
		case trace := <-inst.rt.events:
			if trace.Direction != "stdout" {
				continue
			}
			event, ok := DecodeEvent(trace.Raw)
			if !ok {
				continue
			}
			switch event.Type {
			case "message_end":
				if text, isAssistant := event.AssistantText(); isAssistant {
					final = text
					usage.Turns++
					if event.Aborted() {
						turnAborted = true
					}
				}
				usage.merge(event.Usage)
			case "tool_start":
				inst.Phase = event.ToolPhase()
				s.notify(inst)
			case "agent_start":
				// 新一轮开始：清掉上一次的"完成"标记，撤掉空闲回收
				turnAborted = false
				s.beginTurn(inst)
				if idle != nil {
					stopTimer(idleTimer)
					idle = nil
				}
			case "agent_settled":
				if inst.Mode != ModeContinuable {
					s.finish(inst, StatusCompleted, "", final, usage)
					return
				}
				/**
				 * 可继续：这一轮结束 ≠ 这个子智能体结束。
				 * 记成"空闲 + 最近一轮是否正常完成"，然后等下一轮（或空闲回收）。
				 */
				s.markIdle(inst, final, usage, !turnAborted)
				stopTimer(idleTimer)
				idleTimer.Reset(idleTimeout)
				idle = idleTimer.C
			}

		case <-client.Done():
			// 进程自己退了（正常流程是先 agent_settled 再退）
			if !inst.Status.Finished() {
				if strings.TrimSpace(final) != "" {
					s.finish(inst, StatusCompleted, "", final, usage)
				} else {
					s.finish(inst, StatusFailed, describeSilentExit(inst), "", usage)
				}
			}
			return

		case <-deadline:
			s.finish(inst, StatusCancelled, fmt.Sprintf("超过时限（%s）已停止", req.MaxDuration), final, usage)
			return

		case <-idle:
			// 空闲太久：回收进程（会话目录仍在，冷恢复是后续工作）。
			s.finish(inst, StatusCompleted, "", inst.Result, usage)
			return

		case <-inst.rt.done:
			return
		}
	}
}

// stopTimer 停掉一个定时器并清空可能已就绪的通道（避免误触发一次回收）。
func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

// beginTurn 把实例标成"正在跑一轮"。
//
// 顺手清掉 LastTurnCompleted：新的一轮开始后，"上一轮完成了"就不再是当前事实，
// 界面上的绿点必须立刻变回活动态（否则用户会看到"正在跑"和"已完成"同时为真）。
func (s *Supervisor) beginTurn(inst *Instance) {
	s.mu.Lock()
	if inst.Status.Finished() {
		s.mu.Unlock()
		return
	}
	inst.Status = StatusRunning
	inst.LastTurnCompleted = false
	inst.IdleSince = time.Time{}
	/**
	 * 注意：这里**不**动 turnSettled。
	 *
	 * 一轮开始时它本来就是 nil（上一轮的通道已经在 markIdle/finish 里关掉并置空了）。
	 * 如果在这里再置 nil，会把"等这一轮的 WaitTurn 刚建好的通道"丢掉——
	 * 等待者于是永远收不到通知，只能等到空闲回收或超时（这个 bug 被
	 * TestContinuableStaysIdleAndAcceptsFollowUp 抓到：等到的是 completed 而不是 idle）。
	 */
	s.mu.Unlock()
	s.notify(inst)
}

/**
 * WaitTurn 等到"它有一轮得出结论"（或超时 / 上下文取消 / 进程终结）。
 *
 * 与 Wait 的分工：
 *   - Wait   等**进程结束**（终态）——一次性形态的语义；
 *   - WaitTurn 等**一轮的结论**——可继续形态下这才是调用方要的东西，
 *     否则一次追问就要等它空闲回收（默认 5 分钟）才拿到早就出来的结论。
 */
func (s *Supervisor) WaitTurn(ctx context.Context, id string, timeout time.Duration) (*Instance, bool) {
	s.mu.Lock()
	inst, ok := s.instances[id]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	if snap, ok := s.Get(id); ok && snap.TurnSettled() {
		return snap, true
	}

	s.mu.Lock()
	if inst.rt.turnSettled == nil {
		inst.rt.turnSettled = make(chan struct{})
	}
	settled := inst.rt.turnSettled
	s.mu.Unlock()

	// 建通道期间可能刚好就绪了，再确认一次（避免白等一整轮）
	if snap, ok := s.Get(id); ok && snap.TurnSettled() {
		return snap, true
	}

	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-settled:
		return s.Get(id)
	case <-inst.rt.done:
		return s.Get(id)
	case <-expired:
		return s.Get(id)
	case <-ctx.Done():
		return s.Get(id)
	}
}

// markIdle 把可继续子智能体标成"空闲"（进程还在，等下一轮）。
func (s *Supervisor) markIdle(inst *Instance, final string, usage Usage, completed bool) {
	s.mu.Lock()
	if inst.Status.Finished() {
		s.mu.Unlock()
		return
	}
	inst.Status = StatusIdle
	inst.IdleSince = time.Now()
	inst.LastTurnCompleted = completed
	inst.Usage = usage
	if completed {
		inst.Turns++
		inst.Result = truncateResult(final, inst)
	}
	inst.Phase = ""
	// 唤醒 WaitTurn 的等待者：这一轮有结论了
	settled := inst.rt.turnSettled
	inst.rt.turnSettled = nil
	s.mu.Unlock()
	if settled != nil {
		close(settled)
	}
	s.notify(inst)
	/**
	 * 一轮正常结束 → 送一次阶段成果给父级。
	 * 中断的那一轮不算（completed=false）：那是用户主动停的，把半截内容当结论投过去更容易误导。
	 */
	if completed && s.turnSink != nil {
		if snapshot, ok := s.Get(inst.ID); ok {
			s.turnSink(snapshot)
		}
	}
}

/**
 * Send 给一个可继续子智能体投递一条消息（对齐 DSH 的 send_message）。
 *
 * 语义（照 DSH，逐条对应）：
 *   - **正在工作** → 按 delivery 投递：`steer` 赶最近的检查点，`queue`（默认）排队等这一轮结束；
 *   - **空闲** → 直接开新一轮，进程与会话都还是原来那个（这就是"可继续"的意义）；
 *   - **一次性 / 已结束** → 拒收；
 *   - 返回值只是**受理凭证**（messageId），**不含回复**。回复要等它自己那轮跑完，
 *     在它自己的会话里出现——照 DSH 的说法："回复是另一条显式指定地址的消息，不是本次调用的结果"。
 */
func (s *Supervisor) Send(id, text string, delivery Delivery) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("消息不能为空")
	}
	if delivery == "" {
		delivery = DeliveryQueue
	}

	s.mu.Lock()
	inst, ok := s.instances[id]
	if !ok {
		s.mu.Unlock()
		return "", fmt.Errorf("没有这个子智能体: %s", id)
	}
	if inst.Status.Finished() {
		status := inst.Status
		s.mu.Unlock()
		return "", fmt.Errorf("子智能体 %s 已结束（%s），不能再发消息", id, status)
	}
	if inst.Mode != ModeContinuable {
		s.mu.Unlock()
		return "", ErrNotContinuable
	}
	wasIdle := inst.Status == StatusIdle
	inst.rt.msgSeq++
	msgID := fmt.Sprintf("%s-msg-%d", id, inst.rt.msgSeq)
	inst.rt.clientMu.Lock()
	client := inst.rt.client
	inst.rt.clientMu.Unlock()
	if wasIdle {
		// 乐观置成"在跑"：事件流（agent_start）随后会把它坐实；
		// 投递失败时下面会回滚成空闲，免得界面永远显示一个不会动的"运行中"。
		inst.Status = StatusRunning
		inst.LastTurnCompleted = false
		inst.IdleSince = time.Time{}
	}
	s.mu.Unlock()
	s.notify(inst)

	if client == nil {
		s.rollbackSend(inst, wasIdle)
		return "", fmt.Errorf("子智能体 %s 的进程句柄不可用（可能刚退出）", id)
	}
	if s.deliver == nil {
		s.rollbackSend(inst, wasIdle)
		return "", errors.New("没有配置投递实现（Supervisor.SetDeliver）")
	}

	kind := "follow_up"
	switch {
	case wasIdle:
		kind = "prompt"
	case delivery == DeliverySteer:
		kind = "steer"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.deliver(ctx, client, inst, kind, text); err != nil {
		s.rollbackSend(inst, wasIdle)
		return "", fmt.Errorf("投递失败（%s）：%w", kind, err)
	}
	return msgID, nil
}

// rollbackSend 把"乐观置成运行中"回滚成空闲（投递失败时用）。
func (s *Supervisor) rollbackSend(inst *Instance, wasIdle bool) {
	if !wasIdle {
		return
	}
	s.mu.Lock()
	if !inst.Status.Finished() && inst.Status == StatusRunning {
		inst.Status = StatusIdle
		inst.IdleSince = time.Now()
	}
	s.mu.Unlock()
	s.notify(inst)
}

// Notify 向某个子智能体的进程发一条不登记、不等响应的原始记录。
//
// 用途：替界面应答子智能体的 extension_ui_request（子智能体是独立 Pi 进程，
// 应答必须发到它自己的 stdin，不能发去父会话）。record 的合法性由调用方
// （bridge 层）把关，这里只负责找到进程句柄并投递。
func (s *Supervisor) Notify(id string, record map[string]any) error {
	s.mu.Lock()
	inst, ok := s.instances[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("没有这个子智能体: %s", id)
	}
	if inst.Status.Finished() {
		s.mu.Unlock()
		return fmt.Errorf("子智能体 %s 已结束（%s），不能再应答", id, inst.Status)
	}
	inst.rt.clientMu.Lock()
	client := inst.rt.client
	inst.rt.clientMu.Unlock()
	s.mu.Unlock()

	if client == nil {
		return fmt.Errorf("子智能体 %s 的进程句柄不可用（可能刚退出）", id)
	}
	return client.Notify(record)
}

/**
 * Interrupt 只中断目标**当前这一轮**，不销毁它（对齐 DSH 的 interrupt_agent）。
 *
 * 三条语义都照 DSH：
 *   - 队列与下属都保留：已经排进去的消息仍在，下一轮照样跑；
 *   - 调用在"停止请求被接受"后立即返回，**不等待**它真的停稳；
 *   - 对空闲或已结束的目标是**空操作，但算受理**（用户看到"已请求中断"，不会报错）。
 */
func (s *Supervisor) Interrupt(id string) error {
	s.mu.Lock()
	inst, ok := s.instances[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("没有这个子智能体: %s", id)
	}
	if inst.Status.Finished() || inst.Status == StatusIdle {
		s.mu.Unlock()
		return nil
	}
	inst.rt.clientMu.Lock()
	client := inst.rt.client
	inst.rt.clientMu.Unlock()
	s.mu.Unlock()

	if client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Request(ctx, "abort", nil); err != nil {
		return fmt.Errorf("中断请求失败：%w", err)
	}
	return nil
}

// finish 落地终态（只写一次）。
// describeSilentExit 解释"进程退了但一句话没说"。
//
// 优先带上 stderr 尾巴——那通常就是真正的原因（找不到模块、参数不认、鉴权失败）。
// 没有 stderr 时才退回到那句含糊但诚实的描述。
func describeSilentExit(inst *Instance) string {
	if tail := inst.stderrSummary(); tail != "" {
		return "子智能体进程退出但没有产生任何输出；stderr：" + tail
	}
	return "子智能体进程退出但没有产生任何输出"
}

func (s *Supervisor) finish(inst *Instance, status Status, errText, result string, usage Usage) {
	s.mu.Lock()
	if inst.Status.Finished() {
		s.mu.Unlock()
		return
	}
	inst.Status = status
	inst.Err = errText
	inst.Usage = usage
	inst.EndedAt = time.Now()
	if status == StatusCompleted {
		inst.Result = truncateResult(result, inst)
	}
	// 终态也是"这一轮有结论了"：唤醒 WaitTurn 的等待者，别让它等到超时。
	settled := inst.rt.turnSettled
	inst.rt.turnSettled = nil
	s.mu.Unlock()
	if settled != nil {
		close(settled)
	}
	s.notify(inst)
}

func truncateResult(text string, inst *Instance) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "（子智能体没有给出文本结果；完整过程见它的会话目录：" + inst.SessionDir + "）"
	}
	if len(trimmed) <= maxResultBytes {
		return trimmed
	}
	// 超限时保留**结尾**：结论通常在最后，而且明确告诉父去哪里看全文
	tail := trimmed[len(trimmed)-maxResultBytes:]
	return "…（结果过长已截断，完整内容见子智能体会话：" + inst.SessionDir + "）\n\n" + tail
}

// Kill 停掉一个子智能体（取消上下文 + 关掉进程）。
func (s *Supervisor) Kill(id, reason string) error {
	s.mu.Lock()
	inst, ok := s.instances[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("没有这个子智能体: %s", id)
	}
	cancel := inst.rt.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	inst.rt.closed.Do(func() { close(inst.rt.done) })
	s.finish(inst, StatusKilled, reason, "", inst.Usage)
	return nil
}

// SetSessionDir 由 Launcher 在起进程时调用：登记这个子智能体的会话目录。
//
// 为什么必须有这么一个 setter，而不是让 Launcher 直接写 inst.SessionDir：
// Launcher 跑在**另一个 goroutine** 上（Supervisor.Spawn 起的那个），
// 而界面随时可能在另一个 goroutine 上取快照（All/Get → snapshot 读这个字段）。
// 直接赋值就是一个数据竞争——`go test -race` 会直接抓到（实测抓到过）。
func (s *Supervisor) SetSessionDir(id, dir string) {
	s.mu.Lock()
	if inst, ok := s.instances[id]; ok {
		inst.SessionDir = dir
	}
	s.mu.Unlock()
}
func (s *Supervisor) CancelChildren(parentID, reason string) int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if inst := s.instances[id]; inst != nil && inst.ParentID == parentID && !inst.Status.Finished() {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.Kill(id, reason)
	}
	return len(ids)
}

/**
 * CancelAll 停掉**所有**子智能体，不管它属于哪条会话。
 *
 * 用于整个应用要停下来的时候（关窗、授权失效、手动停止 Pi）：
 * 那时候没有"别的会话还在用"这回事，剩下的进程必须全部收掉。
 * 漏掉的子智能体不会再有人收它的结果（父进程都没了），只会白烧 token。
 */
func (s *Supervisor) CancelAll(reason string) int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if inst := s.instances[id]; inst != nil && !inst.Status.Finished() {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.Kill(id, reason)
	}
	return len(ids)
}

// Wait 等一个子智能体结束（或超时），返回它的终态快照。
//
// 为什么需要这个 API：快照刻意不携带运行时（见 Instance.rt 的注释），
// 所以外部**拿不到** Done() 通道。等结果必须走这里，否则调用方只能轮询，
// 而轮询在"子智能体要跑十分钟"的场景里既笨又费。
func (s *Supervisor) Wait(ctx context.Context, id string, timeout time.Duration) (*Instance, bool) {
	s.mu.Lock()
	inst, ok := s.instances[id]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-inst.rt.done:
		return inst.snapshot(), true
	case <-ctx.Done():
		return inst.snapshot(), true
	case <-expired:
		return inst.snapshot(), true
	}
}

// Get 取一个实例的快照。
func (s *Supervisor) Get(id string) (*Instance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[id]
	if !ok {
		return nil, false
	}
	return inst.snapshot(), true
}

// Children 列出某个父智能体名下的子智能体（按派出顺序）。
func (s *Supervisor) Children(parentID string) []*Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Instance, 0, len(s.order))
	for _, id := range s.order {
		inst := s.instances[id]
		if inst == nil || inst.ParentID != parentID {
			continue
		}
		out = append(out, inst.snapshot())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// All 列出全部子智能体。
func (s *Supervisor) All() []*Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Instance, 0, len(s.order))
	for _, id := range s.order {
		if inst := s.instances[id]; inst != nil {
			out = append(out, inst.snapshot())
		}
	}
	return out
}

// RunningCount 当前还在跑的子智能体数（界面用）。
func (s *Supervisor) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningLocked()
}

func (s *Supervisor) countLocked(parentID string) int {
	count := 0
	for _, id := range s.order {
		if inst := s.instances[id]; inst != nil && inst.ParentID == parentID {
			count++
		}
	}
	return count
}

/**
 * runningLocked 统计"正在执行"的子智能体（starting / running）。
 *
 * 空闲驻留的可继续子智能体**不**算在内：它没有在烧 CPU 与模型 token，
 * 只占着一份进程内存——那件事由 continuableLocked 单独管。
 * 两个上限分开，正是照 DSH 的做法（活动态与驻留是两回事）。
 */
func (s *Supervisor) runningLocked() int {
	count := 0
	for _, inst := range s.instances {
		if inst.Status == StatusStarting || inst.Status == StatusRunning {
			count++
		}
	}
	return count
}

// runningChildrenLocked 统计**这个父级**正在执行的子智能体（并发预算按会话计）。
func (s *Supervisor) runningChildrenLocked(parentID string) int {
	count := 0
	for _, inst := range s.instances {
		if inst.ParentID != parentID {
			continue
		}
		if inst.Status == StatusStarting || inst.Status == StatusRunning {
			count++
		}
	}
	return count
}

// continuableLocked 统计还活着的可继续子智能体（含空闲驻留的那些）。
func (s *Supervisor) continuableLocked() int {
	count := 0
	for _, inst := range s.instances {
		if inst.Mode == ModeContinuable && !inst.Status.Finished() {
			count++
		}
	}
	return count
}

/**
 * notify 把状态变化推给上层（界面刷新）。
 *
 * 快照**必须在锁里取**：finish / markIdle / beginTurn 可能在另一个 goroutine 上
 * 同时改这个 Instance 的字段（状态、结果、用量），锁外读它就是数据竞争
 *（`go test -race` 抓到过：finish 写 / notify→snapshot 读）。
 * 回调本身仍然在**锁外**调用——那是刻意的：OnChange 的实现要做 Emit 之类的动作，
 * 持锁回调等于把整个监督器卡在界面上。
 */
func (s *Supervisor) notify(inst *Instance) {
	if s.options.OnChange == nil {
		return
	}
	s.mu.Lock()
	snapshot := inst.snapshot()
	s.mu.Unlock()
	s.options.OnChange(snapshot)
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
