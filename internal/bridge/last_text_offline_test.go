package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 「复制上一条回答」在无进程会话里的表现：回答在磁盘的 .jsonl 里，
// 不该拿"请先启动 ZeroA"去吓人（此前 promise 直接 reject，点了没任何反应）。

// writeOfflineSession 在会话目录里造一条最小会话文件，返回路径。
func writeOfflineSession(t *testing.T, messages ...string) string {
	t.Helper()
	dir := filepath.Join(sessionsRoot(), "offline-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.jsonl")
	var lines strings.Builder
	lines.WriteString(`{"type":"session","id":"offline-1","cwd":"/tmp","timestamp":"2026-10-09T00:00:00Z"}` + "\n")
	parent := ""
	for i, message := range messages {
		id := string(rune('a' + i))
		parentField := `null`
		if parent != "" {
			parentField = `"` + parent + `"`
		}
		lines.WriteString(`{"type":"message","id":"` + id + `","parentId":` + parentField + `,"message":` + message + `}` + "\n")
		parent = id
	}
	if err := os.WriteFile(path, []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 待恢复的历史会话（pendingSessionFile 指着它）：直接读文件拿最后一条回答。
func TestLastAssistantTextOffline(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	path := writeOfflineSession(t,
		`{"role":"user","content":[{"type":"text","text":"问题"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"前半"},{"type":"text","text":"最终回答"}]}`,
	)
	svc := &PiService{}
	svc.pendingSessionFile = path

	result, err := svc.GetLastAssistantText()
	if err != nil || !result.Success {
		t.Fatalf("无进程读历史不该报错: %v %+v", err, result)
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("应答 Data 应该是 JSON，实际 %q", result.Data)
	}
	// 同一消息内多个 text 块取最后一个（与 mockpi / 有进程时的口径一致）
	if data.Text != "最终回答" {
		t.Errorf("应取最后一条助手消息的最后 text 块，实际 %q", data.Text)
	}
}

// 会话里还没有助手回答（只发过一句用户消息）：空串而不是报错，
// 前端按既有口径提示"还没有可复制的回答"。
func TestLastAssistantTextOfflineNoAnswer(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	path := writeOfflineSession(t, `{"role":"user","content":[{"type":"text","text":"问题"}]}`)
	svc := &PiService{}
	svc.pendingSessionFile = path

	result, err := svc.GetLastAssistantText()
	if err != nil || !result.Success {
		t.Fatalf("没有回答也该返回成功（空 text）: %v %+v", err, result)
	}
	if !strings.Contains(result.Data, `"text":""`) {
		t.Errorf("text 应为空串，实际 %s", result.Data)
	}
}

// 全新会话（pendingSessionFile 为空）：连文件都没有，同样是空串口径。
func TestLastAssistantTextOfflineNoSession(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}

	result, err := svc.GetLastAssistantText()
	if err != nil || !result.Success {
		t.Fatalf("全新会话不该报错: %v %+v", err, result)
	}
	if !strings.Contains(result.Data, `"text":""`) {
		t.Errorf("text 应为空串，实际 %s", result.Data)
	}
}

// 兼容旧形态：content 是裸字符串而不是块数组。
func TestLastTextBlockPlainString(t *testing.T) {
	if got := lastTextBlock(json.RawMessage(`"纯文本回答"`)); got != "纯文本回答" {
		t.Errorf("裸字符串 content 应原样返回，实际 %q", got)
	}
	if got := lastTextBlock(json.RawMessage(`[{"type":"thinking","text":"想"},{"type":"text","text":"说"}]`)); got != "说" {
		t.Errorf("应跳过非 text 块，实际 %q", got)
	}
}
