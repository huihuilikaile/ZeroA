package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"zeroa/internal/agents"
)

// 端到端：父会话派一个子智能体 → 子进程真的起来、跑完 → 结果投递回父会话。
//
// 全程用 mock Pi（父与子都是），所以**不花一分钱 token**，但走的是真代码路径：
// 真进程、真 RPC、真 follow_up。这是"父 Agent 派活后继续干活、子 Agent 完成后
// 结果回到父会话"这条链路的验收测试。
func TestDelegationEndToEndWithMockPi(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	// 1) 父角色：允许派 ctf-recon（要求先审计）
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{
			Enabled:      true,
			Mode:         "background",
			RequireAudit: true,
			AllowedRoles: []string{"ctf-recon"},
			MaxChildren:  2,
			MaxParallel:  1,
		},
	}, "你是 CTF 综合角色")
	// 2) 子角色：只读 + 独立提示词（kind=agent：只有单独定义的子智能体才能被派）
	saveRoleForTest(t, service, Role{
		ID: "ctf-recon", Name: "CTF 资料检索", Kind: "agent",
		Tools: []string{"read", "grep"},
	}, "你是资料检索子智能体")

	// 3) 起一个真实的父会话（mock Pi）
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)

	// 父必须先"审计"过（工具调用），否则委派会被硬闸门挡住
	service.noteParentToolCall("")

	// 4) 派活：必须立刻返回句柄（父要继续干活）
	started := time.Now()
	result, err := service.DelegateTask("ctf-recon", "查一下这道题的常见解法")
	if err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("派活应当立刻返回（父要继续干活），实际耗时 %s", elapsed)
	}
	var handle struct {
		AgentID string `json:"agentId"`
		RoleID  string `json:"roleId"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(result.Data), &handle); err != nil {
		t.Fatalf("解析返回: %v", err)
	}
	if handle.AgentID == "" || handle.RoleID != "ctf-recon" {
		t.Fatalf("返回的句柄不对: %+v", handle)
	}
	if !strings.Contains(handle.Message, "继续") {
		t.Errorf("回给模型的话要说清「你可以继续干活」：%q", handle.Message)
	}

	// 5) 等子智能体**这一轮**得出结论
	//
	//    判据用 WaitTurn 而不是 Wait：默认形态是"可继续"，一轮结束后它是 idle
	//    （进程还在、还能继续追问），要等它进终态得等到空闲回收（默认 5 分钟）。
	sup := service.supervisorFor()
	inst, ok := sup.WaitTurn(context.Background(), handle.AgentID, 30*time.Second)
	if !ok {
		t.Fatal("WaitTurn 找不到子智能体")
	}
	if !inst.TurnSettled() {
		t.Fatalf("子智能体应当得出这一轮的结论，实际 %s（err=%s）", inst.Status, inst.Err)
	}
	if strings.TrimSpace(inst.Result) == "" {
		t.Error("子智能体应当产出结果")
	}
	// 默认形态是可继续：跑完一轮不释放进程，状态是 idle 且标记"这一轮正常完成"
	if inst.Mode != agents.ModeContinuable {
		t.Errorf("默认应当是 continuable 形态，得到 %q", inst.Mode)
	}
	if inst.Status != agents.StatusIdle || !inst.LastTurnCompleted {
		t.Errorf("可继续子智能体跑完一轮应当是 idle + LastTurnCompleted，得到 %s/%v", inst.Status, inst.LastTurnCompleted)
	}
	// 子会话目录独立：完整过程留在那里，父上下文只回摘要
	if inst.SessionDir == "" || !strings.Contains(inst.SessionDir, handle.AgentID) {
		t.Errorf("子智能体应当有自己的会话目录: %q", inst.SessionDir)
	}

	// 6) 结果必须投递进父会话**并且真的被处理**。
	//
	//    这里断言的是"父会话的对话里出现了这条结果"，而不是"队列里有东西"：
	//    父会话此刻是空闲的，投递走 prompt 直接开始新一轮——
	//    之前用 follow_up 时结论只会躺在队列里（界面上显示"队列 1"），
	//    父 Agent 不被唤醒，要用户再敲一条消息才处理（实测踩过）。
	deadline := time.Now().Add(15 * time.Second)
	var delivered string
	for time.Now().Before(deadline) {
		messages, err := service.GetMessages()
		if err == nil && strings.Contains(messages.Data, "[子智能体结果]") {
			delivered = messages.Data
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if delivered == "" {
		t.Fatal("结果没有进父会话的对话（父空闲时必须用 prompt 唤醒它，不能只排进队列）")
	}
	for _, want := range []string{"[子智能体结果]", handle.AgentID, "ctf-recon"} {
		if !strings.Contains(delivered, want) {
			t.Errorf("投递消息缺少 %q", want)
		}
	}
	// 队列里不该有残留：有残留就说明投递方法选错了
	if state, err := service.GetState(); err == nil && strings.Contains(state.Data, `"pendingMessageCount":1`) {
		t.Error("结果同时排进了队列：父空闲时应当直接 prompt，不要留一份在队列里")
	}
}

// 闸门：没审计不许派人；角色没声明不许派人；子角色不在白名单也不许。
func TestDelegationGates(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)

	// 角色没声明 delegation → 拒绝
	saveRoleForTest(t, service, Role{ID: "ctf", Name: "CTF 综合"}, "")
	service.activeRole = "ctf"
	if _, err := service.DelegateTask("ctf-recon", "任务"); err == nil {
		t.Error("角色没声明 delegation 时应当拒绝")
	}

	// 开了委派但白名单为空 → 拒绝（空白名单的语义是"不允许派任何角色"，
	// 曾经被漏掉：Supervisor 只在白名单非空时校验，空切片变成了"全放开"）
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true},
	}, "")
	service.activeRole = "ctf"
	if _, err := service.DelegateTask("ctf-recon", "任务"); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Errorf("空白名单应当拒绝派任何角色，实际: %v", err)
	}

	// 要求先审计 → 没有工具调用时拒绝
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, RequireAudit: true, AllowedRoles: []string{"ctf-recon"}},
	}, "")
	service.activeRole = "ctf"
	if _, err := service.DelegateTask("ctf-recon", "任务"); err == nil || !strings.Contains(err.Error(), "审计") {
		t.Errorf("没审计就派活应当被拒，实际: %v", err)
	}
	service.noteParentToolCall("") // 审计过了

	// 不在白名单的子角色 → 拒绝
	if _, err := service.DelegateTask("ctf-route", "越权"); err == nil {
		t.Error("不在白名单的子角色应当被拒")
	}

	// 白名单内的角色 → 允许（真起进程，测完杀掉）
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	// 审计证据按会话记账：进程起来之后工具调用才记在这条会话头上
	// （真实路径里证据来自事件流的 runtimeId，不存在"起进程之前就有证据"）。
	service.noteParentToolCall("")

	result, err := service.DelegateTask("ctf-recon", "查资料")
	if err != nil {
		t.Fatalf("白名单内的角色应当允许: %v", err)
	}
	var handle struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal([]byte(result.Data), &handle); err != nil {
		t.Fatal(err)
	}
	if handle.AgentID == "" {
		t.Fatal("应当返回 agentId")
	}

	// ListAgents 要能看到它
	listed, err := service.ListAgents()
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if !strings.Contains(listed.Data, handle.AgentID) {
		t.Errorf("Agents 列表里应当有 %s：%s", handle.AgentID, listed.Data)
	}

	// 非法 id 必须被挡住（id 会进会话目录名，是路径穿越的入口）
	if _, err := service.KillAgent("../../etc/passwd"); err == nil {
		t.Error("非法 id 应当被拒")
	}
	if _, err := service.KillAgent(handle.AgentID); err != nil {
		t.Errorf("KillAgent: %v", err)
	}
}

// 父正在跑回合：结果先攒着，等它这一轮结束再投——不打断父的推理。
func TestDelegationHoldsWhileParentBusy(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")
	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	service.noteParentToolCall("")

	sup := service.supervisorFor()
	/**
	 * 父正在解题 —— 一进程一会话之后，"父忙"是**那条会话的进程**的状态。
	 *
	 * 以前用全局的 markParentBusy(true)，那在只有一条会话时看不出区别；
	 * 现在必须走真实路径（事件流里收到 agent_start），否则测的是"全局忙"，
	 * 而投递判断读的是"这条会话忙"，两边对不上（这条用例正是这么失败的）。
	 */
	parentID := service.parentID()
	service.noteRuntimeEvent(service.runtimeByID(parentID), `{"type":"agent_start"}`)
	if !service.runtimeBusy(parentID) {
		t.Fatal("前置条件不成立：这条会话应当是忙的")
	}

	if _, err := service.DelegateTask("ctf-recon", "查资料"); err != nil {
		t.Fatalf("DelegateTask: %v", err)
	}
	// 等子智能体跑完：这时结果应当被挂起，而不是立刻投进父会话
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		all := sup.All()
		if len(all) > 0 && all[0].Status == agents.StatusCompleted {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	router := service.delegationManager().Router()
	if router == nil {
		t.Fatal("路由应当在（已经派过子智能体）")
	}
	if !router.Held(parentID) {
		t.Fatalf("父忙时结果应当被挂起（held=%v pending=%d）",
			router.Held(parentID), router.Pending(parentID))
	}
	state, _ := service.GetState()
	if strings.Contains(state.Data, `"pendingMessageCount":1`) {
		t.Error("父忙时不该已经投递进队列")
	}

	// 父回合结束 → 立刻投出去，而且**要真的进对话**。
	//
	// 父此刻已空闲，投递走 prompt：结果会被当作新一轮的输入处理。
	// 只断言"队列里有东西"是不够的——那正是"结论停在队列里没人处理"那个 bug 的形态。
	service.noteRuntimeEvent(service.runtimeByID(parentID), `{"type":"agent_settled"}`)
	service.markParentBusy(false)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := service.GetMessages()
		if err == nil && strings.Contains(messages.Data, "[子智能体结果]") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	state, _ = service.GetState()
	t.Fatalf("父回合结束后结果应当投递进对话：%s", state.Data)
}

// 委派策略必须能原样活过一次读写：它决定"能不能派、派给谁、派几个"，
// 读写丢字段的表现是"界面配了却不生效"，而且不报错。
func TestDelegationPolicyRoundTrip(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{
			Enabled:            true,
			Mode:               "background",
			RequireAudit:       true,
			DeliverOn:          "turn_end",
			AllowedRoles:       []string{"ctf-recon", "ctf-verify"},
			MaxChildren:        3,
			MaxParallel:        2,
			MaxDurationSeconds: 600,
		},
	}, "")

	view, ok := findRole("ctf")
	if !ok {
		t.Fatal("角色没读回来")
	}
	if view.Role.Delegation == nil {
		t.Fatal("delegation 字段丢了——存进去读不出来")
	}
	got := view.Role.Delegation
	if !got.Enabled || !got.RequireAudit || got.Mode != "background" || got.DeliverOn != "turn_end" {
		t.Errorf("布尔/字符串字段没保住: %+v", got)
	}
	if len(got.AllowedRoles) != 2 || got.AllowedRoles[0] != "ctf-recon" {
		t.Errorf("白名单没保住: %v", got.AllowedRoles)
	}
	if got.MaxChildren != 3 || got.MaxParallel != 2 || got.MaxDurationSeconds != 600 {
		t.Errorf("预算没保住: %+v", got)
	}

	// 策略读取函数要把秒换算成 duration，并把白名单原样交出去
	service.activeRole = "ctf"
	policy, err := service.delegationPolicy()
	if err != nil {
		t.Fatalf("delegationPolicy: %v", err)
	}
	if !policy.Enabled || len(policy.AllowedRoles) != 2 {
		t.Errorf("策略读取不对: %+v", policy)
	}
	if policy.MaxDuration != 10*time.Minute {
		t.Errorf("时长换算不对: %v", policy.MaxDuration)
	}
}
