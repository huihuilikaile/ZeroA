/**
 * 与 Go 后端一一对应的类型。
 *
 * 为什么这里是「字符串」而不是对象：
 * Go 侧用 json.RawMessage 承载 Pi 的 data 字段，Wails 会把它生成成字节数组类型，
 * 在 TS 里非常难用；所以契约统一改成 string（原始 JSON 文本），前端按需 JSON.parse。
 * 这样协议层不猜业务结构，Pi 增加字段时两边都不用改。
 */

/** 对应 Go 的 service.Result */
export interface Result {
  /**
   * 这条结果来自哪个**会话进程**（一进程一会话，见 Go 侧 session_runtime.go）。
   *
   * 它决定这条响应算不算数：切换会话之后，旧进程迟到的响应不能再写进新会话
   * （否则会出现"切过去了，但状态栏显示的还是上一条会话的模型/sessionId"）。
   */
  runtimeId?: string;
  id: string;
  command: string;
  success: boolean;
  /** data 字段的原始 JSON 文本；无数据时为空串 */
  data: string;
  error?: string;
}

/** 对应 Go 的 pi.Trace（Wails 事件 pi:trace）：协议观察器用原始行 */
export interface Trace {
  /** 同上：原始行属于哪个会话进程（多进程并行时区分输出用） */
  runtimeId?: string;
  direction: string; // stdin | stdout | stderr | system
  raw: string;
  time: string;
}

export type EnvelopeKind = 'event' | 'response' | 'system' | 'stderr';

/** 对应 Go 的 agent.Envelope（Wails 事件 pi:event）：类型化事件 */
export interface Envelope {
  /**
   * 这条记录属于哪个**会话进程**：router 靠它把后台会话的输出留在后台
   * （见 rpc/router.ts 开头的说明）。缺省表示"没法判断归属"，按当前会话处理。
   */
  runtimeId?: string;
  /**
   * 收到这条事件的时刻（毫秒）。
   *
   * 为什么不让 router 直接用 Date.now()：重新激活一条后台会话时，事件是
   * **按原顺序回放**的，回放那一刻已经不是它真正到达的时刻了——用它算耗时
   * 会算出"0.0s"这种假数字。所以第一次路由时就盖一个戳，之后一直用它。
   */
  receivedAt?: number;
  agentId: string;
  sessionId: string;
  kind: EnvelopeKind;
  /** 记录的 type 字段；system 记录为 process_started / process_exit / invalid_json 等 */
  type: string;
  time: string;
  /** 原始 JSONL 行 */
  raw: string;
  /** 解析后的对象 JSON 文本；非 JSON 记录为空串 */
  record: string;
}

/** 把 envelope.record 解析成对象；解析失败返回 null（未知事件必须安全忽略） */
export function parseRecord<T = Record<string, unknown>>(env: Envelope): T | null {
  if (!env.record) return null;
  try {
    return JSON.parse(env.record) as T;
  } catch {
    return null;
  }
}

/** 解析 Result.data */
export function parseData<T = Record<string, unknown>>(result: Result): T | null {
  if (!result.data) return null;
  try {
    return JSON.parse(result.data) as T;
  } catch {
    return null;
  }
}

/**
 * 数组兜底：后端 Go 的 nil 切片在 JSON 里是 **null**（不是 []），
 * 界面按 `.map` / `.length` 一读就整个页面崩掉（能力页实测踩过：
 * "Cannot read properties of null (reading 'length')"）。
 * 所有从 parseData 出来的数组字段，要么用它、要么用 `?? []`，二者等价。
 */
export function asArray<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

/** 对象兜底：后端 null / 缺字段 / 类型不对时读成 undefined（调用方再用 ?? 给默认值） */
export function asRecord<T extends object>(value: T | null | undefined): T | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value : undefined;
}

/* ---------------- Pi 协议里我们会消费的部分 ---------------- */

export interface Usage {
  input?: number;
  output?: number;
  cacheRead?: number;
  cacheWrite?: number;
  reasoning?: number;
  totalTokens?: number;
  cost?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number; total?: number };
}

export interface AssistantMessageEvent {
  type?: string; // text_start | text_delta | text_end | thinking_start | thinking_delta | thinking_end
  contentIndex?: number;
  delta?: string;
  content?: string;
}

export interface ContentBlock {
  type?: string; // text | thinking | toolCall
  text?: string;
}

export interface PiMessage {
  role?: string; // user | assistant | toolResult | system
  content?: ContentBlock[] | string;
  errorMessage?: string;
  stopReason?: string;
  model?: string;
  provider?: string;
  usage?: Usage;
  /**
   * 毫秒时间戳（会话文件里每条消息都有）。
   * 历史回放用它重建"这一轮花了多久"——实时靠 agent_start/agent_settled 计时，
   * 重启后没有事件流，只能用消息时间戳算。
   */
  timestamp?: number;
}

export interface PiState {
  isStreaming?: boolean;
  isCompacting?: boolean;
  model?: { id?: string; provider?: string; name?: string; contextWindow?: number };
  thinkingLevel?: string;
  sessionId?: string;
  sessionName?: string;
  /** 当前会话文件路径：切换会话后它就是新的文件，用来高亮左侧列表 */
  sessionFile?: string;
  messageCount?: number;
  pendingMessageCount?: number;
  autoCompactionEnabled?: boolean;
  steeringMode?: string;
  followUpMode?: string;
}

/** 取消息里的纯文本（content 可能是字符串或块数组） */
export function messageText(message: PiMessage | undefined | null): string {
  if (!message) return '';
  const content = message.content;
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    return content.filter(b => b?.type === 'text').map(b => b.text ?? '').join('');
  }
  return '';
}
