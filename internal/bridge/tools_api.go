package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"zeroa/internal/permission"
	toolkit "zeroa/internal/tools"
)

// 工具服务（方案的 P7 / 第二十四节）与权限服务（P10 / 第十六、十七节）。
//
// 这两个服务都只做"检测与策略"，不下载、不安装、不自动放行：
// 第一版的边界就是"让用户看清这台机器有什么、当前允许什么"。

// ListTools 列出工具注册表 + 本机检测结果 + 能力清单。
func (s *PiService) ListTools() (Result, error) {
	// 目录没加载过就先加载（前端的调用顺序不保证，这里兜底）
	s.ensureToolCatalog()

	statuses := toolkit.DetectAll(false)
	available, missing := 0, 0
	for _, status := range statuses {
		if status.Available {
			available++
		} else {
			missing++
		}
	}
	catalog := toolkit.ActiveCatalog()
	piToolList, piToolSource := s.sessionTools()
	packages := readPackages()

	payload, err := json.Marshal(map[string]any{
		// ① 本机环境工具（JSON 清单驱动 + 实时探测）
		"tools":        statuses,
		"available":    available,
		"missing":      missing,
		"capabilities": toolkit.KnownCapabilities(),
		"capLabels":    toolkit.CapabilityLabels(),
		"conflicts":    toolkit.ToolConflicts(),
		// ② 模型可调用的工具（自动加载：会话里的真实清单）
		"piTools":        piToolList,
		"piToolsSource":  piToolSource,
		"piPackages":     s.installedPiPackages(toolNamesOf(piToolList)).Items,
		"packagesSource": s.installedPiPackages(toolNamesOf(piToolList)).Source,
		// ③ MCP 服务器（mcp.json + mcp-cache.json）
		"mcpServers": loadMcpServerInfos(),
		// 目录本身：来源与问题（"我改了为什么没生效"要能查）
		"catalogPaths":    catalog.Paths,
		"catalogIssues":   catalog.Issues,
		"catalogUserPath": toolkit.DefaultCatalogPath(agentDir()),
		"directions":      catalog.Directions,
		"catalogRules":    catalog.Rules,
		"checkedAtUnix":   time.Now().Unix(),
		"webCapable":      hasWebPackage(packages),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_tools", Success: true, Data: string(payload)}, nil
}

func (s *PiService) DetectTools(force bool) (Result, error) {
	// 界面上点「重新检测」= 检测 + 把新发现的路径写回清单。
	//
	// 为什么不是只跑 DetectAll：检测的价值一半在"记住"——工具装在 PATH 之外时
	// （工具箱自带目录、便携包、pip 的 Scripts），只有写回路径下次才能直接命中。
	// 只由显式动作触发，不在启动时偷偷改用户的文件。
	detailed, err := s.CatalogCheck(true)
	if err != nil {
		return Result{}, err
	}
	if force {
		return detailed, nil
	}
	return s.ListTools()
}

// ResolveRole 预览某个角色的编译结果（方案第二十三节的 "Effective Configuration"）。
//
// 这是角色页最有用的一块：用户看到的不是"我写了什么"，而是
// "这台机器上实际会用什么启动 Pi"——包括解析后的工具、缺失的能力、
// 生效的权限与循环策略。
func (s *PiService) ResolveRole(id string) (Result, error) {
	view, ok := findRole(id)
	if !ok {
		return Result{}, errors.New("角色不存在或配置损坏: " + id)
	}
	loadResources := s.loadResources || !s.loadResourcesSet
	compiled, err := compileRole(view.Role, loadResources)
	if err != nil {
		return Result{}, err
	}
	engine := policies.engine(view.Role.Permissions)
	payload, err := json.Marshal(map[string]any{
		"roleId":       compiled.RoleID,
		"spawn":        compiled.Spawn,
		"runtime":      compiled.Runtime,
		"capabilities": compiled.Capabilities,
		"resolutions":  compiled.Resolutions,
		"environment":  compiled.Environment,
		"permissions":  engine.Evaluate(time.Now()),
		"loop":         compiled.Loop,
		"warnings":     compiled.Warnings,
		"effective": map[string]any{
			// 这些都是"实际会生效的值"，不是角色文件里写了什么
			"tools":        view.Role.Tools,
			"excludeTools": compiled.Spawn.ExcludeTools,
			"skills":       view.Role.Skills,
			"model":        compiled.Runtime.ModelID,
			"thinking":     compiled.Runtime.ThinkingLevel,
			"promptFiles":  compiled.Spawn.AppendSystemPrompt,
		},
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "resolve_role", Success: true, Data: string(payload)}, nil
}

// GetPermissions 返回四层策略、临时授权与最终决定。
func (s *PiService) GetPermissions() (Result, error) {
	global, user, temp := policies.snapshot()
	// 角色层：当前工作区选中的角色（没有就用空策略）
	rolePolicy := permission.Policy{}
	if id := s.activeRoleForWorkspace(); id != "" {
		if view, ok := findRole(id); ok {
			rolePolicy = view.Role.Permissions
		}
	}
	engine := policies.engine(rolePolicy)
	payload, err := json.Marshal(map[string]any{
		"actions":    permission.Actions,
		"global":     global,
		"user":       user,
		"role":       rolePolicy,
		"evaluated":  engine.Evaluate(time.Now()),
		"temporary":  temp,
		"rolesUsing": s.activeRoleForWorkspace(),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "get_permissions", Success: true, Data: string(payload)}, nil
}

// SetPermissionPolicy 改一层策略（layer = global / user）。
//
// 为什么允许用户改全局：这套工具的默认值是"偏保守"（写文件要问、网络要问），
// 但每个人的工作方式不同。改完之后所有角色的最终权限跟着变——
// 因为全局层是取交集的一方，改它一定生效。
func (s *PiService) SetPermissionPolicy(layer string, payload string) (Result, error) {
	var raw map[string]string
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return Result{}, errors.New("策略格式不对: " + err.Error())
	}
	policy, err := permission.ParsePolicy(raw)
	if err != nil {
		return Result{}, err
	}
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case "global":
		policies.setGlobal(policy)
	case "user":
		policies.setUser(policy)
	default:
		return Result{}, errors.New("只能改 global 或 user 两层（角色层在角色里编辑）")
	}
	if err := savePoliciesToState(); err != nil {
		return Result{}, err
	}
	Logf("set_permission_policy layer=%s actions=%d", layer, len(policy))
	return s.GetPermissions()
}

// AuthorizePermission 给某个动作一条临时授权（方案第十七节）。
//
// minutes=0 时按 30 分钟处理；上限 12 小时——临时授权的意义就是"临时"，
// 让它能变成永久授权就等于没有授权。
func (s *PiService) AuthorizePermission(action string, minutes int, reason string) (Result, error) {
	target := permission.Action(strings.TrimSpace(action))
	if !permission.Known(target) {
		return Result{}, &permission.UnknownActionError{Action: action}
	}
	if minutes <= 0 {
		minutes = 30
	}
	if minutes > 720 {
		minutes = 720
	}
	until := time.Now().Add(time.Duration(minutes) * time.Minute)
	policies.authorize(target, until, strings.TrimSpace(reason), "用户")
	if err := savePoliciesToState(); err != nil {
		return Result{}, err
	}
	Logf("authorize_permission action=%s until=%s reason=%s", target, until.Format(time.RFC3339), reason)
	return s.GetPermissions()
}

// RevokePermission 撤销临时授权。
func (s *PiService) RevokePermission(action string) (Result, error) {
	target := permission.Action(strings.TrimSpace(action))
	if !permission.Known(target) {
		return Result{}, &permission.UnknownActionError{Action: action}
	}
	policies.revoke(target)
	if err := savePoliciesToState(); err != nil {
		return Result{}, err
	}
	Logf("revoke_permission action=%s", target)
	return s.GetPermissions()
}

// GetLoopState 返回任务循环当前状态（轮次、已用时、是否卡住、最近一次决策）。
func (s *PiService) GetLoopState() (Result, error) {
	snapshot := s.LoopSnapshot()
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "get_loop_state", Success: true, Data: string(payload)}, nil
}

// StopLoop 手动停止循环（方案第十九节的 Manual Abort）。
//
// 注意它**不**中断当前回合：停当前回合是 abort 的事。这里停的是"循环还会不会
// 自动往下发消息"，两件事分开用户才不会误操作。
func (s *PiService) StopLoop() (Result, error) {
	s.loop.Pause("用户手动停止")
	Logf("stop_loop")
	return s.GetLoopState()
}

// activeRoleForWorkspace 取当前工作区选中的角色 id（没有就空串）。
//
// 优先级：进程此刻用的角色 > 工作区记住的角色。前者的意思是"这就是实际情况"，
// 后者是"下次启动会用它"——权限页面显示的是前者。
func (s *PiService) activeRoleForWorkspace() string {
	s.mu.Lock()
	active := s.activeRole
	workspace := s.currentWorkspace
	s.mu.Unlock()
	if active != "" {
		return active
	}
	if workspace == "" {
		workspace = resolveCwd("")
	}
	state := loadGUIState()
	return state.LastRoleByWorkspace[normalizeDir(workspace)]
}
