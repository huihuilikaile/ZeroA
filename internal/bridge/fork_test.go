package bridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 消息分支这条链路：get_fork_messages 给出可分支的用户消息（带 entryId），
// fork 用 entryId 开新分支。两者必须对得上——界面上就是"点某条消息旁的分支按钮"。
func TestForkChainOnMock(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	// waitIdle 是包级辅助、作用在包级 svc 上，所以这里跟着用同一个变量
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	// 先制造两条用户消息
	for _, text := range []string{"第一条消息", "第二条消息"} {
		if _, err := svc.Prompt(text); err != nil {
			t.Fatalf("Prompt(%s): %v", text, err)
		}
		waitTurnDone(t, svc)
	}

	result, err := svc.GetForkMessages()
	if err != nil || !result.Success {
		t.Fatalf("GetForkMessages: %v %+v", err, result)
	}
	if !strings.Contains(result.Data, `"entryId"`) {
		t.Fatalf("必须带 entryId 字段（界面靠它配对），实际: %s", result.Data)
	}
	var payload struct {
		Messages []struct {
			EntryID string `json:"entryId"`
			Text    string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 2 {
		t.Fatalf("应有 2 条可分支的用户消息，实际 %d: %s", len(payload.Messages), result.Data)
	}
	first := payload.Messages[0]
	if first.Text != "第一条消息" {
		t.Errorf("第一条应该是「第一条消息」，实际 %q", first.Text)
	}

	// 用拿到的 entryId 分支：应回被分支消息的文本，且未被取消
	forked, err := svc.Fork(first.EntryID)
	if err != nil || !forked.Success {
		t.Fatalf("Fork: %v %+v", err, forked)
	}
	if !strings.Contains(forked.Data, "第一条消息") {
		t.Errorf("fork 应回被分支消息的文本，实际: %s", forked.Data)
	}
	if !strings.Contains(forked.Data, `"cancelled":false`) {
		t.Errorf("未取消时要显式回 cancelled=false：%s", forked.Data)
	}

	// 缺 entryId 要被挡住（否则会发一条没有意义的请求）
	if _, err := svc.Fork("  "); err == nil {
		t.Error("空 entryId 应该报错")
	}
	// 不存在的 entryId：mock 会拒绝，服务层应如实回报
	if result, err := svc.Fork("entry-does-not-exist"); err == nil && result.Success {
		t.Error("不存在的 entryId 不该成功")
	}

	// clone：复制当前分支
	cloned, err := svc.CloneSession()
	if err != nil || !cloned.Success {
		t.Fatalf("CloneSession: %v %+v", err, cloned)
	}
	if !strings.Contains(cloned.Data, `"cancelled":false`) {
		t.Errorf("clone 也应显式回 cancelled=false：%s", cloned.Data)
	}
}

// waitTurnDone 等这一轮真的跑完（轮询 get_state 的 isStreaming）。
//
// 不用包级 waitIdle：它是"进程还在响应"级别的等待，而这里连发两条消息，
// 必须确定第一条已经结束，否则第二条会被 Pi 拒绝（已有任务正在运行）。
func waitTurnDone(t *testing.T, service *PiService) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		state, err := service.GetState()
		if err == nil && state.Success && !strings.Contains(state.Data, `"isStreaming":true`) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("等待这一轮结束超时")
}
