package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"zeroa/internal/permission"
	"strings"
	"sync"
	"time"

	"zeroa/internal/pi"
)

// 本文件负责「会话列表」这件事在服务层的半边：列表/打开/改名/删除的边界判断、
// 会话与工作区记忆（gui-state.json）、默认值的计算。
//
// 另一半——会话目录解析、文件扫描、索引缓存与历史读取——在 internal/sessionstore，
// 主包通过 sessions_alias.go 的别名继续用原来的名字调用。

// 配置目录的解析统一在 internal/pi（桌面端与命令行入口必须算出同一个目录）。
// 这里只做一层薄封装，保留 main 包内的调用点不变。
func agentDir() string { return pi.AgentDir() }

func agentDirKind() string { return pi.AgentDirKind() }

// ListSessions 列出某个工作目录下的会话，按最近修改倒序。
//
// cwd 为空时按 exe 同目录（与启动默认值一致），保证界面上「工作区」和
// 「实际启动 Pi 用的目录」永远是同一个。
func (s *PiService) ListSessions(cwd string) (Result, error) {
	root := sessionsRoot()
	if root == "" {
		return Result{}, errors.New("找不到会话目录（~/.pi/agent/sessions）")
	}
	target := normalizeDir(resolveCwd(cwd))
	sessions := scanWorkspaceSessions(root, target)
	if len(sessions) > 100 {
		sessions = sessions[:100] // 界面是最近列表，不是归档工具
	}

	payload, err := json.Marshal(map[string]any{
		"cwd":      resolveCwd(cwd),
		"root":     root,
		"sessions": sessions,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_sessions", Success: true, Data: string(payload)}, nil
}

// ListSessionsBatch 一次返回多个工作区的会话列表，给"侧栏同时展开多个分组"用。
//
// groups 与请求的 cwds 一一对应（顺序也一致），前端不需要再按路径反查；
// 每个工作区各自限 100 条，与 ListSessions 的行为保持一致。
func (s *PiService) ListSessionsBatch(cwds []string) (Result, error) {
	root := sessionsRoot()
	if root == "" {
		return Result{}, errors.New("找不到会话目录（~/.pi/agent/sessions）")
	}
	all := scanAllSessionsCached(root)
	type group struct {
		Cwd      string        `json:"cwd"`
		Sessions []sessionInfo `json:"sessions"`
	}
	groups := make([]group, 0, len(cwds))
	for _, cwd := range cwds {
		key := normalizeDir(resolveCwd(cwd))
		list := make([]sessionInfo, 0, 8)
		for _, session := range all {
			if session.CwdKey == key {
				list = append(list, session)
			}
		}
		if len(list) > 100 {
			list = list[:100]
		}
		groups = append(groups, group{Cwd: resolveCwd(cwd), Sessions: list})
	}
	payload, err := json.Marshal(map[string]any{"root": root, "groups": groups})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_sessions_batch", Success: true, Data: string(payload)}, nil
}

/**
 * NewSession 新建一条会话：**只把"当前会话"换成一个待创建的空会话，不起进程**。
 *
 * ## 为什么不立刻起进程
 *
 * 旧实现走 `navigate("", "", fresh=true)`，也就是立刻冷启动一个 Pi 进程。
 * 而实测冷启动是 1–3.5 秒（日志里两条 `session_runtime_started` 的间隔），
 * 用户点「新建对话」要等这么久，而绝大多数时候紧接着的动作是"想一下第一句话怎么写"。
 * 对齐 DSH 的做法：**会话就是日志**，进程只是"干活时按需借来的资源"——
 * 第一条消息到来时由 session_runtime.go 的 ensureActiveRuntime 现起（那时用户在等回答，
 * 1–3.5 秒是花在"等它开始干活"上，而不是花在"点了没反应"上）。
 *
 * 状态与返回形状都交给 enterPendingLocked 一份实现：**打开一条老会话**现在也是
 * 同一种状态（"用户在看它、但它还没有进程"），差别只是带不带 sessionPath。
 * 两处各写一遍迟早会漂。
 */
func (s *PiService) NewSession() (Result, error) {
	s.navigation.Lock()
	defer s.navigation.Unlock()

	s.mu.Lock()
	cwd := strings.TrimSpace(s.options.Cwd)
	workspace := s.currentWorkspace
	s.mu.Unlock()
	if cwd == "" {
		// 还没跑过 Start（没有参数模板）：退回"界面正在看的工作区"，至少给界面一个可显示的值。
		cwd = resolveCwd(workspace)
	}
	// 新会话还没有会话文件：path 传空串（pendingSessionFile 也跟着清空）。
	return s.enterPendingLocked("new_session", cwd, "")
}

/**
 * SwitchSession 打开一条已有会话。
 *
 * 两种情况：
 *  1. 这条会话**已经开着**（进程还活着）→ 换成跟它说话：零 RPC、不重载历史、
 *     不打断任何东西（这就是"切回去还是原来那一轮"）；
 *  2. 没开着 → **不起进程**，只记住"用户在看它"（pendingSessionFile），
 *     历史由 LoadSessionHistory 直接从 .jsonl 读出来给界面；
 *     进程留到第一条消息，由 ensureActiveRuntime 起，并且**先 switch_session 到
 *     这份文件再发 prompt**——否则用户那句话会写进一个刚出生的空会话里。
 *
 * 打开失败（不在会话目录内、文件不存在、首行不是会话头、会话的工作目录没了）
 * 一律报错且**不改动当前会话**：用户点了打不开的会话，手上这一轮不受影响。
 */
func (s *PiService) SwitchSession(sessionPath string) (Result, error) {
	target, err := s.requireSessionPath(sessionPath)
	if err != nil {
		return Result{}, err
	}
	header := readSessionHeader(target)
	if header.ID == "" {
		return Result{}, errors.New("无法读取会话文件")
	}
	/**
	 * 会话的工作目录必须还在。
	 *
	 * 进程还没起，但"这条会话根本打不开"这件事现在就能判定：会话是在哪个目录里
	 * 产生的，进程就得在哪个目录里起（文件树、相对路径全靠它）。
	 * 把这个检查留到第一条消息才做，用户会以为"历史读出来了、能聊"，
	 * 结果一发消息就报错——在"点开"这一步说清楚代价小得多。
	 */
	cwd := resolveCwd(header.Cwd)
	if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
		return Result{}, fmt.Errorf("会话的工作目录不存在：%s", cwd)
	}
	return s.navigate(target, cwd, false, "switch_session")
}

// turnRunning 判断此刻是否有回合在跑（**当前会话**的回合）。
//
// 两个来源取或：parentBusy 来自事件流（agent_start → true，agent_settled → false），
// runningWork 是前端上报的界面状态。
//
// ⚠️ 一进程一会话之后它**不再用于决定"能不能切换"**（切换已经不需要等待了），
// 现在只剩两个用途：关窗前的保护性判断、以及"父回合结束才投子智能体结果"的择时。
func (s *PiService) turnRunning() bool {
	s.mu.Lock()
	reported := s.runningWork
	s.mu.Unlock()
	return s.agentParentBusy() || reported
}

/**
 * 这里原本是"排队切换"那一套：LoadSession 在回合运行时只记住目标、等 agent_settled
 * 之后再执行，外加 PendingSession / runPendingSessionSwitch / performLoadSession。
 *
 * **一进程一会话之后它们全部退役**（见 session_runtime.go 的说明）：
 * 切换不再需要碰正在干活的进程，所以不需要"等"、不需要排队、也不需要"立即切换"这种
 * 显式中断动作。留着它们只会多出两套互相矛盾的状态机
 *（一套说"待切换"，另一套说"已经在别处跑了"），而且那套忙碌判断正是之前
 * "点了没反应"的根源。现在打开会话只有一条路：navigate。
 */

// GUI 会话记忆落在 Pi 配置目录，而不是只依赖 WebView localStorage。
// WebView 的存储分区可能随运行目录或版本变化而丢失，而配置目录跟随便携程序，
// 换机器、换构建都不会把"上次聊到哪"弄丢。
//
// 值的三态语义（缺一不可，否则"新建对话"会在重启后被回退逻辑撤销）：
//   - 键不存在   → 这个工作区还没有记录，可以回退到"最近一次会话"；
//   - 值是空串   → 用户显式新建了空会话，不要回退；
//   - 值是路径   → 显式记住的那条会话。
type guiState struct {
	Sessions map[string]string `json:"sessions"`
	// LastWorkspace 是"上次用的是哪个工作区"。必须存在后端：WebView 的
	// localStorage 会随 WebView2 用户数据目录变化而整体消失（实测确有这种情况），
	// 一旦丢了，重启后就会退回默认目录，界面上表现为"打开的不是我上次那份对话"。
	/**
	 * SkipBuiltinRoles：首启动**不补种内置角色**（默认 false = 补种）。
	 *
	 * 给"无角色包"用：用户要从零开始建角色，一个内置角色都不要。
	 * 放在这里而不是 Pi 的 settings.json：那是 Pi 在读的文件，工程上不该替它加键。
	 */
	SkipBuiltinRoles bool   `json:"skipBuiltinRoles,omitempty"`
	LastWorkspace    string `json:"lastWorkspace,omitempty"`
	// Workspaces 是侧栏的工作区列表（原样保存用户给的路径，去重时再规范化）。
	Workspaces []string `json:"workspaces,omitempty"`
	// Dismissed 是**用户明确删除过的工作区**（规范化键）。
	//
	// 没有它就必然出现"删了又回来"：启动时界面会补上默认工作区、上次工作区、
	// 以及"会话记忆里出现过的目录"，被删的那个只要还挂在这三条里的任何一条上
	// （尤其默认工作区就是 exe 的上级目录），下一次启动就又冒出来了。
	Dismissed []string `json:"dismissed,omitempty"`

	// LastRoleByWorkspace 记录"每个工作区上次用的是哪个角色"（工作区规范化键 → 角色 id）。
	//
	// 为什么按工作区记而不是全局一个：角色跟着**任务方向**走——同一个工作区
	// 通常一直在做同一类事（CTF 目录做 CTF、客户现场目录做应急），
	// 切工作区时把角色也带回去，比每次重新选一遍顺手。
	LastRoleByWorkspace map[string]string `json:"lastRoleByWorkspace,omitempty"`
	// RoleBySession 记录"每条会话是在哪个角色下产生的"（会话文件路径 → 角色 id）。
	// 侧栏靠它给历史会话打角色徽标：否则同一个工作区里的会话看起来一模一样。
	RoleBySession map[string]string `json:"roleBySession,omitempty"`

	// GlobalPolicy / UserPolicy 是权限引擎的两层策略（方案第十六节）；
	// Authorizations 是临时授权（方案第十七节，到点自动失效）。
	// 它们和会话记忆放在同一份文件里：都是"用户在这个应用上的选择"。
	GlobalPolicy   map[string]string          `json:"globalPolicy,omitempty"`
	UserPolicy     map[string]string          `json:"userPolicy,omitempty"`
	Authorizations []permission.Authorization `json:"authorizations,omitempty"`
}

func guiStatePath() string { return filepath.Join(agentDir(), "gui-state.json") }

// loadGUIState 读一次记忆文件。调用方必须持有 configMu（与其它配置写入互斥）。
func loadGUIState() guiState {
	state := guiState{Sessions: map[string]string{}}
	if data, err := os.ReadFile(guiStatePath()); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Sessions == nil {
		state.Sessions = map[string]string{}
	}
	return state
}

// saveGUIState 用「临时文件 + rename」落盘：中途失败不会留下半截 JSON。
func saveGUIState(state guiState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := guiStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	/**
	 * 临时文件名必须**每次都不一样**。
	 *
	 * 之前是固定的 `gui-state.json.tmp`：两个并发写入抢同一个临时文件，
	 * 后到的那个直接失败（实测报 "The process cannot access the file because it
	 * is being used by another process"），而失败意味着**这次状态更新丢了**。
	 * 换成带序号的名字后，写入之间不再互相干扰；最后落盘的是后一个，
	 * 对并发操作来说这是可接受的结果。
	 */
	guiStateWriteSeqMu.Lock()
	guiStateWriteSeq++
	seq := guiStateWriteSeq
	guiStateWriteSeqMu.Unlock()
	tmp := fmt.Sprintf("%s.%d.tmp", path, seq)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// guiStateWriteSeq 给临时文件名编号（见 saveGUIState 的注释）。
var (
	guiStateWriteSeqMu sync.Mutex
	guiStateWriteSeq   int64
)

// rememberSessionLocked 写入一条记忆。空路径写成显式的"已清空"标记。
// sessionPath 非空时只接受会话目录内、且确实属于该工作区的会话文件：
// 迟到的异步回调带着旧工作区写进来时，这里会挡住串工作区的污染。
func (s *PiService) rememberSessionLocked(key, sessionPath string) error {
	state := loadGUIState()
	if strings.TrimSpace(sessionPath) == "" {
		state.Sessions[key] = ""
		return saveGUIState(state)
	}
	target, err := s.requireSessionPath(sessionPath)
	if err != nil {
		return err
	}
	if actual := sessionCwd(target); actual != "" && actual != key {
		return fmt.Errorf("会话属于工作区 %s，不是 %s", actual, key)
	}
	state.Sessions[key] = target
	return saveGUIState(state)
}

// RememberSession 供前端在"打开/新建会话"后同步记忆（cwd 为空时不写）。
func (s *PiService) RememberSession(cwd, sessionPath string) (Result, error) {
	key := normalizeDir(resolveCwd(cwd))
	if key == "" {
		return Result{}, errors.New("缺少工作区")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := s.rememberSessionLocked(key, sessionPath); err != nil {
		return Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{"cwd": key, "sessionPath": strings.TrimSpace(sessionPath)})
	return Result{Command: "remember_session", Success: true, Data: string(payload)}, nil
}

// rememberCurrentSession 把"当前正在用的会话"记到它自己的工作区下。
// 工作区取自会话文件头（而不是界面当前目录），所以即使用户刚切走也不会记错地方。
func (s *PiService) rememberCurrentSession() {
	s.mu.Lock()
	current := s.sessionFile
	s.mu.Unlock()
	if strings.TrimSpace(current) == "" {
		return
	}
	target, err := s.requireSessionPath(current)
	if err != nil {
		return
	}
	key := sessionCwd(target)
	if key == "" {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	_ = s.rememberSessionLocked(key, target)
}

// GetRememberedSession 返回"进入这个工作区时该恢复哪条会话"。
//
// source 的取值：memory（显式记住）、recent（回退到该工作区最近一次会话）、
// cleared（用户主动开过新对话，不要恢复）、none（没有可恢复的会话）。
// 只有显式记住不够用：用户可能从没点过会话行，或记忆文件是新装的，
// 而磁盘上明明有历史——那种情况下"没有记忆"就是 bug 而不是设计。
func (s *PiService) GetRememberedSession(cwd string) (Result, error) {
	key := normalizeDir(resolveCwd(cwd))
	s.configMu.Lock()
	state := loadGUIState()
	memory, hasMemory := state.Sessions[key]
	cleared := hasMemory && strings.TrimSpace(memory) == ""
	if !cleared && strings.TrimSpace(memory) != "" {
		if target, err := s.requireSessionPath(memory); err == nil {
			if actual := sessionCwd(target); actual == "" || actual == key {
				s.configMu.Unlock()
				Logf("remembered key=%s source=memory path=%s", key, target)
				return rememberedResult(key, target, "memory")
			}
		}
		// 记录已失效（文件被删/移走）：当作没有记录，继续往下回退。
	}
	if cleared {
		s.configMu.Unlock()
		Logf("remembered key=%s source=cleared", key)
		return rememberedResult(key, "", "cleared")
	}
	s.configMu.Unlock()

	recent := newestSessionFor(key)
	if recent == "" {
		Logf("remembered key=%s source=none", key)
		return rememberedResult(key, "", "none")
	}
	Logf("remembered key=%s source=recent path=%s", key, recent)
	return rememberedResult(key, recent, "recent")
}

// rawOrNull 把可能为空的 data 变成合法的 JSON 值：json.RawMessage("") 会让
// Marshal 直接报错，而"命令没有 data"在协议里是正常情况。
func rawOrNull(data string) json.RawMessage {
	if strings.TrimSpace(data) == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(data)
}

// guiLogMu 串行化 Logf：trace 回调、委派监督器、运行时回收器都在各自的
// goroutine 里写日志，没有锁的话两行会交织成一行（排查时连一条完整记录都找不到），
// 超限判断与清空之间也会互相踩。
var guiLogMu sync.Mutex

// Logf 往 <配置目录>/gui-log.txt 追加一行诊断。上限 256KB，超过就**轮转**：
// 当前文件改名成 gui-log.1.txt（上一代完整保留），新文件从头写并记一行轮转标记。
//
// 为什么轮转而不是清空：这份日志是出问题时唯一的现场记录，"攒满就全丢"意味着
// 恰恰在跑了很久、最需要追溯的时刻它是空的。只留两代是有意的——它是诊断尾巴
// 不是归档，无限留代会把磁盘吃满。
//
// 为什么要有它：会话恢复这条链路横跨前端状态、后端记忆和工作区路径，
// 出问题时只看界面无法判断是哪一环断了。日志只记"决定"（用了哪个工作区、
// 恢复了哪条会话、成功还是失败），不记消息内容。
func Logf(format string, args ...any) {
	dir := agentDir()
	if dir == "" {
		return
	}
	guiLogMu.Lock()
	defer guiLogMu.Unlock()
	path := filepath.Join(dir, "gui-log.txt")
	if info, err := os.Stat(path); err == nil && info.Size() > 256*1024 {
		// Windows 上 Rename 不能覆盖已存在的目标：先删掉再改名。
		rotated := filepath.Join(dir, "gui-log.1.txt")
		_ = os.Remove(rotated)
		_ = os.Rename(path, rotated)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	line := time.Now().Format("2006-01-02 15:04:05.000") + " " + fmt.Sprintf(format, args...) + "\n"
	_, _ = file.WriteString(line)
}

// SetActiveWorkspace 记录"当前工作区"，并把用户给的路径并入侧栏列表。
//
// 这是重启恢复的起点：没有它，界面只能退回默认目录，会话记忆也就无从谈起。
func (s *PiService) SetActiveWorkspace(cwd string) (Result, error) {
	// 项目级工具清单跟着工作区走：换了工作区要重新加载一次
	// （<工作区>/.pi/tool-catalog.json 是团队共享的那一层）。
	defer s.loadToolCatalog()

	resolved := resolveCwd(cwd)
	key := normalizeDir(resolved)
	if key == "" {
		return Result{}, errors.New("缺少工作区")
	}
	s.mu.Lock()
	// 文件浏览的边界跟着"界面正在看的工作区"走，在 Pi 起来之前就先生效：
	// 切工作区时前端会先调这里，再重启进程，中间那段时间面板仍然要能列目录。
	s.currentWorkspace = resolved
	s.mu.Unlock()
	s.configMu.Lock()
	state := loadGUIState()
	state.LastWorkspace = key
	// 用户又主动切回这个工作区了：把它从"删过"名单里去掉，
	// 否则它虽然能用，却永远进不了启动时的自动补全。
	state.Dismissed = reviveWorkspace(state.Dismissed, key)
	state.Workspaces = appendWorkspace(state.Workspaces, resolved)
	err := saveGUIState(state)
	workspaces := append([]string(nil), state.Workspaces...)
	s.configMu.Unlock()
	if err != nil {
		return Result{}, err
	}
	Logf("set_active_workspace key=%s workspaces=%d", key, len(workspaces))
	payload, _ := json.Marshal(map[string]any{"cwd": resolved, "workspaces": workspaces})
	return Result{Command: "set_active_workspace", Success: true, Data: string(payload)}, nil
}

// inferLastWorkspace 在还没有 lastWorkspace 记录时（例如刚升级到本版本），
// 从会话记忆里倒推出"用户最后碰过哪个工作区"：取会话文件修改时间最新的那一条。
//
// 这是升级路径的关键一环：老版本只记了会话、没记工作区，不做推断的话，
// 用户升级后的第一次重启仍会退回默认目录——也就是他抱怨的那个现象。
func inferLastWorkspace(state guiState) (key, display string) {
	var bestTime time.Time
	for workspace, sessionPath := range state.Sessions {
		if strings.TrimSpace(sessionPath) == "" {
			continue
		}
		info, err := os.Stat(sessionPath)
		if err != nil {
			continue
		}
		if key == "" || info.ModTime().After(bestTime) {
			key, bestTime = workspace, info.ModTime()
			if display = sessionHeaderCwd(sessionPath); display == "" {
				display = workspace
			}
		}
	}
	return key, display
}

// appendWorkspace 按规范化路径去重后追加，保留用户原本的写法（大小写/分隔符）。
func appendWorkspace(list []string, path string) []string {
	key := normalizeDir(path)
	for _, item := range list {
		if normalizeDir(item) == key {
			return list
		}
	}
	list = append(list, path)
	if len(list) > 12 {
		list = list[len(list)-12:]
	}
	return list
}

// dismissWorkspace 记下"用户删过这个工作区"，并从列表里去掉。
func dismissWorkspace(list []string, key string) []string {
	for _, item := range list {
		if item == key {
			return list
		}
	}
	list = append(list, key)
	if len(list) > 64 {
		list = list[len(list)-64:]
	}
	return list
}

// reviveWorkspace 把工作区从"删过"名单里去掉：用户又主动用它了。
func reviveWorkspace(list []string, key string) []string {
	kept := make([]string, 0, len(list))
	for _, item := range list {
		if item != key {
			kept = append(kept, item)
		}
	}
	return kept
}

func dismissedSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, item := range list {
		set[item] = true
	}
	return set
}

func rememberedResult(key, sessionPath, source string) (Result, error) {
	payload, _ := json.Marshal(map[string]any{"cwd": key, "sessionPath": sessionPath, "source": source})
	return Result{Command: "get_remembered_session", Success: true, Data: string(payload)}, nil
}

/**
 * RenameSession 给一个会话改名（Pi 的 set_session_name）。
 *
 * `set_session_name` 只作用于**当前会话**，所以旧实现在改别人的名字时要
 * "切过去 → 改名 → 再切回来"（两次 switch_session）：回合运行中这一下就把
 * 正在跑的任务掐掉两次，所以那时只能拒绝改名。
 *
 * 一进程一会话之后改成：**在目标会话自己的进程里改**——
 *   · 它已经开着 → 直接对它发命令（完全不碰前台）；
 *   · 它没开着 → 起一个临时进程加载它，改完就关（同样不碰前台）。
 * 于是"改名会不会打断我手上的活"这个问题从根上消失了。
 */
func (s *PiService) RenameSession(sessionPath, name string) (Result, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return Result{}, errors.New("请输入会话名")
	}
	target, err := s.requireSessionPath(sessionPath)
	if err != nil {
		return Result{}, err
	}
	s.navigation.Lock()
	defer s.navigation.Unlock()

	// 目标会话已经开着：直接找它，别动别的进程。
	s.mu.Lock()
	var resident *sessionRuntime
	for _, candidate := range s.runtimes {
		if candidate.sessionFile != "" && normalizeDir(candidate.sessionFile) == normalizeDir(target) && runtimeAlive(candidate) {
			if resident == nil || candidate.lastUsed.After(resident.lastUsed) {
				resident = candidate
			}
		}
	}
	opts := s.options
	/**
	 * 判"参数模板准备好没有"，而不是判"有没有活着的进程"。
	 *
	 * 懒启动之后"一个进程都没有"是正常状态（刚进应用、刚打开一条历史会话），
	 * 而改名走的**不是**懒启动——它需要 Pi 真的去写会话名，所以下面会按需起一个
	 * 临时进程。那时只要有参数模板就够了；按进程数判会让"打开会话之后马上改名"
	 * 报一句莫名其妙的"请先启动 Pi"。
	 */
	ready := s.optionsReady
	s.mu.Unlock()
	if !ready {
		return Result{}, errors.New("请先启动 ZeroA")
	}

	if resident == nil {
		// 没开着：起一个临时进程（按会话头里的工作目录），改完立刻关掉。
		header := readSessionHeader(target)
		if header.ID == "" {
			return Result{}, errors.New("无法读取会话文件")
		}
		opts.Cwd = resolveCwd(header.Cwd)
		// 改名用的临时进程不派子智能体，角色传空（它改完就被关掉）
		runtime, err := s.startRuntime(opts, "")
		if err != nil {
			return Result{}, err
		}
		defer s.closeRuntime(runtime, "改名用的临时进程")
		result, err := runtimeRequest(runtime, "switch_session", map[string]any{"sessionPath": target})
		if err != nil {
			return result, err
		}
		var data struct {
			Cancelled bool `json:"cancelled"`
		}
		_ = json.Unmarshal([]byte(result.Data), &data)
		if data.Cancelled {
			return Result{}, errors.New("改名被扩展的 session_before_switch 钩子取消了")
		}
		resident = runtime
	}
	result, err := runtimeRequest(resident, "set_session_name", map[string]any{"name": trimmedName})
	invalidateSessionIndex(target)
	return result, err
}

// DeleteSession 删除一个会话文件。
//
// 删除当前会话时必须先成功切到新会话，再删除旧文件。这个顺序让操作具备安全的
// 失败语义：new_session 失败时旧历史仍在；删除失败时最多是已经切到了新会话，
// 不会出现「历史已经永久删除，但 Pi 还握着失效路径」的状态。
//
// 仍然只允许删会话目录里的 .jsonl 普通文件：这是把界面上的一次点击限制在
// 数据目录内的底线。
// dropSessionRole 删掉某条会话的角色记忆（会话本身已经被删了）。
//
// 放在 sessions.go 而不是 roles.go：它是"删会话"这条链路的一部分，
// 与角色存储的关系只是"写同一份状态文件"。
func dropSessionRole(sessionPath string) {
	if strings.TrimSpace(sessionPath) == "" {
		return
	}
	state := loadGUIState()
	if state.RoleBySession == nil {
		return
	}
	found := false
	for path := range state.RoleBySession {
		if normalizeDir(path) == normalizeDir(sessionPath) {
			delete(state.RoleBySession, path)
			found = true
		}
	}
	if !found {
		return
	}
	if err := saveGUIState(state); err != nil {
		Logf("drop_session_role_failed path=%s err=%v", sessionPath, err)
	}
}

// GetDefaults 把「界面该显示什么默认值」交给后端算。
func (s *PiService) DeleteSession(sessionPath string) (Result, error) {
	target, err := s.requireSessionPath(sessionPath)
	if err != nil {
		return Result{}, err
	}
	s.navigation.Lock()
	defer s.navigation.Unlock()

	s.mu.Lock()
	current := s.sessionFile
	// 这条会话是不是正开在一个进程里？开着就先把它收掉——
	// 否则那个进程会继续往一个已经被删掉的文件里写（写出来的东西没人能再看到）。
	var resident *sessionRuntime
	for _, candidate := range s.runtimes {
		if candidate.sessionFile != "" && normalizeDir(candidate.sessionFile) == normalizeDir(target) {
			if resident == nil || candidate.lastUsed.After(resident.lastUsed) {
				resident = candidate
			}
		}
	}
	s.mu.Unlock()
	wasActive := current != "" && normalizeDir(current) == normalizeDir(target)

	/**
	 * 顺序很重要：删当前会话时**先**把"当前会话"换成一条待创建的新会话，再删文件。
	 *
	 * 这个顺序让失败语义是安全的：
	 *   · 换会话失败 → 直接返回，原文件一个字没动（用户的历史还在）；
	 *   · 删除失败   → 最多是"已经切到新会话了"，不会出现"历史已永久删除、
	 *     而进程还握着一个失效路径"的半截状态。
	 * 反过来（先删后建）在 Pi 起不来的时候会直接把用户的历史吃掉——这条有专门的回归用例。
	 *
	 * ⚠️ 这里是 enterPendingLocked 而不是冷启动一条新会话：删掉当前会话之后
	 * 用户只是"回到没有会话的空位"，下一条消息才需要进程（见 NewSession 的说明）。
	 * 顺手冷启动一个进程，就又回到了"随便点一下等 1–3.5 秒"的老路上。
	 * 返回值形状与 NewSession 一致（前端按同一套 pending 处理）。
	 */
	var activation *Result
	if wasActive {
		s.mu.Lock()
		ready := s.optionsReady
		cwd := s.options.Cwd
		s.mu.Unlock()
		/**
		 * 从没跑过 Start（没有参数模板）时仍然**拒绝**删除当前会话。
		 *
		 * 即使换会话已经不需要进程了，这个门也要留着：那种状态下界面上的"当前会话"
		 * 本身就不成立（连往哪起进程都不知道），此时删掉用户的历史文件是不可逆的损失，
		 * 而拒绝的代价只是"下次启动正常之后再删"。
		 */
		if !ready {
			return Result{}, fmt.Errorf("无法先切换到新会话，原会话未删除: %w", errors.New("请先启动 ZeroA"))
		}
		result, err := s.enterPendingLocked("new_session", cwd, "")
		if err != nil {
			return Result{}, fmt.Errorf("无法先切换到新会话，原会话未删除: %w", err)
		}
		activation = &result
	}

	if err := os.Remove(target); err != nil {
		return Result{}, fmt.Errorf("删除失败: %w", err)
	}
	invalidateSessionIndex(target)

	// 文件删掉之后再收进程：反过来的话，删失败就白白把一个正在用的会话关掉了。
	if resident != nil {
		s.closeRuntime(resident, "会话文件已删除")
	}

	// 会话没了，它记的角色也要清掉。
	//
	// 不清的后果不是"多一行垃圾"，而是**打开一条不存在的会话时拿着一个陈旧的
	// 角色去重启 Pi**：用户会看到点了没反应，或者角色莫名其妙变了。
	// 清理放在删除成功之后——删失败时角色记忆还得留着。
	dropSessionRole(target)

	payload, err := json.Marshal(map[string]any{"path": target, "wasActive": wasActive, "activation": activation})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "delete_session", Success: true, Data: string(payload)}, nil
}

// requireSessionPath 把「界面上点的那一条」限制在会话目录内的 .jsonl 普通文件上。
// 不能用 strings.HasPrefix 判断目录归属：sessions-old/x.jsonl 也是 sessions 的字符串前缀。
func (s *PiService) requireSessionPath(sessionPath string) (string, error) {
	trimmed := strings.TrimSpace(sessionPath)
	if trimmed == "" {
		return "", errors.New("缺少会话文件路径")
	}
	rootValue := strings.TrimSpace(sessionsRoot())
	if rootValue == "" {
		return "", errors.New("找不到会话目录")
	}
	root, err := filepath.Abs(rootValue)
	if err != nil {
		return "", fmt.Errorf("会话目录无效: %w", err)
	}
	target, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("会话路径无效: %w", err)
	}
	// 已存在的路径解析符号链接，防止会话目录里的链接指向目录外。
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	if resolved, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
		target = resolved
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("会话文件不在会话目录内: %s", trimmed)
	}
	if !strings.EqualFold(filepath.Ext(target), ".jsonl") {
		return "", errors.New("只允许操作 .jsonl 会话文件")
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("会话文件不存在: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("会话路径不是普通文件")
	}
	return filepath.Clean(target), nil
}

// StatFolder 检查一个目录是否存在、是不是目录。
//
// 用途：切换工作区之前先验一下。用户可能把某个工作区目录删了或改名了，
// 这时应该当场在对话区报一条清楚的错误，而不是拿着不存在的路径去启动 Pi，
// 让错误变成一句"找不到文件"。
func (s *PiService) StatFolder(path string) (Result, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return Result{}, errors.New("缺少目录路径")
	}
	resolved := resolveCwd(trimmed)
	payload := map[string]any{"path": resolved, "exists": false, "isDir": false}
	if info, err := os.Stat(resolved); err == nil {
		payload["exists"] = true
		payload["isDir"] = info.IsDir()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "stat_folder", Success: true, Data: string(data)}, nil
}

// DefaultWorkspacePath 返回"默认工作区"：exe 同目录下的 workspace/。
//
// 它的存在意义是**永远删不掉的那个工作区**：用户可以把别的工作区都删掉，
// 这里始终留着一块能干活的地方，不会出现"一个工作区都没有"的死局。
func DefaultWorkspacePath() string {
	dir := exeDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "workspace")
}

// EnsureDefaultWorkspace 保证默认工作区存在（不存在就建），返回它的路径。
// 建不出来（安装目录只读）就返回空串，界面上只是少一个不可删的工作区，不影响使用。
func EnsureDefaultWorkspace() string {
	path := DefaultWorkspacePath()
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		Logf("default_workspace_create_failed path=%s err=%v", path, err)
		return ""
	}
	return path
}

// DeleteWorkspace 删除一个工作区：移除登记、删掉它名下的会话文件，**不动磁盘目录**。
//
// 三条拒绝规则，顺序即优先级：
//  1. 默认工作区不能删；
//  2. 当前正在使用的工作区不能删（Pi 的会话就在这里面）；
//  3. 当前会话文件属于它时同样算"正在使用"（切工作区与切会话之间有中间态）。
//
// 只有这些都不成立——也就是这个工作区里的会话全部停下来了——才真的删。
// 删目录本身不在职责内：那是用户的文件，不是应用的数据。
func (s *PiService) DeleteWorkspace(cwd string) (Result, error) {
	resolved := resolveCwd(cwd)
	key := normalizeDir(resolved)
	if key == "" {
		return Result{}, errors.New("缺少工作区")
	}
	if def := normalizeDir(DefaultWorkspacePath()); def != "" && key == def {
		return Result{}, errors.New("默认工作区不能删除")
	}

	s.mu.Lock()
	activeDir := s.workspaceDir
	viewing := s.currentWorkspace
	current := s.sessionFile
	s.mu.Unlock()
	/**
	 * 什么时候拒绝删一个工作区：**"进程用过它"并且"界面正在看它"**。
	 *
	 * 为什么不能只看 workspaceDir（这是用户实测报的那个 bug）：懒启动之后
	 * 切工作区不再需要起进程（历史从文件读），于是 workspaceDir 只在"真的起过进程"
	 * 时才更新，会一直停在最后干活的那个目录。用户在 web_sever 干过活、切回 workspace，
	 * 再删 web_sever 就被一句"工作区正在使用：先切到别的工作区"挡住——
	 * 而那个工作区里根本没有任何东西在跑。
	 *
	 * 加上"界面正在看它"这一半之后：过期的 workspaceDir 单独出现不再误拒；
	 * 而"你正在用的那个工作区不能删"这条原有语义（以及指望它的用例）完整保留。
	 */
	if activeDir != "" && normalizeDir(activeDir) == key &&
		(viewing == "" || normalizeDir(viewing) == key) {
		Logf("workspace_delete_refused key=%s reason=active_workspace activeDir=%s viewing=%s", key, activeDir, viewing)
		return Result{}, errors.New("工作区正在使用：先切到别的工作区，等它里面的会话都停下来再删")
	}
	if current != "" && sessionCwd(current) == key {
		// 记下完整判定依据：这条拒绝以前只报一句话，用户看不到"为什么"。
		// 实测踩过：明明一个会话文件都没有，却被判定成"还有会话被打开着"。
		Logf("workspace_delete_refused key=%s reason=current_session sessionFile=%s sessionCwd=%s",
			key, current, sessionCwd(current))
		return Result{}, errors.New("工作区正在使用：它里面还有会话正被 ZeroA 打开着")
	}

	root := sessionsRoot()
	if root == "" {
		return Result{}, errors.New("找不到会话目录")
	}
	deleted := 0
	for _, session := range scanWorkspaceSessions(root, key) {
		target, err := s.requireSessionPath(session.Path)
		if err != nil {
			continue // 越界或已消失的直接跳过，删除不因为个别文件失败而中断
		}
		if err := os.Remove(target); err != nil {
			return Result{}, fmt.Errorf("删除会话失败: %w", err)
		}
		deleted++
	}

	// 记忆与侧栏列表一起清掉，否则下次启动它会带着"记忆"又冒出来
	s.configMu.Lock()
	state := loadGUIState()
	kept := make([]string, 0, len(state.Workspaces))
	for _, workspace := range state.Workspaces {
		if normalizeDir(resolveCwd(workspace)) != key {
			kept = append(kept, workspace)
		}
	}
	state.Workspaces = kept
	// 记进"删过"名单：启动时不再把它当成默认/上次/记忆里的工作区补回来
	state.Dismissed = dismissWorkspace(state.Dismissed, key)
	// 被移除的那个如果正好是"上次工作区"，也要一起清掉：否则下次启动会依据它
	// 把这一行又加回列表——用户刚删掉的行自己长回来，是最容易被当成"没生效"的行为。
	if state.LastWorkspace == key {
		state.LastWorkspace = ""
	}
	delete(state.Sessions, key)
	saveErr := saveGUIState(state)
	s.configMu.Unlock()
	if saveErr != nil {
		return Result{}, saveErr
	}
	Logf("delete_workspace key=%s sessions=%d", key, deleted)

	payload, _ := json.Marshal(map[string]any{"cwd": resolved, "sessions": deleted})
	return Result{Command: "delete_workspace", Success: true, Data: string(payload)}, nil
}

// GetDefaults 把「界面该显示什么默认值」交给后端算。
// 目前是：工作目录（exe 同目录）、会话根目录、可执行文件路径、运行模式。
//
// 运行模式不再由界面选择：默认就是本机真实 Pi（mode=real）。
// 保留 PI_GUI_MODE=mock 这个环境变量，是为了让自动化测试能在不花钱、
// 不依赖本机 Pi 配置的情况下跑完整个界面。
func (s *PiService) GetDefaults() (Result, error) {
	exe, _ := os.Executable()
	mode := strings.TrimSpace(os.Getenv("PI_GUI_MODE"))
	if mode != "mock" {
		mode = "real"
	}
	// 供界面显示"当前用的是哪个 Pi"（自带 / 全局 / 自定义），不启动进程
	piSource := pi.DescribeCLI("")
	s.configMu.Lock()
	state := loadGUIState()
	dismissed := dismissedSet(state.Dismissed)
	workspaces := make([]string, 0, len(state.Workspaces))
	for _, item := range state.Workspaces {
		if !dismissed[normalizeDir(resolveCwd(item))] {
			workspaces = append(workspaces, item)
		}
	}
	// 记忆里存的是**规范化**键（小写 + 正斜杠），而界面要显示磁盘上的原始写法。
	// 两者混用会让前端把同一个目录当成两个（工作区列表出现重复行），
	// 所以这里统一还原成"用户认识的那个路径"，并且用规范化比较去重。
	lastKey := strings.TrimSpace(state.LastWorkspace)
	if lastKey == "" {
		lastKey, _ = inferLastWorkspace(state)
	}
	// 被删过的工作区不能当"上次工作区"：否则它下一次启动又回来了。
	if dismissed[lastKey] {
		lastKey = ""
	}
	lastWorkspace := ""
	if lastKey != "" {
		// 优先用工作区列表里的写法：那是用户在对话框里选的路径，大小写与分隔符
		// 就是磁盘上的样子。退而求其次才用会话头里的 cwd —— 它可能被旧版本
		// 喂进去的规范化键污染成小写（e:\foo\bar），虽然能用但看着不对。
		for _, item := range workspaces {
			if normalizeDir(resolveCwd(item)) == lastKey {
				lastWorkspace = item
				break
			}
		}
		if lastWorkspace == "" {
			if sessionPath := state.Sessions[lastKey]; strings.TrimSpace(sessionPath) != "" {
				lastWorkspace = sessionHeaderCwd(sessionPath)
			}
		}
		if lastWorkspace == "" {
			lastWorkspace = lastKey // 退化情况：只有键，没有已知写法
		}
		workspaces = appendWorkspace(workspaces, lastWorkspace)
	}
	// 侧栏列表也补上"会话记忆里出现过的目录"：localStorage 一旦丢失，
	// 只有这份文件还记得用户去过哪些工作区，否则侧栏会只剩当前一个。
	for _, sessionPath := range state.Sessions {
		if strings.TrimSpace(sessionPath) == "" {
			continue
		}
		display := sessionHeaderCwd(sessionPath)
		if display == "" {
			continue
		}
		if dismissed[normalizeDir(display)] {
			continue
		}
		if info, err := os.Stat(display); err != nil || !info.IsDir() {
			continue
		}
		workspaces = appendWorkspace(workspaces, display)
	}
	defaultWorkspace := EnsureDefaultWorkspace()
	defaultProvider, defaultModel, defaultThinking := piConfiguredDefaults()
	// 启动目录：默认是 exe 的上一级（构建目录会回退），但如果用户把这个目录
	// 当工作区删掉了，就不能再拿它当默认——否则"删了又回来"永远治不好。
	// 这种情况下退到那个删不掉的默认工作区。
	defaultCwd := resolveCwd("")
	if dismissed[normalizeDir(defaultCwd)] {
		if defaultWorkspace != "" {
			defaultCwd = defaultWorkspace
		}
	}
	// 顺带把"这次会进入哪个工作区"告诉文件浏览层：界面可能在 Pi 启动之前就
	// 打开文件面板（面板开合状态是记住的），那时它需要一个边界。
	activeForFiles := lastWorkspace
	if activeForFiles == "" {
		activeForFiles = defaultCwd
	}
	s.configMu.Unlock()
	s.mu.Lock()
	if strings.TrimSpace(activeForFiles) != "" {
		s.currentWorkspace = activeForFiles
	}
	s.mu.Unlock()
	payload, err := json.Marshal(map[string]any{
		"cwd":          defaultCwd,
		"exe":          exe,
		"exeDir":       exeDir(),
		"sessionsRoot": sessionsRoot(),
		"mode":         mode,
		// 默认工作区（exe 同目录的 workspace/）：永远删不掉的那一个，
		// 保证用户把别的工作区都删了之后还有地方能干活。
		"defaultWorkspace": defaultWorkspace,
		// 上次用的工作区：重启后靠它回到用户真正用过的那个目录，而不是默认目录。
		// 它必须来自后端文件——WebView 的 localStorage 实测会整体丢失。
		"lastWorkspace": lastWorkspace,
		"workspaces":    workspaces,
		// 用户删过的工作区：前端要据此忽略自己 localStorage 里残留的那一份
		"dismissed": state.Dismissed,
		// 用的是哪个 Pi：设置页显示"应用自带 1.0.2 / 全局安装 / 自定义路径"，
		"piPath":    piSource.Path,
		"piKind":    piSource.Kind,
		"piVersion": piSource.Version,
		// 配置目录（便携 .pi 还是用户目录）也告诉界面，便于排查"配置改了没生效"
		// 调试开关：打开后界面会渲染一行滚动/跟随的实时指标。
		// 存在的意义是让"自动滚动是否生效"这类问题**可被测量**，
		// 而不是靠肉眼看截图猜（UIA 读不到 DOM 属性，只能读文本）。
		"debug": strings.TrimSpace(os.Getenv("PI_GUI_DEBUG")) == "1",
		// 是否加载扩展/技能/模板：界面开关的初始状态
		"loadResources": loadResourcesDefault(),
		"agentDir":      agentDir(),
		"agentDirKind":  agentDirKind(),
		// Pi 配置的默认模型/思考级别（settings.json）：进程还没起时，
		// 界面靠它们显示"将要使用哪个模型"（模型徽标）。
		"defaultProvider":      defaultProvider,
		"defaultModel":         defaultModel,
		"defaultThinkingLevel": defaultThinking,
		// 诊断日志的完整路径：会话恢复/工作区记忆出问题时看这个文件
		"logPath": filepath.Join(agentDir(), "gui-log.txt"),
	})
	if err != nil {
		return Result{}, err
	}
	Logf("get_defaults defaultCwd=%s lastWorkspace=%s workspaces=%d agentDir=%s",
		resolveCwd(""), lastWorkspace, len(workspaces), agentDir())
	return Result{Command: "get_defaults", Success: true, Data: string(payload)}, nil
}

// CreateFolder 在工作区里新建一个文件夹，并把它作为新的工作目录返回。
//
// 「新建文件夹」不是装饰：它决定 Pi 接下来在哪里读写文件、会话按哪个目录归档。
// name 允许两种写法：
//   - 相对名（如 notes）：在 parentDir 下创建；
//   - 绝对路径：直接创建该路径（方便把工作区指到已有目录）。
func (s *PiService) CreateFolder(parentDir, name string) (Result, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return Result{}, errors.New("请输入文件夹名")
	}
	// 只取单层名字，避免 "a/b" 这类写法在界面上表现得像路径却悄悄多建几层
	if strings.ContainsAny(trimmed, `/\`) && !filepath.IsAbs(trimmed) {
		return Result{}, errors.New("相对名不能带路径分隔符；要指定路径请填绝对路径")
	}
	parent := resolveCwd(parentDir)
	target := trimmed
	if !filepath.IsAbs(target) {
		target = filepath.Join(parent, target)
	}
	target = filepath.Clean(target)

	info, err := os.Stat(target)
	switch {
	case err == nil && !info.IsDir():
		return Result{}, fmt.Errorf("同名文件已存在: %s", target)
	case err == nil && info.IsDir():
		// 已经是目录：当成"切到这个工作区"，不再报错
	case os.IsNotExist(err):
		if err := os.MkdirAll(target, 0o755); err != nil {
			return Result{}, fmt.Errorf("创建文件夹失败: %w", err)
		}
	default:
		return Result{}, err
	}

	payload, err := json.Marshal(map[string]any{"path": target})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "create_folder", Success: true, Data: string(payload)}, nil
}

// loadResourcesDefault 是"是否加载扩展/技能/模板"的默认值。
//
// 默认 true：界面的斜杠命令面板靠 get_commands，而它返回的正是扩展命令、
// 提示模板与技能 —— 全关掉时列表永远是空的（用户会以为"输入 / 没反应"）。
// 环境变量 PI_GUI_NO_RESOURCES=1 可以关掉（用于排障：确认某个问题是不是扩展引起的）。
func loadResourcesDefault() bool {
	return strings.TrimSpace(os.Getenv("PI_GUI_NO_RESOURCES")) != "1"
}
