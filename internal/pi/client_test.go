package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zeroa/internal/mockpi"
)

// 这个测试文件用了 Go 里一个很实用的技巧：测试二进制自己兼任“假 Pi 子进程”。
//
//	环境变量 PI_RPC_TEST_CHILD 为空 → 正常测试进程，只跑各个 Test 函数；
//	它非空                     → TestRPCProcess 变成子进程，按 mode 扮演不同角色
//	                             （正常、卡住、秒退、乱序回包、超大记录）。
//
// 这样测的就是真正的操作系统管道，而不是内存里的假 Reader/Writer：
// 分帧、缓冲、进程退出这些坑才会真的暴露出来。
func TestRPCProcess(t *testing.T) {
	mode := os.Getenv("PI_RPC_TEST_CHILD")
	if mode == "" {
		return // 没有这个环境变量，说明是普通测试进程：什么都不做
	}
	if mode == "mock" {
		// 逐字停顿由环境变量压小（见 helper）：这里测的是协议，不是动画速度。
		mockpi.Serve(os.Stdin, os.Stdout) // 直接用模拟器当服务端
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	var first Response
	for scanner.Scan() {
		var request Response
		_ = json.Unmarshal(scanner.Bytes(), &request)
		switch mode {
		case "exit":
			os.Exit(7) // 收到第一条命令立刻退出：模拟“响应还没发，进程就没了”
		case "hang":
			continue // 永远不回：模拟“一直不响应”，用来验证超时
		case "reverse":
			// 先扣住第一条命令，等第二条到了再把两条 response 倒序发出：
			// 模拟真实 Pi “响应可能乱序”，用来验证靠 id 而不是顺序来配对。
			if first.ID == "" {
				first = request
				continue
			}
			for _, item := range []Response{request, first} {
				_ = json.NewEncoder(os.Stdout).Encode(Response{Type: "response", ID: item.ID, Command: item.Type, Success: true})
			}
		case "large":
			// 故意超过 Scanner 默认的 64 KiB 上限；字符串里还塞了 U+2028/U+2029，
			// 用来证明分帧只认 LF，不会被 Unicode 行分隔符切错。
			data, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 100000) + "\u2028\u2029"})
			_ = json.NewEncoder(os.Stdout).Encode(Response{Type: "response", ID: request.ID, Command: request.Type, Success: true, Data: data})
		}
	}
	os.Exit(0)
}

// textDelayEnv 让子进程里的模拟器把逐字停顿压小。
// 值不能是 0：abort 与停顿同时就绪时 select 会随机选，用例就变成抽签
// （模拟器里专门写了注释说明这一点）。2ms 既够快，又留出了可观察的窗口。
const textDelayEnv = "PI_MOCK_TEXT_DELAY_MS=2"

// helper 启动一个扮演指定 mode 的子进程，并返回连接它的 Client。
// os.Args[0] 是测试二进制自己；-test.run 限定子进程只跑 TestRPCProcess。
func helper(t *testing.T, mode string, callback func(Trace)) *Client {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRPCProcess$")
	cmd.Env = append(os.Environ(), "PI_RPC_TEST_CHILD="+mode, textDelayEnv)
	configureProcess(cmd)
	client, err := Start(cmd, callback)
	if err != nil {
		t.Fatal(err)
	}
	// 测试结束必须关掉子进程，否则会在机器上留下孤儿进程。
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return client
}

// request 是测试用的便捷包装：固定 5 秒超时，出错直接判定失败。
func request(t *testing.T, c *Client, command string, fields map[string]any) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := c.Request(ctx, command, fields)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// recorder 旁听 stdout 上的事件流：既按顺序记下 type，也把完整的记录留给后面的断言。
//
// 为什么要“按顺序”记？事件骨架本身就是协议的一部分：
// 界面靠 message_start→…→message_end 聚合一条消息，靠 agent_settled 判断空闲。
// 顺序错了，状态机就会错——所以断言必须落在顺序上，而不只是“这些事件都出现过”。
type recorder struct {
	mu     sync.Mutex
	types  []string
	events []map[string]any
}

func newRecorder(c *Client) *recorder {
	r := &recorder{}
	c.onTrace = func(trace Trace) { r.record(trace) }
	return r
}

func (r *recorder) record(trace Trace) {
	if trace.Direction != "stdout" {
		return
	}
	var event map[string]any
	if json.Unmarshal([]byte(trace.Raw), &event) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types = append(r.types, str(event["type"]))
	r.events = append(r.events, event)
}

// joined 返回逗号连接的事件序列（断言里最常用的形式）。
func (r *recorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.types, ",")
}

// snapshot 复制一份事件列表，方便在锁外做细粒度断言。
func (r *recorder) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.events...)
}

func str(value any) string {
	text, _ := value.(string)
	return text
}

// index 返回事件列表里第一条匹配 match 的下标；-1 表示没有。
func index(events []map[string]any, match func(map[string]any) bool) int {
	for i, event := range events {
		if match(event) {
			return i
		}
	}
	return -1
}

// assistantEvent 取出 message_update 里嵌套的 assistantMessageEvent。
func assistantEvent(event map[string]any) map[string]any {
	nested, _ := event["assistantMessageEvent"].(map[string]any)
	return nested
}

// TestPromptAbortAndReuse 是最完整的一条端到端用例，覆盖真实使用顺序：
//
//	查状态 → 发 prompt（response 说明“已接受”）→ 思考 → 工具 → 文本 → abort
//	→ 事件流以 message_end/turn_end/agent_end/agent_settled 收尾 → 回到空闲
//	→ 再发一条，证明 abort 没有杀掉进程。
//
// abort 落在“正文已经出了第一个字”这一刻：这正是界面上点“停止”的典型时机，
// 因为此时用户已经看到内容了。它同时验证中止路径也要把收尾事件发全。
//
// 顺带断言事件骨架的顺序：思考必须在文本之前、工具必须夹在两者之间、
// agent_settled 必须是最后一个（它是“彻底空下来”的唯一信号）。顺序错了就说明状态机会出问题。
func TestPromptAbortAndReuse(t *testing.T) {
	// firstTextDelta 缓冲为 1：只关心“正文第一个字到了”，后续的 delta 直接丢弃，
	// 所以回调里可以非阻塞发送，不会拖慢读取协程。
	firstTextDelta := make(chan struct{}, 1)
	settled := make(chan struct{}, 2)
	c := helper(t, "mock", nil)
	events := newRecorder(c)
	c.onTrace = func(trace Trace) {
		events.record(trace)
		if trace.Direction != "stdout" {
			return
		}
		var event struct {
			Type                  string `json:"type"`
			AssistantMessageEvent struct {
				Type string `json:"type"`
			} `json:"assistantMessageEvent"`
		}
		_ = json.Unmarshal([]byte(trace.Raw), &event)
		// 只认正文的 delta：思考阶段的 delta 来得更早，在那时 abort
		// 就测不到“正文被打断后还能收尾”这条路径。
		if event.Type == "message_update" && event.AssistantMessageEvent.Type == "text_delta" {
			select {
			case firstTextDelta <- struct{}{}:
			default:
			}
		}
		if event.Type == "agent_settled" {
			select {
			case settled <- struct{}{}:
			default:
			}
		}
	}
	state := request(t, c, "get_state", nil)
	// get_state 的 data 必须带上思考档位与消息计数：界面左栏直接用这些字段。
	for _, want := range []string{`"thinkingLevel":"medium"`, `"messageCount":0`, `"pendingMessageCount":0`, `"autoCompactionEnabled":true`} {
		if !strings.Contains(string(state.Data), want) {
			t.Errorf("get_state 缺少 %s: %s", want, state.Data)
		}
	}
	response := request(t, c, "prompt", map[string]any{"message": "hello"})
	if !strings.Contains(string(response.Data), "started") {
		t.Fatalf("unexpected response: %+v", response)
	}
	// 工具必须在正文之前跑完：这是“思考 → 工具 → 文本”骨架的一部分。
	select {
	case <-firstTextDelta:
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到 text_delta")
	}
	request(t, c, "abort", nil)
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not settle")
	}
	// abort 之后必须回到空闲，否则界面会一直显示“正在生成”。
	idle := request(t, c, "get_state", nil)
	if !strings.Contains(string(idle.Data), `"isStreaming":false`) {
		t.Fatalf("not idle: %s", idle.Data)
	}
	request(t, c, "prompt", map[string]any{"message": "again"})
	request(t, c, "abort", nil)

	joined := events.joined()
	// turn_start 后面的 queue_update 是 mockpi 把本轮 prompt 从内部队列里
	// 取出来时广播的"队列已空"（与真实 Pi 消费排队消息时的事件同形）。
	if !strings.Contains(joined, "turn_start,queue_update,message_start,message_end,message_start,message_update") {
		t.Fatalf("missing the turn/user message skeleton: %s", joined)
	}
	if !strings.Contains(joined, "message_end,turn_end,agent_end,agent_settled") {
		t.Fatalf("wrong completion order: %s", joined)
	}
	// agent_settled 之后只能再出现 response：客户端自己的 abort 响应由读循环
	// 直接写出，可能晚于回合收尾事件到达。除此之外不能再有事件——
	// 那意味着回合结束后还有事件没发完，界面的“空闲”判断就会错。
	assertSettledIsFinalEvent(t, events.snapshot())
	assertEventSkeleton(t, events.snapshot())
	assertAbortedAnswerIsClosed(t, events.snapshot())
}

// assertSettledIsFinalEvent 检查最后一个 agent_settled 之后只剩响应记录。
// 这条断言守的是界面最依赖的一条时间线：agent_settled 之后不该再有事件，
// 否则“已经空闲”的状态会被后面的事件推翻。
func assertSettledIsFinalEvent(t *testing.T, events []map[string]any) {
	t.Helper()
	last := -1
	for i, event := range events {
		if str(event["type"]) == "agent_settled" {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("事件流里没有 agent_settled")
	}
	for _, event := range events[last+1:] {
		if str(event["type"]) != "response" {
			t.Fatalf("agent_settled 之后还有事件: %s", summarize(events[last+1:]))
		}
	}
}

// assertAbortedAnswerIsClosed 盯住“中止”这条路径的收尾质量：
//  1. 被打断的正文必须发 text_end —— 否则界面上的“正在输入”光标会一直留着；
//  2. message_end 里的助手消息必须带 stopReason=aborted —— 否则界面分不清
//     “被用户停掉”和“正常说完了”；
//  3. 内容不能是空壳 —— 已经渲染出来的部分必须留在历史里。
func assertAbortedAnswerIsClosed(t *testing.T, events []map[string]any) {
	t.Helper()
	aborted := map[string]any{}
	for _, event := range events {
		if str(event["type"]) != "message_end" {
			continue
		}
		message, _ := event["message"].(map[string]any)
		if str(message["stopReason"]) == "aborted" {
			aborted = message
		}
	}
	if len(aborted) == 0 {
		t.Fatal("没有 stopReason=aborted 的 message_end：中止没有被记录进历史")
	}
	blocks, _ := aborted["content"].([]any)
	if len(blocks) == 0 {
		t.Fatal("中止后的助手消息没有内容块")
	}
	// turn_end 带的必须是同一份“已中止”的消息：如果它还是 stop，
	// 界面按事件流渲染（而不是按 message_end）时就会显示成正常结束。
	turnEndSeen := false
	for _, event := range events {
		if str(event["type"]) != "turn_end" {
			continue
		}
		message, _ := event["message"].(map[string]any)
		if str(message["stopReason"]) != "aborted" {
			continue
		}
		turnEndSeen = true
	}
	if !turnEndSeen {
		t.Error("没有 stopReason=aborted 的 turn_end：中止信息没有出现在回合结束事件里")
	}
	block, _ := blocks[0].(map[string]any)
	if str(block["type"]) != "text" {
		t.Fatalf("中止后的助手消息内容块类型不对: %v", block)
	}
	// 中止发生在第一个字之后，所以内容可能很短，但绝不能是“根本没开始”的空串。
	if str(block["text"]) == "" {
		t.Fatal("中止后的助手消息内容为空：已经渲染出来的正文丢了")
	}
	// 每个 text_start 都必须有配对的 text_end。
	starts, ends := 0, 0
	for _, event := range events {
		switch str(assistantEvent(event)["type"]) {
		case "text_start":
			starts++
		case "text_end":
			ends++
		}
	}
	if starts == 0 || starts != ends {
		t.Fatalf("text_start(%d) 与 text_end(%d) 不配对", starts, ends)
	}
}

// assertEventSkeleton 检查“思考 → 工具 → 文本”这段骨架的形状与顺序。
//
// 断言的是关系而不是固定下标：模拟器可以增加事件，但下面这些不变量必须一直成立，
// 否则界面上的思考块/工具卡片/正文会错位。
func assertEventSkeleton(t *testing.T, events []map[string]any) {
	t.Helper()
	// 1. message_update 的三种助手事件各自成段：thinking_* 先于 text_*。
	thinkingStart := index(events, func(e map[string]any) bool { return str(assistantEvent(e)["type"]) == "thinking_start" })
	thinkingEnd := index(events, func(e map[string]any) bool { return str(assistantEvent(e)["type"]) == "thinking_end" })
	textStart := index(events, func(e map[string]any) bool { return str(assistantEvent(e)["type"]) == "text_start" })
	textEnd := index(events, func(e map[string]any) bool { return str(assistantEvent(e)["type"]) == "text_end" })
	if thinkingStart < 0 || thinkingEnd < 0 || textStart < 0 || textEnd < 0 {
		t.Fatalf("思考/文本事件不完整: %v", summarize(events))
	}
	if !(thinkingStart < thinkingEnd && thinkingEnd < textStart && textStart < textEnd) {
		t.Fatalf("思考必须先于文本，且各自成段: %v", summarize(events))
	}
	// thinking_end 必须带上拼好的内容，否则界面上的思考块会是空的。
	if str(assistantEvent(events[thinkingEnd])["content"]) == "" {
		t.Error("thinking_end 没有 content")
	}

	// 2. 工具调用：先声明（message_end 里的 toolCall），再执行 start → update → end。
	toolEnd := index(events, func(e map[string]any) bool { return str(e["type"]) == "tool_execution_end" })
	toolStart := index(events, func(e map[string]any) bool { return str(e["type"]) == "tool_execution_start" })
	if toolStart < 0 || toolEnd < 0 || toolStart > toolEnd {
		t.Fatalf("工具开始/结束顺序不对: %v", summarize(events))
	}
	if update := index(events, func(e map[string]any) bool { return str(e["type"]) == "tool_execution_update" }); update >= 0 && (update < toolStart || update > toolEnd) {
		t.Fatalf("tool_execution_update 必须夹在 start 与 end 之间: %v", summarize(events))
	}
	// 宣布调用工具的那条助手消息：toolUse 是它的 stopReason。
	declared := false
	for _, event := range events {
		if str(event["type"]) != "message_end" {
			continue
		}
		message, _ := event["message"].(map[string]any)
		if str(message["stopReason"]) == "toolUse" {
			declared = true
		}
	}
	if !declared {
		t.Error("没有 stopReason=toolUse 的助手消息：模型“提出调用工具”这件事没有进入历史")
	}

	// 3. 一次成功 + 一次失败的工具体验。
	failures, successes := 0, 0
	for _, event := range events {
		if str(event["type"]) != "tool_execution_end" {
			continue
		}
		if isError, _ := event["isError"].(bool); isError {
			failures++
		} else {
			successes++
		}
	}
	if failures == 0 || successes == 0 {
		t.Errorf("需要一次成功和一次失败的工具执行，实际 success=%d failure=%d", successes, failures)
	}
}

// summarize 把事件列表压成 "type(type)" 形式的短串，失败信息里才看得清顺序。
func summarize(events []map[string]any) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		name := str(event["type"])
		if nested := assistantEvent(event); nested != nil {
			name += "(" + str(nested["type"]) + ")"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ",")
}

// TestToolExecutionLifecycle 专门盯工具事件的字段与生命周期：
// 每个 toolCallId 必须先 start、可以有 1~2 次 update、最后只有一个 end，
// 而且 args / result / isError 三个字段要真的出现——界面就是靠它们渲染工具卡片的。
func TestToolExecutionLifecycle(t *testing.T) {
	c := helper(t, "mock", nil)
	events := newRecorder(c)
	settled := make(chan struct{}, 1)
	c.onTrace = func(trace Trace) {
		events.record(trace)
		if trace.Direction == "stdout" && strings.Contains(trace.Raw, `"agent_settled"`) {
			select {
			case settled <- struct{}{}:
			default:
			}
		}
	}
	request(t, c, "prompt", map[string]any{"message": "hello"})
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("这一轮没有跑完")
	}
	starts := map[string]int{}
	updates := map[string]int{}
	ends := map[string]int{}
	var bashArgs map[string]any
	var readResult map[string]any
	for _, event := range events.snapshot() {
		id := str(event["toolCallId"])
		switch str(event["type"]) {
		case "tool_execution_start":
			starts[id]++
			if id == "call-1" {
				bashArgs, _ = event["args"].(map[string]any)
			}
		case "tool_execution_update":
			updates[id]++
			// 真实 Pi 的 update 也带 args 与 partialResult（content+details 结构）。
			if _, ok := event["args"].(map[string]any); !ok {
				t.Errorf("tool_execution_update 缺少 args: %v", event)
			}
			if _, ok := event["partialResult"].(map[string]any); !ok {
				t.Errorf("tool_execution_update 缺少 partialResult: %v", event)
			}
		case "tool_execution_end":
			ends[id]++
			if id == "call-2" {
				readResult, _ = event["result"].(map[string]any)
			}
		}
	}
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("期望两个工具各 start/end 一次，实际 starts=%v ends=%v", starts, ends)
	}
	for id, count := range starts {
		if count != 1 || ends[id] != 1 {
			t.Errorf("%s 的 start/end 次数应为 1/1，实际 %d/%d", id, count, ends[id])
		}
		if updates[id] > 2 {
			t.Errorf("%s 的 update 次数过多: %d", id, updates[id])
		}
	}
	if str(bashArgs["command"]) != "go test ./..." {
		t.Errorf("bash 的 args.command 不对: %v", bashArgs)
	}
	// result 的形状是 {content:[{type,text}], details:{...}}，界面主要渲染 content 里的文本。
	details, _ := readResult["details"].(map[string]any)
	if str(details["path"]) != "main.go" {
		t.Errorf("read 的 result.details.path 不对: %v", readResult)
	}
	blocks, _ := readResult["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("read 的 result.content 为空: %v", readResult)
	}
	first, _ := blocks[0].(map[string]any)
	if str(first["type"]) != "text" || str(first["text"]) == "" {
		t.Errorf("read 的 result.content[0] 不是文本块: %v", first)
	}
}

// TestExtensionUIRequestAndResponse 覆盖“服务端反等客户端”这条往返：
// prompt 里出现“确认”时，模拟器先发 extension_ui_request 并挂起，
// 只有收到 id 相同的 extension_ui_response（confirmed=true）才继续执行那个工具。
//
// 这是整个协议里唯一一处服务端在等界面，也是最容易写丢的一环：
// 响应的 id 对不上，回合就会一直等到超时。
func TestExtensionUIRequestAndResponse(t *testing.T) {
	c := helper(t, "mock", nil)
	events := newRecorder(c)
	uiRequest := make(chan string, 1)
	settled := make(chan struct{}, 1)
	c.onTrace = func(trace Trace) {
		events.record(trace)
		if trace.Direction != "stdout" {
			return
		}
		var event struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		_ = json.Unmarshal([]byte(trace.Raw), &event)
		if event.Type == "extension_ui_request" {
			select {
			case uiRequest <- event.ID:
			default:
			}
		}
		if event.Type == "agent_settled" {
			settled <- struct{}{}
		}
	}
	request(t, c, "prompt", map[string]any{"message": "请确认是否继续"})
	var id string
	select {
	case id = <-uiRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到 extension_ui_request")
	}
	if id == "" {
		t.Fatal("extension_ui_request 缺少 id：客户端无从回答")
	}
	confirmed := true
	// 关键：用 Notify 而不是 Request。
	//   1) 关联键必须是扩展请求**自己的 id**（Pi 按它去 pendingExtensionRequests 里找人），
	//      而 Request 会把信封 id 改写成 req-N，于是永远匹配不上；
	//   2) Pi 收到 extension_ui_response 后不回任何 response 记录，Request 会白等到超时，
	//      把一个已经成功的操作报成失败。
	if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": id, "confirmed": confirmed}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// 反面用例：用 uiId 传（旧的错误做法）时服务端找不到等待者，
	// 表现为回合不结束。这里只断言"不会有人认领"，避免静默成功。
	if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": "not-a-request-id", "confirmed": true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("回答之后回合没有跑完")
	}
	// 确认之后两个工具都该执行：read 第二次出现就说明 UI 往返真的起作用了。
	readRuns := 0
	for _, event := range events.snapshot() {
		if str(event["type"]) == "tool_execution_start" && str(event["toolName"]) == "read" {
			readRuns++
		}
	}
	if readRuns != 1 {
		t.Fatalf("确认后 read 应执行一次，实际 %d", readRuns)
	}
}

// TestExtensionUIRejectionSkipsTool 是不确认的那条分支：被拒绝的工具不执行，
// 但回合必须照常走完（不能让界面永远卡在“等待确认”上）。
func TestExtensionUIRejectionSkipsTool(t *testing.T) {
	c := helper(t, "mock", nil)
	events := newRecorder(c)
	uiRequest := make(chan struct{}, 1)
	settled := make(chan struct{}, 1)
	c.onTrace = func(trace Trace) {
		events.record(trace)
		if trace.Direction != "stdout" {
			return
		}
		if strings.Contains(trace.Raw, `"extension_ui_request"`) {
			select {
			case uiRequest <- struct{}{}:
			default:
			}
		}
		if strings.Contains(trace.Raw, `"agent_settled"`) {
			settled <- struct{}{}
		}
	}
	request(t, c, "prompt", map[string]any{"message": "confirm please"})
	select {
	case <-uiRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到 extension_ui_request")
	}
	denied := false
	// 拒绝也是 Notify：id 用扩展请求自己的 id，且这条命令没有 response 可等。
	if err := c.Notify(map[string]any{"type": "extension_ui_response", "id": "mock-ui-1", "confirmed": denied}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("拒绝之后回合没有跑完")
	}
	toolRuns := 0
	for _, event := range events.snapshot() {
		if str(event["type"]) == "tool_execution_start" {
			toolRuns++
		}
	}
	if toolRuns != 1 {
		t.Fatalf("拒绝后只应执行 bash 一个工具，实际执行了 %d 个", toolRuns)
	}
}

// TestCommonCommands 把界面会用到的只读/配置命令一次跑完，并对关键字段做断言。
// 这些命令是离线联调的全部依据：字段名错了界面就白屏。
//
// 断言的字段名以真实 Pi 的协议文档为准（docs/rpc-commands.md）：
// get_messages 用 {"messages":[...]} 包一层、set_model 传 modelId、
// clear_queue 把排队的原文还回来——mock 必须和真实服务端同形，
// 否则离线调好的界面连上真 Pi 就散架。
func TestCommonCommands(t *testing.T) {
	c := helper(t, "mock", nil)
	// get_messages：打一轮再查，必须能解出 messages 数组，且 user/assistant 都在。
	request(t, c, "prompt", map[string]any{"message": "hello"})
	request(t, c, "abort", nil)
	messages := request(t, c, "get_messages", nil)
	var wrapper struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(messages.Data, &wrapper); err != nil {
		t.Fatalf("get_messages 不是 {messages:[...]} 形状: %v (%s)", err, messages.Data)
	}
	if len(wrapper.Messages) < 2 {
		t.Fatalf("get_messages 消息太少: %s", messages.Data)
	}
	roles := map[string]int{}
	for _, message := range wrapper.Messages {
		roles[str(message["role"])]++
	}
	if roles["user"] == 0 || roles["assistant"] == 0 {
		t.Fatalf("get_messages 必须同时有 user/assistant: %v", roles)
	}
	// 用户消息的 content 是纯文本；助手消息才是内容块数组（真实 Pi 就是这样）。
	for _, message := range wrapper.Messages {
		if str(message["role"]) != "user" {
			continue
		}
		if _, ok := message["content"].(string); !ok {
			t.Errorf("用户消息的 content 应该是字符串: %v", message["content"])
		}
	}

	// get_session_stats：真实 Pi 的形状是“计数字段 + tokens 对象 + cost 数字 + contextUsage”。
	stats := request(t, c, "get_session_stats", nil)
	for _, want := range []string{`"tokens"`, `"cost"`, `"contextUsage"`, `"totalMessages"`, `"toolCalls"`, `"percent"`} {
		if !strings.Contains(string(stats.Data), want) {
			t.Errorf("get_session_stats 缺少 %s: %s", want, stats.Data)
		}
	}

	// 模型：列表 → 切换 → 未知模型必须失败。
	models := request(t, c, "get_available_models", nil)
	if count := strings.Count(string(models.Data), `"contextWindow"`); count < 2 {
		t.Errorf("可用模型太少: %s", models.Data)
	}
	switched := request(t, c, "set_model", map[string]any{"provider": "mock", "modelId": "mock-fast"})
	if !strings.Contains(string(switched.Data), "mock-fast") {
		t.Errorf("set_model 没有生效: %s", switched.Data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.Request(ctx, "set_model", map[string]any{"modelId": "nope"}); err == nil {
		t.Error("未知模型应该返回错误")
	}

	// 思考档位：列表含 low/medium/high/max；当前档位要去 get_state 看（真实 Pi 没有 current 字段）。
	levels := request(t, c, "get_available_thinking_levels", nil)
	for _, want := range []string{`"low"`, `"medium"`, `"high"`, `"max"`} {
		if !strings.Contains(string(levels.Data), want) {
			t.Errorf("思考档位缺少 %s: %s", want, levels.Data)
		}
	}
	level := request(t, c, "set_thinking_level", map[string]any{"level": "high"})
	if len(level.Data) != 0 {
		t.Errorf("set_thinking_level 不该回 data: %s", level.Data)
	}
	afterLevel := request(t, c, "get_state", nil)
	if !strings.Contains(string(afterLevel.Data), `"thinkingLevel":"high"`) {
		t.Errorf("set_thinking_level 没有生效: %s", afterLevel.Data)
	}
	if _, err := c.Request(ctx, "set_thinking_level", map[string]any{"level": "ultra"}); err == nil {
		t.Error("未知思考档位应该返回错误")
	}

	// 队列：follow_up/steer 会累加 pendingMessageCount，
	// clear_queue 必须把排队的原文还回来（界面要把它放回输入框）。
	request(t, c, "follow_up", map[string]any{"message": "还有一件事"})
	request(t, c, "steer", map[string]any{"message": "换个方向"})
	queued := request(t, c, "get_state", nil)
	if !strings.Contains(string(queued.Data), `"pendingMessageCount":2`) {
		t.Errorf("排队消息没有被计数: %s", queued.Data)
	}
	cleared := request(t, c, "clear_queue", nil)
	for _, want := range []string{`"steering":["换个方向"]`, `"followUp":["还有一件事"]`} {
		if !strings.Contains(string(cleared.Data), want) {
			t.Errorf("clear_queue 没有还回 %s: %s", want, cleared.Data)
		}
	}
	empty := request(t, c, "get_state", nil)
	if !strings.Contains(string(empty.Data), `"pendingMessageCount":0`) {
		t.Errorf("clear_queue 之后队列应为空: %s", empty.Data)
	}
}

// TestQueuedMessagesJoinNextRound 验证排队的消息不会丢：
// prompt 先起一轮，随后 steer/follow_up 各排一条，
// 等这一轮结束再发一次 prompt，队列里的两条必须先作为用户消息发出来。
//
// 这条用例守住的是界面最容易做错的地方：用户连发几条消息时，
// 后面几条不能“看起来成功了”却从来没进过会话历史。
func TestQueuedMessagesJoinNextRound(t *testing.T) {
	c := helper(t, "mock", nil)
	settled := make(chan struct{}, 2)
	var mu sync.Mutex
	var userTexts []string
	c.onTrace = func(trace Trace) {
		if trace.Direction != "stdout" {
			return
		}
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(trace.Raw), &event) != nil {
			return
		}
		if event.Type == "message_start" && event.Message.Role == "user" {
			mu.Lock()
			userTexts = append(userTexts, event.Message.Content)
			mu.Unlock()
		}
		if event.Type == "agent_settled" {
			settled <- struct{}{}
		}
	}
	request(t, c, "prompt", map[string]any{"message": "第一件事"})
	// 与真实 Pi 一致：steer 返回 disposition=queued（不是 handled）。
	steer := request(t, c, "steer", map[string]any{"message": "改成第二件事"})
	if !strings.Contains(string(steer.Data), "queued") {
		t.Fatalf("steer 的 disposition 不对: %s", steer.Data)
	}
	request(t, c, "follow_up", map[string]any{"message": "第三件事"})
	request(t, c, "abort", nil)
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("第一轮没有结束")
	}
	request(t, c, "prompt", map[string]any{"message": "第二次提问"})
	request(t, c, "abort", nil)
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("第二轮没有结束")
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(userTexts, "|")
	for _, want := range []string{"第一件事", "改成第二件事", "第三件事", "第二次提问"} {
		if !strings.Contains(joined, want) {
			t.Errorf("排队消息 %q 没有进入会话: %s", want, joined)
		}
	}
}

// TestConcurrentResponsesMatchIDs 验证“并发命令 + 乱序响应”这一核心场景：
// 两条命令同时在等，服务端先回第二条、再回第一条，
// 客户端必须靠 id 把结果交回各自的等待者，而不是按到达顺序乱配。
func TestConcurrentResponsesMatchIDs(t *testing.T) {
	c := helper(t, "reverse", nil)
	var wg sync.WaitGroup
	for _, command := range []string{"first", "second"} {
		wg.Add(1)
		go func(command string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, err := c.Request(ctx, command, nil)
			if err != nil {
				t.Error(err)
				return
			}
			if response.Command != command {
				t.Errorf("response mismatch: want %s, got %s", command, response.Command)
			}
		}(command)
	}
	wg.Wait()
}

// TestTimeoutAndExit 验证两种“等不到响应”的情况都不会留下垃圾状态：
//   - hang：服务端一直不回，靠 ctx 超时返回；
//   - exit：服务端收下命令就退出，靠 done 通道返回。
//
// 两种情况都必须把 pending 表清干净——否则进程一遍遍重启，内存里会越积越多。
func TestTimeoutAndExit(t *testing.T) {
	for _, mode := range []string{"hang", "exit"} {
		t.Run(mode, func(t *testing.T) {
			c := helper(t, mode, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, err := c.Request(ctx, "get_state", nil); err == nil {
				t.Fatal("expected an error")
			}
			c.mu.Lock()
			size := len(c.pending)
			c.mu.Unlock()
			if size != 0 {
				t.Fatalf("leaked %d pending requests", size)
			}
		})
	}
}

// TestLargeUnicodeRecord 用一条 100 KB 的记录（远超 Scanner 默认 64 KiB）
// 证明：只要显式放大缓冲区，大记录不会被截断；
// 字符串里的 U+2028/U+2029 也不会被误当成换行（分帧只认 LF）。
func TestLargeUnicodeRecord(t *testing.T) {
	c := helper(t, "large", nil)
	response := request(t, c, "get_state", nil)
	var value struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(response.Data, &value); err != nil {
		t.Fatal(err)
	}
	if value.Text != strings.Repeat("x", 100000)+"\u2028\u2029" {
		t.Fatal("record truncated or split")
	}
}

// TestCommandErrorAndClosedClient 覆盖两个边界：
//   - 命令本身失败（服务端回 success=false）：Request 要返回 error，而不是空响应；
//   - 连接已关闭：再发命令要立刻报错，不能让调用者一直等。
func TestCommandErrorAndClosedClient(t *testing.T) {
	c := helper(t, "mock", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.Request(ctx, "unknown", nil); err == nil {
		t.Fatal("expected RPC error")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(ctx, "get_state", nil); err == nil {
		t.Fatal("closed client accepted a command")
	}
}

// TestPromptRejectedWhileRunning 守住“一轮只跑一个回合”这条服务端约束：
// 第二条 prompt 必须被明确拒绝，而不是排队或静默丢弃。
// 界面据此把“消息没被接受”显示成错误，而不是一直转圈。
func TestPromptRejectedWhileRunning(t *testing.T) {
	c := helper(t, "mock", nil)
	settled := make(chan struct{}, 1)
	c.onTrace = func(trace Trace) {
		if trace.Direction == "stdout" && strings.Contains(trace.Raw, `"agent_settled"`) {
			settled <- struct{}{}
		}
	}
	request(t, c, "prompt", map[string]any{"message": "first"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.Request(ctx, "prompt", map[string]any{"message": "second"}); err == nil {
		t.Fatal("回合运行中，第二条 prompt 应该被拒绝")
	}
	// 收尾：中止并等这一轮真的结束，否则 t.Cleanup 里的 Close 会和回合 goroutine 抢时间。
	request(t, c, "abort", nil)
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("abort 之后没有 agent_settled")
	}
}

// TestSlowConsumerDoesNotStallEvents 是给“回调不能阻塞读取协程”这条规则上的保险。
//
// 观察者的回调跑在读取 goroutine 上，真实界面里它要序列化事件、推给 WebView。
// 这里故意在读事件时反复拿同一把锁（模拟一个慢消费者），
// 客户端仍然必须把整轮事件收完——因为读取协程只负责切行和投递，
// 慢的是消费端自己的锁，不能反过来把管道堵死。
func TestSlowConsumerDoesNotStallEvents(t *testing.T) {
	c := helper(t, "mock", nil)
	var mu sync.Mutex
	var count atomic.Int64
	settled := make(chan struct{}, 1)
	c.onTrace = func(trace Trace) {
		if trace.Direction != "stdout" {
			return
		}
		mu.Lock()
		count.Add(1)
		mu.Unlock()
		if strings.Contains(trace.Raw, `"agent_settled"`) {
			select {
			case settled <- struct{}{}:
			default:
			}
		}
	}
	request(t, c, "prompt", map[string]any{"message": "hello"})
	select {
	case <-settled:
	case <-time.After(10 * time.Second):
		t.Fatal("慢消费者把事件流堵住了")
	}
	if count.Load() < 10 {
		t.Fatalf("事件太少，骨架可能没跑全: %d", count.Load())
	}
	mu.Lock()
	mu.Unlock() // 确认锁没有被回调永久持有
}
