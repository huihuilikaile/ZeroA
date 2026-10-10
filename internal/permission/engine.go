// Package permission 是方案的 P10 + 第十七节：权限引擎与临时授权。
//
// 一条铁律（方案原文）：**Prompt 不是安全边界**。所以权限不能写成提示词里的一句
// "请不要访问网络"，而必须是 Go 侧算出来的、能真正改变 Agent 行为的东西。
//
// 公式（方案第十六节）：
//
//	Role Capability ∩ User Authorization ∩ Global Policy ∩ Session Policy
//	        ↓
//	Effective Permission
//
// 交集的方向是"取最严"：任何一层 deny 就是 deny，任何一层 ask 就是至少 ask。
package permission

import (
	"sort"
	"strings"
	"time"
)

// Action 是受管控的动作类型（方案第十六节的第一版清单）。
type Action string

const (
	ActionFilesystemRead  Action = "filesystem.read"
	ActionFilesystemWrite Action = "filesystem.write"
	ActionShellExecute    Action = "shell.execute"
	ActionNetworkPublic   Action = "network.public"
	ActionNetworkTarget   Action = "network.target" // CTF 靶机：单独一类，由授权目标控制
	ActionProcessSpawn    Action = "process.spawn"
)

// Actions 是全部动作（界面按这个顺序渲染）。
var Actions = []Action{
	ActionFilesystemRead,
	ActionFilesystemWrite,
	ActionShellExecute,
	ActionNetworkPublic,
	ActionNetworkTarget,
	ActionProcessSpawn,
}

// Decision 是三态权限。
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// rank 越大越宽松；权限取交集就是取最小 rank。
func rank(decision Decision) int {
	switch decision {
	case Allow:
		return 2
	case Ask:
		return 1
	default:
		return 0 // deny：包括空值（没声明 = 不放行）
	}
}

func stricter(a, b Decision) Decision {
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

// Policy 是一层策略：动作 → 决定。缺省动作按 Deny 处理（安全默认）。
type Policy map[Action]Decision

// Normalize 去掉空动作、把未知决定收敛成 Deny。
func (p Policy) Normalize() Policy {
	out := Policy{}
	for action, decision := range p {
		switch decision {
		case Allow, Ask, Deny:
			out[action] = decision
		default:
			out[action] = Deny
		}
	}
	return out
}

// Authorization 是临时授权（方案第十七节）：到期自动回到原策略。
//
// 存的是"到什么时候为止允许"，不是提示词里的一句话——
// 这样界面能显示倒计时，到期后下一次评估就自然回到 Ask/Deny。
type Authorization struct {
	Action    Action    `json:"action"`
	ExpiresAt time.Time `json:"expiresAt"`
	Reason    string    `json:"reason,omitempty"`
	// GrantedBy 记录是谁给的（界面用，便于审计）
	GrantedBy string `json:"grantedBy,omitempty"`
}

// Active 判断这条临时授权此刻是否有效。
func (a Authorization) Active(now time.Time) bool {
	return a.ExpiresAt.After(now)
}

// Engine 把四层策略 + 临时授权算成"当前实际生效的权限"。
type Engine struct {
	Global  Policy
	User    Policy
	Role    Policy
	Session Policy
	Temp    []Authorization
}

// Effective 计算某个动作的最终决定。
//
// 层与层的关系是**交集取最严**，但"某一层没提到这个动作"不等于 deny——
// 那叫弃权（abstain）。真按 deny 算的话，用户必须在四层里把六个动作全配一遍，
// 否则到处都是 deny：那不是安全，是不能用。
// 所以：谁提到谁参与，都没提到才用安全默认 deny。
//
// 临时授权**只能把 ask 放宽成 allow，不能突破 deny**：deny 是硬边界
// （比如全局禁止访问靶机），临时授权不该能绕过它。
func (e Engine) Effective(action Action, now time.Time) Decision {
	decision := Decision("") // 空 = 还没有任何一层表态
	for _, layer := range []Policy{e.Global, e.User, e.Role, e.Session} {
		value, ok := layer[action]
		if !ok {
			continue // 弃权：不参与取严
		}
		if decision == "" {
			decision = value
			continue
		}
		decision = stricter(decision, value)
	}
	if decision == "" {
		return Deny // 没有任何一层表态 = 不放行（安全默认）
	}
	if decision == Deny {
		return Deny // deny 是硬边界，临时授权也不能绕过
	}
	for _, authorization := range e.Temp {
		if authorization.Action == action && authorization.Active(now) {
			return Allow
		}
	}
	return decision
}

// Snapshot 是给界面看的完整状态：每个动作的决定 + 来源 + 临时授权。
type Snapshot struct {
	Action    Action         `json:"action"`
	Decision  Decision       `json:"decision"`
	Role      Decision       `json:"role"`
	User      Decision       `json:"user"`
	Global    Decision       `json:"global"`
	Session   Decision       `json:"session"`
	Temporary *Authorization `json:"temporary,omitempty"`
	// Constrained/Enforced 说明这条权限**在当前架构下能不能真的执行**
	Enforced bool   `json:"enforced"`
	Note     string `json:"note,omitempty"`
}

// Enforcement 描述一个动作能不能被真正强制。
//
// 这一点必须对用户诚实：Pi 的 bash 一旦启用，Go 就没法逐个命令拦截
// （方案里把它交给 v2 的 WebToolGateway 与 role-controller 扩展）。
// 我们现在能强制的是"给不给这个工具"，所以：
//   - 能被工具开关覆盖的（网络、文件写、shell）标 Enforced，并说明强制方式
//   - 只能记录/提示的，明确标出来，不假装它被拦住了
var enforcement = map[Action]struct {
	Enforced bool
	Note     string
}{
	ActionFilesystemRead:  {true, "deny 时从工具白名单里去掉 read/grep/ls/find（模型无法读文件）"},
	ActionFilesystemWrite: {true, "deny 时去掉 write/edit；ask 时保留工具并在审计日志里记录"},
	ActionShellExecute:    {true, "deny 时去掉 bash/powershell（模型无法执行命令）"},
	ActionNetworkPublic:   {true, "deny 时去掉 web_search/web_fetch 等联网工具；细粒度拦截需要 v2 的 Web 网关"},
	ActionNetworkTarget:   {false, "靶机访问由 bash/curl 完成，Go 拦截不到单个请求：ask 只记录与提示"},
	ActionProcessSpawn:    {false, "ZeroA 的 bash 能起任意进程：Go 只能记录，无法逐次拦截"},
}

// Evaluate 产出完整快照（界面直接渲染，不用自己拼）。
func (e Engine) Evaluate(now time.Time) []Snapshot {
	out := make([]Snapshot, 0, len(Actions))
	for _, action := range Actions {
		info := enforcement[action]
		snapshot := Snapshot{
			Action:   action,
			Decision: e.Effective(action, now),
			Role:     valueOr(e.Role, action),
			User:     valueOr(e.User, action),
			Global:   valueOr(e.Global, action),
			Session:  valueOr(e.Session, action),
			Enforced: info.Enforced,
			Note:     info.Note,
		}
		for _, authorization := range e.Temp {
			if authorization.Action == action && authorization.Active(now) {
				copy := authorization
				snapshot.Temporary = &copy
				break
			}
		}
		out = append(out, snapshot)
	}
	return out
}

// DecisionAbstain 是**只在展示层**用的取值：这一层没有对这个动作表态。
//
// 不能拿 Deny 顶替：界面上会把"角色层没提这件事"显示成"角色禁止"，
// 而最终结果又因为弃权而不参与取严 → 出现"角色 禁止 · 全局 允许 → 最终 允许"
// 这种自相矛盾的显示（用户会以为算错了）。
const DecisionAbstain Decision = "abstain"

func valueOr(policy Policy, action Action) Decision {
	if value, ok := policy[action]; ok {
		return value
	}
	return DecisionAbstain
}

// Authorize 追加一条临时授权（同动作的旧授权被覆盖）。
func (e *Engine) Authorize(action Action, until time.Time, reason, by string) {
	kept := make([]Authorization, 0, len(e.Temp))
	for _, authorization := range e.Temp {
		if authorization.Action != action {
			kept = append(kept, authorization)
		}
	}
	e.Temp = append(kept, Authorization{Action: action, ExpiresAt: until, Reason: reason, GrantedBy: by})
}

// Revoke 撤销某个动作的临时授权。
func (e *Engine) Revoke(action Action) {
	kept := make([]Authorization, 0, len(e.Temp))
	for _, authorization := range e.Temp {
		if authorization.Action != action {
			kept = append(kept, authorization)
		}
	}
	e.Temp = kept
}

// DropExpired 清掉过期授权，返回是否发生了变化（界面据此决定要不要刷新）。
func (e *Engine) DropExpired(now time.Time) bool {
	kept := make([]Authorization, 0, len(e.Temp))
	changed := false
	for _, authorization := range e.Temp {
		if authorization.Active(now) {
			kept = append(kept, authorization)
		} else {
			changed = true
		}
	}
	e.Temp = kept
	return changed
}

// ParsePolicy 把界面传来的 JSON（动作 → 决定）解析成策略，顺带校验。
func ParsePolicy(raw map[string]string) (Policy, error) {
	policy := Policy{}
	for key, value := range raw {
		action := Action(strings.TrimSpace(key))
		if !Known(action) {
			return nil, &UnknownActionError{Action: key}
		}
		switch Decision(strings.ToLower(strings.TrimSpace(value))) {
		case Allow, Ask, Deny:
			policy[action] = Decision(strings.ToLower(strings.TrimSpace(value)))
		default:
			policy[action] = Deny
		}
	}
	return policy, nil
}

// Known 判断动作是否在受管控清单里。
func Known(action Action) bool {
	for _, item := range Actions {
		if item == action {
			return true
		}
	}
	return false
}

// UnknownActionError 让界面能明确告诉用户"这个动作名不认识"。
type UnknownActionError struct{ Action string }

func (e *UnknownActionError) Error() string { return "未知的权限动作: " + e.Action }

// DefaultGlobalPolicy 是全局默认：只读放开，写与执行要问，网络默认不放行。
//
// 选择理由：这套工具的定位是安全分析工作台，误伤代价高（写文件、连内网），
// 所以"要问"是默认，而不是"允许"。
func DefaultGlobalPolicy() Policy {
	return Policy{
		ActionFilesystemRead:  Allow,
		ActionFilesystemWrite: Ask,
		ActionShellExecute:    Ask,
		ActionNetworkPublic:   Ask,
		ActionNetworkTarget:   Deny,
		ActionProcessSpawn:    Ask,
	}
}

// ToolDenyList 把权限翻译成"要禁用哪些 Pi 工具"。
//
// 这是当前架构下**唯一真正能强制**的地方：Pi 的工具在启动时确定
// （--tools/--exclude-tools），所以 deny 就体现为"不给这个工具"。
func ToolDenyList(decision func(Action) Decision) []string {
	var denied []string
	tools := map[Action][]string{
		ActionFilesystemRead:  {"read", "grep", "ls", "find"},
		ActionFilesystemWrite: {"write", "edit"},
		ActionShellExecute:    {"bash", "powershell"},
		ActionNetworkPublic:   {"web_search", "web_fetch", "fetch_content"},
	}
	actions := make([]string, 0, len(tools))
	for action := range tools {
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	for _, raw := range actions {
		action := Action(raw)
		if decision(action) != Deny {
			continue
		}
		denied = append(denied, tools[action]...)
	}
	sort.Strings(denied)
	return denied
}
