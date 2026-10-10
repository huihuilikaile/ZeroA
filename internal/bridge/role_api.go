package bridge

import (
	"encoding/json"
	"errors"
	"strings"

	"zeroa/internal/roles"
)

// 角色相关的绑定方法（前端 → Go）。
//
// 设计上只做四件事：列、取、存、删，外加"设成当前角色"。
// 没有"启动/切换"这类动作——切换角色本质上就是**用新参数重启 Pi**，
// 那件事由前端复用现有的 start(true) 流程（见 useAgent.switchRole）。
// 这样 Go 侧只有"数据 + 编译"，进程生命周期仍然只有一处（PiService.Start/Stop）。

// rolePayload 是保存角色时的入参形状：元数据 + 提示词正文。
//
// 提示词单独一个字段而不是塞进 Role：它在磁盘上是独立的 .md 文件
// （长文本好编辑、好 diff），在界面上也是独立的多行编辑器。
type rolePayload struct {
	Role   Role   `json:"role"`
	Prompt string `json:"prompt"`
}

// ListRoles 列出全部角色 + 各工作区上次用的角色。
//
// 这里顺带补种内置角色（只在文件缺失时写）：角色目录首次使用时是空的，
// 让"列角色"这件事顺便把它准备好，比在启动流程里塞一步更省心，也天然幂等。
func (s *PiService) ListRoles() (Result, error) {
	seedBuiltinRoles()
	s.pruneMissingSessionRoles()
	views := loadRoles()
	if views == nil {
		views = []roleView{}
	}
	s.configMu.Lock()
	state := loadGUIState()
	byWorkspace := map[string]string{}
	for key, role := range state.LastRoleByWorkspace {
		byWorkspace[key] = role
	}
	bySession := map[string]string{}
	for path, role := range state.RoleBySession {
		bySession[path] = role
	}
	s.configMu.Unlock()

	// 当前工作区的角色：以界面正在看的工作区为准（与文件浏览同源）
	s.mu.Lock()
	workspace := s.currentWorkspace
	activeRole := s.activeRole
	s.mu.Unlock()
	current := byWorkspace[normalizeDir(workspace)]

	payload, err := json.Marshal(map[string]any{
		"roles":           views,
		"roleByWorkspace": byWorkspace,
		"roleBySession":   bySession,
		"workspace":       workspace,
		"selected":        current,
		"processRole":     activeRole,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_roles", Success: true, Data: string(payload)}, nil
}

// GetRole 取单个角色（含提示词正文，供编辑）。
func (s *PiService) GetRole(id string) (Result, error) {
	view, ok := findRole(id)
	if !ok {
		return Result{}, errors.New("角色不存在或配置损坏: " + id)
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "get_role", Success: true, Data: string(payload)}, nil
}

// SaveRole 新建或更新角色（含提示词文件）。
//
// 校验都在 roles.saveRole 里：id 格式、名称、思考级别、提示词长度、提示词路径。
// 校验不过宁可让保存失败，也不留下"能存进去但一启动就炸"的配置。
func (s *PiService) SaveRole(payload string) (Result, error) {
	var body rolePayload
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		return Result{}, errors.New("角色数据格式不对: " + err.Error())
	}
	id, err := requireRoleID(body.Role.ID)
	if err != nil {
		return Result{}, err
	}
	body.Role.ID = id
	// 内置标记与补种版本由程序维护，不接受界面传值：
	// 否则复制一个内置角色就能让副本变成"删不掉的"。
	normalized := strings.ToLower(strings.TrimSpace(body.Role.ID))
	if builtinRoleIDs[normalized] {
		body.Role.Builtin = true
	} else {
		body.Role.Builtin = false
	}
	if err := saveRole(body.Role, body.Prompt); err != nil {
		return Result{}, err
	}
	Logf("save_role id=%s promptBytes=%d", body.Role.ID, len(body.Prompt))
	result, err := s.GetRole(body.Role.ID)
	if err != nil {
		return Result{}, err
	}
	result.Command = "save_role"
	return result, nil
}

// DeleteRole 删除角色。内置角色拒删；正在被某个工作区使用时提示先切走。
func (s *PiService) DeleteRole(id string) (Result, error) {
	normalized, err := requireRoleID(id)
	if err != nil {
		return Result{}, err
	}
	s.configMu.Lock()
	state := loadGUIState()
	inUse := ""
	for workspace, role := range state.LastRoleByWorkspace {
		if role == normalized {
			inUse = workspace
			break
		}
	}
	s.configMu.Unlock()
	if inUse != "" {
		return Result{}, errors.New("这个角色正在被工作区「" + inUse + "」使用：先切到别的角色再删")
	}
	if err := deleteRole(normalized); err != nil {
		return Result{}, err
	}
	// 会话徽标里的引用一起清掉，避免删完还显示一个不存在的角色
	s.configMu.Lock()
	state = loadGUIState()
	for workspace, role := range state.LastRoleByWorkspace {
		if role == normalized {
			delete(state.LastRoleByWorkspace, workspace)
		}
	}
	for path, role := range state.RoleBySession {
		if role == normalized {
			delete(state.RoleBySession, path)
		}
	}
	saveErr := saveGUIState(state)
	s.configMu.Unlock()
	if saveErr != nil {
		Logf("delete_role_state_save_failed id=%s err=%v", normalized, saveErr)
	}
	Logf("delete_role id=%s", normalized)
	payload, _ := json.Marshal(map[string]any{"id": normalized})
	return Result{Command: "delete_role", Success: true, Data: string(payload)}, nil
}

// SetActiveRole 把角色记成"当前工作区要用哪个"。
//
// 注意它**不启动进程**：切换角色必然要重启 Pi，而重启是界面的事
// （先问用户"会中断当前回合"，再 start(true)）。Go 侧只负责记住选择。
func (s *PiService) SetActiveRole(id string) (Result, error) {
	normalized := strings.ToLower(strings.TrimSpace(id))
	if normalized != "" {
		view, ok := findRole(normalized)
		if !ok {
			return Result{}, errors.New("角色不存在或配置损坏: " + id)
		}
		if view.Role.IsAgent() {
			return Result{}, errors.New("「" + view.Role.Name + "」是子智能体，不能当主会话角色（它只能被其他角色委派）")
		}
	}
	s.mu.Lock()
	workspace := s.currentWorkspace
	s.mu.Unlock()
	if workspace == "" {
		workspace = resolveCwd("")
	}
	s.configMu.Lock()
	state := loadGUIState()
	if state.LastRoleByWorkspace == nil {
		state.LastRoleByWorkspace = map[string]string{}
	}
	key := normalizeDir(workspace)
	if normalized == "" {
		delete(state.LastRoleByWorkspace, key)
	} else {
		state.LastRoleByWorkspace[key] = normalized
	}
	err := saveGUIState(state)
	s.configMu.Unlock()
	if err != nil {
		return Result{}, err
	}
	Logf("set_active_role workspace=%s role=%s", key, normalized)
	payload, _ := json.Marshal(map[string]any{"workspace": workspace, "role": normalized, "restartRequired": true})
	return Result{Command: "set_active_role", Success: true, Data: string(payload)}, nil
}

// pruneMissingSessionRoles 清掉"会话文件已经不在"的角色记忆。
//
// 为什么要在读列表时顺手做：文件可能被用户手工删掉、或被上一版程序删过
// （那一版还没有清理逻辑）。留着的坏处不是占空间，而是**打开一条不存在的
// 会话时会拿着一个陈旧角色去重启 Pi**——排查起来极费劲。
// 只在真的清掉了东西时才写文件，避免每次读列表都产生一次写盘。
//
// 与 gui-state 的其它读写一样持 configMu：读-改-写不持锁会在并发下丢更新
// （同文件的 DeleteRole / RememberSessionRole / SetActiveRole 都是这么持锁的）。
func (s *PiService) pruneMissingSessionRoles() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	state := loadGUIState()
	if len(state.RoleBySession) == 0 {
		return
	}
	removed := 0
	for path := range state.RoleBySession {
		if _, err := osStat(path); err != nil {
			delete(state.RoleBySession, path)
			removed++
		}
	}
	if removed == 0 {
		return
	}
	if err := saveGUIState(state); err != nil {
		Logf("prune_session_roles_failed removed=%d err=%v", removed, err)
		return
	}
	Logf("prune_session_roles removed=%d", removed)
}

// RememberSessionRole 记录"这条会话属于哪个角色"，供侧栏徽标与"打开会话切回角色"使用。
//
// 由前端在新建会话后调用，不是必须成功的操作：失败只影响徽标，不影响会话本身。
//
// ⚠️ 键要**按规范化路径去重**：同一个会话可能以不同写法传进来
// （C:\a\b.jsonl 与 c:/a/b.jsonl）。直接拿原串当键会写出两条记录，
// 而界面是按会话列表里的路径精确查的——查不到就表现为"角色没跟回来"。
// 去重后保留本次传入的写法（它就是界面此刻用的那个）。
func (s *PiService) RememberSessionRole(sessionPath, roleID string) (Result, error) {
	path := strings.TrimSpace(sessionPath)
	if path == "" {
		return Result{}, errors.New("缺少会话文件路径")
	}
	normalized := strings.ToLower(strings.TrimSpace(roleID))
	key := normalizeDir(path)
	s.configMu.Lock()
	state := loadGUIState()
	if state.RoleBySession == nil {
		state.RoleBySession = map[string]string{}
	}
	// 先删掉所有"同一个会话但写法不同"的旧键
	for existing := range state.RoleBySession {
		if normalizeDir(existing) == key {
			delete(state.RoleBySession, existing)
		}
	}
	if normalized != "" {
		state.RoleBySession[path] = normalized
	}
	err := saveGUIState(state)
	s.configMu.Unlock()
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "remember_session_role", Success: true}, nil
}

// ResetRole 把内置角色恢复成随程序分发的默认内容（提示词 + 元数据）。
//
// 只对内置角色开放：用户自建的角色没有"默认内容"可恢复。
func (s *PiService) ResetRole(id string) (Result, error) {
	normalized, err := requireRoleID(id)
	if err != nil {
		return Result{}, err
	}
	if err := roles.ResetBuiltin(normalized); err != nil {
		return Result{}, err
	}
	return s.GetRole(normalized)
}
