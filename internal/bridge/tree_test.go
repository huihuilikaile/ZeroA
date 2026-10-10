package bridge

import (
	"encoding/json"
	"testing"
)

// 分支树：get_tree 给的是 [{entry, children}] + leafId。
// 界面靠它把"当前在哪个节点、有没有别的分支"画出来，所以形状必须钉住。
func TestGetTreeShapeFromMock(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	if _, err := svc.Prompt("树测试一条"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitTurnDone(t, svc)

	result, err := svc.GetTree()
	if err != nil || !result.Success {
		t.Fatalf("GetTree: %v %+v", err, result)
	}

	type treeNode struct {
		Entry struct {
			Type     string  `json:"type"`
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
			Role     string  `json:"role"`
		} `json:"entry"`
		Children []treeNode `json:"children"`
	}
	var payload struct {
		Tree   []treeNode `json:"tree"`
		LeafID string     `json:"leafId"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("data 应是 {tree:[...], leafId}: %v（%s）", err, result.Data)
	}
	// 结果必须是**数组**（文档明确：导航 API 可能造出多个根）
	if len(payload.Tree) == 0 {
		t.Fatalf("tree 不该为空: %s", result.Data)
	}
	root := payload.Tree[0]
	if root.Entry.ID == "" || root.Entry.Type == "" {
		t.Errorf("根节点要有 entry.id 与 entry.type: %+v", root.Entry)
	}
	if root.Entry.ParentID != nil {
		t.Errorf("根节点的 parentId 应为 null，实际 %v", *root.Entry.ParentID)
	}
	// 至少要有子节点（我们发过消息，链上不止一个节点）
	if len(root.Children) == 0 {
		t.Errorf("应有子节点，实际没有: %s", result.Data)
	}
	child := root.Children[0]
	if child.Entry.ParentID == nil || *child.Entry.ParentID != root.Entry.ID {
		t.Errorf("子节点的 parentId 应指向父节点 %s，实际 %v", root.Entry.ID, child.Entry.ParentID)
	}
	if payload.LeafID == "" {
		t.Error("应回 leafId（界面用它标记当前所在节点）")
	}
}
