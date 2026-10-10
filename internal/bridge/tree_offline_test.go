package bridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// 「分支树」在无进程会话里的表现：树在磁盘的 .jsonl 里，
// 建树口径与真实 Pi 的 SessionManager.getTree 一致（会话头不进树、
// 父缺失/自指为根、leafId 是最后一条带 id 的记录）。

func msgEntry(id, parent string, message string) sessionEntry {
	e := sessionEntry{Type: "message", ID: id, Message: json.RawMessage(message)}
	if parent != "" {
		e.ParentID = &parent
	}
	return e
}

// 单链 + 会话头：根应该是第一条业务记录而不是会话头（Pi 的 getEntries 排除 session），
// leafId 落在最后一条记录上，消息的 role/text 从 message 载荷里展平出来。
func TestBuildOfflineTreeChain(t *testing.T) {
	entries := []sessionEntry{
		{Type: "session", ID: "sess-1"},
		{Type: "model_change", ID: "m1"}, // parentId nil → 根
		msgEntry("a", "m1", `{"role":"user","content":[{"type":"text","text":"问题"}]}`),
		msgEntry("b", "a", `{"role":"assistant","content":[{"type":"text","text":"回答"}]}`),
	}
	roots, leafID := buildOfflineTree(entries)
	if len(roots) != 1 || roots[0].Entry.ID != "m1" {
		t.Fatalf("应只有 model_change 一个根（会话头不进树），实际 %+v", roots)
	}
	if leafID != "b" {
		t.Errorf("leafId 应是最后一条记录 b，实际 %q", leafID)
	}
	user := roots[0].Children[0]
	if user.Entry.Role != "user" || user.Entry.Text != "问题" {
		t.Errorf("用户消息的 role/text 应展平在 entry 上，实际 %+v", user.Entry)
	}
	assistant := user.Children[0]
	if assistant.Entry.Role != "assistant" || assistant.Entry.Text != "回答" {
		t.Errorf("助手消息的 role/text 应展平在 entry 上，实际 %+v", assistant.Entry)
	}
	if assistant.Children == nil {
		t.Error("空节点的 children 应是 [] 而不是 null（契约：前端直接 map）")
	}
}

// 分叉：两条消息挂在同一父节点下，两个分支都要在树里。
func TestBuildOfflineTreeFork(t *testing.T) {
	entries := []sessionEntry{
		msgEntry("a", "", `{"role":"user","content":[{"type":"text","text":"问题"}]}`),
		msgEntry("b1", "a", `{"role":"assistant","content":[{"type":"text","text":"分支一"}]}`),
		msgEntry("b2", "a", `{"role":"assistant","content":[{"type":"text","text":"分支二"}]}`),
	}
	roots, _ := buildOfflineTree(entries)
	if len(roots) != 1 {
		t.Fatalf("应只有一个根，实际 %d 个", len(roots))
	}
	if got := len(roots[0].Children); got != 2 {
		t.Fatalf("分叉后父节点应有两个孩子，实际 %d 个", got)
	}
}

// 损坏文件兜底：父记录在子记录之后出现（两趟扫描仍能挂对）；
// parentId 指向自己 → 根（前端递归渲染，环绝不能进树）。
func TestBuildOfflineTreeCorrupt(t *testing.T) {
	entries := []sessionEntry{
		msgEntry("child", "late-parent", `{"role":"user","content":[{"type":"text","text":"子"}]}`),
		msgEntry("late-parent", "", `{"role":"user","content":[{"type":"text","text":"父"}]}`),
		msgEntry("self", "self", `{"role":"user","content":[{"type":"text","text":"自指"}]}`),
	}
	roots, _ := buildOfflineTree(entries)
	if len(roots) != 2 { // late-parent 与 self
		t.Fatalf("父后置挂回父下、自指升为根：应两个根，实际 %d 个", len(roots))
	}
	var parent *treeNodeView
	for _, root := range roots {
		if root.Entry.ID == "late-parent" {
			parent = root
		}
	}
	if parent == nil || len(parent.Children) != 1 || parent.Children[0].Entry.ID != "child" {
		t.Fatalf("父记录后置也应挂对父子关系: %+v", roots)
	}
}

// 待恢复的历史会话（pendingSessionFile 指着它）：无进程直接从文件建树，
// 契约与有进程一致（{tree, leafId}），不该拿"请先启动 ZeroA"弹红卡。
func TestGetTreeOffline(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	path := writeOfflineSession(t,
		`{"role":"user","content":[{"type":"text","text":"问题"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"回答"}]}`,
	)
	svc := &PiService{}
	svc.pendingSessionFile = path

	result, err := svc.GetTree()
	if err != nil || !result.Success {
		t.Fatalf("无进程建树不该报错: %v %+v", err, result)
	}
	var data struct {
		Tree []struct {
			Entry    json.RawMessage   `json:"entry"`
			Children []json.RawMessage `json:"children"`
		} `json:"tree"`
		LeafID string `json:"leafId"`
	}
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("应答 Data 应该是 JSON，实际 %q", result.Data)
	}
	if len(data.Tree) != 1 || data.LeafID != "b" {
		t.Errorf("应是一个根、leafId=b，实际 roots=%d leafId=%q", len(data.Tree), data.LeafID)
	}
	if !strings.Contains(result.Data, `"role":"user"`) || !strings.Contains(result.Data, `"text":"回答"`) {
		t.Errorf("树里应带展平的 role/text: %s", result.Data)
	}
}

// 全新会话（pendingSessionFile 为空）：空树 + 空 leafId 的成功应答，
// 前端按既有口径显示"这个会话还没有条目"。
func TestGetTreeOfflineNoSession(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}

	result, err := svc.GetTree()
	if err != nil || !result.Success {
		t.Fatalf("全新会话不该报错: %v %+v", err, result)
	}
	if !strings.Contains(result.Data, `"tree":[]`) || !strings.Contains(result.Data, `"leafId":""`) {
		t.Errorf("应是空树应答，实际 %s", result.Data)
	}
}
