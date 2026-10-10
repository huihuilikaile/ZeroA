package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * "直接读会话文件出消息"这条路径的回归测试（见 session_history.go）。
 *
 * 为什么单独立一组用例：这是"打开会话不再冷启动 Pi"唯一的内容来源——
 * 它一旦漏字段或挑错分支，界面照样渲染（因为形状还是 {messages:[...]}），
 * 只是内容不对：工具卡片变成"没有对应记录"、分过支的会话把别的分支也画出来。
 * 所以断言要盯住三件事：**形状与 get_messages 一致**、**工具调用配得上**、
 * **只给当前分支**。
 */

// writeSessionLines 按行写一份会话文件（每行一个 JSON 对象）。
func writeSessionLines(t *testing.T, path string, lines ...map[string]any) {
	t.Helper()
	var builder strings.Builder
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(raw)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// historyMessages 取 LoadSessionHistory 的 messages 数组。
func historyMessages(t *testing.T, result Result) []map[string]any {
	t.Helper()
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("data 应当是 {messages:[...]}: %v（%s）", err, result.Data)
	}
	return payload.Messages
}

// 基准：形状与 get_messages 一致，且工具的调用与结果**配得上**。
//
// "配对"这件事的落点在这里：assistant 的 toolCall 块带 id，toolResult 消息带
// 消息级 toolCallId，两者都原样出现在输出里（界面就是靠它们把一次工具调用
// 还原成一张卡片）。少任何一个字段，回放出来的工具行都会显示"没有对应记录"。
func TestLoadSessionHistoryParsesMessagesAndToolPairs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	path := filepath.Join(root, "2026-10-07T00-00-00-000Z_tools.jsonl")
	writeSessionLines(t, path,
		map[string]any{"type": "session", "id": "tools", "cwd": t.TempDir()},
		map[string]any{"type": "model_change", "id": "m1", "parentId": nil, "provider": "deepseek", "modelId": "x"},
		map[string]any{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "text", "text": "跑一下工具"}},
		}},
		map[string]any{"type": "message", "id": "a1", "parentId": "u1", "message": map[string]any{
			"role": "assistant", "stopReason": "toolUse",
			"content": []any{
				map[string]any{"type": "text", "text": "我来看看"},
				map[string]any{"type": "toolCall", "id": "call-1", "name": "bash",
					"arguments": map[string]any{"command": "ls"}},
			},
		}},
		map[string]any{"type": "message", "id": "t1", "parentId": "a1", "message": map[string]any{
			"role": "toolResult", "toolCallId": "call-1", "toolName": "bash", "isError": false,
			"content": []any{map[string]any{"type": "text", "text": "main.go"}},
		}},
		map[string]any{"type": "message", "id": "a2", "parentId": "t1", "message": map[string]any{
			"role": "assistant", "stopReason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": "看完了"}},
		}},
	)

	// 连 Start 都没跑过：读历史**不需要进程**，这正是这次改造的核心认识。
	svc := &PiService{}
	result, err := svc.LoadSessionHistory(path)
	if err != nil {
		t.Fatalf("LoadSessionHistory: %v", err)
	}
	if !result.Success || result.Command != "load_session_history" {
		t.Fatalf("返回不对: %+v", result)
	}
	messages := historyMessages(t, result)
	if len(messages) != 4 {
		t.Fatalf("应当有 4 条消息（user/assistant/toolResult/assistant），实际 %d: %s", len(messages), result.Data)
	}
	wantRoles := []string{"user", "assistant", "toolResult", "assistant"}
	for index, want := range wantRoles {
		if got := fmtSprint(messages[index]["role"]); got != want {
			t.Errorf("第 %d 条角色应当是 %s，实际 %s", index, want, got)
		}
	}

	// ① assistant 的工具调用块原样保留（id/name/arguments）
	callID := ""
	blocks, _ := messages[1]["content"].([]any)
	for _, item := range blocks {
		block, _ := item.(map[string]any)
		if fmtSprint(block["type"]) == "toolCall" {
			callID = fmtSprint(block["id"])
			if fmtSprint(block["name"]) != "bash" {
				t.Errorf("工具名丢了: %+v", block)
			}
			if !strings.Contains(fmtSprint(block["arguments"]), "ls") {
				t.Errorf("工具参数丢了: %+v", block)
			}
		}
	}
	if callID == "" {
		t.Fatalf("助手消息里的工具调用块没有被解析出来: %s", result.Data)
	}

	// ② toolResult 的消息级配对字段原样保留，并且真的能配上那个调用
	if got := fmtSprint(messages[2]["toolCallId"]); got != callID {
		t.Errorf("toolResult 的 toolCallId 应当是 %s，实际 %q（配不上的话界面会显示「没有对应记录」）", callID, got)
	}
	if fmtSprint(messages[2]["toolName"]) != "bash" {
		t.Errorf("toolResult 的 toolName 丢了: %+v", messages[2])
	}
	if _, ok := messages[2]["isError"].(bool); !ok {
		t.Errorf("toolResult 的 isError 丢了（界面靠它决定这张卡片是不是红的）: %+v", messages[2])
	}
}

// 分过支的会话只给**当前分支**：别的分支上的消息不能混进来。
//
// 会话是一棵只追加的树（fork 就是在旧节点上再分一条），而 get_messages 只给
// 当前所在的那条路径。全给出去的表现是"用户看到自己没说过的话"。
func TestLoadSessionHistoryFollowsCurrentBranch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	path := filepath.Join(root, "2026-10-07T00-00-01-000Z_branch.jsonl")
	message := func(role, text string) map[string]any {
		return map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}
	}
	writeSessionLines(t, path,
		map[string]any{"type": "session", "id": "branch", "cwd": t.TempDir()},
		map[string]any{"type": "message", "id": "r1", "parentId": nil, "message": message("user", "根节点问题")},
		map[string]any{"type": "message", "id": "b1", "parentId": "r1", "message": message("assistant", "第一个回答")},
		// 被放弃的分支：从 b1 继续往下走
		map[string]any{"type": "message", "id": "b2", "parentId": "b1", "message": message("user", "被放弃的问题")},
		map[string]any{"type": "message", "id": "b3", "parentId": "b2", "message": message("assistant", "被放弃的回答")},
		// 重新从 b1 分出来，并成为当前所在的位置（文件最后一条）
		map[string]any{"type": "message", "id": "c2", "parentId": "b1", "message": message("user", "新分支问题")},
		map[string]any{"type": "message", "id": "c3", "parentId": "c2", "message": message("assistant", "新分支回答")},
		// 树之外还有一条无关的元数据行（不能影响分支还原）
		map[string]any{"type": "session_info", "id": "n1", "parentId": "c3", "name": "改过名的会话"},
	)

	svc := &PiService{}
	result, err := svc.LoadSessionHistory(path)
	if err != nil {
		t.Fatalf("LoadSessionHistory: %v", err)
	}
	data := result.Data
	for _, want := range []string{"根节点问题", "第一个回答", "新分支问题", "新分支回答"} {
		if !strings.Contains(data, want) {
			t.Errorf("当前分支上应当有 %q：%s", want, data)
		}
	}
	for _, unwanted := range []string{"被放弃的问题", "被放弃的回答"} {
		if strings.Contains(data, unwanted) {
			t.Errorf("别的分支的内容不该出现（%q）：%s", unwanted, data)
		}
	}
	if messages := historyMessages(t, result); len(messages) != 4 {
		t.Errorf("当前分支应当有 4 条消息，实际 %d", len(messages))
	}
}

// 输入不合法一律报错，绝不 panic——这个入口的参数直接来自界面点击。
func TestLoadSessionHistoryRejectsBadInput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	svc := &PiService{}

	// 空路径
	if _, err := svc.LoadSessionHistory("   "); err == nil {
		t.Error("空路径应当报错")
	}
	// 不存在的文件
	if _, err := svc.LoadSessionHistory(filepath.Join(root, "nope.jsonl")); err == nil {
		t.Error("不存在的文件应当报错")
	}
	// 不是 .jsonl
	other := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(other, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoadSessionHistory(other); err == nil {
		t.Error("非 .jsonl 应当报错")
	}
	// 会话目录之外
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeSessionLines(t, outside, map[string]any{"type": "session", "id": "x", "cwd": root})
	if _, err := svc.LoadSessionHistory(outside); err == nil {
		t.Error("会话目录之外的文件应当报错")
	}
	// 首行不是会话头：碰巧叫 .jsonl 的文件不是会话记录
	notSession := filepath.Join(root, "not-session.jsonl")
	if err := os.WriteFile(notSession, []byte("{\"hello\":\"world\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoadSessionHistory(notSession); err == nil {
		t.Error("没有会话头的文件应当报错")
	}
	// 目录本身（不是普通文件）
	if _, err := svc.LoadSessionHistory(root); err == nil {
		t.Error("目录应当报错")
	}
}

// 坏行（进程被杀时写了一半）不该让整条历史打不开：前面的消息仍然要读出来。
func TestLoadSessionHistorySkipsBrokenLines(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	path := filepath.Join(root, "2026-10-07T00-00-02-000Z_partial.jsonl")
	raw, err := json.Marshal(map[string]any{"type": "session", "id": "partial", "cwd": root})
	if err != nil {
		t.Fatal(err)
	}
	good, err := json.Marshal(map[string]any{"type": "message", "id": "u1", "parentId": nil,
		"message": map[string]any{"role": "user", "content": "半截历史也要能读"}})
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw) + "\n" + string(good) + "\n" + `{"type":"message","id":"a1","paren`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := &PiService{}
	result, err := svc.LoadSessionHistory(path)
	if err != nil {
		t.Fatalf("半截历史不该整条打不开: %v", err)
	}
	if !strings.Contains(result.Data, "半截历史也要能读") {
		t.Errorf("好行应当被读出来：%s", result.Data)
	}
}
