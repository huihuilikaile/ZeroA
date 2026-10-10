// Package agent 把 Pi 线路上的一行行文本，翻译成前端可以直接消费的“类型化事件”。
//
// 为什么要有这一层？internal/pi 的 Trace 是刻意保持“原样”的：
// 它只有 direction/raw/time 三个字段，谁都能用，但对界面并不友好——
// 前端要自己判断这一行是 response 还是 event、要不要 JSON.parse、
// 坏了该往哪儿放。这类判断一旦散落在 React 组件里，就会随事件类型增加而失控。
//
// 所以这里只做一件事：把一行文本变成一个信封（Envelope）。
// 协议层不认识业务字段，这一层也不认识——它只按“能不能解析、type 是不是 response”
// 分类，因此 Pi 增加新事件类型时这里不用改（前端按 type 自己分发即可）。
//
// 这一层不 import internal/pi：它只依赖“一行文本”这个最小输入，
// 所以既能接真实子进程的输出，也能在测试里直接喂字符串。
package agent

import (
	"encoding/json"
	"strings"
	"time"
)

// 事件的 kind 取值。前端的监听器只按 kind 做粗分流，再按 type 细分：
//   - event    Pi 主动发出的事件（事件流的主体）
//   - response 某条命令的响应（有 id，靠它回填请求结果）
//   - system   客户端侧生成的记录（进程启停、解析失败等），不是 Pi 说的话
//   - stderr   子进程的诊断输出（不属于协议）
const (
	KindEvent    = "event"
	KindResponse = "response"
	KindSystem   = "system"
	KindStderr   = "stderr"
)

// 方向取值。system 用来表示“这条不是从管道里读到的，而是客户端自己造出来的”。
const (
	DirectionStdout = "stdout"
	DirectionStderr = "stderr"
	DirectionSystem = "system"
)

// 解析失败时的 type：给前端一个稳定的字符串去判断“这行坏了”，
// 而不是让它去匹配 err.Error() 的具体措辞。
const TypeInvalidJSON = "invalid_json"

// TimeLayout 是信封里 Time 字段的格式（精确到毫秒）。
// 前端拿它直接显示，不需要再解析成 Date——观察器里“相对时间”靠人眼就够。
const TimeLayout = "15:04:05.000"

// Envelope 是发给前端的一个类型化事件（Wails 事件名 "pi:event"）。
//
// 它是有意“双份”的：Record 是解析后的对象，省掉前端一次 JSON.parse；
// Raw 是原始行，让“协议观察器”能显示真实字节，也方便排查
// Record 与 Raw 不一致的情况（编码问题、字段被吞等）。
type Envelope struct {
	// RuntimeID 是这条记录所属的会话进程（一进程一会话，见 session_runtime.go）。
	// 前端靠它把后台会话的输出留在后台，不串进当前正在看的那条会话。
	RuntimeID string `json:"runtimeId,omitempty"`
	AgentID   string `json:"agentId"`   // 目前恒为 "default"（为多 Agent 预留）
	SessionID string `json:"sessionId"` // 未知时为空串
	Kind      string `json:"kind"`      // "event" | "response" | "system" | "stderr"
	Type      string `json:"type"`      // 记录的 type 字段；system 记录用 "process_started"/"process_exit"/"invalid_json" 等
	Time      string `json:"time"`      // "15:04:05.000"
	Raw       string `json:"raw"`       // 原始 JSONL 行
	Record    string `json:"record"`    // 解析后的对象 JSON 文本；非 JSON 记录为空串
}

// Classify 把一行记录分类成信封：direction 取 "stdout"/"stderr"/"system"。
//
// 分类规则（顺序不能换，否则 response 会被当成普通事件）：
//  1. stderr / system 方向：不参与协议，Kind 直接定死，Record 留空；
//  2. 解析失败：Kind=system、Type="invalid_json"，Raw 仍然保留，
//     这样“哪一行坏了”在界面上还看得到原文；
//  3. 解析成功且 type=="response"：Kind=response（前端据此回填某条命令的结果）；
//  4. 其余合法 JSON：Kind=event。
//
// 只有“解析失败”和“完全不是 JSON 标记的记录”会走到 Record 为空的分支，
// 所以前端可以放心假设：Record 非空 ⇒ 可以 JSON.parse，不会再失败。
func Classify(direction, line string, at time.Time) Envelope {
	envelope := Envelope{
		AgentID: "default",
		Time:    at.Format(TimeLayout),
		Raw:     line,
	}
	// stderr 只是诊断信息，永远不参与协议；system 是客户端自己造的记录，
	// 两者都没有可解析的对象，直接给 kind 就返回。
	switch direction {
	case DirectionStderr:
		envelope.Kind = KindStderr
		return envelope
	case DirectionSystem:
		envelope.Kind = KindSystem
		// 客户端侧的记录文本是 "类型" 或 "类型: 细节"两种形状
		//（如 "process_started"，或进程崩溃时的 "process_exit: exit status 1"）。
		// Type 只保留冒号前的类型本身：下游全部按精确类型匹配
		//（前端路由 env.type === 'process_exit'、main.go 的退出日志、
		// ObserveAgentEvent 的回合收尾）。带着细节的 "process_exit: …" 会
		// 匹配不上 "process_exit"——崩溃退出时界面永远收不到"进程已死"，
		// 输入框卡在"运行中"，停止/插入全都报"Pi 在响应前退出"（实测故障）。
		// 细节不丢：完整文本仍在 Raw 里，观察器与日志照常用它。
		envelope.Type = SystemRecordType(line)
		return envelope
	}
	// 这里只取 type/command/sessionId 三个字段：分类用不到业务字段，
	// 多解析一次只是为了分类，代价可以忽略。
	var record struct {
		Type      string `json:"type"`
		Command   string `json:"command"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		// 坏行不能让整个观察器失效：标记出来，继续处理下一条。
		envelope.Kind = KindSystem
		envelope.Type = TypeInvalidJSON
		return envelope
	}
	envelope.SessionID = record.SessionID
	if record.Type == KindResponse {
		// response 有 id：前端拿它把结果交回对应的调用方。
		// 这里用 Command 更贴近界面显示习惯（“get_state ← 成功”）。
		envelope.Kind = KindResponse
		envelope.Type = record.Command
	} else {
		envelope.Kind = KindEvent
		envelope.Type = record.Type
	}
	// Record 直接用原始行：既然这一行已经是合法 JSON，就没有必要再 Marshal 一次。
	// 二次编码会重排字段、把数字改成浮点写法，观察器里看到的就不再是“原文”了。
	envelope.Record = line
	return envelope
}

// SystemRecordType 取一条 system 记录的类型部分（第一个冒号之前）。
//
// internal/pi 产生的 system 记录只有两类形状："process_started"、"process_exit"
// 这类纯类型，以及 "process_exit: exit status 1"、"stdout_read_error: …" 这类
// "类型: 细节"。类型里没有冒号，所以按第一个冒号切开是安全的。
func SystemRecordType(line string) string {
	if index := strings.IndexByte(line, ':'); index >= 0 {
		return line[:index]
	}
	return line
}
