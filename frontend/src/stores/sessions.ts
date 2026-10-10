import { create } from 'zustand';
import { Events, PI_SESSION } from '@/bridge/events';
import { piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import {
  beginSessionSwitch, enterPendingSession, finishSessionSwitch,
  routeMessages, routeResult, type SessionPayload,
} from '@/rpc/router';
import { parseData, type Result } from '@/rpc/types';
import { useRuntime, loadPref, savePref } from '@/stores/runtime';
import { useAgents } from '@/stores/agents';
import { useUI } from '@/stores/ui';

/** 会话目录里的一个会话文件（由 service.go 的 ListSessions 读出） */
export interface SessionEntry {
  id: string;
  path: string;
  /** 这条会话属于哪个工作区（会话头里的 cwd）：渲染/刷新分组时要用 */
  cwd?: string;
  title: string;
  name?: string;
  updatedAt: string; // RFC3339，界面自己转「刚刚 / 3天」
  size: number;
}

interface SessionsData {
  cwd?: string;
  root?: string;
  sessions?: SessionEntry[];
}

interface BatchData {
  root?: string;
  groups?: { cwd?: string; sessions?: SessionEntry[] }[];
}

/** 一个工作区的会话列表状态 */
export interface WorkspaceSessions {
  items: SessionEntry[];
  loading: boolean;
  /** 是否已经加载过（哪怕结果是空列表）。按需加载靠它避免反复请求。 */
  loaded: boolean;
}

const EMPTY_GROUP: WorkspaceSessions = { items: [], loading: false, loaded: false };

const WORKSPACES_KEY = 'pi-gui-workspaces';
const SESSION_MAP_PREF = 'sessions';

/** Windows 路径大小写与分隔符不敏感；映射键统一后，同一工作区不会存出多份。 */
export function workspaceKey(path: string): string {
  return path.trim().replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase();
}

/**
 * 两个路径是不是同一个工作区。
 *
 * ⚠️ 不能用 `a.toLowerCase() === b.toLowerCase()`：后端存的是规范化键
 * （小写 + 正斜杠），而磁盘上的路径是 `E:\Foo\Bar` 这种写法，
 * 只比小写会把同一个文件夹判成两个——工作区列表就是这么出现重复行的。
 */
export function sameWorkspace(a: string, b: string): boolean {
  return workspaceKey(a) === workspaceKey(b);
}

/** 每个工作区分别记住最后打开的会话，避免从 A 切到 B 后覆盖 A 的记忆。 */
function loadSessionMap(): Record<string, string> {
  try {
    const raw = loadPref(SESSION_MAP_PREF, '{}');
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    return Object.fromEntries(Object.entries(parsed as Record<string, unknown>)
      .filter((entry): entry is [string, string] => typeof entry[1] === 'string'));
  } catch {
    return {};
  }
}

export function rememberedSession(cwd: string): string {
  const mapped = loadSessionMap()[workspaceKey(cwd)];
  if (mapped) return mapped;
  // 兼容旧版本只保存一个 session 的偏好：仅当前记住的工作区可继承，避免串工作区。
  const rememberedCwd = loadPref('cwd', '');
  return workspaceKey(rememberedCwd) === workspaceKey(cwd) ? loadPref('session', '') : '';
}

export function saveRememberedSession(cwd: string, sessionPath: string): void {
  const key = workspaceKey(cwd);
  if (!key) return;
  const sessions = loadSessionMap();
  if (sessionPath) sessions[key] = sessionPath;
  else delete sessions[key];
  savePref(SESSION_MAP_PREF, JSON.stringify(sessions));
  // 同时写后端 gui-state.json：localStorage 丢失或 WebView 存储分区改变时仍可恢复。
  void piBridge.rememberSession(cwd, sessionPath).catch(() => { /* localStorage 仍是降级路径 */ });
  savePref('session', sessionPath);
}

/** 记忆来源：memory=显式记住；recent=该工作区最近一次会话；cleared=用户主动开过新对话；
 *  none=没有可恢复的会话；local=后端不可用，退到 localStorage。 */
export type SessionMemorySource = 'memory' | 'recent' | 'cleared' | 'none' | 'local';

export interface SessionMemory {
  sessionPath: string;
  source: SessionMemorySource;
}

/**
 * 问后端「进入这个工作区该恢复哪条会话」。
 *
 * 后端掌握三态记忆（记住 / 显式清空 / 无记录），并且能在"没有记录"时回退到
 * 该工作区最近一次会话——这就是第二个工作区过去没有记忆的原因：
 * 它从没被显式记过，而旧实现只认显式记录。
 */
export async function persistedSession(cwd: string): Promise<SessionMemory> {
  try {
    const result = await piBridge.getRememberedSession(cwd);
    const payload = parseData<{ sessionPath?: string; source?: SessionMemorySource }>(result);
    if (payload) {
      const source = payload.source ?? 'none';
      if (source === 'cleared') return { sessionPath: '', source };
      if (payload.sessionPath) return { sessionPath: payload.sessionPath, source };
    }
  } catch {
    /* 后端状态不可用时降级到 localStorage */
  }
  return { sessionPath: rememberedSession(cwd), source: 'local' };
}

/** 工作区列表持久化：用户用「选择文件夹」加过的目录，下次启动还在 */
function loadWorkspaces(): string[] {
  try {
    const raw = localStorage.getItem(WORKSPACES_KEY);
    const parsed = raw ? (JSON.parse(raw) as unknown) : null;
    const list = Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : [];
    // 旧版本可能存进过重复/写法不同的同一条目录，读的时候顺手去重
    return list.filter((item, index) => list.findIndex(other => sameWorkspace(other, item)) === index);
  } catch {
    return [];
  }
}

function saveWorkspaces(list: string[]): void {
  try { localStorage.setItem(WORKSPACES_KEY, JSON.stringify(list.slice(0, 12))); } catch { /* 隐私模式忽略 */ }
}

/* ============================================================
   这里原本有一份"会话快照缓存（LRU）"
   ------------------------------------------------------------
   它的作用是"切换时先把上一条会话的内容同步画出来，别白屏等 RPC"。
   一进程一会话之后它**多余且有害**：
     · 多余——每条会话的视图现在留在 rpc/router.ts 的 views 里，热切换取回原样
       （连滚动位置与工具卡片都在），比"重新画一遍 get_messages"更完整；
     · 有害——预渲染是往**当前**时间线里灌另一条会话的内容，而 runtimeId 没变，
       等于人为制造一次串台；切换万一失败，之前的内容已经被覆盖掉了。
   所以整块删掉，切换路径只认"结果里有没有 messages"。
   ============================================================ */

interface SessionsStore {
  /**
   * 每个工作区各自的会话列表。
   *
   * 侧栏会同时列出**所有展开的工作区**的会话（见 ui-workspace 的分组做法），
   * 所以列表不能再是一个全局数组：那样只有当前工作区能显示自己的会话。
   * 键是 workspaceKey(cwd)。
   */
  byWorkspace: Record<string, WorkspaceSessions>;
  /** 当前会话文件路径：Pi 的 get_state.sessionFile 会给，用来高亮当前对话 */
  activePath: string;
  /**
   * 当前会话还没有名字（刚新建、首条消息还没发）。
   *
   * 它决定"发送时要不要按消息命名"：Pi 的会话标题默认取首条用户消息，
   * 但**文件里没内容之前没有标题**——新建会话后列表里就显示成空的/无会话。
   * 所以新建后先显示占位名"新建会话"，发出第一条消息时再按它命名。
   */
  currentNamed: boolean;
  /**
   * 当前有一条"待创建的新会话"：用户点了新建，但还没有发出第一条消息，
   * 所以后端**还没有为它起进程**（会话=日志，进程懒创建，见 Go 的 NewSession）。
   *
   * 为什么要单独一个标记（而不是只看 connected/runtimeId）：
   *   · 空态与状态行要能说清"这条会话还没有进程，发第一条消息时会自动启动"，
   *     而不是笼统的"未连接"；
   *   · 有些老逻辑把 `connected` 当成"能不能用"的总开关（例如在某个目录里新建会话），
   *     待创建状态必须与"真的连不上"区分开。
   */
  pendingNew: boolean;
  /**
   * 标记当前会话是否已经有名字。
   *
   * true = 不要再按首条消息改名（打开历史会话就是这种）；
   * false = 还等着首条消息来命名（刚新建、刚切角色）。
   */
  setCurrentNamed(named: boolean): void;
  root: string;
  /** 工作区（= Pi 的工作目录）候选列表，第一个是当前激活的 */
  workspaces: string[];

  addWorkspace(path: string): void;
  removeWorkspace(path: string): void;
  /** 忘掉一批工作区（后端说它们是用户删过的）：清掉 localStorage 与列表里的残留 */
  dropWorkspaces(keys: string[]): void;
  /** 加载一个工作区的会话列表（写入 byWorkspace 对应的分组） */
  refresh(cwd?: string): Promise<void>;
  /** 一次加载多个工作区（后端只扫一遍会话目录） */
  refreshMany(cwds: string[]): Promise<void>;
  /** 作废一个工作区的缓存（下次展开时重新加载） */
  invalidate(cwd: string): void;
  /**
   * 打开会话（用户点侧栏那一行）。
   *
   * 三种结果：
   *   · 这条会话**已经开着**（有进程）→ 零 RPC 复用：界面把之前存下的视图原样取回
   *     （不重载历史、不打断任何正在跑的回合）；
   *   · **没开着** → 不起进程：历史直接读会话文件（loadSessionHistory），
   *     当前会话进入"待创建"，进程留到第一条消息（那时后端会先 switch_session
   *     再发送，见 Go 的 ensureActiveRuntime）；
   *   · 打不开（文件不在会话目录内、会话的工作目录没了…）→ 当前会话原封不动，
   *     手上这一轮不受影响（返回 false）。
   *
   * 返回是否**真的**切过去了：启动恢复不能把"失败"误当成成功。
   */
  open(sessionPath: string): Promise<boolean>;
  create(): Promise<void>;
  /**
   * 切工作区。
   *
   * 只把"当前会话"换成新工作区里的一条（没有就冷启动一条），
   * **不停止**其它工作区里正在跑的会话进程（旧实现是"停进程再按新目录启动"，
   * 等于把甲工作区正在跑的任务杀掉）。
   */
  switchWorkspace(cwd: string): Promise<boolean>;
  rename(sessionPath: string, name: string, cwd?: string): Promise<void>;
  remove(sessionPath: string, cwd?: string): Promise<void>;
  reset(): void;
  /**
   * 订阅 pi:session（后端在**后台**换了当前会话时推来）。
   *
   * 一进程一会话之后前端自己调的 switch_session 有返回值可用，用不到这条；
   * 保留是因为任务循环仍可能在后台换当前会话——那种切换前端没参与，
   * 只能由事件通知。收到就按一次普通切换的结果处理。
   *
   * 返回注销函数而不是自己在模块里挂一次：React 严格模式会挂载 → 卸载 → 再挂载，
   * 订阅写在 useEffect 的清理函数里才不会"同一条事件被处理两遍"。
   */
  watch(): () => void;
}

/** 读一个工作区的列表状态（没加载过时返回空对象，避免组件里到处写 ??） */
export function sessionsOf(byWorkspace: Record<string, WorkspaceSessions>, cwd: string): WorkspaceSessions {
  return byWorkspace[workspaceKey(cwd)] ?? EMPTY_GROUP;
}

/**
 * 「切换会话」的唯一实现：open / create / switchWorkspace 都走它。
 *
 * 步骤顺序有讲究，别调换：
 *   ① 已经有切换在飞 → 直接返回：用户快速连点两个会话时只发一次切换
 *      （后点的那个**不该**排队——架构上已经不需要排队了，等服务端把自己
 *      那次切换做完，用户再点一下即可；排队反而会变成"点完等半天"）；
 *   ② beginSessionSwitch：把当前这一帧的增量冲干净，并进入"切换中"；
 *   ③ 发 RPC（switch_session / new_session / switch_workspace）；
 *   ④ 成功 → 待创建（pending）走 enterPendingSession，热切换走 finishSessionSwitch；
 *   ⑤ 任何失败 → finishSessionSwitch()（不带 result）：视图一动不动，
 *      只把切换期间攒下的输出回放到当前视图里。
 */
async function navigate(action: () => Promise<Result>, fresh = false): Promise<boolean> {
  const runtime = useRuntime.getState();
  if (runtime.switching || runtime.busy) return false;
  /**
   * 新建会话时先忘掉旧路径：新会话的会话文件此刻还没落盘，
   * 留着旧路径的话，下次启动会拿它去恢复——恢复出来的是上一个会话。
   */
  if (fresh) saveRememberedSession(useRuntime.getState().cwd, '');
  beginSessionSwitch();
  try {
    const result = await action();
    if (!result.success) {
      reportError(result.error || '切换会话失败', '切换会话');
      return false;
    }
    // 扩展的 session_before_switch 钩子可以拦下这次切换：如实说明，别让用户以为切过去了
    if (parseData<{ cancelled?: boolean }>(result)?.cancelled) {
      useUI.getState().showNotice('切换会话被扩展取消');
      return false;
    }
    /**
     * 待创建：目标会话**还没有进程**（Go 侧只记住"用户在看它"）。
     *
     * 两种情况都走这里，区别只在带不带 sessionPath：
     *   · 新建会话（sessionPath 为空）→ 清空时间线，等第一条消息开一条新会话；
     *   · 打开一条历史会话（sessionPath 有值）→ 历史**直接从 .jsonl 读**
     *     （见下面的 loadSessionHistory），进程留到第一条消息。
     *
     * 为什么不能交给 finishSessionSwitch：那条路要求结果里带 runtimeId 或 messages，
     * 而这里两者都没有——交过去只会"什么都没发生"，上一条会话的 runtimeId 与时间线
     * 留在界面上，用户看到的就是"点了没反应"。
     */
    const pending = parseData<{ pending?: boolean; sessionPath?: string; cwd?: string }>(result);
    if (pending?.pending) {
      const targetPath = pending.sessionPath ?? '';
      // cwd 用后端给的（历史会话的 cwd 取自它自己的会话头，可能不在当前工作区）
      const targetCwd = pending.cwd || useRuntime.getState().cwd;
      enterPendingSession(targetCwd);
      useSessions.setState({
        /**
         * 泛化的"待创建"：**可能带一条会话文件**。
         *
         * 带 = 用户在看一条历史会话（进程还没建，历史已经在屏幕上）；
         * 不带 = 新建的空会话（等第一条消息才命名）。
         */
        activePath: targetPath,
        currentNamed: Boolean(targetPath),
        pendingNew: true,
      });
      if (targetPath) {
        /**
         * runtime.sessionFile 也要指过去。
         *
         * enterPendingSession 走的是 resetRuntime()（会话路径清空），而这条会话
         * **就是**用户此刻在看的那条：状态行、侧栏高亮、以及"重新载入消息"都读它
         * （没有进程时那条走 loadSessionHistory，路径就从这里来）。
         * 后端那份合成 get_state 报的也是同一个值，两边的口径保持一致。
         */
        useRuntime.setState({ sessionFile: targetPath });
        // 这条历史会话就是"上次聊到哪"，重启后要能回到它
        saveRememberedSession(targetCwd, targetPath);
        await loadSessionHistory(targetPath);
      }
      return true;
    }
    finishSessionSwitch(result);
    const current = useRuntime.getState();
    if (current.sessionFile) saveRememberedSession(current.cwd, current.sessionFile);
    useSessions.setState({
      activePath: current.sessionFile,
      // 新建的会话还没有名字：等首条消息来命名（见 useAgent 的 nameSessionFromFirstMessage）
      currentNamed: !fresh,
      // 切到了真会话（有进程）就不再是"待创建"状态
      pendingNew: false,
    });
    /**
     * 切换/新建之后补问一次用量：不然环上显示的是**上一条会话**的占用，
     * 或者（刚启动时）是 0。这条是 fire-and-forget：拿不到也不该挡住切换。
     * 它的 runtimeId 会被 routeResult 校验，迟到的结果不会写进别的会话。
     */
    void piBridge.getSessionStats().then(routeResult).catch(() => { /* 统计失败不影响会话 */ });
    // 列表里已经有这条会话就不再扫一遍会话目录：启动预加载刚扫过，重复扫描只会白白拖慢启动。
    const listed = sessionsOf(useSessions.getState().byWorkspace, current.cwd)
      .items.some(item => sameWorkspace(item.path, current.sessionFile));
    if (!listed) await useSessions.getState().refresh(current.cwd);
    return true;
  } catch (error) {
    reportError(error, '切换会话');
    return false;
  } finally {
    /**
     * 失败路径的收尾：finishSessionSwitch() **不带 result**。
     *
     * 这时什么都不能动——runtimeId、时间线、当前会话全是原来的；
     * 但切换期间攒下的输出要回放到当前视图里（它们本来就属于这条会话）。
     * 成功时 switching 已经是 false，这里不会重复执行。
     */
    if (useRuntime.getState().switching) finishSessionSwitch();
  }
}

/**
 * 用「读会话文件」的结果重建时间线（打开一条还没有进程的历史会话）。
 *
 * 与 routeMessages 的关系：那条路走的是 `get_messages`（进程里的内存），
 * 这条路走 `load_session_history`（磁盘上的 .jsonl）——**形状完全一样**，
 * 所以解析只有一份（rpc/router.ts 的 rebuildTimeline），这里只负责把结果递过去。
 *
 * 失败时说清楚是"读这条会话的历史"，而不是笼统的"切换会话失败"：
 * 用户点了那一条，需要知道是那一条的问题（文件被删、权限、不是会话文件）。
 */
async function loadSessionHistory(sessionPath: string): Promise<void> {
  try {
    const result = await piBridge.loadSessionHistory(sessionPath);
    if (!result.success) {
      reportError(result.error || '读取会话历史失败', '打开会话');
      return;
    }
    routeMessages(result);
  } catch (error) {
    reportError(error, '打开会话');
  }
}

/**
 * 会话列表 + 工作区列表。
 *
 * 数据来源两种，别混：
 *   · 列表：Pi 的 RPC 没有「列会话」命令，所以读会话目录（后端 ListSessions）；
 *   · 新建/切换/改名：走 RPC（new_session / switch_session / set_session_name），
 *     因为只有 Pi 知道怎么把会话文件装进当前进程。
 *   · 删除：只允许会话目录内的 JSONL；当前会话会先切到新会话再删除。
 */
export const useSessions = create<SessionsStore>((set, get) => ({
  byWorkspace: {},
  activePath: rememberedSession(useRuntime.getState().cwd),
  // 启动时恢复的是已有会话：默认当作已经有名字，不要擅自改名
  currentNamed: true,
  // 启动时没有"待创建的新会话"（那是用户点「新建对话」之后才有的状态）
  pendingNew: false,
  root: '',
  workspaces: loadWorkspaces(),

  /**
   * 加入工作区列表：**已存在就什么都不做**。
   *
   * 这里曾经是"无脑插到最前面"，而切换工作区时（switchFolder 会调它）
   * 被点的那一行就跳到第一行去了——列表顺序必须和"点了谁"无关。
   * 新目录追加到末尾，顺序从此只由"第一次加入的时间"决定。
   */
  addWorkspace: path => {
    const exists = get().workspaces.some(item => sameWorkspace(item, path));
    if (exists) return;
    const list = [...get().workspaces, path];
    saveWorkspaces(list);
    set({ workspaces: list });
  },

  removeWorkspace: path => {
    const list = get().workspaces.filter(item => !sameWorkspace(item, path));
    saveWorkspaces(list);
    const byWorkspace = { ...get().byWorkspace };
    delete byWorkspace[workspaceKey(path)];
    set({ workspaces: list, byWorkspace });
  },

  /**
   * 丢掉一批工作区。
   *
   * 后端把"用户删过的工作区"记在 gui-state.json 里；localStorage 里可能还留着
   * 同一份（WebView 存储与后端文件是两套），启动时必须按后端的名单清一遍，
   * 否则删掉的工作区会从本地残留里复活。
   */
  dropWorkspaces: keys => {
    if (keys.length === 0) return;
    const drop = new Set(keys.map(workspaceKey));
    const list = get().workspaces.filter(item => !drop.has(workspaceKey(item)));
    saveWorkspaces(list);
    const byWorkspace: Record<string, WorkspaceSessions> = {};
    for (const [key, value] of Object.entries(get().byWorkspace)) {
      if (!drop.has(key)) byWorkspace[key] = value;
    }
    set({ workspaces: list, byWorkspace });
  },

  refresh: async cwd => {
    const target = cwd ?? useRuntime.getState().cwd;
    const key = workspaceKey(target);
    if (!key) return;
    const mark = (patch: Partial<WorkspaceSessions>) => set(state => ({
      byWorkspace: {
        ...state.byWorkspace,
        [key]: { ...(state.byWorkspace[key] ?? EMPTY_GROUP), ...patch },
      },
    }));
    mark({ loading: true });
    try {
      const result = await piBridge.listSessions(target);
      if (!result.success) {
        reportError(result.error || '读取会话列表失败');
        // 失败也要标记 loaded：否则展开时会无限重试同一个坏目录
        mark({ loading: false, loaded: true });
        return;
      }
      const data = parseData<SessionsData>(result);
      mark({ items: data?.sessions ?? [], loading: false, loaded: true });
      if (data?.root) set({ root: data.root });
      // 当前会话路径：高亮用。只在它确实属于这个工作区时才写。
      const sessionFile = useRuntime.getState().sessionFile;
      if (sessionFile && sameWorkspace(useRuntime.getState().cwd, target)) set({ activePath: sessionFile });
    } catch (error) {
      reportError(error);
      mark({ loading: false, loaded: true });
    }
  },

  /**
   * 一次加载多个工作区。
   *
   * 侧栏默认把所有分组都展开，逐个调用 refresh 会让后端把整个会话目录扫 N 遍
   * （每个会话文件都要读开头找标题）。这里合并成一次扫描，代价与工作区数量无关。
   */
  refreshMany: async cwds => {
    const targets = cwds.filter(Boolean);
    if (targets.length === 0) return;
    const keys = targets.map(workspaceKey);
    set(state => {
      const byWorkspace = { ...state.byWorkspace };
      for (const key of keys) {
        byWorkspace[key] = { ...(byWorkspace[key] ?? EMPTY_GROUP), loading: true };
      }
      return { byWorkspace };
    });
    try {
      const result = await piBridge.listSessionsBatch(targets);
      if (!result.success) {
        reportError(result.error || '读取会话列表失败');
        set(state => {
          const byWorkspace = { ...state.byWorkspace };
          for (const key of keys) {
            byWorkspace[key] = { ...(byWorkspace[key] ?? EMPTY_GROUP), loading: false, loaded: true };
          }
          return { byWorkspace };
        });
        return;
      }
      const data = parseData<BatchData>(result);
      const groups = data?.groups ?? [];
      set(state => {
        const byWorkspace = { ...state.byWorkspace };
        targets.forEach((target, index) => {
          const key = keys[index];
          // 后端按请求顺序返回，直接用下标对应，避免再按路径反查。
          const items = groups[index]?.sessions ?? [];
          byWorkspace[key] = { items, loading: false, loaded: true };
        });
        return { byWorkspace, root: data?.root ?? state.root };
      });
    } catch (error) {
      reportError(error);
      set(state => {
        const byWorkspace = { ...state.byWorkspace };
        for (const key of keys) {
          byWorkspace[key] = { ...(byWorkspace[key] ?? EMPTY_GROUP), loading: false, loaded: true };
        }
        return { byWorkspace };
      });
    }
  },

  invalidate: cwd => {
    const key = workspaceKey(cwd);
    set(state => {
      if (!state.byWorkspace[key]) return state;
      const byWorkspace = { ...state.byWorkspace };
      byWorkspace[key] = { ...byWorkspace[key], loaded: false };
      return { byWorkspace };
    });
  },

  /**
   * 打开历史会话（用户点侧栏那一行）。
   *
   * ⚠️ 「点同一条不重复切」这条守卫必须在发 RPC 之前：后端会认出"这条会话
   * 已经开着"并零 RPC 复用，但界面这边照样要走一遍 begin/finish，
   * 白白闪一次"切换中"，还会把输入框禁掉一下。
   *
   * 冷的那条路（会话还没有进程）由后端在返回值里给出 pending + sessionPath，
   * navigate 会接着把历史从 .jsonl 读出来（见上面的 loadSessionHistory）——
   * 所以"打开一条老会话"不再有 1–3.5 秒的冷启动等待。
   */
  open: sessionPath => {
    if (sameWorkspace(sessionPath, useRuntime.getState().sessionFile)) return Promise.resolve(false);
    return navigate(() => piBridge.switchSession(sessionPath));
  },

  /**
   * 新建对话：**立刻可用**，不再等后端起进程。
   *
   * 后端只把"当前会话"换成一个待创建的空会话（会话=日志，进程懒创建，
   * 见 Go 的 NewSession），所以这里没有 1–3.5 秒的冷启动等待；
   * 进程在用户发出第一条消息时按需创建（见 useAgent 的 prompt）。
   * 原来那条会话的进程不受影响，继续在它自己的进程里跑。
   */
  create: async () => {
    const ok = await navigate(() => piBridge.newSession(), true);
    if (!ok) return;
    /**
     * 新建一条会话就把子智能体登记表收干净（用户要的"开一条新的"）。
     *
     * 注意它**不是**会话视图的一部分：子智能体的进程独立于会话进程，
     * 而且 Go 侧的 agent 事件不带 runtimeId，前端无从判断"它属于哪条会话"
     * （见 rpc/router.ts 里 SessionView 的说明）。所以这里按"开新的就清一下"
     * 的老规矩处理，而不是按会话存两份。
     */
    useAgents.getState().clear();
    /* 不弹"已新建对话"提示：界面本身已经切到一条空会话，看得见就是反馈 */
  },

  /** 切工作区：只换"在看哪一条"，不停别的进程（见接口上的说明） */
  switchWorkspace: cwd => navigate(() => piBridge.switchWorkspace(cwd)),

  /**
   * 订阅 pi:session：后端在**后台**换了当前会话时推来。
   *
   * 前端自己调的 switch_session 有返回值可用，这条路只兜底"任务循环在后台
   * 换会话"那种情况。处理方式与普通切换完全一致（走同一条 finishSessionSwitch），
   * 所以两边的界面表现不会漂移。
   */
  watch: () => {
    const off = Events.On(PI_SESSION, event => {
      const payload = event.data as SessionPayload | undefined;
      if (!payload) return;
      /**
       * 进程正在被我们停掉/重启，或者已经没有活着的进程时，这条推送属于
       * **上一个进程周期**：画上去就是串台。直接丢掉。
       */
      const runtime = useRuntime.getState();
      if (runtime.stopping || !runtime.connected) return;
      if (payload.error) {
        reportError(payload.error, '切换会话');
        return;
      }
      if (payload.cancelled) return;
      /**
       * 把它翻译成"一次普通的切换结果"，交给 router 的同一条路径处理：
       * 换视图（有 runtimeId 时）、按需重建时间线（有 messages 时）、
       * 没有 messages 就不动时间线。
       */
      const result: Result = {
        id: '',
        command: 'switch_session',
        success: true,
        runtimeId: payload.runtimeId,
        data: JSON.stringify({
          state: payload.state,
          messages: payload.messages,
          cwd: payload.cwd ?? runtime.cwd,
          sessionPath: payload.path ?? '',
        }),
      };
      beginSessionSwitch();
      finishSessionSwitch(result);
      const current = useRuntime.getState();
      if (current.sessionFile) {
        saveRememberedSession(current.cwd, current.sessionFile);
        set({ activePath: current.sessionFile, currentNamed: true, pendingNew: false });
        void get().refresh(current.cwd);
      }
    });
    return () => off();
  },

  /** 改名：Pi 的 set_session_name 只作用于当前会话，后端会切过去再切回来 */
  rename: async (sessionPath, name, cwd) => {
    try {
      const result = await piBridge.renameSession(sessionPath, name);
      if (!result.success) {
        reportError(result.error || '重命名失败');
        return;
      }
      // 刷新这条会话所在的工作区分组（可能不是当前激活的那个）
      invalidateGroup(get, set, cwd);
      await get().refresh(cwd ?? useRuntime.getState().cwd);
      useUI.getState().showNotice(`已重命名为「${name}」`, 'success');
    } catch (error) {
      reportError(error);
    }
  },

  remove: async (sessionPath, cwd) => {
    if (useRuntime.getState().switching) return;
    beginSessionSwitch();
    try {
      const result = await piBridge.deleteSession(sessionPath);
      if (!result.success) {
        reportError(result.error || '删除失败');
        return;
      }
      const payload = parseData<{ wasActive?: boolean; activation?: Result }>(result);
      /**
       * 删掉的正好是当前会话：后端已经把"当前会话"换成一条**待创建**的新会话
       * （不起进程，见 Go 的 DeleteSession），`activation` 就是那次切换的载荷。
       *
       * 两种情况分开处理，因为它们的界面动作完全不同：
       *   · 待创建（pending）→ 没有进程可换，只能把视图切成"未命名新会话"
       *     （清空时间线、侧栏留占位行）；直接交给 finishSessionSwitch 会
       *     **什么都不做**——用户会继续看着一条已经被删掉的会话。
       *   · 有 runtimeId（历史路径）→ 与普通切换完全一样，走同一条路。
       */
      const activation = payload?.activation;
      if (activation && parseData<{ pending?: boolean; cwd?: string }>(activation)?.pending) {
        const targetCwd = parseData<{ cwd?: string }>(activation)?.cwd || useRuntime.getState().cwd;
        enterPendingSession(targetCwd);
        set({ activePath: '', currentNamed: false, pendingNew: true });
      } else if (activation) {
        finishSessionSwitch(activation);
      }
      if (payload?.wasActive || rememberedSession(useRuntime.getState().cwd) === sessionPath) {
        saveRememberedSession(useRuntime.getState().cwd, '');
        set({ activePath: '' });
      }
      invalidateGroup(get, set, cwd);
      await get().refresh(cwd ?? useRuntime.getState().cwd);
      useUI.getState().showNotice('会话已删除', 'success');
    } catch (error) {
      reportError(error);
    } finally {
      // 删除失败或"删的不是当前会话"：视图一动不动，只把攒下的输出回放掉
      if (useRuntime.getState().switching) finishSessionSwitch();
    }
  },

  setCurrentNamed: named => set({ currentNamed: named }),

  reset: () => set({ byWorkspace: {}, activePath: '', pendingNew: false }),
}));

/** 让某个工作区的缓存失效，下次刷新一定重新读（改过名/删过会话之后用） */
function invalidateGroup(
  get: () => SessionsStore,
  set: (partial: Partial<SessionsStore> | ((state: SessionsStore) => Partial<SessionsStore>)) => void,
  cwd?: string,
): void {
  if (!cwd) return;
  const key = workspaceKey(cwd);
  if (!get().byWorkspace[key]) return;
  set(state => ({ byWorkspace: { ...state.byWorkspace, [key]: { ...state.byWorkspace[key], loaded: false } } }));
}
