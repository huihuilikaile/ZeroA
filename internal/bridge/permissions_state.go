package bridge

import (
	"encoding/json"
	"hash/fnv"
	"strconv"
	"sync"
	"time"

	"zeroa/internal/permission"
)

// 权限状态（方案的第十六、十七节）：全局策略 / 用户策略 / 临时授权。
//
// 存哪儿：gui-state.json（和会话记忆同一份文件）。理由很简单——这三样都是
// "用户在这个应用上的选择"，不该散落成三个文件；而且它已经有原子写入与损坏容错。

// policyState 把三层权限状态收在一起，用一把锁保护。
//
// 为什么是包级单例而不是 PiService 的字段：compileRole 是纯函数，
// 但需要一个"当前策略"的读入口；把它做成显式的依赖参数会让每个调用点
// 都要传一遍，收益不大。
type policyState struct {
	mu sync.Mutex
	// Global 是应用级硬边界（用户可以改，但它是所有角色的上限）
	Global permission.Policy
	// User 是用户对所有角色的默认要求（例如"一律先问"）
	User permission.Policy
	// Temp 是临时授权：到点自动失效
	Temp []permission.Authorization
}

var policies = policyState{
	Global: permission.DefaultGlobalPolicy(),
	User:   permission.Policy{},
}

// timeNow 单独抽出来是为了测试能塞一个固定时间（临时授权要测过期）。
var timeNow = time.Now

func (p *policyState) setGlobal(policy permission.Policy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if policy == nil {
		policy = permission.Policy{}
	}
	p.Global = policy.Normalize()
}

func (p *policyState) setUser(policy permission.Policy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if policy == nil {
		policy = permission.Policy{}
	}
	p.User = policy.Normalize()
}

// engine 组装出"此刻"的引擎（带全局/用户策略与未过期的临时授权）。
func (p *policyState) engine(role permission.Policy) permission.Engine {
	now := timeNow()
	p.mu.Lock()
	defer p.mu.Unlock()
	// 顺手清过期授权：界面显示与判定都只认还在生效的
	active := make([]permission.Authorization, 0, len(p.Temp))
	for _, authorization := range p.Temp {
		if authorization.Active(now) {
			active = append(active, authorization)
		}
	}
	if len(active) != len(p.Temp) {
		p.Temp = active
	}
	return permission.Engine{
		Global: p.Global,
		User:   p.User,
		Role:   role,
		Temp:   append([]permission.Authorization(nil), p.Temp...),
	}
}

func (p *policyState) authorize(action permission.Action, until time.Time, reason, by string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	engine := permission.Engine{Temp: p.Temp}
	engine.Authorize(action, until, reason, by)
	p.Temp = engine.Temp
}

func (p *policyState) revoke(action permission.Action) {
	p.mu.Lock()
	defer p.mu.Unlock()
	engine := permission.Engine{Temp: p.Temp}
	engine.Revoke(action)
	p.Temp = engine.Temp
}

func (p *policyState) snapshot() (permission.Policy, permission.Policy, []permission.Authorization) {
	p.mu.Lock()
	defer p.mu.Unlock()
	global := permission.Policy{}
	for action, decision := range p.Global {
		global[action] = decision
	}
	user := permission.Policy{}
	for action, decision := range p.User {
		user[action] = decision
	}
	return global, user, append([]permission.Authorization(nil), p.Temp...)
}

// fingerprint 返回权限状态的指纹（角色编译缓存的失效键之一）。
//
// 与 engine() 同口径地先清过期授权：临时授权到点失效后指纹必须变，
// 否则编译缓存会把"已过期授权换来的放行"继续当成有效结论用。
func (p *policyState) fingerprint() string {
	now := timeNow()
	p.mu.Lock()
	defer p.mu.Unlock()
	active := make([]permission.Authorization, 0, len(p.Temp))
	for _, authorization := range p.Temp {
		if authorization.Active(now) {
			active = append(active, authorization)
		}
	}
	if len(active) != len(p.Temp) {
		p.Temp = active
	}
	hash := fnv.New64a()
	if data, err := json.Marshal(struct {
		Global permission.Policy          `json:"g"`
		User   permission.Policy          `json:"u"`
		Temp   []permission.Authorization `json:"t"`
	}{p.Global, p.User, active}); err == nil {
		hash.Write(data)
	}
	return strconv.FormatUint(hash.Sum64(), 16)
}

// loadPoliciesFromState 从 gui-state 恢复权限状态（启动时调用一次）。
func loadPoliciesFromState() {
	state := loadGUIState()
	if len(state.GlobalPolicy) > 0 {
		if policy, err := permission.ParsePolicy(state.GlobalPolicy); err == nil {
			policies.setGlobal(policy)
		}
	}
	if len(state.UserPolicy) > 0 {
		if policy, err := permission.ParsePolicy(state.UserPolicy); err == nil {
			policies.setUser(policy)
		}
	}
	policies.mu.Lock()
	policies.Temp = state.Authorizations
	policies.mu.Unlock()
}

// savePoliciesToState 把权限状态写回 gui-state。
func savePoliciesToState() error {
	global, user, temp := policies.snapshot()
	policySaveMu.Lock()
	defer policySaveMu.Unlock()
	state := loadGUIState()
	state.GlobalPolicy = policyToMap(global)
	state.UserPolicy = policyToMap(user)
	state.Authorizations = temp
	return saveGUIState(state)
}

// policyToMap 把策略转成 JSON 友好的 map（Decision 是字符串别名，直接转）。
func policyToMap(policy permission.Policy) map[string]string {
	out := make(map[string]string, len(policy))
	for action, decision := range policy {
		out[string(action)] = string(decision)
	}
	return out
}

// policySaveMu 串行化策略写入：与 gui-state 的其他写者共用一把锁会更整齐，
// 但那些写者都在 PiService.configMu 下；这里单独一把锁 + 每次重新读文件，
// 足以避免"读-改-写"互相覆盖。
var policySaveMu sync.Mutex
