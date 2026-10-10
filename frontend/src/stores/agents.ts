import { create } from 'zustand';
import { piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import { useUI } from '@/stores/ui';
import { dropAgentTimeline } from '@/stores/timeline';

/**
 * 子智能体的登记表（谁被派出去了、在跑什么、结果如何、还能不能继续说话）。
 *
 * 注意：**时间线不在这里**。子智能体的会话内容走的是主会话那套解析与渲染
 * （见 stores/timeline.ts 的 timelineFor + rpc/router.ts 按 agentId 分流）。
 * 这里之前自己攒过一份"每个 delta 一条"的记录，效果是一个字一个字往外蹦，
 * 而且工具卡片、思考折叠这些都没有——复用的意义就在这里。
 */

/**
 * 子智能体的形态（对齐 DSH 的 one-shot / continuable）。
 *
 * 它决定界面能不能给用户一个输入框：continuable 跑完一轮会驻留（进程还在），
 * 空闲时可以接着发消息；one-shot 跑完即释放进程，之后只读。
 */
export type AgentMode = 'one-shot' | 'continuable';

/** 给子智能体发消息的投递方式（对齐 DSH 的 Queue / Steer） */
export type AgentDelivery = 'queue' | 'steer';

/** 一个子智能体的对外信息（来自 Go 的 pi:agent-state 事件）。 */
export interface AgentRecord {
  id: string;
  parentId: string;
  roleId: string;
  task: string;
  status: string;
  phase?: string;
  err?: string;
  sessionDir?: string;
  startedAt: string;
  endedAt?: string;
  usage?: { turns?: number; input?: number; output?: number; totalTokens?: number };
  /**
   * 形态。Go 侧一定会给（json 里没有 omitempty）；判断一律写成 `=== 'continuable'`，
   * 所以万一这个字段真的缺了，也会自然落到"一次性"那边——宁可少给一个输入框，
   * 也不要让用户打完一段话才从错误条里知道"它是不能继续的"。
   */
  mode: AgentMode;
  /** 短名（例如 ctf-recon-1）；没有就用 id 显示（见 agentDisplayName） */
  name?: string;
  /**
   * 最近一个**已结束**的轮次是不是正常跑完的。
   *
   * 为什么单独一个布尔而不是多一个 status：`idle` 只说"此刻没有回合在跑"，
   * 成没成是另一件事。混进同一个枚举必然出现两种误报——中断一轮被显示成
   * 绿点"已完成"，或者空闲驻留被显示成"还在忙"（照 DSH 的分工）。
   */
  lastTurnCompleted: boolean;
  /** 已正常结束的轮次数（可继续子智能体可以跑很多轮） */
  turns: number;
  /** 进入空闲的时刻（RFC3339）：耗时按它冻住，不再随钟表增长 */
  idleSince?: string;
}

interface AgentsState {
  /** id → 记录，order 是展示顺序（按派出顺序） */
  agents: Record<string, AgentRecord>;
  order: string[];
  /** 当前看的是哪个子智能体（'' = 主会话） */
  activeId: string;

  upsert(agent: AgentRecord): void;
  setActive(id: string): void;
  clear(): void;
  /** 给它发一条消息（只有可继续形态受理）。返回是否受理成功 */
  send(id: string, message: string, delivery: AgentDelivery): Promise<boolean>;
  /** 只中断它当前这一轮，不销毁它。返回是否请求成功 */
  interrupt(id: string): Promise<boolean>;
  /** 结束它并释放进程（之后不能再发消息）。返回是否成功 */
  kill(id: string): Promise<boolean>;
}

export const useAgents = create<AgentsState>((set, get) => ({
  agents: {},
  order: [],
  activeId: '',

  upsert: agent =>
    set(state => {
      const exists = Boolean(state.agents[agent.id]);
      return {
        agents: { ...state.agents, [agent.id]: { ...state.agents[agent.id], ...agent } },
        order: exists ? state.order : [...state.order, agent.id],
      };
    }),

  setActive: id => set({ activeId: id }),

  /**
   * 给它发一条消息。
   *
   * 成功只代表"已受理"——所以这里**不动时间线**：它的回复要等它自己那一轮跑完，
   * 由它自己的事件流写进它自己的时间线。乐观插一条"我说了什么"看着舒服，
   * 但那条消息可能被中断、可能被排在后面，最后时间线里就多了一条
   * 与真实记录对不上的气泡（时间线的唯一来源必须是事件流）。
   *
   * 返回是否受理成功：调用方据此决定清不清空输入框（失败时留着草稿，
   * 用户不用把刚打的一段话再打一遍）。
   */
  send: async (id, message, delivery) => {
    const text = message.trim();
    if (!text) return false;
    try {
      const result = await piBridge.sendToAgent(id, text, delivery);
      if (!result.success) {
        reportError(result.error || '发送失败', '子智能体');
        return false;
      }
      useUI.getState().showNotice('已受理（它这一轮的结论会稍后出现在它的时间线里）', 'success');
      return true;
    } catch (error) {
      reportError(error, '子智能体');
      return false;
    }
  },

  /** 中断当前回合：进程不退出、队列与后续消息都保留，所以这是"可逆"的操作 */
  interrupt: async id => {
    try {
      const result = await piBridge.interruptAgent(id);
      if (!result.success) {
        reportError(result.error || '中断失败', '子智能体');
        return false;
      }
      useUI.getState().showNotice('已请求中断当前回合：它的队列与后续消息都保留');
      return true;
    } catch (error) {
      reportError(error, '子智能体');
      return false;
    }
  },

  /**
   * 结束并释放进程（标签栏运行中的 ✕ 与会话页空闲时的「结束并关闭」都走它）。
   *
   * 为什么收进 store：两个入口是同一件事，各写一遍 catch 与提示迟早会不一致。
   * 失败仍然是胶囊提示而不是错误条——与它原来在标签栏里的反馈保持一致
   * （按一下按钮没成功，不值得占错误条的位置）。
   * 成功也提示一句：记录不会消失，只是变成"已停止"，没有一句话用户会以为没按上。
   */
  kill: async id => {
    try {
      const result = await piBridge.killAgent(id);
      if (!result.success) {
        useUI.getState().showNotice(result.error || '停止失败', 'warn');
        return false;
      }
      useUI.getState().showNotice('已结束这个子智能体：进程已释放，之后不能再发消息', 'warn');
      return true;
    } catch (error) {
      useUI.getState().showNotice(String((error as Error)?.message ?? error), 'warn');
      return false;
    }
  },

  /**
   * 换会话/切工作区时清空：登记表是"这一次运行"的东西。
   *
   * 每个子智能体的时间线也一起收掉：标签都没了，那些 store 再也点不到，
   * 留着就是"派过的子智能体越多、内存越大"的慢泄漏（另有一道按数量的上限
   * 兜底，见 stores/timeline.ts 的 MAX_AGENT_TIMELINES）。
   */
  clear: () => {
    for (const id of Object.keys(get().agents)) dropAgentTimeline(id);
    set({ agents: {}, order: [], activeId: '' });
  },
}));

/* ============================================================
   派生：把"活动态 + 结果"算成一个界面可以直接用的三元组
   ------------------------------------------------------------
   状态点之所以不能直接用 agent.status，是因为它把两件事混在了一个字段里：
   现在有没有回合在跑（活动态）与最近一轮成没成（结果）。idle 就是那个交叉点
   ——同样显示 idle，可能是"刚正常跑完一轮"（该给绿点），也可能是"被中断了"
   （该给灰点）。所以统一在这里算一次，别让每个组件各猜一遍。
   ============================================================ */

export type AgentVisual = 'running' | 'done' | 'idle' | 'completed' | 'failed' | 'cancelled' | 'killed';

/** 终态：进程已经没了，不能再发消息 */
const TERMINAL_VISUAL: Record<string, AgentVisual> = {
  completed: 'completed',
  failed: 'failed',
  cancelled: 'cancelled',
  killed: 'killed',
};

/**
 * 纯函数：活动态 + 结果 → 界面上的一个状态。
 *
 *   running/starting → running（还在干活）
 *   idle + lastTurnCompleted → done（绿点：最近一轮正常跑完，但**它还活着**）
 *   idle 其它情况 → idle（灰点：空着，可以继续说话）
 *   四个终态 → 各自对应
 *
 * 未知状态落到 idle（灰点）而不是 running：显示成"在跑"会让人一直等一个
 * 根本没在跑的东西；灰点最坏也只是少说了半句话。
 */
export function agentVisual(agent: AgentRecord): AgentVisual {
  if (agent.status === 'running' || agent.status === 'starting') return 'running';
  const terminal = TERMINAL_VISUAL[agent.status];
  if (terminal) return terminal;
  if (agent.status === 'idle' && agent.lastTurnCompleted) return 'done';
  return 'idle';
}

/** 终态判定（进程已经没了）：文案与按钮都靠它 */
export function agentFinished(agent: AgentRecord): boolean {
  return Boolean(TERMINAL_VISUAL[agent.status]);
}

/** 可继续形态：只有它会接受后续消息 */
export function agentContinuable(agent: AgentRecord): boolean {
  return agent.mode === 'continuable';
}

/**
 * 此刻能不能给它发消息：可继续形态 + 已在跑或空着。
 *
 * 为什么把 starting 排除在外：它这一轮还没跑起来，投递方式（排队/插入）此刻
 * 没有意义，发出去只会得到一个含糊的结果——不如让输入框晚一秒出现。
 */
export function agentCanCompose(agent: AgentRecord): boolean {
  return agentContinuable(agent) && (agent.status === 'running' || agent.status === 'idle');
}

/** 显示名：短名优先，没有就用 id（"没有就用 id"这条只写一次，各处都从这里取） */
export function agentDisplayName(agent: AgentRecord): string {
  return agent.name?.trim() || agent.id;
}

/**
 * 耗时文本。
 *
 * 结束的按 endedAt 算、空闲的按 idleSince 冻住，其余按此刻算。
 * 为什么空闲必须冻住：可继续子智能体跑完一轮会一直挂着等下一句，
 * 不冻的话标签上的耗时会无限增长，看上去像"它还在干活"。
 */
export function agentElapsedText(agent: AgentRecord): string {
  const start = Date.parse(agent.startedAt);
  if (Number.isNaN(start)) return '';
  const frozen = agent.endedAt || (agent.status === 'idle' ? agent.idleSince : '') || '';
  const end = frozen ? Date.parse(frozen) : Date.now();
  if (Number.isNaN(end) || end <= start) return '';
  const seconds = (end - start) / 1000;
  return seconds < 60 ? `${seconds.toFixed(1)}s` : `${Math.round(seconds / 60)}m`;
}

/** 正在跑的子智能体数量（标签栏角标用）。判定与状态点同源，避免两处口径不一致 */
export function runningAgentCount(state: AgentsState): number {
  return state.order.filter(id => {
    const agent = state.agents[id];
    return agent ? agentVisual(agent) === 'running' : false;
  }).length;
}
