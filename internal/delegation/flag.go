package delegation

// 本文件实现"主 Agent 给出候选 flag → 停掉还在跑的子智能体"。
//
// 为什么不是"回合结束就停"：Pi 的 agent_settled 是**每个回合**结束都会发的，
// 而"派完活、父继续解题"本身就意味着父的回合刚结束——按回合结束杀子 Agent
// 会让委派完全失效（派出去的活立刻被杀）。只有"已经拿到答案"才真的不需要它们。

import (
	"regexp"
	"strings"
	"time"

	"zeroa/internal/agent"
	"zeroa/internal/agents"
	"zeroa/internal/roles"
)

// flagPatterns 是常见的 CTF flag 形状。
//
// 只认"看起来确实像 flag"的：有前缀 + 花括号。宁可漏（漏了还有 delegation_stop
// 与"父收工"这两条路兜底），也不要误伤——误伤的代价是子智能体的工作白做。
var flagPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:flag|FLAG|Flag)\{[^}\s]{1,200}\}`),
	regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]{1,24}\{[^}\s]{4,200}\}`),
}

// DetectFlagHint 从一段文本里找出候选 flag；找不到返回空串。
func DetectFlagHint(text string) string {
	if text == "" {
		return ""
	}
	for _, pattern := range flagPatterns {
		if match := pattern.FindString(text); match != "" {
			return match
		}
	}
	return ""
}

// MaybeStopChildrenOnFlag 在主 Agent 的回复里发现候选 flag 时停掉子智能体。
//
// 触发条件（三个都满足）：
//
//	· 角色的 delegation.stopOnFlag 不是显式关闭；
//	· 主会话**还有在跑的子智能体**；
//	· 这条 assistant 消息里出现了 flag。
//
// 满足就停，并把原因写进每个子智能体的记录（界面上能看到"为什么被停"）。
func (m *Manager) MaybeStopChildrenOnFlag(text string) {
	flag := DetectFlagHint(text)
	if flag == "" {
		return
	}
	// 用 SupervisorIfAny 而不是 Supervisor()：后者会**创建**一整套 supervisor+router，
	// 父会话文本里只要出现 flag{...} 形状（很常见）就白建一次，违背懒创建的本意。
	sup := m.SupervisorIfAny()
	if sup == nil || sup.RunningCount() == 0 {
		return // 没有在跑的子智能体：什么都不用做
	}

	// 角色的策略：nil 视为开启（CTF 方向默认就该这样）。
	// 按**这条会话**的角色判定（理由同 Manager.Policy）。
	if roleID := m.deps.RoleForParent(m.deps.CurrentParentID()); roleID != "" {
		if view, ok := roles.Find(roleID); ok && view.Role.Delegation != nil {
			if view.Role.Delegation.StopOnFlag != nil && !*view.Role.Delegation.StopOnFlag {
				return
			}
		}
	}

	reason := "主 Agent 已得出候选 flag（" + shortenFlag(flag) + "），不再需要子智能体"
	/**
	 * 只停**这条会话**派出去的子智能体。
	 *
	 * 以前写死 MainAgentID 是对的（只有一条会话）；一进程一会话之后，
	 * 用"当前会话"作父 id 才是对的：A 会话给出 flag 时，不该把 B 会话正在跑的
	 * 子智能体一起停掉（B 的用户会看到自己的活莫名其妙全没了）。
	 */
	stopped := sup.CancelChildren(m.deps.CurrentParentID(), reason)
	if stopped == 0 {
		return
	}
	m.log("delegation_stopped_on_flag count=%d flag=%s", stopped, shortenFlag(flag))
	// 告诉界面：用户要知道"为什么子智能体突然全停了"
	m.deps.EmitNotice("delegation:auto-stop", map[string]any{
		"count":  stopped,
		"flag":   shortenFlag(flag),
		"reason": reason,
	})
}

// shortenFlag 截断过长的 flag 用于日志/提示（真正的 flag 不出现在日志里太长）。
func shortenFlag(flag string) string {
	trimmed := strings.TrimSpace(flag)
	if len(trimmed) <= 60 {
		return trimmed
	}
	return trimmed[:57] + "…"
}

// ObserveParentMessage 处理主会话的一条 message_end：抽文本、查 flag。
//
// 放在这里而不是前端 router 里：停子智能体是 Go 的职责（它才是进程的主人），
// 而且这一层拿到的是已经解析好的事件，不需要前端再回传一次。
func (m *Manager) ObserveParentMessage(envelope agent.Envelope) {
	event, ok := agents.DecodeEvent(envelope.Record)
	if !ok || event.Type != "message_end" {
		return
	}
	text, isAssistant := event.AssistantText()
	if !isAssistant || strings.TrimSpace(text) == "" {
		return
	}
	m.MaybeStopChildrenOnFlag(text)
}

// flagStopGrace 是"检测到 flag 之后再等一会儿"的宽限期（目前未启用）。
//
// 保留这个常量是为了把设计意图写下来：如果将来发现"flag 先出现、子智能体随后
// 才产出关键验证"这种情况，就在这里加一个延迟停止（先标记、N 秒后停），
// 而不是把逻辑挪回"回合结束就停"。
const flagStopGrace = 0 * time.Second
