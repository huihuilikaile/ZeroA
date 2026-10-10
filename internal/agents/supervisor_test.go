package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zeroa/internal/pi"
)

// fakeChildScript 是一个"够像 Pi"的假子进程。
//
// 为什么不用真 Pi 测这一层：Supervisor 的职责是**进程生命周期与结果抽取**，
// 与模型无关。用假子进程能把每种结束路径（正常完成 / 卡住 / 直接退出）
// 精确复现，而且不花一分钱 token。
const fakeChildScript = `
const readline = require('readline');
const rl = readline.createInterface({ input: process.stdin });
const mode = process.argv[2] || 'ok';
rl.on('line', line => {
  let cmd;
  try { cmd = JSON.parse(line); } catch { return; }
  if (cmd.type !== 'prompt') return;
  process.stdout.write(JSON.stringify({ id: cmd.id, type: 'response', command: 'prompt', success: true }) + '\n');
  if (mode === 'hang') return;               // 永不结束：用于测超时与 kill
  if (mode === 'exit') { process.exit(3); }  // 没输出就退出：用于测失败路径
  process.stdout.write(JSON.stringify({ type: 'tool_start', toolName: 'bash', args: { command: 'file challenge.exe' } }) + '\n');
  process.stdout.write(JSON.stringify({
    type: 'message_end',
    message: { role: 'assistant', content: [{ type: 'text', text: '子智能体结论：核心校验在 sub_140001930（' + mode + '）' }] },
    usage: { input: 1200, output: 300, totalTokens: 1500 },
  }) + '\n');
  process.stdout.write(JSON.stringify({ type: 'agent_settled' }) + '\n');
});
`

func writeFakeChild(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过（子进程测试需要 node）")
	}
	path := filepath.Join(t.TempDir(), "fake-child.js")
	if err := os.WriteFile(path, []byte(fakeChildScript), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// nodeLauncher 用真进程（node 脚本）当子智能体。
func nodeLauncher(script, mode string) Launcher {
	return func(ctx context.Context, inst *Instance, onTrace func(pi.Trace)) (*pi.Client, error) {
		cmd := exec.Command("node", script, mode)
		return pi.Start(cmd, onTrace)
	}
}

// newTestSupervisor 建一个把任务用 RPC prompt 发出去的 Supervisor。
func newTestSupervisor(t *testing.T, script, mode string, options Options) *Supervisor {
	t.Helper()
	sup := NewSupervisor(options, nodeLauncher(script, mode))
	sup.SetPrompt(func(ctx context.Context, client *pi.Client, inst *Instance) error {
		_, err := client.Request(ctx, "prompt", map[string]any{"message": inst.Task})
		return err
	})
	return sup
}

func waitStatus(t *testing.T, sup *Supervisor, id string, want Status, timeout time.Duration) *Instance {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if inst, ok := sup.Get(id); ok && inst.Status == want {
			return inst
		}
		time.Sleep(20 * time.Millisecond)
	}
	inst, _ := sup.Get(id)
	got := Status("(不存在)")
	if inst != nil {
		got = inst.Status
	}
	t.Fatalf("等待状态 %s 超时，实际 %s（err=%v）", want, got, instErr(inst))
	return nil
}

func instErr(inst *Instance) string {
	if inst == nil {
		return ""
	}
	return inst.Err
}

// 派一个子智能体 → 它跑完 → Go 拿到结果与用量。
func TestSpawnChildAndCaptureResult(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "ok", Options{})

	inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-recon", Task: "查一下这道题的 wp"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if inst.ID == "" || inst.ParentID != "main" {
		t.Fatalf("实例标识不对: %+v", inst)
	}

	done := waitStatus(t, sup, inst.ID, StatusCompleted, 10*time.Second)
	if !strings.Contains(done.Result, "sub_140001930") {
		t.Errorf("结果没抽出来: %q", done.Result)
	}
	if done.Usage.Turns != 1 || done.Usage.Input != 1200 || done.Usage.Output != 300 {
		t.Errorf("用量没抽对: %+v", done.Usage)
	}
	if done.EndedAt.IsZero() {
		t.Error("结束时间没记")
	}
	// 工具动作要变成一行给人看的状态
	if !strings.Contains(done.Phase, "bash") || !strings.Contains(done.Phase, "challenge.exe") {
		t.Errorf("Phase 没记录当前动作: %q", done.Phase)
	}
	// 结果收口后要通知一次 ResultSink
	var seen int
	sup.SetResultSink(func(*Instance) { seen++ })
	if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-recon", Task: "再来一次"}); err != nil {
		t.Fatalf("Spawn(2): %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && seen == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if seen == 0 {
		t.Error("子智能体结束时 ResultSink 没有被调用")
	}
}

// 子进程起不来 / 没有任何输出就退出：必须报出原因，不能变成"(no output)"。
//
// 这条来自官方示例的教训（issue #8129）：它把 ChildProcess 的 error 事件丢掉，
// 于是工具只回一句 "Agent error: (no output)"，把真正的原因藏起来了。
func TestChildFailureKeepsReason(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "exit", Options{})

	inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-recon", Task: "会失败的活"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	done := waitStatus(t, sup, inst.ID, StatusFailed, 10*time.Second)
	if !strings.Contains(done.Err, "没有产生任何输出") {
		t.Errorf("失败原因要说清楚，实际: %q", done.Err)
	}
}

// 起不来的进程（错的脚本路径）也要给出可读原因。
func TestLaunchFailureIsReported(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过")
	}
	sup := NewSupervisor(Options{}, nodeLauncher(filepath.Join(t.TempDir(), "not-exist.js"), "ok"))
	inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-recon", Task: "起不来的活"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	done := waitStatus(t, sup, inst.ID, StatusFailed, 10*time.Second)
	// 关键不是"哪一句话"，而是**真正的原因有没有带出来**：node 会往 stderr 写
	// "Cannot find module ..."，这正是排查所需的信息。
	if !strings.Contains(done.Err, "stderr") ||
		(!strings.Contains(done.Err, "Cannot find module") && !strings.Contains(done.Err, "not-exist")) {
		t.Errorf("失败原因要带上 stderr 的真实错误，实际: %q", done.Err)
	}
}

// 超时：到点必须自己收手，不能让子智能体无限跑。
func TestTimeoutCancelsChild(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "hang", Options{})

	inst, err := sup.Spawn(SpawnRequest{
		ParentID: "main", RoleID: "ctf-route", Task: "会卡住的活", MaxDuration: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	done := waitStatus(t, sup, inst.ID, StatusCancelled, 10*time.Second)
	if !strings.Contains(done.Err, "超过时限") {
		t.Errorf("超时要说明原因，实际: %q", done.Err)
	}
}

// Kill：父 Agent 停止时子智能体必须能被强停。
func TestKillStopsRunningChild(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "hang", Options{})

	inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "会被杀掉的活"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitStatus(t, sup, inst.ID, StatusRunning, 5*time.Second)

	if err := sup.Kill(inst.ID, "父会话停止了"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	done := waitStatus(t, sup, inst.ID, StatusKilled, 5*time.Second)
	if done.Err != "父会话停止了" {
		t.Errorf("杀掉的原因要记下来: %q", done.Err)
	}
	// 快照不带运行时，所以"等它结束"要走 Wait
	if _, ok := sup.Wait(context.Background(), inst.ID, 2*time.Second); !ok {
		t.Error("Wait 应当能找到这个实例")
	}
	if err := sup.Kill("agent-999", "不存在"); err == nil {
		t.Error("杀一个不存在的子智能体应当报错")
	}
}

// CancelChildren：父 abort 时一次停掉名下全部子智能体。
func TestCancelChildrenStopsAll(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "hang", Options{MaxParallel: 4, MaxChildren: 4})

	ids := []string{}
	for i := 0; i < 3; i++ {
		inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "并行探索"})
		if err != nil {
			t.Fatalf("Spawn(%d): %v", i, err)
		}
		ids = append(ids, inst.ID)
	}
	if got := sup.RunningCount(); got != 3 {
		t.Fatalf("应当有 3 个在跑，实际 %d", got)
	}
	if stopped := sup.CancelChildren("main", "父会话中止"); stopped != 3 {
		t.Errorf("应当停掉 3 个，实际 %d", stopped)
	}
	if got := sup.RunningCount(); got != 0 {
		t.Errorf("停完不该还有在跑的，实际 %d", got)
	}
	for _, id := range ids {
		inst, _ := sup.Get(id)
		if inst.Status != StatusKilled {
			t.Errorf("%s 状态应为 killed，实际 %s", id, inst.Status)
		}
	}
	// 只停自己的子智能体：别人的不能动
	sup2 := newTestSupervisor(t, script, "hang", Options{MaxParallel: 4, MaxChildren: 4})
	other, err := sup2.Spawn(SpawnRequest{ParentID: "another", RoleID: "ctf-route", Task: "别人的活"})
	if err != nil {
		t.Fatalf("Spawn(other): %v", err)
	}
	waitStatus(t, sup2, other.ID, StatusRunning, 5*time.Second)
	if stopped := sup2.CancelChildren("main", "父会话中止"); stopped != 0 {
		t.Errorf("不该停别人的子智能体，实际停了 %d 个", stopped)
	}
}

// 三道闸门：深度、总量、并发，外加参数校验。
func TestGates(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "hang", Options{MaxParallel: 2, MaxChildren: 3, MaxDepth: 1})

	// 深度：子智能体不能再派子智能体
	if _, err := sup.Spawn(SpawnRequest{ParentID: "agent-1", RoleID: "ctf-route", Task: "孙任务", ParentDepth: 1}); !errors.Is(err, ErrDepthExceeded) {
		t.Errorf("深度超限应当被拒，实际: %v", err)
	}
	// 角色白名单
	if _, err := sup.Spawn(SpawnRequest{
		ParentID: "main", RoleID: "ctf-route", Task: "越权角色",
		AllowedRoles: []string{"ctf-recon", "ctf-verify"},
	}); !errors.Is(err, ErrRoleNotAllowed) {
		t.Errorf("不在白名单里的角色应当被拒，实际: %v", err)
	}
	// 参数
	if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "", Task: "缺角色"}); err == nil {
		t.Error("缺角色应当被拒")
	}
	if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "   "}); err == nil {
		t.Error("空任务应当被拒")
	}

	// 并发上限 2
	for i := 0; i < 2; i++ {
		if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "并行任务"}); err != nil {
			t.Fatalf("前两个应当成功: %v", err)
		}
	}
	if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "第三个"}); !errors.Is(err, ErrTooManyParallel) {
		t.Errorf("超过并发上限应当被拒，实际: %v", err)
	}

	// 总量上限 3：先把两个正在跑的杀掉（它们仍计入累计），再派一个到上限
	sup.CancelChildren("main", "腾位置")
	if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "第三个"}); err != nil {
		t.Fatalf("第三个应当成功（累计 3）: %v", err)
	}
	if _, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-route", Task: "第四个"}); !errors.Is(err, ErrTooManyChildren) {
		t.Errorf("超过总量上限应当被拒，实际: %v", err)
	}
	// 别人的配额不受影响
	if _, err := sup.Spawn(SpawnRequest{ParentID: "another", RoleID: "ctf-route", Task: "另一个父的任务"}); err != nil {
		t.Errorf("另一个父智能体应当有自己的配额: %v", err)
	}
}

// ★ 角色级预算（SpawnRequest 自带的 MaxChildren/MaxParallel）必须真的生效，
// 并且按父级隔离——这是"角色的 delegation.maxChildren/maxParallel 写了就管用"
// 的回归测试（以前这两个字段被读出来却从没传给执行层，提示词里告诉模型的
// 预算与实际拦截口径对不上）。
func TestSpawnRequestBudgetsOverrideDefaults(t *testing.T) {
	script := writeFakeChild(t)
	sup := newTestSupervisor(t, script, "hang", Options{MaxParallel: 2, MaxChildren: 4})
	defer sup.CancelAll("测试结束")

	// 并发预算收窄：角色给 1 → 父 A 的第二个立刻被拒；父 B 不受牵连（按父级计）
	first, err := sup.Spawn(SpawnRequest{ParentID: "A", RoleID: "r", Task: "A-1", MaxParallel: 1})
	if err != nil {
		t.Fatalf("A 的第一个应当成功: %v", err)
	}
	waitStatus(t, sup, first.ID, StatusRunning, 5*time.Second)
	if _, err := sup.Spawn(SpawnRequest{ParentID: "A", RoleID: "r", Task: "A-2", MaxParallel: 1}); !errors.Is(err, ErrTooManyParallel) {
		t.Errorf("A 超出角色并发预算应当被拒，实际: %v", err)
	}
	if _, err := sup.Spawn(SpawnRequest{ParentID: "B", RoleID: "r", Task: "B-1", MaxParallel: 1}); err != nil {
		t.Errorf("B 的第一个应当放行（预算是按父级计的）: %v", err)
	}

	// 并发预算放宽：全局默认 2，角色给 3 → 父 C 能同时跑 3 个
	for i := 0; i < 3; i++ {
		if _, err := sup.Spawn(SpawnRequest{ParentID: "C", RoleID: "r", Task: "C 并行", MaxParallel: 3}); err != nil {
			t.Fatalf("C 的第 %d 个应当成功（角色预算 3）: %v", i+1, err)
		}
	}
	if _, err := sup.Spawn(SpawnRequest{ParentID: "C", RoleID: "r", Task: "C-4", MaxParallel: 3}); !errors.Is(err, ErrTooManyParallel) {
		t.Errorf("C 的第 4 个应当被角色预算 3 拦住，实际: %v", err)
	}

	// 总量预算：角色给 1 → 父 D 派过一个之后（哪怕它还在跑）再派就被拒
	if _, err := sup.Spawn(SpawnRequest{ParentID: "D", RoleID: "r", Task: "D-1", MaxChildren: 1, MaxParallel: 9}); err != nil {
		t.Fatalf("D 的第一个应当成功: %v", err)
	}
	if _, err := sup.Spawn(SpawnRequest{ParentID: "D", RoleID: "r", Task: "D-2", MaxChildren: 1, MaxParallel: 9}); !errors.Is(err, ErrTooManyChildren) {
		t.Errorf("D 超出角色总量预算应当被拒，实际: %v", err)
	}
}

// 结果路由：合并窗口把同一批结果合成一条消息。
func TestRouterCoalescesResults(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	router := NewRouter(RouterOptions{
		Coalesce: 120 * time.Millisecond,
		Deliver: func(parentID, message string) error {
			mu.Lock()
			delivered = append(delivered, parentID+"|"+message)
			mu.Unlock()
			return nil
		},
	})

	router.Submit(&Instance{ID: "agent-1", ParentID: "main", RoleID: "ctf-recon", Status: StatusCompleted, Result: "资料结论 A", EndedAt: time.Now(), StartedAt: time.Now()})
	router.Submit(&Instance{ID: "agent-2", ParentID: "main", RoleID: "ctf-route", Status: StatusCompleted, Result: "路线结论 B", EndedAt: time.Now(), StartedAt: time.Now()})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(delivered)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 {
		t.Fatalf("两个结果应当合并成一条消息，实际投递 %d 条: %v", len(delivered), delivered)
	}
	message := delivered[0]
	for _, want := range []string{"2 个任务已完成", "资料结论 A", "路线结论 B", "agent-1", "agent-2"} {
		if !strings.Contains(message, want) {
			t.Errorf("合并消息里应该有 %q：\n%s", want, message)
		}
	}
}

// 父正在跑回合：结果先挂着，等父稳定下来再投（不打断它的推理）。
func TestRouterHoldsWhileParentBusy(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	busy := true
	router := NewRouter(RouterOptions{
		Coalesce:   60 * time.Millisecond,
		ParentBusy: func(string) bool { return busy },
		Deliver: func(parentID, message string) error {
			mu.Lock()
			delivered = append(delivered, message)
			mu.Unlock()
			return nil
		},
	})

	router.Submit(&Instance{ID: "agent-1", ParentID: "main", RoleID: "ctf-recon", Status: StatusCompleted, Result: "研究结论", StartedAt: time.Now(), EndedAt: time.Now()})
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	count := len(delivered)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("父忙时不该投递，实际投了 %d 条", count)
	}
	if !router.Held("main") {
		t.Error("应当标记为挂起等待")
	}

	busy = false
	router.FlushParent("main")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count = len(delivered)
		mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if count != 1 || !strings.Contains(delivered[0], "研究结论") {
		t.Fatalf("父回合结束后应当投递，实际: %v", delivered)
	}
}

// 父会话已经收工：不唤醒（否则会起一个没有意义的回合）。
func TestRouterDoesNotWakeClosedSession(t *testing.T) {
	var dropped []string
	var delivered int
	router := NewRouter(RouterOptions{
		Coalesce:      50 * time.Millisecond,
		SessionClosed: func(string) bool { return true },
		Deliver:       func(string, string) error { delivered++; return nil },
		OnDrop:        func(_ []*Instance, reason string) { dropped = append(dropped, reason) },
	})
	router.Submit(&Instance{ID: "agent-1", ParentID: "main", RoleID: "ctf-recon", Status: StatusCompleted, Result: "迟到的结论"})
	time.Sleep(200 * time.Millisecond)
	if delivered != 0 {
		t.Error("父会话已结束就不该唤醒它")
	}
	if len(dropped) == 0 || !strings.Contains(dropped[0], "已结束") {
		t.Errorf("应当记录「被丢弃」的原因，实际: %v", dropped)
	}
}

// 投递消息的格式：父模型要靠它分辨谁说的、成没成。
func TestFormatResultsShapes(t *testing.T) {
	single := FormatResults([]*Instance{{
		ID: "agent-1", ParentID: "main", RoleID: "ctf-recon", Status: StatusCompleted,
		Result: "资料结论", StartedAt: time.Now().Add(-3 * time.Second), EndedAt: time.Now(),
		Usage: Usage{TotalTokens: 3200},
	}})
	for _, want := range []string{"[子智能体结果]", "agent-1", "ctf-recon", "completed", "3.2k tokens", "资料结论"} {
		if !strings.Contains(single, want) {
			t.Errorf("单条结果格式缺 %q：\n%s", want, single)
		}
	}

	failed := FormatResults([]*Instance{{
		ID: "agent-2", ParentID: "main", RoleID: "ctf-verify", Status: StatusFailed,
		Err: "启动子智能体失败：spawn node ENOENT", StartedAt: time.Now(), EndedAt: time.Now(),
	}})
	if !strings.Contains(failed, "未完成") || !strings.Contains(failed, "ENOENT") {
		t.Errorf("失败也要投递，并带原因：\n%s", failed)
	}
}

// 结果过长必须截断，并告诉父去哪里看全文（不让子会话撑爆父上下文）。
func TestResultTruncation(t *testing.T) {
	inst := &Instance{ID: "agent-1", SessionDir: "C:/tmp/agents/agent-1/sessions"}
	huge := strings.Repeat("A", maxResultBytes+5000)
	out := truncateResult(huge, inst)
	if len(out) > maxResultBytes+400 {
		t.Errorf("截断后仍然过长: %d 字节", len(out))
	}
	if !strings.Contains(out, "已截断") || !strings.Contains(out, inst.SessionDir) {
		t.Errorf("截断必须说明原因与去哪看全文：\n%s", out[:200])
	}
	if !strings.HasSuffix(out, strings.Repeat("A", 100)) {
		t.Error("截断应当保留结尾（结论通常在最后）")
	}
	empty := truncateResult("   ", inst)
	if !strings.Contains(empty, inst.SessionDir) {
		t.Errorf("空结果也要给出会话目录：%s", empty)
	}
}

// 事件解析：两种 content 形状都要认，工具动作要能变成一行摘要。
func TestDecodeEventShapes(t *testing.T) {
	blocks, ok := DecodeEvent(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"第一段"},{"type":"text","text":"第二段"}]},"usage":{"input":10,"output":5}}`)
	if !ok {
		t.Fatal("应当解析成功")
	}
	text, isAssistant := blocks.AssistantText()
	if !isAssistant || text != "第一段\n第二段" {
		t.Errorf("内容块数组没解析对: %q", text)
	}
	if blocks.Usage.Input != 10 || blocks.Usage.Output != 5 || blocks.Usage.TotalTokens != 15 {
		t.Errorf("usage 没算对（缺 total 时应当相加）: %+v", blocks.Usage)
	}

	plain, _ := DecodeEvent(`{"type":"message_end","message":{"role":"assistant","content":"纯字符串回复"}}`)
	if text, _ := plain.AssistantText(); text != "纯字符串回复" {
		t.Errorf("字符串形状没解析对: %q", text)
	}

	user, _ := DecodeEvent(`{"type":"message_end","message":{"role":"user","content":"不是助手"}}`)
	if _, isAssistant := user.AssistantText(); isAssistant {
		t.Error("user 消息不该被当成结果")
	}

	tool, _ := DecodeEvent(`{"type":"tool_start","toolName":"bash","args":{"command":"file   challenge.exe"}}`)
	if phase := tool.ToolPhase(); phase != "bash: file challenge.exe" {
		t.Errorf("工具摘要不对: %q", phase)
	}

	if _, ok := DecodeEvent("not json"); ok {
		t.Error("非 JSON 不该解析成功")
	}
	if _, ok := DecodeEvent(`{"id":"x"}`); ok {
		t.Error("没有 type 的记录不算事件")
	}
}
