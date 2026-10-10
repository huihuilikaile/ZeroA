package bridge

import (
	"encoding/json"
	"strings"
)

/**
 * 本文件负责「没有进程时的分支树」：从会话文件（只追加 JSONL）直接建树。
 *
 * 与 GetLastAssistantText 的离线分支同一个道理：建树需要的 id / parentId /
 * 消息正文全都写在文件里，为看它去冷启动一个进程（实测 1–3.5 秒）不值得。
 * 契约与有进程时完全一致（见 mockpi 的 get_tree）：
 *
 *	data = {tree: [{entry: {type,id,parentId,role?,text?}, children: [...]}], leafId}
 *
 * 前端因此不用分"有进程/没进程"两套逻辑。
 */

// treeNodeView 是 get_tree 契约里的一个节点。
// Children 初始化为空切片而不是 nil：契约里空节点也给 []（前端直接 map，
// 不为 null 兜底），marshal 出来才是 "children":[]。
type treeNodeView struct {
	Entry    treeEntryView   `json:"entry"`
	Children []*treeNodeView `json:"children"`
}

type treeEntryView struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	ParentID *string `json:"parentId"`
	Role     string  `json:"role,omitempty"`
	Text     string  `json:"text,omitempty"`
}

/**
 * buildOfflineTree 把会话记录拼成树，返回根列表与当前所在节点（leafId）。
 *
 * 口径与真实 Pi 的 SessionManager.getTree 逐条对齐（dist/bundle 里的实现）：
 *   · type=="session" 的会话头**不进树**（Pi 的 getEntries 显式排除它）；
 *   · parentId 为 null、指向自己、或指向不存在的记录 → 根（导航 API 可能
 *     造出多个根，所以 tree 是数组）；
 *   · leafId = 最后一条带 id 的非会话头记录（Pi 里 leafId 随每次追加更新，
 *     会话头只在建会话时存在，不会成为 leaf）。
 * Pi 的子节点按 timestamp 排序；文件本身就是追加序，按文件顺序挂树等价，
 * 所以这里不为排序再去解析 timestamp。
 *
 * 两趟扫描（先全建节点、再统一挂父）：父记录出现在子记录之后的损坏文件
 * 也能挂对。成环的子树挂不到任何根上、不会被渲染——与 Pi 的行为一致，
 * 而前端是递归渲染，绝不能让环进树。
 */
func buildOfflineTree(entries []sessionEntry) (roots []*treeNodeView, leafID string) {
	roots = []*treeNodeView{}
	index := make(map[string]*treeNodeView, len(entries))
	for _, entry := range entries {
		if entry.Type == "session" || entry.ID == "" {
			continue // 会话头与没有 id 的记录都进不了树（寻址与挂父都靠 id）
		}
		node := &treeNodeView{
			Entry:    treeEntryView{Type: entry.Type, ID: entry.ID, ParentID: entry.ParentID},
			Children: []*treeNodeView{},
		}
		if entry.Type == "message" && len(entry.Message) > 0 {
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(entry.Message, &msg) == nil {
				node.Entry.Role = msg.Role
				node.Entry.Text = lastTextBlock(msg.Content)
			}
		}
		index[entry.ID] = node
		leafID = entry.ID
	}
	for _, entry := range entries {
		node, ok := index[entry.ID]
		if !ok {
			continue
		}
		parentID := ""
		if entry.ParentID != nil {
			parentID = strings.TrimSpace(*entry.ParentID)
		}
		parent, found := index[parentID]
		if parentID == "" || parentID == entry.ID || !found {
			roots = append(roots, node)
		} else {
			parent.Children = append(parent.Children, node)
		}
	}
	return roots, leafID
}
