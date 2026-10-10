package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Deliver 把一条"子智能体结果"消息送进父会话。
//
// 上层实现：用 RPC 的 follow_up（回合结束后投递）或 steer（下一个检查点插话）。
// 返回错误只记日志——投递失败不该让子智能体的结果凭空消失，
// 但也不该让调用方重试到把父会话刷屏。
type Deliver func(parentID, message string) error

// RouterOptions 是结果投递的配置。
type RouterOptions struct {
	// Coalesce 是合并窗口：这么短时间内完成的结果合成一条消息（默认 1.5s）。
	//
	// 为什么需要：同时派 3 个探索 Agent，它们往往前后脚完成。逐个投递会把
	// 父上下文刷成三小段碎片，既浪费 token 又打断它的思路。
	Coalesce time.Duration
	// Deliver 是投递实现。
	Deliver Deliver
	// OnDelivered 投递成功后的回调（界面标记"已合并"/"已送达"）。
	OnDelivered func(insts []*Instance, message string)
	// ParentBusy 返回父会话此刻是否在跑回合。
	//
	// 父忙的含义：它的模型正在出字或调工具。这时候塞消息只有两个选择——
	// 等它这一轮结束，或强行插话打断。默认等（见 ShouldHold）。
	ParentBusy func(parentID string) bool
	// ShouldHold 决定"父忙时要不要等它结束"。
	// 默认 true（等），返回 false 时立即投递（对应角色声明 deliverOn: checkpoint）。
	ShouldHold func(inst *Instance) bool
	// SessionClosed 返回父会话是否已经收工（用户已离开/会话结束）。
	// 这时不唤醒——否则会起一个没有意义的回合。
	SessionClosed func(parentID string) bool
	// OnHeld 结果被挂起等待时的回调（界面显示"待投递"）。
	OnHeld func(insts []*Instance)
	// OnDrop 结果被丢弃时的回调（父会话已收工）。
	OnDrop func(insts []*Instance, reason string)
}

// Router 把子智能体的结果合并、择时投递回父会话。
type Router struct {
	mu      sync.Mutex
	options RouterOptions
	pending map[string][]*Instance // parentID → 等待投递的结果
	timers  map[string]*time.Timer
	// held 表示"攒着但父亲忙，等它 settled 再投"
	held map[string]bool
}

// NewRouter 建一个 Router。
func NewRouter(options RouterOptions) *Router {
	if options.Coalesce <= 0 {
		options.Coalesce = 1500 * time.Millisecond
	}
	if options.ShouldHold == nil {
		options.ShouldHold = func(*Instance) bool { return true }
	}
	return &Router{
		options: options,
		pending: map[string][]*Instance{},
		timers:  map[string]*time.Timer{},
		held:    map[string]bool{},
	}
}

// Submit 收下一个已完成的结果。
func (r *Router) Submit(inst *Instance) {
	if inst == nil || inst.ParentID == "" {
		return
	}
	if r.options.SessionClosed != nil && r.options.SessionClosed(inst.ParentID) {
		if r.options.OnDrop != nil {
			r.options.OnDrop([]*Instance{inst}, "父会话已结束，结果不再唤醒它")
		}
		return
	}

	r.mu.Lock()
	r.pending[inst.ParentID] = append(r.pending[inst.ParentID], inst)
	hasTimer := r.timers[inst.ParentID] != nil
	r.mu.Unlock()

	if hasTimer {
		return // 已经在等合并窗口，让它收进这一批
	}

	// 父忙且允许等待：先挂着，等父回合结束（FlushParent）再投
	if r.options.ParentBusy != nil && r.options.ParentBusy(inst.ParentID) && r.options.ShouldHold(inst) {
		r.mu.Lock()
		r.held[inst.ParentID] = true
		r.mu.Unlock()
		if r.options.OnHeld != nil {
			r.options.OnHeld(r.snapshot(inst.ParentID))
		}
		return
	}

	r.mu.Lock()
	r.timers[inst.ParentID] = time.AfterFunc(r.options.Coalesce, func() {
		r.Flush(inst.ParentID)
	})
	r.mu.Unlock()
}

// FlushParent 在父会话回合结束时调用：把挂起的结果投出去。
func (r *Router) FlushParent(parentID string) {
	r.mu.Lock()
	held := r.held[parentID]
	r.mu.Unlock()
	if !held {
		return
	}
	r.mu.Lock()
	delete(r.held, parentID)
	r.mu.Unlock()
	r.Flush(parentID)
}

// Reset 丢弃某个父会话挂起的结果（不投递）。
//
// 用在中止/换会话时：那些结果属于一个已经不要了的回合，
// 投进去只会变成噪音（用户会觉得"我明明停了它还在说话"）。
func (r *Router) Reset(parentID string) {
	r.mu.Lock()
	delete(r.pending, parentID)
	delete(r.held, parentID)
	if timer := r.timers[parentID]; timer != nil {
		timer.Stop()
		delete(r.timers, parentID)
	}
	r.mu.Unlock()
}

// Flush 立刻投递某个父会话攒下的全部结果（合并成一条）。
func (r *Router) Flush(parentID string) {
	r.mu.Lock()
	batch := r.pending[parentID]
	delete(r.pending, parentID)
	if timer := r.timers[parentID]; timer != nil {
		timer.Stop()
		delete(r.timers, parentID)
	}
	r.mu.Unlock()

	if len(batch) == 0 {
		return
	}
	message := FormatResults(batch)
	if r.options.Deliver != nil {
		if err := r.options.Deliver(parentID, message); err != nil {
			// 投递失败：不重试到刷屏，留给上层日志与界面（结果本身仍在 Agents 页可见）
			if r.options.OnDrop != nil {
				r.options.OnDrop(batch, "投递失败："+err.Error())
			}
			return
		}
	}
	if r.options.OnDelivered != nil {
		r.options.OnDelivered(batch, message)
	}
}

// Pending 返回某个父会话挂起（还没投递）的结果数。
func (r *Router) Pending(parentID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending[parentID])
}

// Held 返回某个父会话是否有"等着父回合结束"的结果。
func (r *Router) Held(parentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held[parentID]
}

func (r *Router) snapshot(parentID string) []*Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Instance, len(r.pending[parentID]))
	copy(out, r.pending[parentID])
	return out
}

// FormatResults 把一批结果渲染成**一条**给父模型读的消息。
//
// 格式固定而不是自由散文：父模型要靠它一眼分辨"谁说的、成没成、要不要信"。
// 失败也要投——静默失败会让父以为任务还在跑。
func FormatResults(insts []*Instance) string {
	var builder strings.Builder
	if len(insts) == 1 {
		builder.WriteString("[子智能体结果] " + instanceHeader(insts[0]) + "\n")
	} else {
		builder.WriteString(fmt.Sprintf("[子智能体结果] %d 个任务已完成\n", len(insts)))
	}
	for _, inst := range insts {
		if len(insts) > 1 {
			builder.WriteString("\n── " + instanceHeader(inst) + "\n")
		}
		builder.WriteString("\n")
		switch {
		case inst.Status == StatusCompleted:
			builder.WriteString(inst.Result)
		case inst.Status == StatusIdle && inst.LastTurnCompleted:
			/**
			 * 可继续子智能体的"阶段成果"：它还在（空闲着等下一句），
			 * 但这一轮确实正常跑完了——不能按"未完成"投，否则父级会以为任务黄了。
			 */
			builder.WriteString(inst.Result)
			builder.WriteString("\n（这只是阶段性结论：它现在空闲着，还可以继续给它派活。）")
		default:
			builder.WriteString("（未完成：" + string(inst.Status) + "）")
			if inst.Err != "" {
				builder.WriteString(" " + inst.Err)
			}
			if strings.TrimSpace(inst.Result) != "" {
				builder.WriteString("\n\n已完成的部分：\n" + inst.Result)
			}
		}
		builder.WriteString("\n")
		if inst.SessionDir != "" {
			builder.WriteString("（完整过程：" + inst.SessionDir + "）\n")
		}
	}
	builder.WriteString("\n请继续你手上的工作；如果这些结论改变了你的判断，说明一下你采纳了什么、忽略了什么。")
	return builder.String()
}

func instanceHeader(inst *Instance) string {
	parts := []string{inst.ID, inst.RoleID, string(inst.Status)}
	if !inst.EndedAt.IsZero() {
		parts = append(parts, fmt.Sprintf("%.1fs", inst.EndedAt.Sub(inst.StartedAt).Seconds()))
	}
	if inst.Usage.TotalTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s tokens", humanTokens(inst.Usage.TotalTokens)))
	}
	return strings.Join(parts, " · ")
}

func humanTokens(value int64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

// RunOnce 是给测试与"手动触发一次投递"用的便捷封装。
func (r *Router) RunOnce(ctx context.Context, parentID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	r.Flush(parentID)
	return nil
}
