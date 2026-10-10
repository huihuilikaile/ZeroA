package bridge

import (
	"testing"
	"time"
)

/**
 * 进程**意外死亡**（崩溃 / 被外部杀掉）时的回归测试。
 *
 * 故障链（用户实测，2026-10-08）：进程悄悄死掉 → runtimes 里留着死句柄 →
 * s.client 仍指向它 → abort/steer 全部打到死进程上报"Pi 在响应前退出"，
 * 界面还卡在"运行中"，只能重启应用。更隐蔽的是：下一条消息懒启动的新进程
 * 绑的是一份**空会话**，用户那句话从此落进另一份历史（日志里能看到
 * `lazy_started … session=` 后面是空的）。
 *
 * 这里钉住的契约：
 *  1. 进程退出后登记必须清掉（activeRuntime/client 不再指向死进程）；
 *  2. 死的是当前会话时，它的会话文件要交还给 pendingSessionFile；
 *  3. 下一条消息懒重启后必须**切回同一份会话文件**（历史不断成两条）；
 *  4. 后台会话的进程死掉不能动前台的任何指针。
 */
func TestActiveRuntimeExitRestoresPendingSession(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := sessionFixture(t, root, cwd, "crash-a")
	if _, err := s.SwitchSession(a); err != nil {
		t.Fatalf("打开会话: %v", err)
	}
	if _, err := s.Prompt("第一句话"); err != nil {
		t.Fatalf("懒启动的第一条消息: %v", err)
	}
	s.mu.Lock()
	r := s.activeRuntime
	s.mu.Unlock()
	if r == nil {
		t.Fatal("第一条消息之后应当有一个会话进程")
	}
	if normalizeDir(r.sessionFile) != normalizeDir(a) {
		t.Fatalf("进程应当绑在打开的那条会话上：got %q want %q", r.sessionFile, a)
	}

	// 模拟进程意外死亡：关掉 stdin，mock 读到 EOF 退出——与真实崩溃一样，
	// 都会走 Wait goroutine → process_exit trace → noteRuntimeExit 这条路。
	if err := r.client.Close(); err != nil {
		t.Fatalf("关闭进程: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		gone := s.activeRuntime == nil && s.client == nil && len(s.runtimes) == 0
		pending := s.pendingSessionFile
		s.mu.Unlock()
		if gone {
			if normalizeDir(pending) != normalizeDir(a) {
				t.Fatalf("当前会话死亡后应把它的会话文件交还 pending：got %q want %q", pending, a)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("进程退出后登记没有清掉（activeRuntime/client/runtimes 仍指向死进程）")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 下一条消息：懒重启，并且必须先切回**同一份**会话文件再发。
	if _, err := s.Prompt("第二句话"); err != nil {
		t.Fatalf("进程死亡后的第一条消息: %v", err)
	}
	s.mu.Lock()
	restarted := s.activeRuntime
	s.mu.Unlock()
	if restarted == nil || restarted == r {
		t.Fatal("死亡之后应当重新起一个会话进程")
	}
	if normalizeDir(restarted.sessionFile) != normalizeDir(a) {
		t.Fatalf("懒重启后必须切回原来的会话文件（否则历史劈成两条）：got %q want %q", restarted.sessionFile, a)
	}
}

// 后台会话的进程死掉：前台的 pending/当前会话指针一个都不许动，
// 但那个 runtime 自己的忙碌标记必须清掉（否则回收器永远跳过这具尸体）。
func TestBackgroundRuntimeExitKeepsForeground(t *testing.T) {
	s := &PiService{}
	foreground := &sessionRuntime{id: "runtime-1", sessionFile: `C:\sessions\a.jsonl`}
	background := &sessionRuntime{id: "runtime-2", sessionFile: `C:\sessions\b.jsonl`, busy: true}
	s.runtimes = map[string]*sessionRuntime{foreground.id: foreground, background.id: background}
	s.activeRuntime = foreground

	s.noteRuntimeExit(background)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingSessionFile != "" {
		t.Errorf("后台进程退出不能动前台的 pending，实际 %q", s.pendingSessionFile)
	}
	if s.activeRuntime != foreground {
		t.Error("后台进程退出不能动当前会话")
	}
	if background.busy {
		t.Error("进程退出后它的忙碌标记必须清掉")
	}
}

// 主动 Stop 不收编：Stop 自己把 pendingSessionFile 清成"下一条消息开新会话"，
// 退出收尾不能把它又填回去（ Stop 先置空 activeRuntime，这里自然跳过）。
func TestStopDoesNotRestorePendingSession(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	a := sessionFixture(t, root, cwd, "stop-a")
	if _, err := s.SwitchSession(a); err != nil {
		t.Fatalf("打开会话: %v", err)
	}
	if _, err := s.Prompt("一句话"); err != nil {
		t.Fatalf("懒启动: %v", err)
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop 之后进程退出 trace 才会陆续到达：等它们都处理完再断言。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		pending := s.pendingSessionFile
		s.mu.Unlock()
		if pending != "" {
			t.Fatalf("Stop 之后 pendingSessionFile 必须保持为空（下一条消息开新会话），实际 %q", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
