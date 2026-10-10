package agents

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"zeroa/internal/pi"
)

/**
 * 可继续子智能体（仿 DSH 的 continuable）的行为测试。
 *
 * 用一个"够像 Pi"的假子进程：它认识 prompt / steer / follow_up / abort，
 * 每轮延迟若干毫秒后发 message_end + agent_settled；收到 abort 立刻以
 * stopReason=aborted 收尾。这样"跑完一轮 / 被中断 / 空闲回收 / 再开一轮"
 * 四条路径都能精确复现，且不花 token。
 */
const continuableChildScript = `
const readline = require('readline');
const rl = readline.createInterface({ input: process.stdin });
const turnDelay = Number(process.argv[2] || 50);
let turn = 0;
let pendingAbort = null;
function emit(obj) { process.stdout.write(JSON.stringify(obj) + '\n'); }

function runTurn(text) {
  turn += 1;
  const timer = setTimeout(() => {
    pendingAbort = null;
    emit({ type: 'message_end', message: { role: 'assistant', content: [{ type: 'text', text: '第' + turn + '轮结论：' + text }] }, usage: { input: 100, output: 20, totalTokens: 120 } });
    emit({ type: 'agent_settled' });
  }, turnDelay);
  pendingAbort = () => {
    clearTimeout(timer);
    emit({ type: 'message_end', message: { role: 'assistant', stopReason: 'aborted', content: [{ type: 'text', text: '（被中断）' }] } });
    emit({ type: 'agent_settled' });
  };
}

rl.on('line', line => {
  let cmd;
  try { cmd = JSON.parse(line); } catch { return; }
  if (cmd.type === 'abort') {
    emit({ id: cmd.id, type: 'response', command: 'abort', success: true });
    if (pendingAbort) { const f = pendingAbort; pendingAbort = null; f(); }
    return;
  }
  if (cmd.type === 'prompt' || cmd.type === 'follow_up' || cmd.type === 'steer') {
    emit({ id: cmd.id, type: 'response', command: cmd.type, success: true });
    // steer 是"插进当前轮"，不新起一轮；prompt / follow_up 都会开新轮
    if (cmd.type !== 'steer') runTurn(cmd.message);
    return;
  }
});
`

// newContinuableSupervisor 建一个认识四种命令的 Supervisor（含 deliver 实现）。
func newContinuableSupervisor(t *testing.T, turnDelay time.Duration, options Options) *Supervisor {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("没有 node，跳过（子进程测试需要 node）")
	}
	script := filepath.Join(t.TempDir(), "continuable-child.js")
	if err := os.WriteFile(script, []byte(continuableChildScript), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := func(ctx context.Context, inst *Instance, onTrace func(pi.Trace)) (*pi.Client, error) {
		cmd := exec.Command("node", script, turnDelay.String())
		return pi.Start(cmd, onTrace)
	}
	sup := NewSupervisor(options, launch)
	sup.SetPrompt(func(ctx context.Context, client *pi.Client, inst *Instance) error {
		_, err := client.Request(ctx, "prompt", map[string]any{"message": inst.Task})
		return err
	})
	sup.SetDeliver(func(ctx context.Context, client *pi.Client, inst *Instance, kind, text string) error {
		_, err := client.Request(ctx, kind, map[string]any{"message": text})
		return err
	})
	return sup
}

// 一次性形态的老行为必须原样保留：跑完一轮 → 结算 → 释放进程。
func TestOneShotStillFinishesAfterOneTurn(t *testing.T) {
	sup := newContinuableSupervisor(t, 20*time.Millisecond, Options{})
	inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-recon", Task: "一次性任务", Mode: ModeOneShot})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	snap, ok := sup.Wait(context.Background(), inst.ID, 20*time.Second)
	if !ok {
		t.Fatal("Wait 找不到实例")
	}
	if snap.Status != StatusCompleted {
		t.Fatalf("一次性子智能体应当 completed，得到 %s（err=%s）", snap.Status, snap.Err)
	}
	if snap.Mode != ModeOneShot {
		t.Errorf("形态应当是 one-shot，得到 %q", snap.Mode)
	}
	// 走完这一步进程就该被释放：再发消息必须被拒
	if _, err := sup.Send(inst.ID, "喂", DeliveryQueue); err == nil {
		t.Error("已结束的一次性子智能体不该接受消息")
	}
}

/**
 * 可继续形态：一轮结束后**不释放进程**，进入 idle 且标记"这一轮正常完成"；
 * 之后再发消息会开新一轮（同一个实例、同一份上下文）。
 */
func TestContinuableStaysIdleAndAcceptsFollowUp(t *testing.T) {
	sup := newContinuableSupervisor(t, 30*time.Millisecond, Options{})
	inst, err := sup.Spawn(SpawnRequest{
		ParentID: "main", RoleID: "ctf-recon", Task: "第一轮",
		Mode: ModeContinuable, IdleTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	first, ok := sup.WaitTurn(context.Background(), inst.ID, 20*time.Second)
	if !ok {
		t.Fatal("WaitTurn 找不到实例")
	}
	if first.Status != StatusIdle {
		t.Fatalf("跑完一轮应当是 idle（进程还在），得到 %s", first.Status)
	}
	if !first.LastTurnCompleted {
		t.Fatal("正常跑完的一轮应当把 LastTurnCompleted 置真")
	}
	if first.Turns != 1 {
		t.Errorf("轮次计数应当是 1，得到 %d", first.Turns)
	}
	if !first.TurnSettled() {
		t.Error("TurnSettled 应当为真（父级据此知道结论到了）")
	}

	// 追一句：应当在同一实例上开新一轮
	if _, err := sup.Send(inst.ID, "再补充一点", DeliveryQueue); err != nil {
		t.Fatalf("Send 到空闲实例应当成功: %v", err)
	}
	second, ok := sup.WaitTurn(context.Background(), inst.ID, 20*time.Second)
	if !ok {
		t.Fatal("第二轮 WaitTurn 失败")
	}
	if second.ID != inst.ID {
		t.Fatal("追问必须复用同一个子智能体实例")
	}
	if second.Turns < 2 {
		t.Errorf("第二轮结束后轮次计数应当至少 2，得到 %d", second.Turns)
	}
	if second.Status != StatusIdle {
		t.Errorf("第二轮结束后仍应是 idle，得到 %s", second.Status)
	}

	// 空闲回收：超时后进终态（进程释放）
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok := sup.Get(inst.ID); ok && snap.Status.Finished() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	snap, _ := sup.Get(inst.ID)
	t.Fatalf("空闲超时后应当被回收（终态），实际 %s", snap.Status)
}

// 中断只停当前这一轮：进程与实例都保留，之后还能继续说话。
func TestInterruptKeepsContinuableAlive(t *testing.T) {
	sup := newContinuableSupervisor(t, 3*time.Second, Options{}) // 每轮跑 3 秒，留足中断窗口
	inst, err := sup.Spawn(SpawnRequest{
		ParentID: "main", RoleID: "ctf-recon", Task: "会被中断的任务",
		Mode: ModeContinuable, IdleTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if snap := waitStatus(t, sup, inst.ID, StatusRunning, 10*time.Second); snap == nil {
		t.Fatal("子智能体没有进入 running")
	}

	if err := sup.Interrupt(inst.ID); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	snap, ok := sup.WaitTurn(context.Background(), inst.ID, 20*time.Second)
	if !ok {
		t.Fatal("中断后应当仍能等到这一轮结束")
	}
	if snap.Status.Finished() {
		t.Fatalf("中断**不能**销毁可继续子智能体，实际 %s", snap.Status)
	}
	if snap.Status != StatusIdle {
		t.Fatalf("中断后应当是 idle，得到 %s", snap.Status)
	}
	if snap.LastTurnCompleted {
		t.Error("被中断的那一轮不该被标记成正常完成（否则界面会显示「完成」绿点）")
	}
	// 对空闲实例再中断一次：空操作但受理（照 DSH）
	if err := sup.Interrupt(inst.ID); err != nil {
		t.Errorf("对空闲实例的中断应当是空操作并受理，得到 %v", err)
	}
}

// 达到可继续上限时直接拒绝，不排队（照 DSH：排队会和"父等子"相互死锁）。
func TestContinuableCapacityRejectsInsteadOfQueueing(t *testing.T) {
	sup := newContinuableSupervisor(t, time.Second, Options{MaxContinuable: 1})
	if _, err := sup.Spawn(SpawnRequest{
		ParentID: "main", RoleID: "ctf-recon", Task: "占位", Mode: ModeContinuable, IdleTimeout: 10 * time.Second,
	}); err != nil {
		t.Fatalf("第一个应当成功: %v", err)
	}
	_, err := sup.Spawn(SpawnRequest{
		ParentID: "main", RoleID: "ctf-recon", Task: "超限", Mode: ModeContinuable, IdleTimeout: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("超出可继续上限应当直接报错")
	}
	if !errors.Is(err, ErrTooManyContinuable) {
		t.Fatalf("错误应当是 ErrTooManyContinuable，得到 %v", err)
	}
}

// 一次性子智能体不能收消息：它没有可继续的会话。
func TestOneShotRejectsSend(t *testing.T) {
	sup := newContinuableSupervisor(t, 300*time.Millisecond, Options{})
	inst, err := sup.Spawn(SpawnRequest{ParentID: "main", RoleID: "ctf-recon", Task: "一次性", Mode: ModeOneShot})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, err := sup.Send(inst.ID, "喂", DeliveryQueue); err == nil {
		t.Fatal("一次性子智能体不该接受消息")
	} else if !errors.Is(err, ErrNotContinuable) {
		t.Fatalf("错误应当是 ErrNotContinuable，得到 %v", err)
	}
}
