package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 会话列表是「左侧工作区里的对话记录」的数据源，而 Pi 没有列会话的 RPC，
// 所以这段读文件逻辑必须有测试兜着：过滤、标题、排序、越界路径四条。
func TestListSessionsFiltersByCwdAndTitles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	target := filepath.Join(t.TempDir(), "workspace")
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	writeSession := func(group, name, id, cwd, firstUserText string) string {
		dir := filepath.Join(root, group)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		lines := []string{
			`{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-10-03T04:55:32.796Z","cwd":` + jsonString(cwd) + `}`,
			`{"type":"message","id":"m1","parentId":null,"timestamp":"2026-10-03T04:55:33.000Z","message":{"role":"user","content":` + jsonString(firstUserText) + `,"timestamp":1}}`,
			`{"type":"message","id":"m2","parentId":"m1","message":{"role":"assistant","content":[{"type":"text","text":"好"}],"timestamp":2}}`,
		}
		body := ""
		for _, line := range lines {
			body += line + "\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	mine := writeSession("--group-a--", "2026-10-03T04-55-32-796Z_01a1001e-10bc-712e-aae9-cf8fd602e4a0.jsonl", "01a1001e-10bc-712e-aae9-cf8fd602e4a0", target, "帮我给这个 Go 项目加一个 -version 参数\n第二行不该进标题")
	writeSession("--group-b--", "2026-09-30T13-41-37-699Z_01a0f28c-a123-7131-bf8d-e33b1a794d6e.jsonl", "01a0f28c-a123-7131-bf8d-e33b1a794d6e", other, "别的目录的对话")
	// 不是 Pi 会话文件的 .jsonl 必须被忽略，而不是当成一条空对话
	if err := os.WriteFile(filepath.Join(root, "--group-a--", "notes.jsonl"), []byte("{\"hello\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &PiService{}
	result, err := svc.ListSessions(target)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if !result.Success {
		t.Fatalf("ListSessions 失败: %+v", result)
	}

	var data struct {
		Cwd      string `json:"cwd"`
		Sessions []struct {
			ID    string `json:"id"`
			Path  string `json:"path"`
			Title string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}

	if len(data.Sessions) != 1 {
		t.Fatalf("应该只列出目标工作目录的会话，实际 %d 条: %+v", len(data.Sessions), data.Sessions)
	}
	got := data.Sessions[0]
	if got.ID != "01a1001e-10bc-712e-aae9-cf8fd602e4a0" {
		t.Errorf("会话 id 不对: %s", got.ID)
	}
	if got.Path != mine {
		t.Errorf("会话路径不对: %s", got.Path)
	}
	// 标题取第一条用户消息的第一行：比时间戳有用，且不能把第二行带进来
	if got.Title != "帮我给这个 Go 项目加一个 -version 参数" {
		t.Errorf("标题提取不对: %q", got.Title)
	}
}

// 切换会话只允许打开会话目录里的文件：否则界面上的一次点击就能变成任意文件读取。
func TestSwitchSessionRejectsOutsidePath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	svc := &PiService{}

	if _, err := svc.SwitchSession(filepath.Join(os.TempDir(), "whatever.jsonl")); err == nil {
		t.Fatal("会话目录之外的文件应该被拒绝")
	}
	if _, err := svc.SwitchSession("   "); err == nil {
		t.Fatal("空路径应该被拒绝")
	}
}

// jsonString 生成一个合法的 JSON 字符串字面量（Windows 路径里有反斜杠）。
func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
