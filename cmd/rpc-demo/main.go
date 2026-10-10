// rpc-demo 是这套代码里最短的一条完整调用链，也是最适合入门的地方：
//
//	启动子进程 → 发命令 → 等 response → 观察事件 → 优雅退出
//
// 它用的是和桌面界面完全相同的 internal/pi 客户端，只是不依赖 Wails，
// 所以可以在终端里直接看 JSONL 原文。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"zeroa/internal/mockpi"
	"zeroa/internal/pi"
)

func main() {
	// 同一个可执行文件既是客户端也是“假 Pi”：走 mock 模式时，
	// pi.Command() 会再启动一份自己并带上 --mock-pi，由这里进入服务端实现。
	// 好处是模拟模式也真的跨进程、走管道，不会绕过任何协议代码。
	if len(os.Args) > 1 && os.Args[1] == "--mock-pi" {
		mockpi.Serve(os.Stdin, os.Stdout)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1) // 非 0 退出码：脚本和 CI 靠它判断失败
	}
}

func run() error {
	// 常用组合：
	//   go run ./cmd/rpc-demo                         模拟器，默认发一条消息
	//   go run ./cmd/rpc-demo -mode real -check        连真实 Pi，只查状态不调用模型
	//   go run ./cmd/rpc-demo -mode real -prompt "..." 连真实 Pi 发一条消息
	//   go run ./cmd/rpc-demo -mode real -cmd get_available_models
	//                                                  只发一条只读命令并打印结果（不花钱）
	mode := flag.String("mode", "mock", "mock 或 real")
	cli := flag.String("pi", "", "可选：ZeroA cli.js/可执行文件路径")
	cwd := flag.String("cwd", ".", "ZeroA 工作目录")
	message := flag.String("prompt", "你好，解释一下 RPC。", "要发送的消息")
	check := flag.Bool("check", false, "只查询 get_state，不发送消息，不调用模型")
	command := flag.String("cmd", "", "只发一条只读 RPC 命令并打印 data，例如 get_available_models / get_messages / get_session_stats")
	model := flag.String("model", "", "可选：启动时指定模型（provider/id，也支持 id），空 = 用 ZeroA 配置的默认")
	thinking := flag.String("thinking", "", "可选：思考级别 low/medium/high/max，空 = 用默认")
	tools := flag.String("tools", "", "可选：工具白名单 read,write,edit（不含 bash 即禁止执行命令）；none = 全部禁用")
	where := flag.Bool("where", false, "只打印会用哪个 ZeroA（自带/全局/自定义）然后退出，不启动进程")
	flag.Parse()
	if *where {
		source := pi.DescribeCLI(*cli)
		if source.Path == "" {
			fmt.Println("找不到可用的 ZeroA 引擎")
			os.Exit(1)
		}
		fmt.Printf("kind=%s\nversion=%s\npath=%s\n", source.Kind, source.Version, source.Path)
		return nil
	}
	cmd, err := pi.Command(pi.Options{
		Mode:     *mode,
		CLIPath:  *cli,
		Cwd:      *cwd,
		Model:    *model,
		Thinking: *thinking,
		Tools:    toolsOption(*tools),
		// 与桌面端保持一致：配置目录用同一套解析（便携 .pi > ~/.pi/agent）
		AgentDir: pi.AgentDir(),
	})
	if err != nil {
		return err
	}
	// settled 用来把“事件流里出现了 agent_settled”这件事，从回调线程传到主流程。
	// 缓冲为 1 + 非阻塞发送：事件可能来多次，只记第一次就够了，回调绝不能被卡住。
	settled := make(chan struct{}, 1)
	// trace 回调会同时被 stdout 和 stderr 两个读 goroutine 调用，
	// 所以打印必须加锁，否则两行输出会互相穿插。
	var outputMu sync.Mutex
	client, err := pi.Start(cmd, func(trace pi.Trace) {
		outputMu.Lock()
		fmt.Printf("[%s] %-6s %s\n", trace.Time, trace.Direction, trace.Raw)
		outputMu.Unlock()
		// 学习点：这里不是“等待响应”，而是旁听事件流。
		// 客户端不需要为事件注册 id，事件天生就是广播给观察者的。
		var event struct {
			Type string `json:"type"`
		}
		if trace.Direction == "stdout" && json.Unmarshal([]byte(trace.Raw), &event) == nil && event.Type == "agent_settled" {
			select {
			case settled <- struct{}{}:
			default:
			}
		}
	})
	if err != nil {
		return err
	}
	defer client.Close() // 任何返回路径都会关掉子进程，不留孤儿进程
	// Ctrl+C 会取消 ctx；所有 Request 都带这个 ctx，所以能立刻收手。
	ctx, cancelSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancelSignal()
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := client.Request(deadline, "get_state", nil); err != nil {
		return err
	}
	if *check {
		return nil // -check：不调用模型，只验证“能连上、能一问一答”
	}
	// -cmd：只读命令（模型列表、消息、用量统计…），发一条就退出。
	// 模型列表就是这里来的：Pi 按自己的配置（~/.pi/agent/models.json）回答。
	if *command != "" {
		reply, err := client.Request(deadline, *command, nil)
		if err != nil {
			return err
		}
		fmt.Printf("\n=== %s ===\n%s\n", *command, reply.Data)
		return nil
	}
	response, err := client.Request(deadline, "prompt", map[string]any{"message": *message})
	if err != nil {
		return err
	}
	var data struct {
		Disposition string `json:"disposition"`
	}
	_ = json.Unmarshal(response.Data, &data)
	if data.Disposition == "handled" {
		// 扩展直接处理掉了这条 prompt，不会启动 agent，也就永远不会来 agent_settled。
		// 如果不特判，程序会一直等下去——这是真实 RPC 里的典型坑。
		return nil
	}
	// response 已经到了，但那只代表“命令被接受”。真正结束要等 agent_settled。
	select {
	case <-settled:
		return nil
	case <-client.Done():
		return fmt.Errorf("进程在 agent_settled 之前退出")
	case <-deadline.Done():
		// 超时了要主动收手：超时只是“我不等了”，不会让 Pi 停下来。
		abortCtx, cancelAbort := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelAbort()
		_, _ = client.Request(abortCtx, "abort", nil)
		return deadline.Err()
	}
}

// toolsOption 与 service.go 的 parseTools 保持同样的三种语义：
// 空 = 不干预；none/- = 全禁用；其余 = 白名单。
func toolsOption(tools string) []string {
	trimmed := strings.TrimSpace(tools)
	if trimmed == "" {
		return nil
	}
	if trimmed == "none" || trimmed == "-" {
		return []string{}
	}
	var out []string
	for _, part := range strings.Split(trimmed, ",") {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}
