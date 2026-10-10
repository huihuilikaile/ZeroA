package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 任务循环（方案的 P11 + 第十九节）。
//
// 与 Pi 的分工必须说清楚：
//
//	Pi 负责   LLM → 工具 → LLM → … → 本轮结束（agent loop，我们一行都不重写）
//	Go 负责   这一轮结束之后，还要不要继续？继续多少轮？卡住了怎么办？
//
// 第一版只实现方案第十九节点名的五件事：MaxRounds、MaxDuration、Manual Abort、
// Stuck Counter、Follow-up。**刻意不做"目标是否完成"的自动判定**——
// 那需要 Goal Evaluator（方案第三十四节明确排除），硬做只会变成一个
// 到处瞎跑的自动 Agent。
//
// LoopPolicy / StuckPolicy 的定义在 internal/roles（它们是 Role 结构的一部分），
// 主包通过 roles_alias.go 的别名引用。

const (
	stuckDefaultThreshold = 3
	stuckDefaultWindow    = 6
	// stuckHistory 保留最近的工具调用签名，用来判断"是不是在原地打转"
	stuckHistory = 24
)

// loopDecision 是一次循环决策的结果。
type loopDecision struct {
	// Action：continue（什么也不做，等用户）/ replan / summarize / stop
	Action string
	Reason string
	// Message 是要发给 Pi 的 follow-up（replan/summarize 时非空）
	Message string
}

// taskLoop 是循环状态机。它只观察事件、做决定，不直接发消息——
// 发消息由 PiService 做（send 需要 client，而且失败要能报给界面）。
type taskLoop struct {
	mu       sync.Mutex
	policy   LoopPolicy
	rounds   int
	started  time.Time
	toolSigs []string
	stuck    int
	last     loopDecision
	// paused：用户手动停止了循环（Manual Abort），下一次 prompt 会重新开始
	paused bool
}

func newTaskLoop() *taskLoop { return &taskLoop{} }

// SetPolicy 换角色时更新策略，并把计数清零（新角色 = 新任务，不继承旧轮次）。
func (l *taskLoop) SetPolicy(policy LoopPolicy) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.policy = policy
	l.rounds = 0
	l.started = time.Time{}
	l.toolSigs = nil
	l.stuck = 0
	l.paused = false
	l.last = loopDecision{Action: "continue", Reason: "新任务"}
}

// Start 在用户发消息时调用：正式开始计数。
func (l *taskLoop) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rounds = 0
	l.started = time.Now()
	l.toolSigs = nil
	l.stuck = 0
	l.paused = false
	l.last = loopDecision{Action: "continue", Reason: "开始新一轮任务"}
}

// Pause 是 Manual Abort：停了之后循环不再自动发消息。
func (l *taskLoop) Pause(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paused = true
	l.last = loopDecision{Action: "stop", Reason: reason}
}

// ObserveTool 记录一次工具调用（用来判断是不是卡住了）。
func (l *taskLoop) ObserveTool(name string, args any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	signature := toolSignature(name, args)
	l.toolSigs = append(l.toolSigs, signature)
	if len(l.toolSigs) > stuckHistory {
		l.toolSigs = l.toolSigs[len(l.toolSigs)-stuckHistory:]
	}
}

// toolSignature 是"工具 + 参数"的短哈希。
//
// 为什么不比对完整参数：参数里常带时间戳/临时路径（几乎每次都不一样），
// 那样永远判不出"重复"。取名字 + 参数的前 200 字符做哈希，够稳也够省。
func toolSignature(name string, args any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		raw = []byte(fmt.Sprint(args))
	}
	text := string(raw)
	if len(text) > 200 {
		text = text[:200]
	}
	sum := sha256.Sum256([]byte(name + "|" + text))
	return hex.EncodeToString(sum[:8])
}

// Settle 在每一轮结束时调用，产出"接下来怎么办"。
//
// 调用顺序即优先级：先看用户有没有停、再看时间/轮次上限、最后才看卡住检测。
// 这样"到点了"不会被"看起来卡住了"盖过去。
func (l *taskLoop) Settle(now time.Time) loopDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paused {
		l.last = loopDecision{Action: "stop", Reason: "用户已停止循环"}
		return l.last
	}
	l.rounds++

	// 1) 时间上限
	if l.policy.MaxDurationSeconds > 0 && !l.started.IsZero() {
		if elapsed := now.Sub(l.started); elapsed > time.Duration(l.policy.MaxDurationSeconds)*time.Second {
			l.paused = true
			l.last = l.limitDecision(fmt.Sprintf("已运行 %s，达到时长上限 %d 秒",
				elapsed.Round(time.Second), l.policy.MaxDurationSeconds))
			return l.last
		}
	}
	// 2) 轮次上限
	if l.policy.MaxRounds > 0 && l.rounds > l.policy.MaxRounds {
		l.paused = true
		l.last = l.limitDecision(fmt.Sprintf("已进行 %d 轮，达到轮次上限 %d", l.rounds, l.policy.MaxRounds))
		return l.last
	}
	// 3) 卡住检测：最近 window 条工具签名是不是都在重复同一小组
	if policy := l.policy.Stuck; policy != nil && policy.Enabled {
		if repeated := l.repeatedSignature(); repeated >= l.threshold(policy) {
			l.stuck = repeated
			if strings.EqualFold(l.policy.OnStuck, "stop") {
				l.paused = true
				l.last = loopDecision{Action: "stop", Reason: fmt.Sprintf("检测到原地重复 %d 次", repeated)}
				return l.last
			}
			// replan：不清空历史，否则下一条就会被当成"不重复"而永远不再触发
			l.toolSigs = nil
			l.last = loopDecision{
				Action: "replan",
				Reason: fmt.Sprintf("最近 %d 次工具调用高度重复", repeated),
				Message: fmt.Sprintf(
					"你最近的 %d 次工具调用几乎在重复同一件事。先停下来用一句话说明当前卡在哪里、已经排除了哪些可能，"+
						"然后换一个完全不同的思路继续。如果确实无法继续，直接说明缺什么信息。", repeated),
			}
			return l.last
		}
	}
	l.last = loopDecision{Action: "continue", Reason: fmt.Sprintf("第 %d 轮结束", l.rounds)}
	return l.last
}

// limitDecision 按 OnLimit 决定"到限了要不要先总结"。
func (l *taskLoop) limitDecision(reason string) loopDecision {
	if strings.EqualFold(l.policy.OnLimit, "summarize") {
		return loopDecision{
			Action:  "summarize",
			Reason:  reason,
			Message: reason + "。请用要点总结：已经确认了什么、还缺什么、建议下一步做什么。不要再调用工具。",
		}
	}
	return loopDecision{Action: "stop", Reason: reason}
}

func (l *taskLoop) threshold(policy *StuckPolicy) int {
	if policy.Threshold > 0 {
		return policy.Threshold
	}
	return stuckDefaultThreshold
}

// repeatedSignature 看最近 window 条签名里，出现次数最多的那个出现了几次。
func (l *taskLoop) repeatedSignature() int {
	policy := l.policy.Stuck
	window := stuckDefaultWindow
	if policy != nil && policy.Window > 0 {
		window = policy.Window
	}
	if len(l.toolSigs) < window {
		return 0
	}
	recent := l.toolSigs[len(l.toolSigs)-window:]
	counts := map[string]int{}
	best := 0
	for _, signature := range recent {
		counts[signature]++
		if counts[signature] > best {
			best = counts[signature]
		}
	}
	return best
}

// Snapshot 是给界面看的循环状态。
type loopSnapshot struct {
	Policy     LoopPolicy `json:"policy"`
	Rounds     int        `json:"rounds"`
	Elapsed    int        `json:"elapsedSeconds"`
	Stuck      int        `json:"stuckCount"`
	Paused     bool       `json:"paused"`
	LastAction string     `json:"lastAction"`
	LastReason string     `json:"lastReason"`
	// MaxRounds/MaxDurationSeconds 为 0 时界面显示"不限制"
}

func (l *taskLoop) Snapshot(now time.Time) loopSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	elapsed := 0
	if !l.started.IsZero() {
		elapsed = int(now.Sub(l.started).Seconds())
	}
	return loopSnapshot{
		Policy:     l.policy,
		Rounds:     l.rounds,
		Elapsed:    elapsed,
		Stuck:      l.stuck,
		Paused:     l.paused,
		LastAction: l.last.Action,
		LastReason: l.last.Reason,
	}
}

// IsPaused 用于"要不要自动发下一条"这种判断。
func (l *taskLoop) IsPaused() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.paused
}
