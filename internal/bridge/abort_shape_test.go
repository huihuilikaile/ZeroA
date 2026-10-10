package bridge

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"zeroa/internal/pi"
)

// 中止的回合在协议里长什么样，必须钉住：真实 Pi 会同时给
// `stopReason: "aborted"` 和 `errorMessage: "This operation was aborted"`。
//
// 界面靠 stopReason 判断"这是用户按的停止，不是失败"。少了这个断言，
// mock 就会退化成"只给 stopReason"，于是"按停止却弹出报错"这类问题
// 在 mock 下永远复现不出来（真实环境踩过一次）。
func TestAbortedMessageCarriesBothFields(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")

	var mu sync.Mutex
	var raw []string
	svc := &PiService{}
	svc.OnEvent = func(trace pi.Trace) {
		mu.Lock()
		raw = append(raw, trace.Raw)
		mu.Unlock()
	}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	if _, err := svc.Prompt("随便说点什么"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 等第一批文字出来再中止，否则可能在回合开始前就停了
	time.Sleep(150 * time.Millisecond)
	if _, err := svc.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	// 等这一轮收尾（不要用 waitIdle：它读的是别的测试里的包级变量）
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := svc.GetState(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, line := range raw {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role         string `json:"role"`
				StopReason   string `json:"stopReason"`
				ErrorMessage string `json:"errorMessage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.Type != "message_end" {
			continue
		}
		if event.Message.Role != "assistant" || event.Message.StopReason != "aborted" {
			continue
		}
		found = true
		if !strings.Contains(event.Message.ErrorMessage, "aborted") {
			t.Errorf("中止的助手消息应该带 errorMessage（真实 Pi 就是这样的），实际 %q", event.Message.ErrorMessage)
		}
	}
	if !found {
		t.Fatal("没有捕获到 stopReason=aborted 的 message_end")
	}
}
