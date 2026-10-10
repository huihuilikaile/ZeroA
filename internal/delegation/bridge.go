package delegation

// 本文件是"桥"：父 Pi 里的 delegate_task 扩展 → Go 的子智能体管理器。
//
// 为什么是 HTTP + 环境变量令牌，而不是让扩展自己 spawn 进程：
//
//	· 进程生命周期必须归 Go（能单独 kill、限额、观察、记账）；
//	· 扩展在 Node 里跑，HTTP 是两边唯一都天然支持的通道；
//	· 端口随机、只绑 127.0.0.1、令牌每会话随机，而且**只随进程环境传**——
//	  不落盘、不进日志。落盘的密钥迟早会出现在备份、日志或截图里。
//
// 扩展侧只有几十行：读环境变量、POST、把返回文本当工具结果。它不 spawn、
// 不 kill、不判权限、不管预算——那些全是 Go 的事。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"zeroa/internal/pi"
)

const (
	// DelegateURLEnv / DelegateTokenEnv 是注入 Pi 进程环境的两个变量名
	//（导出给主包测试做"环境变量与桥一致"的核对）。
	DelegateURLEnv   = "PI_GUI_DELEGATE_URL"
	DelegateTokenEnv = "PI_GUI_DELEGATE_TOKEN"
	delegateExtFile  = "delegate-task.ts"
)

// Guard 校验令牌（桥上的每个端点都要过它）。暴露给主包是为了让别的域
// （工具清单 catalog）能把端点挂到同一个桥上、共用同一套令牌。
type Guard func(next http.HandlerFunc) http.HandlerFunc

// bridge 是一个只服务本机的小控制口。
type bridge struct {
	token   string
	server  *http.Server
	address string
	manager *Manager
	mu      sync.Mutex
	stopped bool
}

// EnsureBridge 在第一次启动 Pi 之前把桥准备好（幂等）。
//
// extend 让主包把别的域的端点挂到同一个控制口上（工具清单 catalog 就走这条路，
// 与委派共用令牌）；可以为 nil。
func (m *Manager) EnsureBridge(extend func(mux *http.ServeMux, guard Guard)) error {
	m.mu.Lock()
	if m.bridge != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("委派通道监听失败: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		_ = listener.Close()
		return err
	}
	b := &bridge{
		token:   token,
		address: "http://" + listener.Addr().String(),
		manager: m,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/delegate", b.guard(b.handleDelegate))
	mux.HandleFunc("/agents", b.guard(b.handleAgents))
	mux.HandleFunc("/kill", b.guard(b.handleKill))
	// 可继续子智能体：给正在工作的子智能体发消息（steer/queue）、只中断它当前那一轮
	mux.HandleFunc("/message", b.guard(b.handleMessage))
	mux.HandleFunc("/interrupt", b.guard(b.handleInterrupt))
	// 别的域的端点（工具清单等）：与委派共用令牌
	if extend != nil {
		extend(mux, b.guard)
	}
	mux.HandleFunc("/health", b.guard(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	b.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := b.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log("delegate_bridge_serve_failed err=%v", err)
		}
	}()
	m.log("delegate_bridge_started address=%s", b.address)

	// 扩展写不进去不算致命：桥还在，界面与 Go 侧仍可用，
	// 只是"模型自己调 delegate_task"这条路暂时不通。
	if _, err := EnsureDelegateExtension(); err != nil {
		m.log("delegate_extension_failed err=%v", err)
	}

	m.mu.Lock()
	m.bridge = b
	m.mu.Unlock()
	return nil
}

// StopBridge 关掉控制口（应用退出时调）。返回是否真的停掉了一个桥（给日志用）。
func (m *Manager) StopBridge() bool {
	m.mu.Lock()
	b := m.bridge
	m.bridge = nil
	m.mu.Unlock()
	if b != nil {
		b.stop()
		return true
	}
	return false
}

func (b *bridge) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped || b.server == nil {
		return
	}
	b.stopped = true
	_ = b.server.Close()
}

// BridgeEnv 返回启动父 Pi 时要注入的两个环境变量（nil = 桥还没起）。
func (m *Manager) BridgeEnv() map[string]string {
	m.mu.Lock()
	b := m.bridge
	m.mu.Unlock()
	if b == nil {
		return nil
	}
	return map[string]string{
		DelegateURLEnv:   b.address,
		DelegateTokenEnv: b.token,
	}
}

// BridgeAddress 给测试与诊断用（手工 curl 桥的时候需要它）。
func (m *Manager) BridgeAddress() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bridge == nil {
		return ""
	}
	return m.bridge.address
}

// BridgeToken 给测试用（坏令牌/好令牌两条路都要验）。令牌本身不落盘、不进日志。
func (m *Manager) BridgeToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bridge == nil {
		return ""
	}
	return m.bridge.token
}

// TokenPreview 只给日志看前 6 位：完整令牌不进日志。
func (m *Manager) TokenPreview() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bridge == nil || len(m.bridge.token) < 6 {
		return ""
	}
	return m.bridge.token[:6] + "…"
}

// guard 校验令牌。没带对令牌的请求一律拒绝——本机上别的进程能连到这个端口，
// 所以"只绑 127.0.0.1"不够，令牌是必需的。
func (b *bridge) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-delegate-token") != b.token {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "令牌不对"})
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "方法不支持"})
			return
		}
		next(w, r)
	}
}

type delegateRequest struct {
	Role string `json:"role"`
	Task string `json:"task"`
	/**
	 * RuntimeID 是"哪条会话在派这个活"。
	 *
	 * 一进程一会话之后，同一个桥服务着好几条会话；不记归属的话，
	 * A 会话派的子智能体结果会被投到用户当时正在看的 B 会话里。
	 * 这个值来自 Pi 进程的环境变量（启动参数注入），由扩展在调用时带上。
	 */
	RuntimeID string `json:"runtimeId"`
}

func (b *bridge) handleDelegate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "读请求失败"})
		return
	}
	var request delegateRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不对"})
		return
	}
	// 归属为空时退回"当前会话"（老版本扩展/手工调用这条路的兼容）。
	parentID := strings.TrimSpace(request.RuntimeID)
	if parentID == "" {
		parentID = b.manager.deps.CurrentParentID()
	}
	payload, err := b.manager.Delegate(parentID, request.Role, request.Task)
	if err != nil {
		// 被闸门挡住是**正常结果**，不是服务器错误：模型需要看到原因并换个做法
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	message, _ := payload["message"].(string)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"agentId": payload["agentId"],
		"roleId":  payload["roleId"],
		// name/mode 要透出去：扩展（以及看日志的人）靠它判断"能不能继续跟它说话"
		"name":    payload["name"],
		"mode":    payload["mode"],
		"message": message,
	})
}

func (b *bridge) handleAgents(w http.ResponseWriter, r *http.Request) {
	/**
	 * 按会话隔离：扩展（某条会话里的模型）只能看到**自己会话**派出的子智能体。
	 *
	 * 不过滤的后果很具体：delegation_list 文案说"当前会话派出的"，实际列的是全部；
	 * 更糟的是 delegation_stop（不带 agentId）会照着这份全量列表，
	 * 把**别的会话**正在跑的子智能体一起停掉。runtimeId 为空（手工调用/老版本扩展）
	 * 时退回全量，保持兼容。
	 */
	runtimeID := strings.TrimSpace(r.URL.Query().Get("runtimeId"))
	if runtimeID == "" && r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		var request struct {
			RuntimeID string `json:"runtimeId"`
		}
		if len(body) > 0 && json.Unmarshal(body, &request) == nil {
			runtimeID = strings.TrimSpace(request.RuntimeID)
		}
	}
	payload, err := b.manager.ListFor(runtimeID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agents": payload["agents"]})
}

func (b *bridge) handleKill(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	var request struct {
		AgentID string `json:"agentId"`
		Reason  string `json:"reason"`
		// RuntimeID 指名"哪条会话在停它"：只能停自己会话的子智能体，
		// 否则 A 会话的模型一个 delegation_stop 就把 B 会话的活全端了。
		RuntimeID string `json:"runtimeId"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不对"})
		return
	}
	if denied := b.checkOwnership(request.RuntimeID, request.AgentID); denied != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": denied})
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = "调用方请求停止"
	}
	if err := b.manager.Kill(request.AgentID, reason); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// checkOwnership 校验"这个子智能体归发起请求的那条会话管"。
// runtimeID 为空（老版本扩展/手工调用）时不校验，保持兼容。返回空串 = 放行。
func (b *bridge) checkOwnership(runtimeID, agentID string) string {
	if strings.TrimSpace(runtimeID) == "" || strings.TrimSpace(agentID) == "" {
		return ""
	}
	if !b.manager.AgentBelongsTo(agentID, runtimeID) {
		return "这个子智能体不属于你的会话（它是别的会话派出的），不能由你来操作"
	}
	return ""
}

/**
 * handleMessage 是父模型版的 send_message（对应 DSH 的同名工具）。
 *
 * 模型发来的消息走 **steer**（赶最近的检查点插进去）——这是照 DSH 的固定分工：
 * 模型是用"边做边补充要求"的语气说话，而人从界面发消息才是 queue（不打断）。
 */
func (b *bridge) handleMessage(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var request struct {
		AgentID   string `json:"agentId"`
		Message   string `json:"message"`
		Delivery  string `json:"delivery"`
		RuntimeID string `json:"runtimeId"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不对"})
		return
	}
	if denied := b.checkOwnership(request.RuntimeID, request.AgentID); denied != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": denied})
		return
	}
	delivery := strings.TrimSpace(request.Delivery)
	if delivery == "" {
		delivery = "steer" // 模型默认 steer（人从界面发的走 queue）
	}
	payload, err := b.manager.Send(request.AgentID, request.Message, delivery)
	if err != nil {
		// 被拒绝是**正常结果**，不是服务器错误：模型需要看到原因并换个做法
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"message":   "已受理（" + delivery + "）。它的回复不会作为本次调用的结果返回，而是等它那一轮跑完后作为阶段结果出现在你的会话里。",
		"messageId": payload["messageId"],
	})
}

// handleInterrupt 是父模型版的 interrupt_agent：只停当前轮次，不销毁子智能体。
func (b *bridge) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	var request struct {
		AgentID   string `json:"agentId"`
		RuntimeID string `json:"runtimeId"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求格式不对"})
		return
	}
	if denied := b.checkOwnership(request.RuntimeID, request.AgentID); denied != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": denied})
		return
	}
	if err := b.manager.Interrupt(request.AgentID); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "已请求中断它当前这一轮；它还在，之后仍可以用 send_to_agent 继续派活。",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func randomToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// EnsureDelegateExtension 把桥的扩展写进用户扩展目录。
//
// 由应用自己写而不是让用户手动装：这个扩展与 Go 侧的协议是一对一的
// （环境变量名、路径、响应格式），版本不同步就会"工具在、但一调就失败"。
// 内容变了才重写，避免每次启动都动用户的文件。
func EnsureDelegateExtension() (string, error) {
	dir := filepath.Join(pi.AgentDir(), "extensions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, delegateExtFile)
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == DelegateExtensionSource {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(DelegateExtensionSource), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// DelegateExtensionPathForLog 给日志用（排障时想知道扩展写哪了）。
func DelegateExtensionPathForLog() string {
	return filepath.Join(pi.AgentDir(), "extensions", delegateExtFile)
}

// DelegateExtensionSource 是写到用户扩展目录里的那段 TypeScript。
//
// 它刻意做得极薄：注册一个工具、读两个环境变量、POST 一次、把返回文本交回去。
// 任何"聪明的逻辑"（重试、排队、判断该不该派）都属于 Go，不属于这里——
// 放在这里就意味着它绕过了权限与预算。
const DelegateExtensionSource = `/**
 * delegate_task —— 把任务交给子智能体（由工作台生成，不要手改）。
 *
 * 这个文件由 Pi GUI 应用自动写入并与 Go 侧的协议一一对应：
 *   · 地址与令牌来自进程环境（PI_GUI_DELEGATE_URL / PI_GUI_DELEGATE_TOKEN），
 *     不落盘；不是由工作台启动的会话里它们不存在，工具会明确说"通道不可用"。
 *   · 真正的 spawn / kill / 权限 / 预算全在 Go 里。这里只负责"打个电话"。
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

const url = () => process.env.PI_GUI_DELEGATE_URL ?? "";
const token = () => process.env.PI_GUI_DELEGATE_TOKEN ?? "";

async function call(path: string, body: unknown): Promise<any> {
  const base = url();
  if (!base || !token()) {
    throw new Error("委派通道不可用：这个会话不是由工作台启动的（缺 PI_GUI_DELEGATE_URL/TOKEN）");
  }
  // 每个请求都带上 runtimeId 指名"是哪条会话在调"：一进程一会话之后，同一个桥
  // 服务着多条会话——不带归属的话，delegation_list 会列出别的会话的子智能体，
  // delegation_stop 甚至会把它们一起停掉。
  const payload = { runtimeId: process.env.PI_GUI_RUNTIME_ID ?? "", ...(body as Record<string, unknown>) };
  const response = await fetch(base + path, {
    method: "POST",
    headers: { "content-type": "application/json", "x-delegate-token": token() },
    body: JSON.stringify(payload),
  });
  return (await response.json()) as any;
}

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "delegate_task",
    label: "Delegate task",
    description:
      "把一个独立任务交给子智能体在后台执行（它有独立的进程与上下文）。返回后你可以继续手上的工作；子智能体完成后，结果会自动出现在本会话里。" +
      "适合：查资料/writeup、独立复核某个结论、并行探索多条解法。不适合：线性链条中的下一步（那要自己做，拆开会丢线索）。",
    parameters: Type.Object({
      role: Type.String({ description: "子角色 id，例如 ctf-recon（资料检索）、ctf-verify（结论复核）、ctf-route（路线探索）" }),
      task: Type.String({ description: "交给子智能体的任务。要自包含：它看不到你的上下文" }),
    }),
    async execute(_id: string, params: { role: string; task: string }) {
      // runtimeId 指名"是哪条会话在派活"：一进程一会话之后，同一个桥服务着多条会话，
      // 不带归属的话结果会被投到用户当时正在看的那条会话里。这个值由启动参数注入。
      const data = await call("/delegate", { role: params.role, task: params.task, runtimeId: process.env.PI_GUI_RUNTIME_ID ?? "" });
      const text = data?.ok
        ? String(data.message ?? "已派出子智能体。")
        : "这次委派没有成功：" + String(data?.message ?? "未知原因");
      return { content: [{ type: "text", text }], details: {} };
    },
  });

  pi.registerTool({
    name: "delegation_stop",
    label: "Stop subagent",
    description:
      "停掉还在跑的子智能体（停一个或全部）。题目已经解出来、或不再需要它的结论时用它——" +
      "留着它继续跑只会烧预算，而且它的结果已经没人要了。",
    parameters: Type.Object({
      agentId: Type.Optional(Type.String({ description: "要停的子智能体 id；留空表示停掉全部还在跑的" })),
      reason: Type.Optional(Type.String({ description: "停止原因（会记在它的状态里，便于事后回看）" })),
    }),
    async execute(_id: string, params: { agentId?: string; reason?: string }) {
      const all = await call("/agents", {});
      const agents: any[] = Array.isArray(all?.agents) ? all.agents : [];
      // 空闲（可继续形态）也要能被主动结束：它不烧 token，但占着一份进程内存。
      const running = agents.filter(a => a.status === "running" || a.status === "starting" || a.status === "idle");
      const targets = params.agentId
        ? running.filter(a => a.id === params.agentId)
        : running;
      if (targets.length === 0) {
        return { content: [{ type: "text", text: "现在没有还在跑的子智能体。" }], details: {} };
      }
      const stopped: string[] = [];
      for (const agent of targets) {
        const result = await call("/kill", { agentId: agent.id, reason: params.reason ?? "父智能体不再需要它的结论" });
        if (result?.ok) stopped.push(agent.id);
      }
      return {
        content: [{ type: "text", text: stopped.length ? "已停掉：" + stopped.join("、") : "没有停掉任何子智能体。" }],
        details: {},
      };
    },
  });

  pi.registerTool({
    name: "delegation_list",
    label: "List subagents",
    description: "查看当前会话派出的子智能体：谁在跑、谁空闲（还能继续派活）、谁结束了、结果在哪。",
    parameters: Type.Object({}),
    async execute() {
      const data = await call("/agents", {});
      const agents = Array.isArray(data?.agents) ? data.agents : [];
      if (agents.length === 0) {
        return { content: [{ type: "text", text: "这个会话还没有派过子智能体。" }], details: {} };
      }
      // 注意：这里刻意用字符串拼接而不是模板字符串——这段 TypeScript 是
      // 嵌在 Go 的原始字符串里的，反引号会把 Go 的字符串提前收尾。
      const lines = agents.map((agent: any) => {
        const used = agent?.usage?.totalTokens ? " · " + agent.usage.totalTokens + " tokens" : "";
        const dir = agent?.sessionDir ? "\n    过程：" + agent.sessionDir : "";
        // 形态与"空闲"必须显式写出来：模型看不出"idle 还能继续派活"的话，
        // 它会以为子智能体已经死了，于是又派一个新的（重复劳动 + 多一个进程）。
        const mode = agent?.mode === "continuable" ? " · 可继续" : " · 一次性";
        const hint = agent?.status === "idle" ? "（空闲，可用 send_to_agent 继续派活）" : "";
        return "- " + agent.id + " · " + agent.roleId + " · " + agent.status + hint + mode + used + dir;
      });
      return { content: [{ type: "text", text: lines.join("\n") }], details: {} };
    },
  });

  pi.registerTool({
    name: "send_to_agent",
    label: "Message subagent",
    description:
      "给一个**可继续**的子智能体发消息：追加要求、追问、或让它接着做下一步。" +
      "它正在跑时消息会在最近的检查点插进去（steer），它空闲时这条消息会开新的一轮（上下文还在）。" +
      "注意：本调用只表示『已受理』——它的回复会稍后作为阶段结果出现在你的会话里，不是这次调用的返回值。",
    parameters: Type.Object({
      agentId: Type.String({ description: "子智能体 id（delegation_list 里能看到）" }),
      message: Type.String({ description: "要说的话。要自包含：它看不到你的上下文，只看到你发给它的文字" }),
      delivery: Type.Optional(Type.String({ description: "steer（默认，赶最近检查点）| queue（等它这一轮结束再送）" })),
    }),
    async execute(_id: string, params: { agentId: string; message: string; delivery?: string }) {
      const data = await call("/message", {
        agentId: params.agentId,
        message: params.message,
        delivery: params.delivery ?? "steer",
      });
      const text = data?.ok ? String(data.message ?? "已受理。") : "没能送出去：" + String(data?.message ?? "未知原因");
      return { content: [{ type: "text", text }], details: {} };
    },
  });

  pi.registerTool({
    name: "agent_interrupt",
    label: "Interrupt subagent turn",
    description:
      "只中断子智能体**当前这一轮**：它不会消失，之后还能用 send_to_agent 继续派活。" +
      "用在『它跑偏了 / 方向不对，先停下来』。要彻底结束它并释放进程请用 delegation_stop。",
    parameters: Type.Object({
      agentId: Type.String({ description: "子智能体 id" }),
    }),
    async execute(_id: string, params: { agentId: string }) {
      const data = await call("/interrupt", { agentId: params.agentId });
      const text = data?.ok ? String(data.message ?? "已请求中断。") : "中断失败：" + String(data?.message ?? "未知原因");
      return { content: [{ type: "text", text }], details: {} };
    },
  });
}
`
