import { create } from 'zustand';
import type { PiState, Result, Usage } from '@/rpc/types';

export type AgentStatus = 'offline' | 'idle' | 'running' | 'waiting-ui' | 'retrying' | 'compacting' | 'error';

/** get_session_stats 的返回（只声明我们真正用到的字段） */
export interface SessionStats {
  sessionFile?: string;
  totalMessages?: number;
  userMessages?: number;
  assistantMessages?: number;
  tokens?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number; total?: number };
  cost?: number;
  /** Pi 自己算的当前上下文占用；压缩刚结束时 tokens/percent 可能是 null */
  contextUsage?: { tokens?: number | null; contextWindow?: number; percent?: number | null } | null;
}
interface RuntimeStore {
  /**
   * 当前**在看**哪个会话进程（'' = 还没认下来，例如刚启动）。
   *
   * 一进程一会话之后，"当前会话"不再是"那个唯一进程里装着哪份会话文件"，
   * 而是"界面此刻显示的是哪个进程的输出"。所有按 runtimeId 分流的地方
   * （事件归属、迟到的响应、请求完成、草稿）都以它为准。
   */
  runtimeId: string;
  /**
   * 正在切换会话。
   *
   * 这段时间"当前视图"是不确定的（旧视图已经存下、新视图还没立起来），
   * 所以：发出的事件一律先攒着等切换完成再回放，输入区禁用发送/插入/排队，
   * abort 也不发（它必须打到一条明确的会话上，而不是"切换中的那个"）。
   */
  switching: boolean;
  /* 连接配置（设置模态窗里改，启动时作为 Pi 的命令行参数） */
  mode: 'mock' | 'real';
  cliPath: string;
  cwd: string;
  /** 启动时使用的模型："provider/id"，空 = 用 Pi 配置里的默认模型 */
  startModel: string;
  /** 启动时的思考级别，空 = 用默认 */
  startThinking: string;
  /** 工具白名单：'' = Pi 默认（read/bash/edit/write），'none' = 全禁用，其余为逗号分隔 */
  startTools: string;
  busy: boolean;      // 正在启动/关闭进程
  sending: boolean;   // 已发出 prompt，仍在等 response
  connected: boolean; // 由 get_state 能否作答决定，而不是由 start 是否返回决定
  /**
   * 启动参数已就绪（Go 侧跑过一次 Start）。
   *
   * 与 connected 的区别很重要，别混：
   *   · paramsReady = "这套目录/模型/角色能用"，**与有没有进程无关**——
   *     懒启动之后"一个进程都没有"是正常状态，发第一条消息时进程才建；
   *   · connected = "此刻连着一个能应答的进程"（进程崩溃/退出后会变 false）。
   *
   * 所以"能不能新建会话 / 打开会话"这类判断必须读 paramsReady：
   * 读 connected 会把"进程刚崩过"表现成"点了没反应"（而那件事其实还能做）。
   */
  paramsReady: boolean;
  /** 正在主动停进程（重启/关闭）：此时进程退出是预期行为，不该在对话里报一条 */
  stopping: boolean;
  /**
   * 用户刚按过「停止」。用来区分"这回合是被停掉的"和"这回合失败了"：
   * Pi 会把中止的回合写成 `errorMessage: "This operation was aborted"`，
   * 不认这一点就会在按停止时弹一条报错，看着像操作失败。
   */
  aborting: boolean;

  /* Agent 状态（全部由事件驱动） */
  status: AgentStatus;
  turn: number;
  sessionId: string;
  /** 当前会话文件路径（get_state.sessionFile），左侧列表用它高亮 */
  sessionFile: string;
  /** exe 所在目录与会话根目录：设置页展示默认值用（由 GetDefaults 提供） */
  exeDir: string;
  sessionsRoot: string;
  /** 当前用的是哪个 Pi：kind ∈ local|path|env|explicit|missing */
  piKind: string;
  piPath: string;
  piVersion: string;
  /** 诊断日志路径（<配置目录>/gui-log.txt）：设置页显示并提供打开 */
  logPath: string;
  /** 默认工作区（exe 同目录的 workspace/）：永远删不掉的那一个 */
  defaultWorkspace: string;
  /** 调试模式：界面渲染滚动指标（PI_GUI_DEBUG=1） */
  debug: boolean;
  model: string;
  provider: string;
  thinkingLevel: string;
  /**
   * Pi 配置（settings.json）里的默认模型/思考级别（GetDefaults 提供）。
   *
   * 用途：还没有进程的视图（新建会话/打开的历史会话）要显示"将要使用哪个模型"，
   * 而运行时 model 字段可能还留着上一条会话的值——那才是"显示一个模型、
   * 实际跑另一个模型"这种货不对板的来源。进入待创建视图时按
   * startModel 偏好 → 这三个默认值的口径重填（见 router 的 enterPendingSession）。
   */
  cfgModel: string;
  cfgProvider: string;
  cfgThinking: string;
  contextWindow: number;
  /** 当前上下文占用（tokens），来自 get_session_stats 的 contextUsage */
  contextTokens: number;
  /** Pi 算的上下文占用百分比 */
  contextPercent: number;
  messageCount: number;
  pendingCount: number;
  /**
   * 排队中的消息原文（queue_update 事件提供，与 Pi 的 steering/followUp 队列一一对应）。
   *
   * pendingCount 只是计数；输入框上方的"排队面板"要显示原文并提供
   * 插入/编辑/删除，所以两份数组要完整留在这里（随会话视图快照走）。
   * Pi 没有"单条移除"的 RPC，面板操作 = clear_queue 后按新内容重排（见 useAgent）。
   */
  queueSteering: string[];
  queueFollowUp: string[];
  /** 上一回合耗时（秒）：agent_settled 时写入，输入区显示「上次 2.8s」 */
  lastDuration: number;
  /** 本回合开始时刻（agent_start 写入）：运行中显示已跑多久 */
  runStartedAt: number;
  tokens: { input: number; output: number; cacheRead: number; total: number };
  cost: number;
  lastResult: Result | null;

  setMode(mode: 'mock' | 'real'): void;
  setCliPath(v: string): void;
  setCwd(v: string): void;
  setStartModel(v: string): void;
  setStartThinking(v: string): void;
  setStartTools(v: string): void;
  setBusy(v: boolean): void;
  setStopping(v: boolean): void;
  setAborting(v: boolean): void;
  setSending(v: boolean): void;
  setConnected(v: boolean): void;
  setParamsReady(v: boolean): void;
  setStatus(s: AgentStatus): void;
  setLastDuration(seconds: number): void;
  setRunStartedAt(at: number): void;
  bumpTurn(): void;
  setLastResult(r: Result | null): void;
  applyState(state: PiState): void;
  applyUsage(usage: Usage | undefined): void;
  /** get_session_stats 的返回值：会话累计用量 + 当前上下文占用 */
  applyStats(stats: SessionStats): void;
  /**
   * 会话换了一个（新建/切换）：把上一个会话的用量与上下文占用清掉。
   *
   * 为什么不直接用 resetRuntime：那个会把 status 也置成 offline、sending 置 false，
   * 而换会话时进程还活着、可能正在跑——那是"状态"不是"用量"，两件事不该混。
   */
  resetUsage(): void;
  /** 进程换新或退出时回到初始状态 */
  resetRuntime(): void;
}

export const useRuntime = create<RuntimeStore>(set => ({
  runtimeId: '',
  switching: false,
  // 默认本机真实 Pi；界面不再提供模式开关，自动化测试用 PI_GUI_MODE=mock 覆盖
  mode: 'real',
  cliPath: '',
  cwd: loadPref('cwd', ''),
  startModel: loadPref('model', ''),
  startThinking: loadPref('thinking', ''),
  startTools: loadPref('tools', ''),
  busy: false,
  sending: false,
  connected: false,
  paramsReady: false,
  stopping: false,
  aborting: false,

  status: 'offline',
  turn: 0,
  sessionId: '',
  sessionFile: '',
  exeDir: '',
  sessionsRoot: '',
  piKind: '',
  piPath: '',
  piVersion: '',
  logPath: '',
  defaultWorkspace: '',
  debug: false,
  model: '',
  provider: '',
  thinkingLevel: '',
  cfgModel: '',
  cfgProvider: '',
  cfgThinking: '',
  contextWindow: 0,
  /**
   * 当前**上下文占用**（不是会话累计）。
   *
   * Pi 的 get_session_stats 会给出 contextUsage（它自己用来判断该不该压缩的口径，
   * 含系统提示与工具定义的开销），那才是"上下文环"应该显示的数。
   * tokens.total 是整个会话的累计用量——聊得越久它越大，跟"还能塞多少"没关系。
   */
  contextTokens: 0,
  /** Pi 自己算的占用百分比（拿得到就用它，比我们自己除更准） */
  contextPercent: 0,
  messageCount: 0,
  pendingCount: 0,
  queueSteering: [],
  queueFollowUp: [],
  tokens: { input: 0, output: 0, cacheRead: 0, total: 0 },
  cost: 0,
  lastResult: null,
  lastDuration: 0,
  runStartedAt: 0,

  setMode: mode => set({ mode }),
  setCliPath: cliPath => set({ cliPath }),
  setCwd: cwd => { savePref('cwd', cwd); set({ cwd }); },
  setStartModel: startModel => { savePref('model', startModel); set({ startModel }); },
  setStartThinking: startThinking => { savePref('thinking', startThinking); set({ startThinking }); },
  setStartTools: startTools => { savePref('tools', startTools); set({ startTools }); },
  setBusy: busy => set({ busy }),
  setStopping: stopping => set({ stopping }),
  setAborting: aborting => set({ aborting }),
  setSending: sending => set({ sending }),
  setConnected: connected => set({ connected, status: connected ? 'idle' : 'offline' }),
  setParamsReady: paramsReady => set({ paramsReady }),
  setStatus: status => set({ status }),
  setLastDuration: lastDuration => set({ lastDuration }),
  setRunStartedAt: runStartedAt => set({ runStartedAt }),
  bumpTurn: () => set(s => ({ turn: s.turn + 1 })),
  setLastResult: lastResult => set({ lastResult }),

  /** 用 get_state 的响应刷新整块状态：模型、思考级别、token、队列、会话 id */
  applyState: state => set(s => ({
    sessionId: state.sessionId ?? '',
    // get_state 省略 sessionFile 表示当前还是未落盘的新会话，不能沿用上一个会话路径。
    sessionFile: state.sessionFile ?? '',
    model: state.model?.id ?? s.model,
    provider: state.model?.provider ?? s.provider,
    thinkingLevel: state.thinkingLevel ?? s.thinkingLevel,
    contextWindow: state.model?.contextWindow ?? s.contextWindow,
    messageCount: state.messageCount ?? s.messageCount,
    pendingCount: state.pendingMessageCount ?? s.pendingCount,
    status: state.isStreaming ? 'running' : (s.status === 'offline' ? 'idle' : s.status),
  })),

  /**
   * get_session_stats 的结果写进状态。
   *
   * 这是"重开应用之后上下文显示 0"的修复点：tokens 只由流式 usage 事件更新，
   * 而恢复会话（load_session）不会触发那些事件——于是恢复完一直是 0，
   * 直到用户再问一句才跳成真实值。Pi 的 stats 是**基于会话内容算的**，
   * 所以刚打开就能给出正确口径。
   */
  resetUsage: () => set({
    tokens: { input: 0, output: 0, cacheRead: 0, total: 0 },
    contextTokens: 0,
    contextPercent: 0,
    cost: 0,
    messageCount: 0,
    turn: 0,
    runStartedAt: 0,
  }),

  applyStats: stats => set(s => ({
    tokens: {
      input: stats.tokens?.input ?? s.tokens.input,
      output: stats.tokens?.output ?? s.tokens.output,
      cacheRead: stats.tokens?.cacheRead ?? s.tokens.cacheRead,
      total: stats.tokens?.total ?? s.tokens.total,
    },
    cost: stats.cost ?? s.cost,
    contextWindow: stats.contextUsage?.contextWindow ?? s.contextWindow,
    // 压缩之后 Pi 会给 null（还没有新的 usage 数据）：那时保留旧值，别显示成 0
    contextTokens: stats.contextUsage?.tokens ?? s.contextTokens,
    contextPercent: stats.contextUsage?.percent ?? s.contextPercent,
    messageCount: stats.totalMessages ?? s.messageCount,
  })),

  /** message_end / message_update 里带的 usage：token 与花费直接展示在检查器里 */
  applyUsage: usage => set(s => {
    if (!usage) return s;
    const total = usage.totalTokens ?? s.tokens.total;
    return {
      tokens: {
        input: usage.input ?? s.tokens.input,
        output: usage.output ?? s.tokens.output,
        cacheRead: usage.cacheRead ?? s.tokens.cacheRead,
        total,
      },
      cost: usage.cost?.total ?? s.cost,
      model: s.model,
    };
  }),

  /**
   * 进程换新或退出时回到初始状态。
   *
   * ⚠️ 它**不动** runtimeId / switching：那两件是"现在在看哪条会话"的记账，
   * 由 router 的会话视图管理（见 rpc/router.ts 的 finishSessionSwitch）。
   * 冷启动一条会话时正是"先 resetRuntime，再写新的 runtimeId"。
   */
  resetRuntime: () => set({
    status: 'offline',
    turn: 0,
    sessionId: '',
    sessionFile: '',
    messageCount: 0,
    pendingCount: 0,
    queueSteering: [],
    queueFollowUp: [],
    tokens: { input: 0, output: 0, cacheRead: 0, total: 0 },
    contextTokens: 0,
    contextPercent: 0,
    cost: 0,
    sending: false,
    lastResult: null,
  }),
}));

// ── 偏好记忆 ────────────────────────────────────────────────────────────────
// 记住用户上次用的模型、推理强度、工具范围与工作目录，下次打开就是离开时的样子。
// 用 localStorage 而不是写配置文件：这些是"界面偏好"，不属于 Pi 的配置
// （Pi 那边只认 models.json / settings.json，混进去反而会被它当成未知字段）。

const PREF_KEY = 'pi-gui-prefs';

/** 读一个记住的偏好（读不到返回 fallback） */
export function loadPref(key: string, fallback: string): string {
  if (typeof localStorage === 'undefined') return fallback;
  try {
    const raw = localStorage.getItem(PREF_KEY);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    return typeof parsed[key] === 'string' ? (parsed[key] as string) : fallback;
  } catch {
    return fallback;
  }
}

/** 写一个偏好（隐私模式下静默失败） */
export function savePref(key: string, value: string): void {
  if (typeof localStorage === 'undefined') return;
  try {
    const raw = localStorage.getItem(PREF_KEY);
    const parsed = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
    parsed[key] = value;
    localStorage.setItem(PREF_KEY, JSON.stringify(parsed));
  } catch {
    /* 忽略 */
  }
}