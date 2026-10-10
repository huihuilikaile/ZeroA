// Package pi 实现本学习 Demo 需要的 Pi RPC 子集：一个长驻子进程 + stdin/stdout 上的 JSONL。
//
// 协议里只有三种记录（详见 docs/01-协议与数据流.md）：
//
//	command   客户端 → Pi：{"id":"req-1","type":"prompt","message":"..."}
//	response  Pi → 客户端：{"id":"req-1","type":"response","success":true,"data":{...}}
//	event     Pi → 客户端：{"type":"message_update",...}   没有 id，不属于任何请求
//
// 这个包只用标准库，不 import Wails/React：协议逻辑和界面框架分开，
// 才能被 cmd/rpc-demo 独立运行、被 go test 独立测试。
//
// 三条必须守住的规则（下面都在代码处注明了原因）：
//  1. 一次写入 = 一整行 JSON + LF，且同一时刻只有一个写入者；
//  2. 先把请求登记进 pending，再写 stdin，否则可能错过立即返回的 response；
//  3. response 靠 id 关联，不要假设“下一行就是我刚发的命令的结果”。
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Response 是 response 记录的公共字段。
//
// 注意 read() 也用它来解析 event 记录，目的只是先看 Type 字段：
// 只有 type=response 的记录才会被投递给等待中的请求，事件一律走观察器。
//
// Data 用 json.RawMessage 原样保留，由调用方按命令自行解析：
// 协议层不猜业务结构，Pi 增加字段时这里不需要跟着改。
type Response struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Trace 把线路上真实的每一行原样暴露出来，供命令行打印、界面右栏观察。
// 学 RPC 最快的办法就是看原始 JSONL：谁在什么时刻说了什么，一目了然。
type Trace struct {
	/**
	 * RuntimeID 标记这一行来自哪个**会话进程**。
	 *
	 * 一进程一会话之后，同一时刻可能有多个 Pi 进程在说话；事件、响应、stderr 都要带上
	 * 自己的归属，否则界面只能把它们全塞进"当前正在看的那条会话"（串台）。
	 * 空串 = 不属于任何已登记的会话进程（例如启动探测阶段的输出）。
	 */
	RuntimeID string `json:"runtimeId,omitempty"`
	Direction string `json:"direction"`
	Raw       string `json:"raw"`
	Time      string `json:"time"`
	// AgentID 标记这条记录属于哪个智能体：空 = 主会话，agent-N = 子智能体。
	// 没有它，界面和原始日志都分不清是谁在说话（父子进程的输出长得一模一样）。
	AgentID string `json:"agentId,omitempty"`
}

// Client 管理一个 Pi 子进程；一个 Client 对应一次启动，要重启就新建一个。
//
// 两把锁职责不同，不要合并：
//   - writeMu：只保护 stdin 写入。RPC 是行协议，两行 JSON 交错就全废了；
//   - mu：只保护 pending 表，协调 read goroutine 与各个 Request 调用者。
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex // 同一时刻只写出一整行 JSON + LF
	mu      sync.Mutex
	pending map[string]chan Response // id → 等待该 response 的通道
	nextID  atomic.Uint64            // 生成 req-1、req-2…；原子自增且不复用
	done    chan struct{}            // 进程退出时关闭，用来唤醒所有等待者
	onTrace func(Trace)              // 必须立刻返回；绝不能在回调里调用 Request/Close
}

// Start 启动一个子进程并挂上两个读取 goroutine。一个 Client 只对应一次启动。
//
// 这里的顺序不是随意的：
//  1. 先拿三种管道，再 cmd.Start()；中途失败要逐个 Close，避免句柄泄漏；
//  2. 启动成功后立刻起 read goroutine。子进程写 stdout 时如果没人读，
//     管道缓冲区（Windows 上约 64 KiB）写满后 Pi 就会阻塞在写操作上；
//  3. cmd.Wait 必须等两个 reader 读完再调用，见下面的注释；
//  4. 最后才 close(done)：它是“进程已退出”的唯一信号，一关所有等待者立即醒来。
func Start(cmd *exec.Cmd, onTrace func(Trace)) (*Client, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("启动 ZeroA: %w", err)
	}
	c := &Client{cmd: cmd, stdin: stdin, pending: make(map[string]chan Response), done: make(chan struct{}), onTrace: onTrace}
	c.trace("system", "process_started")
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); c.read(stdout, "stdout") }()
	go func() { defer readers.Done(); c.read(stderr, "stderr") }()
	go func() {
		// Wait 不能早于 reader 结束：cmd.Wait 会关闭 StdoutPipe/StderrPipe，
		// 提前调用就会丢掉子进程最后几行输出——包括最后的 agent_settled。
		readers.Wait()
		err := cmd.Wait()
		c.mu.Lock()
		// 进程已经没了，还在等 response 的请求不可能再等到结果：整表清空。
		// 各等待者随后会从 done 或 ctx 分支返回，不会永久阻塞。
		c.pending = make(map[string]chan Response)
		c.mu.Unlock()
		if err != nil {
			c.trace("system", "process_exit: "+err.Error())
		} else {
			c.trace("system", "process_exit")
		}
		close(c.done)
	}()
	return c, nil
}

// trace 只做观察，不参与协议：它把原始行交给上层（命令行打印 / 界面右栏）。
// 正因为它跑在读取 goroutine 上，回调必须立即返回，慢回调会拖慢整个读取循环。
func (c *Client) trace(direction, raw string) {
	if c.onTrace != nil {
		c.onTrace(Trace{Direction: direction, Raw: raw, Time: time.Now().Format("15:04:05.000")})
	}
}

// read 把一个方向上的字节流切成一行行 JSONL。stdout 是协议流，stderr 只是诊断。
func (c *Client) read(r io.Reader, direction string) {
	scanner := bufio.NewScanner(r)
	// 分帧规则：一条记录一行，用 LF 结束。JSON 编码器会把字符串里的换行转义成 \n，
	// 所以一条记录内部绝不会出现真实的换行，按行切分是安全的。
	// 注意 Go 的 ScanLines 只认 LF/CRLF，不认 Unicode U+2028/U+2029——
	// 那两个字节能安全地待在 JSON 字符串里（测试 TestLargeUnicodeRecord 覆盖了这点）。
	// 64 KiB 起始、4 MiB 上限：故意设一个上限，让损坏或超长的记录明确失败，
	// 而不是无声吞掉（超限的行为见本文件末尾和 README 的“约束”）。
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue // 容忍空行，协议本身不产生空行
		}
		c.trace(direction, line)
		if direction != "stdout" {
			continue // stderr 不参与协议，只给观察器看
		}
		var envelope Response
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			c.trace("system", "invalid_json: "+err.Error())
			continue // 一行坏了不代表整个连接坏了：记下来，继续读
		}
		if envelope.Type != "response" {
			continue
		} // 事件只交给观察器，绝不去唤醒某个请求的等待者
		c.mu.Lock()
		ch := c.pending[envelope.ID]
		delete(c.pending, envelope.ID) // 先摘掉再投递：同一 id 的响应只算一次
		c.mu.Unlock()
		if ch != nil {
			ch <- envelope
		} // 通道有 1 个缓冲：调用方可能已经超时离开，投递不能卡住读取循环
	}
	if err := scanner.Err(); err != nil {
		c.trace("system", direction+"_read_error: "+err.Error())
		// stdout 读不下去 = 协议流已经不可用，只能结束进程；
		// stderr 只是诊断信息，一行太长不该把进程杀掉。
		if direction == "stdout" {
			_ = c.cmd.Process.Kill()
		}
	}
}

// Request 发送一条命令并等待它的 response，是本 Demo 唯一的“请求-响应”入口。
//
// Notify 只写出一条记录，不等响应、也不登记 pending。
//
// 为什么需要它：有些"命令"在协议里其实没有响应。最典型的是
// extension_ui_response —— Pi 的实现（dist/rpc-mode.js）收到它之后只是
// 唤醒等待的扩展然后 return，**不会发 response 记录**。
// 如果走 Request，就会白等到 15 秒超时，还会把扩展请求 id 挤成 req-N。
//
// record 由调用方完整构造（包括 id）：调用方要的关联键是什么，就写什么。
func (c *Client) Notify(record map[string]any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return errors.New("ZeroA 进程已退出")
	default:
		c.trace("stdin", string(data))
		_, err = c.stdin.Write(append(data, '\n'))
		return err
	}
}

// Request 发送一条命令并等待它的响应。
//
// 全流程：生成 id → 先登记 → 写 stdin → 三路等待（响应 / 超时 / 进程退出）。
// 三处最容易写错的地方都在下面注明了原因。
func (c *Client) Request(ctx context.Context, command string, fields map[string]any) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err // 已经取消/超时，没必要再写一条命令出去
	}
	// id 是 response 的唯一关联键：原子自增保证并发也不重复，且不复用旧值，
	// 这样迟到的旧响应不可能误配给后面的新请求。
	id := fmt.Sprintf("req-%d", c.nextID.Add(1))
	message := make(map[string]any, len(fields)+2)
	for key, value := range fields {
		message[key] = value
	}
	message["id"], message["type"] = id, command
	data, err := json.Marshal(message)
	if err != nil {
		return Response{}, err
	}
	ch := make(chan Response, 1)
	c.mu.Lock()
	c.pending[id] = ch // 必须先登记再写：Pi 可能在我们开始等待之前就把 response 发回来了
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }() // 无论怎么返回都不留悬挂项

	// One complete record is written under writeMu. The write itself is not bound to
	// ctx: a record larger than the pipe buffer waits until Pi reads it, and Close()
	// kills the process after 3s, which unblocks the writer.
	c.writeMu.Lock()
	select {
	case <-c.done:
		err = errors.New("ZeroA 进程已退出") // 进程都没了，写也只会失败
	default:
		c.trace("stdin", string(data))
		_, err = c.stdin.Write(append(data, '\n')) // 末尾的 LF 就是分帧符
	}
	c.writeMu.Unlock()
	if err != nil {
		return Response{}, err
	}
	select {
	case response := <-ch:
		if !response.Success {
			return response, fmt.Errorf("%s: %s", command, response.Error)
		}
		return response, nil
	case <-ctx.Done():
		// 超时只表示“我不等了”，并不等于 Pi 停止工作：
		// 想真正停下正在跑的回合，要显式发送 abort 命令（见 docs/01）。
		return Response{}, fmt.Errorf("%s 等待响应: %w", command, ctx.Err())
	case <-c.done:
		// 进程退出和响应可能同时就绪：再非阻塞看一眼通道，
		// 抢到响应就算成功，抢不到才判定“响应前退出”。
		select {
		case response := <-ch:
			if !response.Success {
				return response, errors.New(response.Error)
			}
			return response, nil
		default:
			return Response{}, errors.New("ZeroA 在响应前退出")
		}
	}
}

// Done 在 Pi 进程退出后关闭；上层用 select 它来判断“还连不连着”。
func (c *Client) Done() <-chan struct{} { return c.done }

// Close 优雅地结束这个子进程：先关 stdin（Pi 读到 EOF 会自己退出），超时才强杀。
// 本 Demo 禁用了工具，所以不做整棵进程树管理。
//
// 三个阶段的顺序：
//  1. 关 stdin 必须和“写入一行 JSON”互斥，否则可能半个记录写出去就被关掉；
//  2. 但拿锁本身可能被写阻塞（管道满了），所以拿不到锁时先强杀进程，
//     让那次 Write 失败并释放锁；
//  3. 最后等 done：进程退出事件到达才算真的结束。
func (c *Client) Close() error {
	// 关 stdin 的动作放到 goroutine 里，是为了能给它加超时。
	closed := make(chan struct{})
	go func() {
		c.writeMu.Lock()
		_ = c.stdin.Close() // 重复 Close 只会返回错误，不会 panic
		c.writeMu.Unlock()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill() // 有写入者卡在管道上：杀掉进程即可让它返回
		select {
		case <-closed:
		case <-time.After(time.Second):
			// 写还卡在内核里也不能让关机无限等下去
		}
	}
	select {
	case <-c.done:
		return nil
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill() // 温柔方式不奏效，只能强制
		select {
		case <-c.done:
			return nil
		case <-time.After(time.Second):
			return errors.New("等待进程退出超时")
		}
	}
}
