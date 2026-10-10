import { create } from 'zustand';

/**
 * 会话活动表：侧栏那两枚状态指示的数据来源。
 *
 * 需求（用户原话的意思）：
 *   · 这条会话的 Pi **正在跑任务** → 会话名右边显示一个**圆环**，环上体现**上下文占用**；
 *   · 会话**跑完了但它不是当前正在看的那条** → 显示一个**绿色小点**（"有结果了，你还没看"）；
 *   · **点进去之后绿点消失**（已看过）。
 *
 * 为什么单独一张表，而不是直接用 router 里那份会话视图（views）：
 *   1. views 是"整条时间线"的重家伙，只放**不在台上**的会话，还有 LRU 上限 8——
 *      拿它当"侧栏状态"的来源，会导致"聊过的会话一多，指示器就随机消失"；
 *   2. 侧栏是按**会话文件路径**索引的，而事件是按 **runtimeId** 来的，
 *      两者之间需要一张小的反查表（这张表就是干这个的）；
 *   3. 侧栏只想画两个小图标，不该订阅整条时间线。
 *
 * 路径一律**规范化后**再当键：同一个会话可能以 `C:\a\b.jsonl` 与 `c:/a/b.jsonl`
 * 两种写法进来（界面、Go、历史数据里都出现过），不规范化就会"同一个会话两条记录"，
 * 绿点也就点不掉。
 */

export interface SessionActivity {
  /** 这条会话此刻的进程 id（没有 = 还没在当前进程周期里开过） */
  runtimeId: string;
  /** 正在跑回合 */
  running: boolean;
  /** 后台跑完了、用户还没点进去看 */
  done: boolean;
  /** 上下文占用百分比（0-100；没有数据时为 0） */
  percent: number;
}

interface ActivityState {
  /** 规范化路径 → 活动状态 */
  byPath: Record<string, SessionActivity>;
  /** runtimeId → 规范化路径（事件按 runtimeId 来，得反查回会话行） */
  pathByRuntime: Record<string, string>;
  /** runtimeId → 上下文上限（用来把 tokens 换算成百分比） */
  windowByRuntime: Record<string, number>;
  /** runtimeId → 本回合用到的 tokens */
  tokensByRuntime: Record<string, number>;

  /** 激活某条会话：登记 runtime↔路径映射，并**清掉绿点**（用户已经点进来看它了） */
  noteActivation(runtimeId: string, sessionPath: string, contextWindow?: number): void;
  /** 回合开始/结束。isForeground = 这条就是用户正在看的那条 */
  noteRunning(runtimeId: string, running: boolean, isForeground: boolean): void;
  /**
   * 这条 runtime 的进程退出了（崩溃、被回收、被停止都算）。
   *
   * 必须有一个出口把 running 收掉：进程没了就再也不会有 agent_settled，
   * 不收的话侧栏那个圆环会永远转下去（实测：后台会话的进程被闲置回收后，
   * 它的圈挂了一整夜）。done 不动——跑完时点的绿点是"有结果没看"，
   * 进程随后被回收并不能把它抹掉。
   *
   * 顺手清掉这条 runtime 的反查/用量记录：它已经死了，留着只会随
   * "开过的会话数"无限涨（长时间运行下的慢泄漏）。
   */
  noteExit(runtimeId: string): void;
  /** 上下文用量（tokens） */
  noteTokens(runtimeId: string, tokens: number): void;
  /** 用户点了这条会话：绿点立刻消失（不等切换成功——他的意图就是"我要看它"） */
  markSeen(sessionPath: string): void;
  /** 进程全停/换工作区：整张表清空 */
  clearAll(): void;
}

/**
 * 规范化：统一分隔符与大小写。
 *
 * 与 stores/sessions.ts 里的 workspaceKey 同一套规则（Windows 路径大小写不敏感），
 * 只是这里针对的是**文件**路径。
 */
export function activityKey(path: string): string {
  return (path ?? '').trim().replace(/\\/g, '/').toLowerCase();
}

export const useSessionActivity = create<ActivityState>((set, get) => ({
  byPath: {},
  pathByRuntime: {},
  windowByRuntime: {},
  tokensByRuntime: {},

  noteActivation: (runtimeId, sessionPath, contextWindow) => {
    if (!runtimeId || !sessionPath) return;
    const key = activityKey(sessionPath);
    if (!key) return;
    set(state => {
      const previous = state.byPath[key];
      return {
        pathByRuntime: { ...state.pathByRuntime, [runtimeId]: key },
        windowByRuntime: contextWindow && contextWindow > 0
          ? { ...state.windowByRuntime, [runtimeId]: contextWindow }
          : state.windowByRuntime,
        byPath: {
          ...state.byPath,
          // 点进来就算看过了：绿点消失；正在跑的话圆环保留
          [key]: {
            runtimeId,
            running: previous?.running ?? false,
            done: false,
            percent: previous?.percent ?? 0,
          },
        },
      };
    });
  },

  noteRunning: (runtimeId, running, isForeground) => {
    const state = get();
    const key = state.pathByRuntime[runtimeId];
    if (!key) return;
    set(current => {
      const previous = current.byPath[key] ?? { runtimeId, running: false, done: false, percent: 0 };
      return {
        byPath: {
          ...current.byPath,
          [key]: {
            ...previous,
            runtimeId,
            running,
            /**
             * 只有**后台**那条跑完才亮绿点。
             *
             * 用户正看着的那条不需要提示"它结束了"——他眼前就是结果；
             * 给它亮绿点只会让人以为"还有别的事要做"。
             */
            done: running ? false : (isForeground ? false : true),
          },
        },
      };
    });
  },

  noteTokens: (runtimeId, tokens) => {
    const state = get();
    const key = state.pathByRuntime[runtimeId];
    if (!key || !tokens) return;
    const window = state.windowByRuntime[runtimeId] ?? 0;
    const percent = window > 0 ? Math.min(100, Math.max(1, Math.round((tokens / window) * 100))) : 0;
    set(current => {
      const previous = current.byPath[key] ?? { runtimeId, running: false, done: false, percent: 0 };
      return {
        tokensByRuntime: { ...current.tokensByRuntime, [runtimeId]: tokens },
        byPath: { ...current.byPath, [key]: { ...previous, runtimeId, percent } },
      };
    });
  },

  noteExit: runtimeId => {
    if (!runtimeId) return;
    set(state => {
      const key = state.pathByRuntime[runtimeId];
      const pathByRuntime = { ...state.pathByRuntime };
      const windowByRuntime = { ...state.windowByRuntime };
      const tokensByRuntime = { ...state.tokensByRuntime };
      delete pathByRuntime[runtimeId];
      delete windowByRuntime[runtimeId];
      delete tokensByRuntime[runtimeId];
      const previous = key ? state.byPath[key] : undefined;
      if (!previous) return { pathByRuntime, windowByRuntime, tokensByRuntime };
      return {
        pathByRuntime, windowByRuntime, tokensByRuntime,
        // 只收圆环：done（"跑完了还没看"的绿点）与 percent 都是事实，进程回收改变不了它们
        byPath: { ...state.byPath, [key]: { ...previous, running: false } },
      };
    });
  },

  markSeen: sessionPath => {    const key = activityKey(sessionPath);
    if (!key) return;
    set(state => {
      const previous = state.byPath[key];
      if (!previous || !previous.done) return state;
      return { byPath: { ...state.byPath, [key]: { ...previous, done: false } } };
    });
  },

  clearAll: () => set({ byPath: {}, pathByRuntime: {}, windowByRuntime: {}, tokensByRuntime: {} }),
}));

/** 按会话文件路径取活动状态（侧栏每行用）。 */
export function activityOf(path: string): SessionActivity | undefined {
  return useSessionActivity.getState().byPath[activityKey(path)];
}
