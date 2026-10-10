/**
 * 事件路由：全应用唯一的「RPC 记录 → 状态」翻译层。
 *
 * 组件不解析协议；协议也不认识组件。所有分叉都收敛在这个文件里，
 * 好处是新增事件时只改一处，而且未知事件天然安全（走 default 分支）。
 *
 * ============================================================
 * 一进程一会话之后，多出来一个维度：这条记录属于**哪个会话进程**
 * ============================================================
 * 后端每条事件/每个响应都盖了 `runtimeId`（见 Go 侧 session_runtime.go）。
 * 界面因此维护**按 runtime 分开的会话视图**，而不是一份"当前的"状态：
 *
 *   · 切走时把当前视图**存下来**（不是丢掉）；
 *   · 切回时**原样取回**（不是重新加载历史）；
 *   · 后台会话的输出不丢，按到达顺序攒在 pending 里，等它重新成为当前
 *     会话时再按原顺序回放。
 *
 * 为什么后台输出必须留在它自己的视图里：那条会话的进程还在跑，它的每一帧
 * 文字、每一次工具调用、每一个确认框都属于**它自己的**对话。写进前台就是把
 * 两条会话搅在一起——用户切回去会看到一段顺序错乱、彼此穿插的"历史"，
 * 而前台那一轮的内容也被污染了（更糟的是**看不出来**被污染了）。
 * 顺带一提，后台会话的对话框也不能弹到前台：它要的答案得发回它自己的进程，
 * 弹在当前这条会话上，用户点了也不知道点的是谁。
 *
 * 为什么热切换不重载历史：那条会话的时间线本来就在前端手里（就是上面那份
 * 视图），重新拉一遍 get_messages 又慢（会话文件可能几 MB）又没必要。
 * Go 侧也是这么做的——热切换只回 get_state，`messages` 为 null；前端于是
 * **有 messages 才重建，没有就一个字都不动**。
 */
import {
  messageText,
  parseData,
  parseRecord,
  type AssistantMessageEvent,
  type Envelope,
  type PiMessage,
  type PiState,
  type Result,
  type Trace,
  type Usage,
} from '@/rpc/types';
import { piBridge } from '@/bridge/pi';
import type { AgentStatus, SessionStats } from '@/stores/runtime';
import { useRuntime } from '@/stores/runtime';
import { timelineFor, useTimeline, type TimelineState } from '@/stores/timeline';
import { useTraces } from '@/stores/traces';
import { useSessionActivity } from '@/stores/sessionActivity';
import { useUI, type ExtensionDialog } from '@/stores/ui';
import { beginTurn, noteToolEnd, noteToolStart, settleTurn } from '@/stores/changedFiles';
import { isAbortError, reportError } from '@/lib/reportError';

/* ============================================================
   文本增量合帧
   Pi 每秒可能发几十次 text_delta，每个 token 都 setState 会让整棵树重渲染。
   这里按 requestAnimationFrame 合并成一批（不丢字，只是延后一帧）。
   ============================================================ */
/**
 * 合帧缓冲**按 agent 分开**。
 *
 * 之前是三个模块级变量（textBuf / thinkBuf / raf）：主会话与子智能体同时出字时
 * 会互相覆盖——子智能体的字会跑到父会话的时间线里，或者两边都缺字。
 * 现在每个 agent 一份，键是 agentId（'' = 主会话）。
 *
 * 不需要再按 runtime 分：只有**当前**会话的事件才会走到这里，后台会话的
 * 增量先在 pending 里排队（见 routeEnvelope），回放时才进这个缓冲。
 */
interface DeltaBuffer { text: string; think: string; raf: number; /** 本 agent 这一轮的起点（算耗时用，避免借用父会话的起点） */ startedAt: number }
const buffers = new Map<string, DeltaBuffer>();

function bufferFor(agentId: string): DeltaBuffer {
  let buffer = buffers.get(agentId);
  if (!buffer) {
    buffer = { text: '', think: '', raf: 0, startedAt: 0 };
    buffers.set(agentId, buffer);
  }
  return buffer;
}

function flush(agentId: string): void {
  const buffer = bufferFor(agentId);
  buffer.raf = 0;
  const timeline = timelineFor(agentId).getState();
  if (buffer.think) { timeline.appendThinking(buffer.think); buffer.think = ''; }
  if (buffer.text) { timeline.appendText(buffer.text); buffer.text = ''; }
}

function schedule(agentId: string): void {
  const buffer = bufferFor(agentId);
  if (!buffer.raf) buffer.raf = requestAnimationFrame(() => flush(agentId));
}

/**
 * 在"结束类"事件之前强制刷新，避免最后一段文字跑到 message_end 之后。
 *
 * agentId 省略时刷新**所有**在缓冲的 agent：调用方（会话切换、回放后台输出）
 * 关心的是"别丢字"，不该逐个点名。
 */
export function flushDeltas(agentId?: string): void {
  const targets = agentId === undefined ? ['', ...buffers.keys()] : [agentId];
  for (const id of targets) {
    const buffer = buffers.get(id);
    if (!buffer) continue;
    if (buffer.raf) { cancelAnimationFrame(buffer.raf); buffer.raf = 0; }
    flush(id);
  }
}

/* ============================================================
   会话视图：一个 runtime 一份"界面上此刻该是什么样"
   ============================================================ */

/**
 * 视图里"属于这条会话"的运行状态。
 *
 * 取舍标准：**换个会话就该跟着换的**才进快照。启动参数（模型默认值、工具
 * 白名单、Pi 路径）、权限、主题这些是应用级的，两条会话看到的是同一份。
 * connected 与 cwd 必须进来：每个会话进程是各自独立的进程、各有各的工作目录。
 */
interface RuntimeSnapshot {
  runtimeId: string;
  status: AgentStatus;
  connected: boolean;
  sending: boolean;
  aborting: boolean;
  turn: number;
  sessionId: string;
  sessionFile: string;
  cwd: string;
  model: string;
  provider: string;
  thinkingLevel: string;
  contextWindow: number;
  contextTokens: number;
  contextPercent: number;
  messageCount: number;
  pendingCount: number;
  queueSteering: string[];
  queueFollowUp: string[];
  lastDuration: number;
  runStartedAt: number;
  tokens: { input: number; output: number; cacheRead: number; total: number };
  cost: number;
  lastResult: Result | null;
}

/** 子智能体登记表**不进视图**：它是应用级的，不是"这条会话的"。
 *
 * 为什么：Go 侧的子智能体事件（delegation.go 的 SetTraceSink、pi:agent-state）
 * 只带 AgentID、**不带 runtimeId**，前端没有任何依据判断"这个子智能体属于哪条
 * 会话"（它的进程独立于会话进程，父会话的进程没了它照样在跑）。
 * 硬塞进视图反而会出错：在 B 里收到 A 的子智能体更新时不知道该写进哪个视图，
 * 切回 A 时又会用旧快照把新更新盖掉。所以如实按应用级处理——
 * 标签栏显示的是"这台机器上此刻有哪些子智能体在跑"，点开看的是它自己的时间线。
 */
interface SessionView {
  runtime: RuntimeSnapshot;
  timeline: TimelineState;
  dialog: ExtensionDialog | null;
}

/**
 * runtimeId → 那份视图。**只放"不在台上"的会话**：切走时写进来，切回时取出去。
 *
 * 设上限是因为每个视图都握着一整条时间线：一进程一会话之后后端最多同时开
 * 6 个进程（maxRuntimes），但"开过的会话"会一直累积，不设上限就等于
 * "聊过的每一条会话都在内存里留一份"。取 8：比同时活着的进程数略大，
 * 保证正常的来回切换（A/B/C 三条之间跳）永远命中。
 */
const MAX_VIEWS = 8;
const views = new Map<string, SessionView>();

/** 存一份视图（LRU：重新入队，Map 的迭代顺序才是"最近用过"） */
function rememberView(id: string, view: SessionView): void {
  views.delete(id);
  views.set(id, view);
  while (views.size > MAX_VIEWS) {
    const oldest = views.keys().next().value;
    if (typeof oldest !== 'string') break;
    views.delete(oldest);
  }
}

/** 取一份视图（取走即出队：它要上台了，留在"后台视图表"里只会重复占内存） */
function takeView(id: string): SessionView | undefined {
  const view = views.get(id);
  if (view) views.delete(id);
  return view;
}

/**
 * 后台会话攒下的事件（按到达顺序）。
 *
 * 不是"丢掉的输出"，而是**等它重新上台再演一遍**：切回去时按原顺序回放，
 * 得到的就与"一直看着它"完全一样（包括耗时——回放时用的是事件到达时刻，
 * 见 rpc/types.ts 里 receivedAt 的说明）。
 */
const pending = new Map<string, Envelope[]>();
/**
 * 单条会话最多替它攒多少条事件。
 *
 * 为什么要设上限：后台会话可能在**一直跑**（这正是"一进程一会话"要的效果），
 * 一条长回合能产出上万条 text_delta，全按原始对象攒着就是几十 MB，
 * 而用户可能一小时都不切回去看它。
 *
 * 攒爆之后的处置不是"丢掉最老的"（那会拼出一段缺字的假历史，比空着更糟），
 * 而是**不再攒**并记一笔：切回去时直接按会话文件重建（getMessages 才是权威，
 * 反正冷启动走的也是同一条路）。阈值取 20000：正常回合远远够用。
 */
const MAX_PENDING = 20000;
/** 攒爆了的会话：切回去时改为重建历史，而不是逐条回放 */
const truncated = new Set<string>();

/** 把一条后台事件排进队列（超上限就整体降级为"重建历史"） */
function queueEnvelope(env: Envelope): void {
  const id = env.runtimeId;
  if (!id || truncated.has(id)) return;
  const events = pending.get(id) ?? [];
  events.push(env);
  if (events.length > MAX_PENDING) {
    pending.delete(id);
    truncated.add(id);
    return;
  }
  pending.set(id, events);
}

/** 后台会话的 prompt 响应：切回去时要拿它收掉那一边的 sending */
const pendingResults = new Map<string, Result[]>();

function runtimeSnapshot(): RuntimeSnapshot {
  const r = useRuntime.getState();
  return {
    runtimeId: r.runtimeId, status: r.status, connected: r.connected, sending: r.sending,
    aborting: r.aborting, turn: r.turn, sessionId: r.sessionId, sessionFile: r.sessionFile,
    cwd: r.cwd, model: r.model, provider: r.provider, thinkingLevel: r.thinkingLevel,
    contextWindow: r.contextWindow, contextTokens: r.contextTokens, contextPercent: r.contextPercent,
    messageCount: r.messageCount, pendingCount: r.pendingCount,
    queueSteering: r.queueSteering, queueFollowUp: r.queueFollowUp,
    lastDuration: r.lastDuration, runStartedAt: r.runStartedAt,
    tokens: r.tokens, cost: r.cost, lastResult: r.lastResult,
  };
}

/**
 * 只收掉**那个 runtime** 的 sending。
 *
 * 为什么不能无脑 setSending(false)：后台会话的一次 prompt 完成时，前台
 * 可能正跑着另一条会话的一轮——一起收掉的话，前台输入框会从"等待接收"
 * 跳回"可以发送"，用户以为上一句已经答完了。
 */
export function completeSessionRequest(runtimeId: string): void {
  if (useRuntime.getState().runtimeId === runtimeId) {
    useRuntime.setState({ sending: false });
    return;
  }
  const view = views.get(runtimeId);
  if (view) view.runtime.sending = false;
}

/**
 * 进程全体停掉之后（Stop / 重新 Start）把视图清空。
 *
 * 这时所有会话进程都已经不存在了，留着它们的视图只会让下次切换取回一份
 * 早就死掉的状态（connected=true、会话路径还指着旧进程）。
 */
export function resetSessionViews(): void {
  flushDeltas();
  views.clear();
  pending.clear();
  truncated.clear();
  pendingResults.clear();
  /**
   * 侧栏那张"会话活动表"（圆环 / 绿点）也要一起清。
   *
   * 进程全没了，表里记的却是**上一批进程**的"正在跑 / 跑完了"：不清的话侧栏会
   * 一直挂着几个永远不动的圈和绿点（Stop 之后、或者重新 Start 之后最明显），
   * 而且绿点点进去也不会消失——那条会话的 runtimeId 已经不存在了，
   * 后面再来什么事件都改不动它。
   */
  useSessionActivity.getState().clearAll();
  useRuntime.setState({ runtimeId: '', switching: false });
}

/**
 * 切换会话前：把当前这一帧的增量冲干净，并进入"切换中"。
 *
 * 为什么必须先 flush：切换期间到达的增量会被当成后台输出攒起来，
 * 而**已经收在缓冲里**的那几个字若不先落地，就会被记到切换之后的会话名下
 * （最后一个字的归属错了，表现为"切过去还能看到上一个会话的半句话"）。
 */
export function beginSessionSwitch(): void {
  flushDeltas();
  useRuntime.setState({ switching: true });
}

/**
 * 切换会话后：激活目标 runtime 的视图。
 *
 * result 省略 = **切换失败**（或者压根没切）：此时
 *   · 不动 runtimeId、不动时间线、不清内容——用户手上那一轮完全不受影响；
 *   · 但要把切换期间攒下的输出回放到**当前**视图里，不能吞掉。
 *
 * result 有值 = 切换成功：目标 runtimeId 与当前不同就换视图，无值就说明
 * "还是同一个进程"（例如切到同一个工作区），那就只把状态刷新一遍。
 */
export function finishSessionSwitch(result?: Result): void {
  const previous = useRuntime.getState().runtimeId;
  /** 这次是不是真的换了进程；换了进程又没取回视图时，前端手上没有这条会话的内容 */
  let swapped = false;
  let restored = false;
  if (result?.runtimeId && result.runtimeId !== previous) {
    swapped = true;
    // 「本轮改动」是"当前这条会话这一轮"的清单：换人就该清，不能把上一条会话的改动带过去
    beginTurn();
    // ① 先把当前这一份存下来：历史、工具卡片、草稿之外的一切都在里面
    if (previous) {
      rememberView(previous, {
        runtime: runtimeSnapshot(),
        timeline: useTimeline.getState(),
        dialog: useUI.getState().dialog,
      });
    }
    // ② 再把目标那一份立起来
    const cached = takeView(result.runtimeId);
    restored = Boolean(cached);
    if (cached) {
      // 热切换：这条会话的时间线早就在前端手里，原样取回即可（不重载历史）
      useRuntime.setState(cached.runtime);
      useTimeline.setState(cached.timeline);
      useUI.setState({ dialog: cached.dialog });
    } else {
      /**
       * 冷启动：这条会话的进程是刚起来的，前端手上什么都没有。
       * 先清空，再由下面的 messages 重建。
       */
      useRuntime.getState().resetRuntime();
      useTimeline.getState().reset();
      useUI.getState().closeDialog();
      useRuntime.setState({
        runtimeId: result.runtimeId, connected: true, status: 'idle',
        runStartedAt: 0, lastDuration: 0,
        /**
         * 模型/思考级别/会话路径必须一起清掉。
         *
         * 不清的后果是"切过去的一瞬间显示上一条会话的模型"——下面的
         * routeResult(state) 只会在字段缺失时保留旧值，于是旧值就成了默认值。
         * 清成空之后，get_state 给什么就是什么。
         */
        model: '', provider: '', thinkingLevel: '', contextWindow: 0,
        sessionId: '', sessionFile: '',
      });
    }
  }
  if (result) {
    const data = parseData<{ state?: Result; messages?: Result | null; cwd?: string; sessionPath?: string }>(result);
    if (data) {
      if (data.cwd) useRuntime.getState().setCwd(data.cwd);
      /**
       * messages **只有冷启动才有**（Go 侧 activateRuntime：热切换不搬历史，
       * 回的是 null）。所以这里是"有值才重建"——没有就一个字都不动，
       * 前端手上的时间线就是权威。
       */
      if (data.messages) routeMessages(data.messages);
      if (data.state) routeResult(data.state);
      /**
       * 侧栏指示器：登记"这条 runtime 是哪份会话"。
       *
       * 点进来（成为当前会话）就算看过了 —— 绿点在这里消失。
       * 上下文上限也顺手记下：后台再收到它的用量事件时，要靠这个数换算百分比。
       *
       * 路径以 sessionPath 为准；它缺失时退回 get_state 里的 sessionFile——
       * 两者说的就是同一个文件，Go 侧只保证 activateRuntime 的结果里带上前者，
       * 而 store 那条 pi:session 路径、以及测试里手工拼的结果都可能没有它。
       * 不兜这一步的后果是"反查表里没有这条 runtime"，后台的用量事件永远找不到
       * 该写哪一行。
       */
      const activatedPath = data.sessionPath || useRuntime.getState().sessionFile;
      if (activatedPath && result.runtimeId) {
        useSessionActivity.getState().noteActivation(
          result.runtimeId,
          activatedPath,
          useRuntime.getState().contextWindow,
        );
      }
      /**
       * 例外：**换了进程、又没有视图、还没有历史**。
       *
       * 正常不会走到这里（换进程 = 新进程 = 冷启动 = 一定有 messages），
       * 但有两种边缘情况会：视图被 LRU 挤掉（连着切过很多条会话），
       * 以及"Go 那边刚判定为热切换、执行时那个进程正好被回收"这类竞态。
       * 那时候什么都不做的话，用户会看到一条**空白**的会话——
       * 明明历史还在，界面却像被清空了。宁可多问一次 get_messages，
       * 也不能把"没有内容"当成"内容就是空的"。
       */
      if (swapped && !restored && !data.messages) {
        void piBridge.getMessages().then(routeMessages).catch(() => { /* 拉不到就保持空视图 */ });
      }
    }
  }
  useRuntime.setState({ switching: false });
  // ③ 切换期间攒下的后台输出：它现在是前台了，按原顺序回放
  replayBacklog(useRuntime.getState().runtimeId);
}

/**
 * 把某条 runtime 攒下的后台输出按原顺序回放。
 *
 * 两个调用方——「切换成功」与「认领刚出生的进程」（见 adoptPendingRuntime）——
 * 必须用同一份实现：它们面对的是同一件事（这条会话在后台攒了一堆事件，
 * 现在它上台了），各写一遍迟早有一边忘了处理 `truncated`（攒爆降级）或
 * pendingResults（那边 prompt 的受理凭证）。
 */
function replayBacklog(runtimeId: string): void {
  const overflowed = truncated.delete(runtimeId);
  const events = pending.get(runtimeId) ?? [];
  pending.delete(runtimeId);
  if (overflowed) {
    /**
     * 这一条的后台输出攒爆过（跑得太久）：逐条回放只会拼出一段缺字的假历史，
     * 所以改成按会话文件重建——与冷启动走同一条路，内容以磁盘为准。
     */
    void piBridge.getMessages().then(routeMessages).catch(() => { /* 拉不到就保持原样 */ });
  } else {
    for (const event of events) routeEnvelope(event);
  }
  const results = pendingResults.get(runtimeId) ?? [];
  pendingResults.delete(runtimeId);
  for (const item of results) routeResult(item);
  // 回放里的增量也进了合帧缓冲：这里统一落地，调用方（以及断言）才看得到
  flushDeltas();
}

/**
 * 进入「待创建的新会话」（懒启动）。
 *
 * 与 finishSessionSwitch 的区别：目标**还没有进程**（runtimeId 为空），
 * 所以没有视图可换、没有状态可套。它只做三件事：
 *   ① 把当前这条会话的视图存下来（切回去还能原样取回，包括工具卡片与滚动内容）；
 *   ② 清空当前视图与时间线——它们是**上一条会话**的内容，而新会话此刻一个字都没有；
 *   ③ 把 runtimeId 置空 = "还没有进程"。
 *
 * ⚠️ runtimeId 必须置空，不能沿用上一条会话的：否则事件分流、迟到的响应、
 * 消息归属全都会指向上一条会话（界面看不出错，但内容会串）。
 * 新进程的 id 要等第一条消息把它带起来之后才存在（见 adoptPendingRuntime）。
 */
export function enterPendingSession(cwd?: string): void {
  flushDeltas();
  const previous = useRuntime.getState().runtimeId;
  if (previous) {
    rememberView(previous, {
      runtime: runtimeSnapshot(),
      timeline: useTimeline.getState(),
      dialog: useUI.getState().dialog,
    });
  }
  useUI.getState().closeDialog();
  useTimeline.getState().reset();
  useRuntime.getState().resetRuntime();
  // 「本轮改动」也属于上一条会话：新会话的清单从它的第一条消息开始算
  beginTurn();
  /**
   * ⚠️ 这里**不动 connected**。
   *
   * 语义上"还没有进程"确实等于没连上，但侧栏那排"在某个工作区里新建会话"的
   * 按钮（features/shell/Sidebar.tsx 的 newSessionIn）把 connected 当成总开关：
   * 置 false 之后，用户在同一个工作区里再点一次"新建"会**静默什么都不做**
   * （而那正是"点了没反应"这一类最难查的问题）。
   * 所以"这条会话还没有进程"由 sessions store 的 pendingNew 单独表达
   * （空态文案与状态行都读它），connected 保持在"这套启动参数可用"的口径上。
   */
  useRuntime.setState({ runtimeId: '', switching: false });
  if (cwd) useRuntime.getState().setCwd(cwd);
  /**
   * 重填模型徽标：上一条会话的 model/provider/thinkingLevel 还留在 store 里，
   * 而这条待创建的会话发出第一条消息时，进程用的是 startModel 偏好
   * （会作为 --model 传下去），没记住过就用 Pi settings.json 的默认模型。
   * 不在这里重填，徽标就会把上一条会话的模型当成这条的将用模型。
   * 打开历史会话时 loadSessionHistory 紧接着会用会话文件里的
   * model_change 记录再覆盖一次（那条才是该会话真实用过的模型）。
   */
  {
    const state = useRuntime.getState();
    const pref = state.startModel;
    const slash = pref.indexOf('/');
    const prefProvider = slash > 0 ? pref.slice(0, slash) : '';
    const prefModel = slash > 0 ? pref.slice(slash + 1) : pref;
    useRuntime.setState({
      model: prefModel || state.cfgModel,
      provider: prefProvider || state.cfgProvider,
      thinkingLevel: state.startThinking || state.cfgThinking,
    });
  }
}

/**
 * 认领「刚出生的进程」（懒启动的另一半）。
 *
 * 场景：用户点「新建对话」时后端**没有**起进程（会话=日志，进程按需创建），
 * 第一条消息到达后端时才现起一个（Go 的 ensureActiveRuntime）。这个新进程的
 * runtimeId 随 prompt 的返回值回来，界面在这里把它认作"当前会话"。
 *
 * 为什么不能直接走 finishSessionSwitch：
 *   · 它会 `useTimeline.reset()` + 按 messages 重建——而用户刚发的那条消息
 *     已经在时间线上（本地加过，服务端还没回放），重建会让它闪一下不见；
 *   · 它要求 result 里带 {state, messages, cwd, sessionPath} 这套切换载荷，
 *     而 prompt 的返回值是"受理凭证"，没有这些。
 * 所以这里只做"认领 + 回放"：把等它出生期间攒下的事件按原顺序放出来。
 */
export function adoptPendingRuntime(runtimeId: string): void {
  if (!runtimeId) return;
  const current = useRuntime.getState().runtimeId;
  if (current === runtimeId) return;
  /**
   * 已经有当前会话了：这不是"待创建的新会话被第一条消息带起来"，
   * 而是后台某条会话的响应（迟到的返回值）。前台一个字都不能动——
   * 那正是"迟到的响应不能覆盖别的会话"这条规矩（见 routeResult）。
   */
  if (current) return;
  useRuntime.setState({
    runtimeId,
    connected: true,
    status: 'idle',
    runStartedAt: 0,
    lastDuration: 0,
    /**
     * 模型/思考级别/会话路径必须一起清掉：它们是**上一条会话**的。
     * 不清的话，新进程的 get_state 回来之前，状态行上显示的是上一条会话的模型。
     */
    model: '', provider: '', thinkingLevel: '', contextWindow: 0,
    sessionId: '', sessionFile: '',
  });
  // 等它出生期间攒下的事件（agent_start / text_delta …）按原顺序回放
  replayBacklog(runtimeId);
  /**
   * 顺手问一次状态：新进程的模型、会话路径只有 get_state 知道
   * （会话文件通常要等第一条消息落盘之后才有）。拿不到不影响这一轮。
   */
  void piBridge.getState().then(routeResult).catch(() => { /* 拿不到就等事件里的信息 */ });
}

function usageOf(value: unknown): Usage | undefined {
  return value && typeof value === 'object' ? (value as Usage) : undefined;
}

/**
 * 把"服务端注入的用户消息"放进时间线，并按文本去重。
 *
 * 为什么需要去重：用户自己发的消息前端先加过一次（addUser），
 * 服务端随后也会把它回放一遍；而子智能体的结论没有前一次。
 * 同一条规则要同时满足"不重复"和"不遗漏"——只跟最后一条用户消息比即可，
 * 注入的消息在语义上总是最新那条。
 */
function addInjectedUser(state: TimelineState, text: string): void {
  // 整条会话里比一遍：同一条消息可能来自"界面上先加的"（用户自己发的、
  // 或者我们派活时种下的任务）与"服务端回放的"，两边文案完全一致。
  for (const item of state.items) {
    if (item.kind === 'user' && (item.text ?? '').trim() === text) return;
  }
  state.addUser(text);
}

/**
 * 历史回放时补出"这一轮结束"那一行（见 rebuildTimeline 开头的说明）。
 *
 * 判定"回合结束"看 stopReason：`toolUse` 表示后面还有工具结果与更多回合，不算结束；
 * 其余（end_turn / max_tokens / aborted / error）都是一轮的终点。
 * 没有 stopReason 说明这条消息还没收尾（例如正好在流式中被历史截断），不补。
 *
 * **这里不写耗时**：实时那一行的 "3.9s" 是 agent_start → agent_settled 量的；
 * 历史里只有消息，而 Pi 给 assistant 消息的 timestamp 是**请求开始**的时刻
 * （实测：用户消息 14:36:47.476 → assistant 14:36:47.520，只有 44ms，
 * 而界面当时显示 1.5s）。用它算出来的是个假数，宁可少写一项也不写错。
 * 会话文件里也没有回合边界记录，所以耗时确实无法还原。
 */
function appendSettledFromHistory(timeline: TimelineState, message: PiMessage): void {
  const reason = String(message.stopReason ?? '');
  if (!reason || /tool[_-]?use/i.test(reason)) return;
  const parts = ['agent_settled · 运行结束'];
  const tokens = message.usage?.totalTokens ?? 0;
  if (tokens) parts.push(`累计 ${tokens.toLocaleString()} tokens`);
  timeline.addSettled(parts.join(' · '));
  // 被停止的回合：实时路径会另给一条中性的"已中止"，历史里也要有，否则两处不一致
  if (/abort/i.test(reason)) {
    timeline.addSystem('info', '已中止', '这一回合被停止（abort），已生成的内容保留。');
  }
}
/**
 * 观察一条事件，更新侧栏的"会话活动表"（圆环 / 绿点）。
 *
 * 只看三种东西：这一轮开始了、这一轮结束了、上下文用了多少。
 * 判定用的是原始记录的 type —— 与下面事件分支同一套字段，避免两处解析漂移。
 */
function observeSessionActivity(env: Envelope, isForeground: boolean): void {
  const runtimeId = env.runtimeId ?? '';
  if (!runtimeId || env.kind !== 'event') return;
  const record = parseRecord<Record<string, unknown>>(env);
  if (!record) return;
  const type = String(record.type ?? '');
  if (type === 'agent_start') {
    useSessionActivity.getState().noteRunning(runtimeId, true, isForeground);
  } else if (type === 'agent_settled') {
    useSessionActivity.getState().noteRunning(runtimeId, false, isForeground);
  }
  const message = record.message as { usage?: Usage } | undefined;
  const usage = usageOf(message?.usage ?? (record.usage as Usage | undefined));
  if (usage?.totalTokens) useSessionActivity.getState().noteTokens(runtimeId, usage.totalTokens);
}

/**
 * collapseTurn 把"这一轮"在界面上收干净：不再发送中、不再中止中、状态落回
 * idle（进程已不在则 offline）、收掉流式光标。
 *
 * 为什么要有这个统一出口（实测踩到两次同类 bug）：
 *   · 前台那条会话的进程退出时，事件可能被当成"后台事件"攒着，状态机永远收不了尾；
 *   · 点「停止」但 abort 请求失败时（进程已死 → `Pi 在响应前退出`），只弹一条错误，
 *     输入框就永远停在"运行中"、停止按钮也不消失，只能重启应用。
 * 两次都是"该收尾时没收尾"。所以收尾动作只写一份，所有"这一轮结束了 / 不可能继续了"
 * 的路径都调它——各写一份必然漂移（第一处修了第二处忘了，正是这次的情况）。
 */
export function collapseTurn(): void {
  const runtime = useRuntime.getState();
  runtime.setSending(false);
  runtime.setAborting(false);
  /**
   * 进程还在 → idle（还能继续发）；进程不在 → offline（提示需要重启）。
   * 用 connected 判断而不是"我们觉得它在不在"：那是进程存活状态的唯一真相来源。
   */
  runtime.setStatus(runtime.connected ? 'idle' : 'offline');
  timelineFor('').getState().finishStreaming();
}

/** 原始 JSONL：只进观察器，不参与状态推导 */
export function routeTrace(trace: Trace): void {
  useTraces.getState().add(trace);
}

/**
 * 把一次失败翻译成给人看的一句话。
 *
 * 为什么要单独一个函数（而不是直接拼模板）：一条 `success:false` 但缺 `command`
 * 的记录，旧写法会渲染成 **"undefined 失败"**——用户看到的就是这个，既不知道
 * 谁失败了，也不知道该怎么办（实测踩过：Go 侧事件 payload 形状漂移就是这样暴露的）。
 * 这里保证任何形状下都能说清"谁失败了"。
 */
export function describeFailure(result: Result): string {
  if (result.error) return result.error;
  const who = result.command || result.id || '命令';
  return `${who} 失败（ZeroA 没有给出原因）`;
}

/** Service 方法（前端主动调用）的返回值 */
export function routeResult(result: Result): void {
  const before = useRuntime.getState();
  /**
   * 认下第一个带 runtimeId 的响应。
   *
   * Go 的 Start 不返回进程 id，界面只能从第一条响应（启动后那次 get_state）
   * 里知道"现在在看哪个 runtime"。在认下来之前到达的事件会先攒着，
   * 等这里认下来之后由下面的 finishSessionSwitch 回放。
   */
  const initializing = Boolean(result.runtimeId && !before.runtimeId);
  if (initializing) useRuntime.setState({ runtimeId: result.runtimeId });

  /**
   * 别的会话进程的响应：**一律不写前台**。
   *
   * 迟到的 get_state / get_messages 写进来就是串台：用户明明已经切到另一条
   * 会话，界面上的模型、sessionId、上下文占用却变成了上一条的（而且看不出错）。
   * 唯一要留下的是 prompt 的响应——它是"那条会话这一轮已被受理"的凭证，
   * 切回去时用它收掉那一边的 sending（见 pendingResults / completeSessionRequest）。
   */
  if (result.runtimeId && result.runtimeId !== useRuntime.getState().runtimeId) {
    if (result.command === 'prompt') {
      const list = pendingResults.get(result.runtimeId) ?? [];
      list.push(result);
      pendingResults.set(result.runtimeId, list);
    }
    return;
  }

  const runtime = useRuntime.getState();
  runtime.setLastResult(result);
  if (!result.success) {
    reportError(describeFailure(result));
    return;
  }
  if (result.command === 'get_state') {
    const state = parseData<PiState>(result);
    if (state) runtime.applyState(state);
    /**
     * 侧栏指示器的反查表：get_state 是"这条 runtime 是哪份会话"的权威来源。
     *
     * 懒启动的会话尤其需要这一步：它出生时只有 runtimeId（会话文件要等第一条消息
     * 落盘之后才有），不补登记的话，侧栏那张表里永远没有它的行——
     * 圆环和"跑完了"的绿点都画不出来（用户看到的是"后台跑完却没有任何提示"）。
     */
    const activatedPath = useRuntime.getState().sessionFile;
    if (activatedPath && result.runtimeId) {
      useSessionActivity.getState().noteActivation(
        result.runtimeId,
        activatedPath,
        useRuntime.getState().contextWindow,
      );
    }
    /**
     * 刚认下 runtimeId（启动阶段）：把"还没有归属"时攒下的事件回放掉，
     * 否则新进程开头那几帧（session_start、get_state 之前的事件）会一直躺在
     * pending 里，直到用户切一次会话才冒出来。
     */
    if (initializing && !runtime.switching) finishSessionSwitch();
  }
  if (result.command === 'get_session_stats') {
    /**
     * 这条以前被丢掉了：stats 只进原始观察器，不写状态。
     * 后果就是"重开应用后上下文环显示 0"——tokens 只由流式 usage 事件更新，
     * 而恢复会话不会产生那些事件。Pi 的 stats 是按会话内容算的，随时可以问。
     */
    const stats = parseData<SessionStats>(result);
    if (stats) runtime.applyStats(stats);
  }
  if (result.command === 'prompt') {
    // disposition=handled 表示被扩展直接处理，本轮不会启动 agent，也就永远等不到 agent_settled
    const data = parseData<{ disposition?: string }>(result);
    if (data?.disposition === 'handled') {
      flushDeltas();
      runtime.setSending(false);
      runtime.setStatus('idle');
      useTimeline.getState().addSystem('info', '命令已被扩展处理', 'disposition=handled：本轮不会启动 agent，不需要等待 agent_settled。');
    }
  }
}

/**
 * pi:session 的 payload 形状。
 *
 * 这条事件在一进程一会话之后只剩一个用途：**后端自己在后台换了当前会话**
 * （例如任务循环跑到下一条会话），前端没参与这次切换，得由事件通知。
 * 前端自己调的 switch_session 走返回值，用不到它。
 *
 * messages / state 声明成 unknown 而不是具体类型：它们来自 JSON.parse，
 * 结构由 Pi 决定——协议加字段时两边都不用改（同 rpc/types.ts 开头那段）。
 */
export interface SessionPayload {
  cancelled?: boolean;
  /** 目标会话路径；失败时是被放弃的那条 */
  path?: string;
  /** 排队的切换执行失败的原因 */
  error?: string;
  /** 这条推送来自哪个会话进程 */
  runtimeId?: string;
  /** 新会话的工作目录（缺省时沿用当前值） */
  cwd?: string;
  /** get_messages 的原始形状（{messages:[...]}，也可能是 null） */
  messages?: unknown;
  /** get_state 的原始形状 */
  state?: unknown;
}

/**
 * 用一份 payload 重建当前时间线与状态。
 *
 * 只有一个调用方（routeMessages）：它的输入形状就是 get_messages 的产物。
 */
export function applySessionPayload(payload: SessionPayload): void {
  // messages 缺失时**不重建**：那会把时间线清空，而调用方可能只想更新状态
  if (payload.messages !== undefined) rebuildTimeline(payload.messages);
  if (payload.state) useRuntime.getState().applyState(payload.state as PiState);
}

/**
 * 用一份 messages 重建整条时间线（冷启动一条会话时用）。
 *
 * 工具调用配对、回合结束行这些细节只能有一份实现——分成几份迟早会漂移。
 */
function rebuildTimeline(messages: unknown): void {
  const data = messages as { messages?: PiMessage[] } | PiMessage[] | null | undefined;
  const list = Array.isArray(data) ? data : (data?.messages ?? []);
  const timeline = useTimeline.getState();
  timeline.reset();
  /**
   * 历史里的工具调用要"配回来"。
   *
   * 会话文件（以及 get_messages）里，一次工具调用是**两条记录**：
   *   assistant  message.content[] = { type:"toolCall", id:"call_xx", name, arguments }
   *   toolResult message.toolCallId   = "call_xx"，另有 toolName / content / isError
   *
   * 以前我们只把 toolResult 渲染成一行「工具结果」系统消息，没建成工具记录，
   * 于是回放历史时助手里那些 toolCall 占位行永远找不到记录，界面显示
   * 「工具调用 call_xx 没有对应记录」——看着像协议出问题，其实是回放缺口。
   * 现在按 Pi 给的 toolCallId 严格配对：配上了就走"已完成"的工具行（带输出），
   * 配不上（例如只有半截记录）才退回系统消息。
   */
  const pendingCalls = new Map<string, { name: string; args?: unknown }>();
  /**
   * 重建"回合结束"那一行。
   *
   * 实时的 `agent_settled · 运行结束 · 3.9s · 累计 10,170 tokens` 是**事件**造出来的，
   * 只存在于本次运行；重启后走历史消息，没有这条事件，那一行就消失了（用户报的）。
   * 历史里其实有足够的信息：每条 assistant 消息都带 timestamp 与 usage，
   * 于是累计 tokens = 本条消息的 usage.totalTokens（Pi 这个字段本身就是会话累计值，
   * 与实时口径一致）。耗时仍然无法还原，见 appendSettledFromHistory 的说明。
   */
  for (const message of list) {
    if (message.role === 'user') {
      timeline.addUser(messageText(message));
    } else if (message.role === 'assistant') {
      // 先记住这条助手消息里的工具调用，供后面的 toolResult 配对。
      // ⚠️ 两种字段拼写都要认：真实 Pi 用 id/name/arguments（见会话文件），
      // mock 用 toolCallId/toolName/args。只认一种就会出现"真实能用、mock 不能用"。
      for (const block of (message.content ?? []) as {
        type?: string; id?: string; toolCallId?: string; name?: string; toolName?: string;
        arguments?: unknown; args?: unknown;
      }[]) {
        const callID = block?.id ?? block?.toolCallId;
        if (block?.type === 'toolCall' && callID) {
          pendingCalls.set(String(callID), {
            name: String(block.name ?? block.toolName ?? 'tool'),
            args: block.arguments ?? block.args,
          });
        }
      }
      timeline.beginAssistant();
      const text = messageText(message);
      if (text) {
        timeline.beginText();
        timeline.appendText(text);
        timeline.endText(text);
      }
      timeline.finalizeAssistant(message, message.errorMessage);
      appendSettledFromHistory(timeline, message);
    } else if (message.role === 'toolResult') {
      const callID = String((message as { toolCallId?: string }).toolCallId ?? '');
      const call = callID ? pendingCalls.get(callID) : undefined;
      if (callID && call) {
        timeline.startTool(callID, call.name, call.args);
        timeline.endTool(callID, messageText(message), Boolean((message as { isError?: boolean }).isError));
      } else {
        timeline.addSystem('info', '工具结果', messageText(message).slice(0, 200));
      }
    }
  }
}

/** get_messages 的返回值：用它重建整条时间线（重新连接后恢复界面） */
export function routeMessages(result: Result): void {
  /**
   * 迟到的 get_messages（属于别的会话进程）不能用来重建当前时间线：
   * 那会把用户正在看的内容整条换掉。冷启动那条路径由 finishSessionSwitch
   * 直接调用，runtimeId 一定是刚切过去的那一个。
   */
  if (result.runtimeId && result.runtimeId !== useRuntime.getState().runtimeId) return;
  if (!result.success) {
    reportError(result.error || 'get_messages 失败');
    return;
  }
  const data = parseData<{ messages?: PiMessage[]; model?: { id?: string; provider?: string }; thinkingLevel?: string } | PiMessage[]>(result);
  /**
   * load_session_history 除了 messages 还会带回"这条会话文件里记的模型/思考级别"
   * （进程没起时模型徽标只能靠它）。get_messages 没有这两个键，这里自然跳过。
   */
  if (data && !Array.isArray(data)) {
    const patch: { model?: string; provider?: string; thinkingLevel?: string } = {};
    if (data.model?.id) patch.model = data.model.id;
    if (data.model?.provider) patch.provider = data.model.provider;
    if (data.thinkingLevel) patch.thinkingLevel = data.thinkingLevel;
    if (Object.keys(patch).length > 0) useRuntime.setState(patch);
  }
  applySessionPayload({ messages: data });
}

export function routeEnvelope(env: Envelope): void {
  /**
   * 到达时刻只盖一次戳。
   *
   * 后台会话的事件是**回放**的，回放那一刻已经不是它真正到达的时刻——
   * 不盖戳的话，agent_start→agent_settled 的耗时会被算成回放所花的那几毫秒。
   */
  env.receivedAt ??= Date.now();

  /**
   * 分流：**只有当前会话的事件走前台**。
   *
   * 正在切换时（switching）连当前 runtime 的事件也先攒着：那一刻"当前视图"
   * 正处于旧已存下、新还没立起来的状态，写进去就会落到错误的会话上。
   *
   * 只攒 event / system 两种：response 是前端自己调的 RPC 的重复播报
   * （真正的返回值走 routeResult），后台会话的响应错误不该冒到前台来。
   */
  const current = useRuntime.getState();
  if (env.runtimeId && (current.switching || env.runtimeId !== current.runtimeId)) {
    /**
     * 后台会话的事件：**先更新侧栏指示器，再攒着等回放**。
     *
     * 为什么必须在提前 return **之前**做：这一段之后就 return 了，
     * 那条会话的"开始跑了 / 跑完了"永远传不到侧栏——用户就会看到
     * "后台那条明明在跑，侧栏却什么都没有"。
     *
     * 子智能体除外：它有自己的进程与生命周期，把它的一轮开始/结束当成这条
     * 会话的，侧栏那个圈会跟着子智能体一起闪（前台那条在下面就是这么过滤的，
     * 两条路必须用同一套判据）。
     */
    const backgroundSubagent = Boolean(env.agentId && env.agentId !== 'default');
    if (!backgroundSubagent) observeSessionActivity(env, false);
    /**
     * 进程退出（不论前后台）：这条 runtime 已经死了，它的积压倒这就地清掉。
     *
     * 为什么不攒着等回放：进程死了之后这个 runtimeId 永远不会再成为"当前会话"
     * （Go 侧 noteRuntimeExit 已把它从登记表摘掉，会话回到待创建态，下次打开
     * 会按会话文件重建历史）——攒着的事件等不到回放的那一天，只会随"开过又死掉的
     * 会话数"无限涨（长时间运行下的慢泄漏）。内容不会丢：磁盘上的会话文件才是权威。
     *
     * 侧栏圆环也必须在这里停下：进程没了就再也不会有 agent_settled，
     * 不收的话后台会话的圈会永远转下去。
     */
    if (env.kind === 'system' && env.type === 'process_exit') {
      if (env.runtimeId) {
        pending.delete(env.runtimeId);
        pendingResults.delete(env.runtimeId);
        truncated.delete(env.runtimeId);
        useSessionActivity.getState().noteExit(env.runtimeId);
      }
      /**
       * 前台那条的进程退出，不能只当后台事件攒着（实测踩到：进程死了，输入框却
       * 一直卡在"运行中"、停止按钮也不消失，用户只能强杀应用）。
       *
       * 为什么会走到这里：退出事件带着它自己的 runtimeId，而这一刻前台可能
       * "还不是它"——两种典型情形：
       *   · 懒启动刚起进程就崩了：前台 runtimeId 还是空的（进程还没认领）；
       *   · 正在切换（switching）时进程死了。
       * 这两种情况下事件原本都会走"攒起来等回放"这条路，可那条会话已经没有进程了，
       * 回放永远不会发生 → 前台状态机永远收不了尾。所以这里要就地把前台收干净。
       *
       * 判据必须卡死"退出的就是前台这条"：**后台会话的进程退出不能把前台判成掉线**
       *（A 会话在跑、后台 B 崩了，界面上 A 必须照常）。
       */
      const front = useRuntime.getState();
      const exitingIsForeground = !front.runtimeId || env.runtimeId === front.runtimeId;
      if (exitingIsForeground) {
        front.setSending(false);
        front.setStatus('offline');
        front.setConnected(false);
        // 与主路径同理：死进程的 id 不能继续占着"当前会话"，
        // 否则下一条消息懒启动的新进程认领不进来（见下方 process_exit 主分支的注释）
        useRuntime.setState({ runtimeId: '' });
        timelineFor('').getState().finishStreaming();
        useUI.getState().closeDialog();
      }
      return;
    }
    if (env.kind === 'event' || env.kind === 'system') queueEnvelope(env);
    return;
  }

  const runtime = useRuntime.getState();
  const ui = useUI.getState();

  /**
   * 事件属于谁：'' = 主会话，其他 = 某个子智能体。
   *
   * 时间线按 agentId 取（各自一份，但**用的是同一段解析代码**——
   * 这正是"子智能体复用主会话解析"的落点）。
   * 而 runtime（连接状态、运行中、用量）只反映主会话：子智能体的一轮结束
   * 不该把父会话的输入框变成"空闲"，也不该改父会话的 token 统计。
   */
  const agentId = env.agentId && env.agentId !== 'default' ? env.agentId : '';
  const isSubagent = agentId !== '';
  const timeline = timelineFor(agentId).getState();
  // 前台会话的事件：同一条观察路径（isForeground=true 时跑完**不**亮绿点）
  if (!isSubagent) observeSessionActivity(env, true);

  if (env.kind === 'system') {
    if (env.type === 'process_exit') {
      flushDeltas(agentId);
      if (isSubagent) {
        // 子智能体进程退出：只收尾它自己的时间线
        timeline.finishStreaming();
        return;
      }
      // 主动停进程（重启会话/关闭）时退出是预期行为：
      // 这时不往对话里插一条"进程已退出"，否则每次重启都会多一行噪音。
      if (!runtime.stopping) {
        timeline.addSystem('error', 'ZeroA 进程已退出', env.raw);
      }
      runtime.setConnected(false);
      runtime.setStatus('offline');
      /**
       * 把死进程的 runtimeId 从"当前会话"上摘下来（置空 = 这条会话没有进程）。
       *
       * 不摘的后果（实测踩到）：进程崩溃后用户再发消息，后端懒启动起一个
       * **新 runtimeId** 的进程，而 adoptPendingRuntime 只在"当前没有 runtimeId"
       * 时才认领（它要保护"迟到的响应不能覆盖别的会话"）——旧 id 还占着位，
       * 新进程的认领被拒绝，它的事件全部按"后台会话"攒进 backlog 永不回放：
       * 界面上就看到"发了消息、agent 也在跑，可对话一个字都不动"，重启应用才好。
       * 进程既已退出，这个 id 就不会再有任何事件到来，置空是如实的。
       */
      useRuntime.setState({ runtimeId: '' });
      // 它攒下的 backlog 也一并丢掉：进程死了，这些事件等不到回放的那一天
      if (env.runtimeId) {
        pending.delete(env.runtimeId);
        pendingResults.delete(env.runtimeId);
        truncated.delete(env.runtimeId);
        // 侧栏的圆环/反查表也收掉（主路径与后台攒队列路径必须做同一件事）
        useSessionActivity.getState().noteExit(env.runtimeId);
      }
      // 进程没了就没人再发 agent_settled 了：必须自己收尾，
      // 否则输入框一直"等待接收"、时间线里的光标和工具卡片会永远转圈。
      runtime.setSending(false);
      runtime.setAborting(false);
      runtime.setRunStartedAt(0);
      // 队列随进程一起没了：计数与原文一起清，不然排队面板会显示一批死消息
      useRuntime.setState({ pendingCount: 0, queueSteering: [], queueFollowUp: [] });
      timeline.finishStreaming();
      ui.closeDialog();
    }
    return; // 其余 system 记录只进观察器
  }
  if (env.kind === 'stderr') return;
  if (env.kind === 'response') {
    const response = parseRecord<Result>(env);
    if (response && !response.success) reportError(describeFailure(response));
    return;
  }

  const event = parseRecord<Record<string, unknown>>(env);
  if (!event) return;
  const type = String(event.type ?? '');

  switch (type) {
    case 'agent_start':
      if (isSubagent) {
        // 子智能体开始干活：只在它自己的时间线上留个起点，不动父会话的状态
        bufferFor(agentId).startedAt = Date.now();
        timeline.addSystem('info', '开始工作', '');
        break;
      }
      runtime.setStatus('running');
      runtime.bumpTurn();
      useUI.getState().dismissErrorNotices(); // 新回合开始，收掉上一轮的报错通知
      /**
       * 记下本回合起点：agent_settled 时用来算「这一轮花了多久」。
       *
       * 用 agent_start 而不是 prompt 的 response，是因为重试/多回合都从这里开始，
       * 界面上看到的耗时正好对应用户感知的"它干活用了多久"。
       *
       * 起点存在 store 里（而不是模块级变量）：它是**每条会话自己的**东西——
       * 甲会话跑着的时候去看乙会话，甲那边的起点不能丢，也不能被乙的覆盖。
       */
      runtime.setRunStartedAt(env.receivedAt);
      break;

    case 'message_start': {
      const message = event.message as PiMessage | undefined;
      if (message?.role === 'assistant') timeline.beginAssistant();
      break;
    }


    case 'message_update': {
      const ame = (event.assistantMessageEvent ?? {}) as AssistantMessageEvent;
      // 用量只统计主会话：子智能体有它自己的用量（在标签与 Agents 列表里显示）
      if (!isSubagent) runtime.applyUsage(usageOf(event.usage));
      switch (ame.type) {
        case 'thinking_start': break;
        case 'thinking_delta': bufferFor(agentId).think += ame.delta ?? ''; schedule(agentId); break;
        case 'thinking_end': flushDeltas(agentId); timeline.endThinking(); break;
        case 'text_start': flushDeltas(agentId); timeline.beginText(); break;
        case 'text_delta': bufferFor(agentId).text += ame.delta ?? ''; schedule(agentId); break;
        case 'text_end': flushDeltas(agentId); timeline.endText(ame.content); break;
        default: break; // 未知子类型安全忽略
      }
      break;
    }

    case 'message_end': {
      const message = event.message as PiMessage | undefined;
      if (message?.role === 'user') {
        // 服务端注入的用户消息（例如子智能体的结论）——让它在对话里看得见
        const text = messageText(message).trim();
        if (text) addInjectedUser(timeline, text);
        break;
      }
      if (message?.role === 'assistant') {
        flushDeltas(agentId);
        if (!isSubagent) runtime.applyUsage(message.usage as Usage | undefined);
        /**
         * 被「停止」结束的回合不算失败。
         *
         * Pi 会把中止的回合写成 `stopReason: "aborted"` + `errorMessage:
         * "This operation was aborted"`；以前这两行照单全收，于是用户每次点停止
         * 都会看到红字和一条报错条，像是操作出错了。现在按 stopReason 判定：
         * 中止只留一条中性的「已中止」说明。
         */
        const aborted = String(message.stopReason ?? '') === 'aborted' || runtime.aborting;
        timeline.finalizeAssistant(message, aborted ? undefined : message.errorMessage);
        if (message.errorMessage && !aborted) reportError(message.errorMessage);
        if (aborted) {
          timeline.addSystem('info', '已中止', '这一回合被停止（abort），已生成的内容保留。');
        }
        runtime.setAborting(false);
      }
      break;
    }

    case 'tool_execution_start':
      timeline.startTool(String(event.toolCallId ?? ''), String(event.toolName ?? 'tool'), event.args);
      noteToolStart(
        String(event.toolCallId ?? ''),
        String(event.toolName ?? ''),
        event.args,
        useRuntime.getState().cwd,
        agentId,
      );
      break;
    case 'tool_execution_update':
      timeline.touchTool(String(event.toolCallId ?? ''));
      break;
    case 'tool_execution_end': {
      // 工具被中止时 Pi 会给一条 "This operation was aborted" 的失败结果：
      // 它是"用户按了停止"的副产物，按中性显示，不要染成红色错误。
      const isError = Boolean(event.isError);
      const text = typeof event.result === 'string' ? event.result : JSON.stringify(event.result ?? '');
      const abortedTool = isError && (runtime.aborting || /operation was aborted|aborted by user/i.test(text));
      timeline.endTool(String(event.toolCallId ?? ''), event.result, isError, abortedTool);
      noteToolEnd(String(event.toolCallId ?? ''), isError || abortedTool);
      break;
    }

    case 'queue_update': {
      // Pi 给的是消息原文数组（agent-session 的 _emitQueueUpdate）：
      // 排队面板要显示原文并提供插入/编辑/删除，只记计数是不够的。
      const steering = Array.isArray(event.steering) ? event.steering.map(String) : [];
      const followUp = Array.isArray(event.followUp) ? event.followUp.map(String) : [];
      useRuntime.setState({
        queueSteering: steering,
        queueFollowUp: followUp,
        pendingCount: steering.length + followUp.length,
      });
      break;
    }

    case 'compaction_start':
      runtime.setStatus('compacting');
      timeline.addSystem('compaction', '上下文压缩', '正在压缩早期消息以腾出上下文…');
      break;
    case 'compaction_end': {
      // 压缩有两种：发生在回合里的（后面还会有 agent_settled）和空闲/手动触发的
      // （整个流程就到此为止）。只有前者才该回到 running，否则界面会永远转圈。
      const aborted = Boolean(event.aborted);
      const errorText = String(event.errorMessage ?? '');
      // 摘要字段是 result.summary（见 json.md）；早前读的是 event.summary，永远是空的
      const result = (event.result ?? {}) as { summary?: string };
      const summary = String(result.summary ?? '');
      runtime.setStatus(runtime.runStartedAt ? 'running' : 'idle');
      // "Nothing to compact" 不是失败：会话太短，没有可压的东西。时间线如实记一笔，
      // 但不往右上角扔错误卡（调用方那一路会给温和提示，见 reportCompactResult）。
      const benign = /nothing to compact/i.test(errorText);
      timeline.addSystem(
        'compaction',
        aborted ? '上下文压缩已中止' : benign ? '上下文压缩' : errorText ? '上下文压缩失败' : '上下文压缩完成',
        summary || (benign ? '会话还太短，没有可压缩的内容' : errorText) || (aborted ? '压缩被中止，上下文保持原样' : ''),
      );
      if (errorText && !benign) reportError(errorText, '上下文压缩');
      break;
    }

    case 'auto_retry_start':
      runtime.setStatus('retrying');
      timeline.addSystem('retry', `自动重试 #${event.attempt ?? 1}`, String(event.errorMessage ?? event.reason ?? ''));
      break;
    case 'auto_retry_end':
      // 重试失败时要说明白，并把状态收回到 idle——不然界面会一直停在"重试中"
      if (event.success === false) {
        runtime.setStatus('idle');
        runtime.setSending(false);
        timeline.addSystem('retry', '自动重试失败', String(event.finalError ?? event.errorMessage ?? ''));
      } else {
        runtime.setStatus(runtime.runStartedAt ? 'running' : 'idle');
      }
      break;

    case 'extension_ui_request': {
      const requestId = String(event.id ?? '');
      const method = String(event.method ?? 'confirm');
      const title = String(event.title ?? '扩展请求');
      const message = String(event.message ?? event.placeholder ?? '');
      /**
       * 只有这四种是**对话框**：会阻塞扩展、等我们回 extension_ui_response。
       *
       * 其余（notify / setStatus / setWidget / setTitle / set_editor_text）是
       * **即发即忘**——协议文档原话："emits but does not expect a response.
       * The client can display the information or ignore it."
       *
       * 踩过的坑：以前不看 method，一律当成要用户点的确认框，还把状态设成 waiting-ui。
       * 而示例扩展 my-extension.ts 在 session_start 里 `ctx.ui.notify("Extension loaded!")`
       * ——于是每次打开/新建会话都会：弹一个"允许一次/始终允许/拒绝"的假确认框，
       * 并且界面永远停在"运行中 + 停止"。用户看到的就是"打开任何会话都显示正在运行"。
       */
      const isDialog = method === 'confirm' || method === 'select' || method === 'input' || method === 'editor';
      if (isDialog && isSubagent) {
        /**
         * 子智能体的对话框：在它自己的时间线上出可点的卡片，并弹出全局对话框。
         *
         * 应答（extension_ui_response）必须发回**发起它的那个进程**——
         * 子智能体是独立 Pi 进程，所以走 RespondAgentUI（按 agentId 找它的句柄），
         * 不能走主会话的 RespondUI。agentId 挂在时间线条目与对话框上，
         * 应答时用它路由到正确的进程。
         *
         * 注意不动父会话的运行状态（不设 waiting-ui）：等回答的是子智能体，
         * 父会话该是什么状态还是什么状态。
         */
        timeline.addExtension(requestId, method, title, message, agentId || undefined);
        ui.openDialog({
          requestId,
          method,
          title: `${title}（子智能体 ${agentId}）`,
          message,
          options: Array.isArray(event.options) ? (event.options as string[]) : undefined,
          placeholder: event.placeholder ? String(event.placeholder) : undefined,
          agentId: agentId || undefined,
        });
        break;
      }
      if (!isDialog) {
        // 通知类：弹个胶囊提示就够了，绝不改状态、绝不弹框
        if (method === 'notify') {
          const level = String(event.level ?? 'info');
          useUI.getState().showNotice(
            message || title,
            level === 'error' ? 'error' : level === 'warn' ? 'warn' : 'success',
          );
        }
        // setStatus / setWidget / setTitle / set_editor_text 是给 TUI 的，
        // 在 GUI 里没有对应位置——原始事件日志里仍然能看到它们，不丢信息。
        break;
      }
      runtime.setStatus('waiting-ui');
      timeline.addExtension(requestId, method, title, message);
      ui.openDialog({
        requestId,
        method,
        title,
        message,
        options: Array.isArray(event.options) ? (event.options as string[]) : undefined,
        placeholder: event.placeholder ? String(event.placeholder) : undefined,
      });
      break;
    }

    case 'agent_end':
      // agent_end 不等于结束：可能还会重试
      if (event.willRetry) runtime.setStatus('retrying');
      break;

    case 'agent_settled': {
      flushDeltas(agentId);
      /**
       * 安全网：回合既已结束，这条时间线上不可能再有新的增量。
       *
       * 正常路径里 message_end 已把助手消息收尾；但 steer 在流式中途插话时，
       * Pi 会直接丢弃那条没发完的消息——message_end 不来，它就永远停在
       * streaming，光标一直在对话里闪（用户实测报的 bug）。在这里兜底关掉。
       */
      timeline.finishStreaming();
      settleTurn(); // 回合有写入或跑过 bash 时，通知文件面板刷新
      if (isSubagent) {
        /**
         * 子智能体的一轮结束：只写它自己的时间线。
         *
         * **不能**碰父会话的运行状态（那会让父会话的输入框从"运行中"跳成"空闲"，
         * 用户会以为父 Agent 干完了），也不该把子智能体的耗时当成父回合的耗时。
         */
        const childSeconds = (Date.now() - (bufferFor(agentId).startedAt || Date.now())) / 1000;
        timeline.addSettled(`agent_settled · 这一轮结束${childSeconds > 0.5 ? ` · ${childSeconds.toFixed(1)}s` : ''}`);
        break;
      }
      runtime.setStatus('idle');
      runtime.setSending(false);
      const t = useRuntime.getState().tokens.total;
      // 耗时：有起点就报实际耗时（用事件到达时刻，不是回放时刻），没有就不编数字
      const seconds = runtime.runStartedAt ? (env.receivedAt - runtime.runStartedAt) / 1000 : 0;
      const parts = ['agent_settled · 运行结束'];
      if (seconds > 0) parts.push(seconds < 1 ? `${Math.round(seconds * 1000)}ms` : `${seconds.toFixed(1)}s`);
      if (t) parts.push(`累计 ${t.toLocaleString()} tokens`);
      timeline.addSettled(parts.join(' · '));
      if (seconds > 0) useRuntime.getState().setLastDuration(seconds);
      useRuntime.getState().setRunStartedAt(0);
      // 回合彻底结束了：中止标记到这里才清（可能跨 message_end / 工具收尾多个事件）
      useRuntime.getState().setAborting(false);
      /**
       * 每回合结束补问一次用量。
       *
       * 流式 usage 事件只给了"这一轮用了多少"，而**当前上下文占用**（含系统提示、
       * 工具定义、以及压缩后的新基准）只有 get_session_stats 的 contextUsage 才有。
       * 没有这一步，压缩之后环上的数字会一直停在压缩前。
       */
      void piBridge
        .getSessionStats()
        .then(routeResult)
        .catch(() => { /* 统计拿不到不影响对话 */ });
      break;
    }

    // 错误事件有三种形态，都要能看见：
    //   error           —— 通用错误
    //   extension_error —— 扩展报错（真机会发这个）
    //   agent_error     —— 运行时报错
    // 少写一个 case 的后果是"扩展炸了但界面一片安静"，很难排查。
    // 例外：刚按过「停止」时 Pi 也会回一条中止错误，那是预期结果，不当报错。
    case 'error':
    case 'agent_error':
    case 'extension_error': {
      const text = String(event.message ?? event.error ?? event.errorMessage ?? '未知错误');
      /**
       * 中止不是错误。
       *
       * 父会话看 runtime.aborting（用户按了停止）；子智能体那边这个标记从来
       * 不会被置位（子智能体不动父会话状态），所以以前子智能体被停掉时，
       * 它回的 "This operation was aborted" 会直接冒到应用错误条上——
       * 用户看到"出错了"，其实什么都没出错。
       */
      if (isAbortError(text)) {
        timeline.addSystem('info', '已中止', text);
        break;
      }
      if (isSubagent) {
        // 子智能体自己的错误写在**它自己的**时间线里，不要污染父会话的错误条
        timeline.addSystem('error', type === 'extension_error' ? '子智能体扩展出错' : '子智能体错误', text);
        break;
      }
      timeline.addSystem('error', type === 'extension_error' ? '扩展出错' : '错误', text);
      reportError(text);
      break;
    }

    case 'turn_end':
      // 一个 turn 结束不代表整轮结束（还可能有下一个 turn 或重试），
      // 这里只把工具行状态收敛一下，状态机交给 agent_settled。
      break;

    default:
      break; // 未知事件必须安全忽略：协议演进时老客户端不能崩
  }
}
