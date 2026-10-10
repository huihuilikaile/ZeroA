package bridge

import (
	"encoding/json"
	"fmt"

	"zeroa/internal/sessionstore"
)

/**
 * session_history.go：**直接读会话文件**把对话历史取出来。
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
 *
 * 解析与分支还原的实现已经迁入 internal/sessionstore（readSessionEntries /
 * sessionMessages 是别名），这里只剩服务层的边界校验与结果打包。
 *
 * ## 与 pendingSessionFile 的关系
 *
 * 读历史**不改变**"用户在看哪条会话"：那条记录由 SwitchSession 写进
 * pendingSessionFile（见 session_runtime.go）。进程留到用户发出第一条消息时
 * 由 ensureActiveRuntime 按这个路径现起，并且**先 switch_session 再发 prompt**——
 * 否则新进程会把消息写进一个刚出生的空会话里。
 */

/**
 * LoadSessionHistory 读一条会话文件，返回**与 get_messages 同形状**的
 * `{"messages":[...]}`。
 *
 * 形状必须一致：前端只用一套解析（rpc/router.ts 的 rebuildTimeline，经
 * routeMessages 进来），多一种形状就多一处会漂的解析代码。所以这里原样透传
 * 文件里的 message 对象（含 content 块、消息级 toolCallId/toolName/isError），
 * 一个字段都不改写。
 *
 * 输入不合法（不在会话目录内、不是 .jsonl、文件不存在、不是 Pi 的会话文件）
 * 一律返回 error，绝不 panic：这个入口的参数直接来自界面点击。
 */
func (s *PiService) LoadSessionHistory(sessionPath string) (Result, error) {
	// requireSessionPath 是唯一的口径：会话目录内的 .jsonl 普通文件，越界即报错。
	target, err := s.requireSessionPath(sessionPath)
	if err != nil {
		return Result{}, err
	}
	// 第一行必须是会话头：否则这就是一个碰巧叫 .jsonl 的文件，不是会话记录。
	if header := readSessionHeader(target); header.ID == "" {
		return Result{}, fmt.Errorf("不是 ZeroA 的会话文件（首行没有会话头）: %s", target)
	}
	entries, err := readSessionEntries(target)
	if err != nil {
		return Result{}, err
	}
	messages := sessionMessages(entries)
	if messages == nil {
		// 空数组而不是 null：前端一律按数组处理，少一个分支（与 mock 的 get_messages 一致）。
		messages = []json.RawMessage{}
	}
	// 顺带把"这条会话用的是哪个模型/哪档思考"交出去：打开历史会话时进程还没起，
	// 界面只能靠会话文件里的 model_change / thinking_level_change 记录显示模型名，
	// 否则模型徽标要等第一条消息发出（进程起来、get_state 应答）之后才出现。
	provider, modelID, thinkingLevel := sessionstore.ModelState(entries)
	payloadMap := map[string]any{"messages": messages}
	if modelID != "" {
		// 形状对齐 PiState 的 model 字段（{id, provider}），前端一份解析就够。
		payloadMap["model"] = map[string]any{"id": modelID, "provider": provider}
	}
	if thinkingLevel != "" {
		payloadMap["thinkingLevel"] = thinkingLevel
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return Result{}, err
	}
	Logf("session_history_loaded path=%s messages=%d model=%s/%s", target, len(messages), provider, modelID)
	return Result{Command: "load_session_history", Success: true, Data: string(payload)}, nil
}
