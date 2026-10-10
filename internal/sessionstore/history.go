package sessionstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

/**
 * 本文件负责「读历史」：直接读会话文件把对话历史取出来。
 *
 * ## 为什么读历史不需要 Pi 进程
 *
 * Pi 的会话就是一份只追加的 JSONL（pi 的 docs/session-format.md）：
 *
 *	{"type":"session","id":...,"cwd":...,"timestamp":...}     会话头（第一行）
 *	{"type":"message","id":..,"parentId":..,"message":{...}}   一条消息
 *	{"type":"model_change" | "thinking_level_change" | "session_info" | ...}
 *
 * 消息本身（role / content / toolCallId …）完整地写在文件里，而 `get_messages`
 * 不过就是把这些消息从**内存**里取出来（rpc-mode.js：`{messages: session.messages}`）。
 * 两者内容一致，所以"打开一条历史会话"这件事根本不需要进程——实测冷启动是
 * 1–3.5 秒，而那几秒换来的只是一份磁盘上早就有的数组。
 */

// MaxLineBytes 是单行会话记录的长度上限。
//
// 与 ScanFile 保持一致（8MB）：会话里的工具结果可能很大（整份文件内容
// 都能进一行），缓冲区给小了会在读大历史时直接报"token too long"。
const MaxLineBytes = 8 * 1024 * 1024

// Entry 是会话文件里的一行（只取我们关心的字段）。
//
// ParentID 用指针：会话头的 parentId 可能是 null（树根），而"没有这个键"
// 与"显式 null"在分支还原里的处理是一样的，这里不区分。
//
// Provider/ModelID/ThinkingLevel 是 model_change / thinking_level_change
// 记录的载荷（{"provider":"deepseek","modelId":"…"} /
// {"thinkingLevel":"high"}）；其它类型的记录这三个字段是空串。
type Entry struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	ParentID *string         `json:"parentId"`
	Message  json.RawMessage `json:"message"`

	Provider      string `json:"provider"`
	ModelID       string `json:"modelId"`
	ThinkingLevel string `json:"thinkingLevel"`
}

// ReadEntries 逐行解析会话文件（坏行跳过，不让一行毁掉整条历史）。
func ReadEntries(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开会话文件失败: %w", err)
	}
	defer file.Close()

	entries := make([]Entry, 0, 64)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxLineBytes)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var entry Entry
		if json.Unmarshal(raw, &entry) != nil {
			/**
			 * 单行解析失败就跳过。
			 *
			 * 正在写入的会话最后一行可能是半截 JSON（进程被杀），而那份历史
			 * 前面 99% 都是好的——为了最后一行把整条会话判成"打不开"，
			 * 用户会以为历史丢了。
			 */
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		// 行太长或读盘出错：已经读到的部分仍然是有用的历史，日志里留一条痕迹。
		host.Logf("session_history_read_partial path=%s entries=%d err=%v", path, len(entries), err)
		if len(entries) == 0 {
			return nil, fmt.Errorf("读取会话文件失败: %w", err)
		}
	}
	return entries, nil
}

/**
 * Messages 从会话记录里挑出**当前分支**的消息，保持原有顺序。
 *
 * 为什么要还原分支：会话是一棵只追加的树（fork 就是在旧节点上再分一条），
 * 而 `get_messages` 只给"当前所在的那条路径"。把文件里的消息一股脑全给出去，
 * 分过支的会话就会把**别的分支**的内容也画进对话里——用户看到的是"我明明没说过这句话"。
 *
 * 做法与 Pi 自己一致：文件最后一条带 id 的记录就是当前所在的位置（leaf），
 * 顺着 parentId 一路回到根，这条链上的消息才是当前分支。
 */
func Messages(entries []Entry) []json.RawMessage {
	// 兜底顺序：id 不全（旧文件/被裁剪过）时按文件顺序给出全部消息——
	// 顺序至少是对的，比"什么都没有"有用得多。
	fallback := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		if entry.Type == "message" && len(entry.Message) > 0 {
			fallback = append(fallback, entry.Message)
		}
	}
	if len(fallback) == 0 {
		return nil
	}

	// 没有任何带 id 的记录时走兜底：分支无从谈起，按文件顺序给全部消息。
	path := BranchPath(entries)
	if len(path) == 0 {
		return fallback
	}

	messages := make([]json.RawMessage, 0, len(entries))
	for _, cursor := range path {
		entry := entries[cursor]
		if entry.Type == "message" && len(entry.Message) > 0 {
			messages = append(messages, entry.Message)
		}
	}
	if len(messages) == 0 {
		return fallback
	}
	return messages
}

/**
 * BranchPath 还原**当前分支**的记录位置，按根→叶的顺序返回。
 *
 * 与 Pi 自己一致：文件最后一条带 id 的记录就是当前所在的位置（leaf），
 * 顺着 parentId 一路回到根。Messages 用它挑消息，ModelState 用它挑
 * 模型设置——两条路必须走同一条分支，否则"消息是新分支的、模型却是
 * 旧分支的"这种拼接就出来了。
 */
func BranchPath(entries []Entry) []int {
	index := make(map[string]int, len(entries))
	for position, entry := range entries {
		if entry.ID != "" {
			index[entry.ID] = position
		}
	}
	leaf := -1
	for position := len(entries) - 1; position >= 0; position-- {
		if entries[position].ID != "" {
			leaf = position
			break
		}
	}
	if leaf < 0 {
		return nil
	}

	// 从 leaf 往根走，收集位置；visited 防环（文件被写坏时不能死循环）。
	path := make([]int, 0, len(entries))
	visited := make(map[string]bool, len(entries))
	for cursor := leaf; cursor >= 0; {
		entry := entries[cursor]
		if entry.ID != "" {
			if visited[entry.ID] {
				break
			}
			visited[entry.ID] = true
		}
		path = append(path, cursor)
		if entry.ParentID == nil || strings.TrimSpace(*entry.ParentID) == "" {
			break
		}
		next, ok := index[strings.TrimSpace(*entry.ParentID)]
		if !ok {
			// 父节点不在文件里（例如压缩/裁剪只保留了后半段）：就此停下，
			// 已经收集到的这段仍然是这条分支上真实的内容。
			break
		}
		cursor = next
	}

	// 翻转成根→叶：调用方按"会话实际发生的顺序"读。
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

/**
 * ModelState 取当前分支上**最后一次**模型/思考级别设置。
 *
 * 用途：打开一条还没有进程的历史会话时，界面要显示"这条会话用的是哪个
 * 模型"——答案写在会话文件里（model_change / thinking_level_change 记录），
 * 不需要为此启动 Pi。任一项没出现过就返回空串（调用方按"未知"处理，
 * 别编一个默认值冒充）。
 */
func ModelState(entries []Entry) (provider, modelID, thinkingLevel string) {
	for _, cursor := range BranchPath(entries) {
		entry := entries[cursor]
		switch entry.Type {
		case "model_change":
			if entry.Provider != "" {
				provider = entry.Provider
			}
			if entry.ModelID != "" {
				modelID = entry.ModelID
			}
		case "thinking_level_change":
			if entry.ThinkingLevel != "" {
				thinkingLevel = entry.ThinkingLevel
			}
		}
	}
	return provider, modelID, thinkingLevel
}
