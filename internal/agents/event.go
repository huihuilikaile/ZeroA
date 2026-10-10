package agents

import (
	"encoding/json"
	"strings"
)

// Event 是子进程输出里我们真正会用到的字段。
//
// 刻意只声明必要的几个：Pi 的事件种类很多（message_update、tool_end、各种
// 生命周期），但"一个子智能体做完了什么"只需要最终文本与用量。
// 其余的原样走 TraceSink 抛给界面，不在这一层二次解释——每多解释一层，
// 就多一处会随 Pi 版本漂移的假设。
type Event struct {
	Type string `json:"type"`

	// message_end / message_update 里带的消息
	Message *EventMessage `json:"message,omitempty"`
	// 有些事件把用量放在顶层
	UsageRaw *EventUsage `json:"usage,omitempty"`

	// tool_start / tool_call 里的工具名与参数
	ToolName string          `json:"toolName,omitempty"`
	ToolArgs json.RawMessage `json:"args,omitempty"`

	// agent_settled / message_end 里的结束原因
	StopReason   string `json:"stopReason,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`

	Usage Usage `json:"-"`
}

// EventMessage 是事件里的消息体。
type EventMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
	Usage   *EventUsage     `json:"usage,omitempty"`
	/**
	 * StopReason 在**消息体里**：真实 Pi 的 message_end 写的是
	 * `{"message":{"role":"assistant","stopReason":"aborted",…}}`。
	 *
	 * 顶层那个同名字段是给 agent_end 一类事件用的，两处都要看——
	 * 只看顶层会永远读到空串，于是"被中断的一轮"会被当成正常完成（实测踩到）。
	 */
	StopReason string `json:"stopReason,omitempty"`
}

// Aborted 判断这一轮是不是被中断结束的（顶层与消息体两处都看）。
func (e Event) Aborted() bool {
	if strings.EqualFold(e.StopReason, "aborted") {
		return true
	}
	return e.Message != nil && strings.EqualFold(e.Message.StopReason, "aborted")
}

// EventUsage 是 Pi 的 usage 结构（只取我们展示与计价需要的）。
type EventUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
}

// DecodeEvent 把一行 JSONL 解析成事件。不是 JSON 或没有 type 就返回 false。
func DecodeEvent(raw string) (Event, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return Event{}, false
	}
	var event Event
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		return Event{}, false
	}
	if event.Type == "" {
		return Event{}, false
	}
	// usage 可能来自消息体，也可能来自事件顶层；两处都收。
	if event.Message != nil && event.Message.Usage != nil {
		event.Usage = usageFrom(*event.Message.Usage)
	} else if event.UsageRaw != nil {
		event.Usage = usageFrom(*event.UsageRaw)
	}
	return event, true
}

func usageFrom(raw EventUsage) Usage {
	total := raw.TotalTokens
	if total == 0 {
		total = raw.Input + raw.Output + raw.CacheRead + raw.CacheWrite
	}
	return Usage{
		Input:       raw.Input,
		Output:      raw.Output,
		CacheRead:   raw.CacheRead,
		TotalTokens: total,
	}
}

// AssistantText 取出 assistant 消息的纯文本。
//
// 第二个返回值表示"这条确实是 assistant 消息"。工具调用的内容块被忽略：
// 我们要的是它能读的结论，不是它调了什么（那是界面的事）。
func (e Event) AssistantText() (string, bool) {
	if e.Message == nil || e.Message.Role != "assistant" {
		return "", false
	}
	return textOfContent(e.Message.Content), true
}

// textOfContent 兼容两种形状：字符串，或内容块数组 [{type:"text",text:"…"}]。
func textOfContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// 形状一：直接是字符串
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	// 形状二：内容块数组
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type != "text" || block.Text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(block.Text)
	}
	return strings.TrimSpace(builder.String())
}

// ToolPhase 生成一行"它现在在干什么"，给界面用。
//
// 形如 `bash: file challenge.exe`——比只显示工具名信息量大得多，
// 而且不需要界面理解每个工具的 schema。
func (e Event) ToolPhase() string {
	name := strings.TrimSpace(e.ToolName)
	if name == "" {
		return ""
	}
	summary := summarizeArgs(e.ToolArgs)
	if summary == "" {
		return name
	}
	if len(summary) > 80 {
		summary = summary[:80] + "…"
	}
	return name + ": " + summary
}

// summarizeArgs 从工具参数里挑一个最能说明意图的字段。
func summarizeArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ""
	}
	// 按"信息量"排序：命令 > 路径 > 查询 > 其余第一个字符串
	for _, key := range []string{"command", "cmd", "path", "file_path", "filePath", "url", "query", "pattern"} {
		if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
			return flatten(value)
		}
	}
	for _, value := range fields {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return flatten(text)
		}
	}
	return ""
}

func flatten(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
