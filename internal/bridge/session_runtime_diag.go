package bridge

import (
	"strings"
	"sync"

	"zeroa/internal/pi"
)

/**
 * 会话进程的诊断尾巴：**进程为什么会退出**。
 *
 * 为什么需要它（实测踩到的坑）：
 *   用户报告「点一下会话 → 出错了：切换会话 / Pi 在响应前退出」，日志里同时出现
 *   11 次 `session_runtime_closed reason=冷启动失败`——**但真正的原因一个字都没有**。
 *   新起的 Pi 进程在回答第一条命令前就死了，而它的 stderr（模块找不到、参数不认、
 *   端口被占、杀软拦下…）全都随进程一起消失。排查只能靠猜。
 *
 * 为什么放在这个文件（而不是改 session_runtime.go）：
 *   `session_runtime.go` 在别处正在被改动（同一时刻有两个写入者），
 *   而这份诊断只需要"看到每一行 trace"和"在进程退出时把它打出来"两件事——
 *   两者都能在事件出口（main.go 的 onEvent）完成，**一行都不用碰那个文件**。
 *
 * 代价与取舍：只保留最后 maxRuntimeStderrLines 行，且按 runtime 分开存。
 *   多进程场景下必须分开：否则 A 进程的报错会被当成 B 的。
 */
const maxRuntimeStderrLines = 30

var runtimeStderr = struct {
	mu   sync.Mutex
	tail map[string][]string
}{tail: map[string][]string{}}

// RecordRuntimeStderr 收下一条 stderr（由 main.go 的事件出口调用）。
//
// 只记非空行：Pi 的 stderr 里常有成片的空行与进度刷新，留着会把有效信息挤掉。
func (s *PiService) RecordRuntimeStderr(trace pi.Trace) {
	if trace.Direction != "stderr" {
		return
	}
	line := strings.TrimSpace(trace.Raw)
	if line == "" {
		return
	}
	runtimeStderr.mu.Lock()
	defer runtimeStderr.mu.Unlock()
	tail := append(runtimeStderr.tail[trace.RuntimeID], line)
	if len(tail) > maxRuntimeStderrLines {
		tail = tail[len(tail)-maxRuntimeStderrLines:]
	}
	runtimeStderr.tail[trace.RuntimeID] = tail
}

// RuntimeStderrSummary 取某个会话进程的 stderr 尾巴（一行，最多 1200 字）。
//
// 拼成一行是刻意的：它要进 gui-log 的单行日志，也要能塞进一句错误文本里给用户看。
func RuntimeStderrSummary(runtimeID string) string {
	runtimeStderr.mu.Lock()
	defer runtimeStderr.mu.Unlock()
	lines := runtimeStderr.tail[runtimeID]
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, " ⏎ ")
	if len(joined) > 1200 {
		joined = joined[len(joined)-1200:]
	}
	return joined
}

// ForgetRuntimeStderr 进程被正常收掉时清掉它的尾巴，避免长期占用内存
//（一条会话聊一天，stderr 尾巴最多也就几十行，但仍不该无限攒）。
func ForgetRuntimeStderr(runtimeID string) {
	runtimeStderr.mu.Lock()
	delete(runtimeStderr.tail, runtimeID)
	runtimeStderr.mu.Unlock()
}
