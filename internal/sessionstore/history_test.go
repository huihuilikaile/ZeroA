package sessionstore

import (
	"encoding/json"
	"testing"
)

// 造一条带 id 链的记录：type → id → parentId。
func entry(typ, id, parent string) Entry {
	e := Entry{Type: typ, ID: id}
	if parent != "" {
		e.ParentID = &parent
	}
	return e
}

func message(id, parent, text string) Entry {
	e := entry("message", id, parent)
	e.Message = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"` + text + `"}]}`)
	return e
}

func modelChange(id, parent, provider, modelID string) Entry {
	e := entry("model_change", id, parent)
	e.Provider = provider
	e.ModelID = modelID
	return e
}

func thinkingChange(id, parent, level string) Entry {
	e := entry("thinking_level_change", id, parent)
	e.ThinkingLevel = level
	return e
}

// ModelState 要取当前分支上**最后一次**设置：旧的被新的覆盖，
// 别的分支（fork 出去的叉）上的设置不能混进来。
func TestModelState_LastOnCurrentBranch(t *testing.T) {
	// fork 出去的另一条分支：它的模型设置不该影响当前分支。
	// 注意放在主干消息**之前**：leaf 按文件顺序取最后一条带 id 的记录，
	// 放在末尾那条分支才会变成"当前分支"。
	entries := []Entry{
		entry("session", "s", ""),
		modelChange("m1", "s", "openai", "gpt-5.4"),
		thinkingChange("t1", "m1", "medium"),
		message("u1", "t1", "第一条"),
		modelChange("m2", "u1", "deepseek", "deepseek-v4-pro"),
		thinkingChange("t2", "m2", "high"),
		modelChange("x1", "u1", "anthropic", "claude-x"), // fork 分支
		message("u2", "t2", "第二条"),
	}
	provider, modelID, level := ModelState(entries)
	if provider != "deepseek" || modelID != "deepseek-v4-pro" || level != "high" {
		t.Fatalf("ModelState = %s/%s %s, 想要 deepseek/deepseek-v4-pro high", provider, modelID, level)
	}
}

// leaf 在 fork 分支上时，设置要跟着分支走。
func TestModelState_FollowsLeafBranch(t *testing.T) {
	entries := []Entry{
		entry("session", "s", ""),
		modelChange("m1", "s", "openai", "gpt-5.4"),
		message("u1", "m1", "主干"),
		// 当前 leaf 在 fork 分支上：它没见过 m1 之后的任何变化
		modelChange("x1", "u1", "anthropic", "claude-x"),
		message("u2", "x1", "分支消息"),
	}
	provider, modelID, _ := ModelState(entries)
	if provider != "anthropic" || modelID != "claude-x" {
		t.Fatalf("ModelState = %s/%s, 想要 anthropic/claude-x", provider, modelID)
	}
}

// 从没换过模型的会话：三个返回值都该是空串（调用方按"未知"处理，别编默认值）。
func TestModelState_NoneRecorded(t *testing.T) {
	entries := []Entry{
		entry("session", "s", ""),
		message("u1", "s", "只有消息"),
	}
	provider, modelID, level := ModelState(entries)
	if provider != "" || modelID != "" || level != "" {
		t.Fatalf("ModelState = %q/%q/%q, 想要全空", provider, modelID, level)
	}
}

// Messages 的行为不能因为抽了 BranchPath 而改变：仍然只给当前分支的消息。
func TestMessages_BranchStillRespected(t *testing.T) {
	entries := []Entry{
		entry("session", "s", ""),
		message("u1", "s", "根消息"),
		message("u2", "u1", "主干消息"),
		// fork 分支：leaf 仍在主干（u2 在最后），这条不该出现
		message("x1", "u1", "别分支消息"),
	}
	// 把 leaf 挪回主干：末尾再加一条主干消息
	entries = append(entries, message("u3", "u2", "主干结尾"))
	got := Messages(entries)
	if len(got) != 3 {
		t.Fatalf("Messages 给了 %d 条, 想要 3 条（根/主干/主干结尾）", len(got))
	}
	for _, raw := range got {
		if string(raw) == `{"role":"user","content":[{"type":"text","text":"别分支消息"}]}` {
			t.Fatal("别分支的消息混进了当前分支")
		}
	}
}
