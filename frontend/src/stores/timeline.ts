import { create } from 'zustand';
import { messageText, type ContentBlock, type PiMessage } from '@/rpc/types';

/* ============================================================
   时间线的状态模型
   ------------------------------------------------------------
   一条「消息」不是一个字符串，而是一串 block：
     thinking → text → tool → text …（按事件到达顺序）
   这样"思考 → 读文件 → 回答"就能像真实 Agent 那样排布，
   而不是把思考、工具、正文拆成几十条互不相干的聊天气泡。
   ============================================================ */

export type Block =
  | { kind: 'thinking'; text: string; done: boolean }
  | { kind: 'text'; text: string; done: boolean }
  | { kind: 'tool'; toolCallId: string };

export type SystemSubtype = 'compaction' | 'retry' | 'error' | 'info';

export type TimelineItem =
  | { id: number; kind: 'user'; text: string; time: string; attachments?: string[] }
  | { id: number; kind: 'assistant'; blocks: Block[]; status: 'streaming' | 'done' | 'error'; error?: string; time: string }
  | { id: number; kind: 'system'; subtype: SystemSubtype; title: string; text: string; time: string }
  | { id: number; kind: 'extension'; requestId: string; method: string; title: string; message: string; status: 'pending' | 'allowed' | 'denied'; agentId?: string; time: string }
  | { id: number; kind: 'settled'; text: string; time: string };

export interface ToolExecution {
  id: string;
  name: string;
  args: string;
  /** aborted = 被「停止」中断，既不算成功也不算失败，界面上按中性显示 */
  status: 'running' | 'success' | 'error' | 'aborted';
  startedAt: number;
  finishedAt?: number;
  updates: number;
  result: string;
  isError: boolean;
}

interface TimelineStore {
  items: TimelineItem[];
  tools: Record<string, ToolExecution>;
  currentAssistantId: number | null;

  addUser(text: string, attachments?: string[]): void;
  beginAssistant(): void;
  /** 强制收尾流式内容与运行中的工具（进程退出/中止/切换会话时用） */
  finishStreaming(reason?: string): void;
  appendThinking(delta: string): void;
  endThinking(): void;
  beginText(): void;
  appendText(delta: string): void;
  endText(content?: string): void;
  finalizeAssistant(message: PiMessage | undefined, errorMessage?: string): void;
  startTool(id: string, name: string, args: unknown): void;
  touchTool(id: string): void;
  /** aborted=true 表示这次工具执行是被「停止」中断的（中性状态，不是失败） */
  endTool(id: string, result: unknown, isError: boolean, aborted?: boolean): void;
  addSystem(subtype: SystemSubtype, title: string, text: string): void;
  addExtension(requestId: string, method: string, title: string, message: string, agentId?: string): void;
  resolveExtension(requestId: string, status: 'allowed' | 'denied'): void;
  addSettled(text: string): void;
  reset(): void;
}

let seq = 0;
const nextId = () => ++seq;
const clock = () => new Date().toLocaleTimeString('zh-CN', { hour12: false });

/** 把任意值压成卡片里显示的单行摘要 */
function brief(value: unknown, limit = 160): string {
  if (value === undefined || value === null) return '';
  const text = typeof value === 'string' ? value : JSON.stringify(value) ?? '';
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

/** 工具结果可能是 {content:[{text}]} 或纯字符串，统一提取文本 */
function resultText(result: unknown): string {
  if (result === undefined || result === null) return '';
  if (typeof result === 'string') return result;
  const maybe = result as { content?: { text?: string }[]; text?: string; output?: string };
  if (typeof maybe.text === 'string') return maybe.text;
  if (typeof maybe.output === 'string') return maybe.output;
  if (Array.isArray(maybe.content)) return maybe.content.map(c => c?.text ?? '').join('\n');
  return brief(result, 4000);
}

/**
 * 建一个时间线 store。
 *
 * 做成工厂是为了**子智能体复用同一套解析与渲染**：主会话与每个子智能体
 * 各有一份时间线，但它们用的是同一段代码（同一种块合并、同一套组件）。
 * 之前子智能体自己攒"每个 delta 一条记录"，结果文字是一个字一个字蹦出来的
 * ——那是没复用主会话解析的直接后果。
 */
export function createTimelineStore() {
  return create<TimelineStore>((set, get) => {
  /** 找到当前正在流式输出的助手消息（没有就新建一条） */
  const withAssistant = (fn: (item: Extract<TimelineItem, { kind: 'assistant' }>, items: TimelineItem[]) => TimelineItem[]) =>
    set(s => {
      const items = [...s.items];
      let index = -1;
      for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        if (item.kind === 'assistant' && item.id === s.currentAssistantId) { index = i; break; }
      }
      if (index < 0) {
        const created: TimelineItem = { id: nextId(), kind: 'assistant', blocks: [], status: 'streaming', time: clock() };
        items.push(created);
        return { items: fn(created as Extract<TimelineItem, { kind: 'assistant' }>, items), currentAssistantId: created.id };
      }
      const item = items[index] as Extract<TimelineItem, { kind: 'assistant' }>;
      return { items: fn(item, items) };
    });

  /** 往当前助手消息尾部追加块：同类型且未结束就续写，否则新起一块 */
  const pushBlock = (block: Block) => withAssistant((item, items) => {
    const blocks = [...item.blocks];
    const last = blocks[blocks.length - 1];
    if (block.kind === 'text' && last?.kind === 'text' && !last.done) {
      blocks[blocks.length - 1] = { ...last, text: last.text + block.text };
    } else if (block.kind === 'thinking' && last?.kind === 'thinking' && !last.done) {
      blocks[blocks.length - 1] = { ...last, text: last.text + block.text };
    } else if (block.kind === 'tool' && last?.kind === 'tool' && last.toolCallId === block.toolCallId) {
      // 同一个工具连续更新：不重复插入块
    } else {
      blocks.push(block);
    }
    items[items.indexOf(item)] = { ...item, blocks, status: 'streaming' };
    return items;
  });

  return {
    items: [],
    tools: {},
    currentAssistantId: null,

    addUser: (text, attachments) => set(s => ({
      items: [...s.items, { id: nextId(), kind: 'user', text, time: clock(), attachments }],
      currentAssistantId: null,
    })),

    beginAssistant: () => set(s => {
      const created: TimelineItem = { id: nextId(), kind: 'assistant', blocks: [], status: 'streaming', time: clock() };
      return { items: [...s.items, created], currentAssistantId: created.id };
    }),

    /**
     * 强制收尾所有"还在流式"的东西。
     *
     * 用途：进程退出、abort、切换会话这类"不会再有 message_end / agent_settled"的场景。
     * 不做这件事的后果很直观：光标一直在闪、工具卡片一直显示"运行中"。
     */
    finishStreaming: (reason?: string) => set(s => ({
      items: s.items.map(item => {
        if (item.kind === 'assistant' && item.status === 'streaming') {
          return { ...item, status: 'done' as const, blocks: item.blocks.map(b => ({ ...b, done: true })) };
        }
        return item;
      }),
      tools: Object.fromEntries(Object.entries(s.tools).map(([id, tool]) => [
        id,
        tool.status === 'running'
          ? { ...tool, status: 'error' as const, isError: true, finishedAt: Date.now(), result: tool.result || (reason ?? '进程已退出，工具调用未完成') }
          : tool,
      ])),
      currentAssistantId: null,
    })),

    appendThinking: delta => pushBlock({ kind: 'thinking', text: delta, done: false }),
    endThinking: () => withAssistant((item, items) => {
      const blocks = item.blocks.map(b => (b.kind === 'thinking' && !b.done ? { ...b, done: true } : b));
      items[items.indexOf(item)] = { ...item, blocks };
      return items;
    }),

    beginText: () => pushBlock({ kind: 'text', text: '', done: false }),
    appendText: delta => pushBlock({ kind: 'text', text: delta, done: false }),
    endText: content => withAssistant((item, items) => {
      const blocks: Block[] = item.blocks.map(b => (b.kind === 'text' && !b.done ? { ...b, done: true, text: content ?? b.text } : b));
      items[items.indexOf(item)] = { ...item, blocks };
      return items;
    }),

    /**
     * message_end 是权威结果：用它重建块顺序（Pi 的 content 数组本身就带顺序），
     * 流式期间拼出来的临时文本到这里被纠正。
     */
    finalizeAssistant: (message, errorMessage) => withAssistant((item, items) => {
      const content = Array.isArray(message?.content) ? (message.content as ContentBlock[]) : null;
      let blocks: Block[];
      if (content && content.length) {
        blocks = content.flatMap<Block>(b => {
          if (b.type === 'text') return [{ kind: 'text' as const, text: b.text ?? '', done: true }];
          if (b.type === 'thinking') return [{ kind: 'thinking' as const, text: b.text ?? '', done: true }];
          if (b.type === 'toolCall') {
            const id = (b as { id?: string; toolCallId?: string }).id ?? (b as { toolCallId?: string }).toolCallId ?? '';
            return id ? [{ kind: 'tool' as const, toolCallId: id }] : [];
          }
          return [];
        });
      } else {
        const text = messageText(message);
        const kept = item.blocks.map(b => (b.kind === 'text' ? { ...b, done: true } : b));
        const hasText = kept.some(b => b.kind === 'text');
        blocks = text && !hasText ? [...kept, { kind: 'text', text, done: true }] : kept;
      }
      items[items.indexOf(item)] = {
        ...item,
        blocks: blocks.length ? blocks : item.blocks,
        status: errorMessage ? 'error' : 'done',
        error: errorMessage,
      };
      return items;
    }),

    startTool: (id, name, args) => {
      set(s => ({ tools: { ...s.tools, [id]: { id, name, args: brief(args), status: 'running', startedAt: Date.now(), updates: 0, result: '', isError: false } } }));
      pushBlock({ kind: 'tool', toolCallId: id });
    },

    touchTool: id => set(s => {
      const tool = s.tools[id];
      if (!tool) return s; // 迟到的 update：忽略，不新建
      return { tools: { ...s.tools, [id]: { ...tool, updates: tool.updates + 1 } } };
    }),

    endTool: (id, result, isError, aborted = false) => set(s => {
      const tool = s.tools[id];
      if (!tool) return s;
      return {
        tools: {
          ...s.tools,
          [id]: {
            ...tool,
            status: aborted ? 'aborted' : isError ? 'error' : 'success',
            // 被中止的工具不算失败：报错行、红色图标都不该出现
            isError: aborted ? false : isError,
            result: aborted ? '已中止（这一回合被停止）' : resultText(result),
            finishedAt: Date.now(),
          },
        },
      };
    }),

    addSystem: (subtype, title, text) => set(s => ({
      items: [...s.items, { id: nextId(), kind: 'system', subtype, title, text, time: clock() }],
    })),

    addExtension: (requestId, method, title, message, agentId) => set(s => ({
      items: [...s.items, { id: nextId(), kind: 'extension', requestId, method, title, message, status: 'pending', agentId, time: clock() }],
    })),

    resolveExtension: (requestId, status) => set(s => ({
      items: s.items.map(item =>
        item.kind === 'extension' && item.requestId === requestId ? { ...item, status } : item),
    })),

    addSettled: text => set(s => ({ items: [...s.items, { id: nextId(), kind: 'settled', text, time: clock() }] })),

    reset: () => set({ items: [], tools: {}, currentAssistantId: null }),
  };
  });
}

/** 时间线的状态类型（router 里按 agentId 取状态时用得到） */
export type TimelineState = ReturnType<ReturnType<typeof createTimelineStore>['getState']>;

/** 主会话的时间线（保持原来的引用方式不变） */
export const useTimeline = createTimelineStore();

/**
 * 按 agentId 取时间线：'' = 主会话，其他 = 对应的子智能体。
 *
 * 懒创建：没派过子智能体就不会多出任何 store。
 */
const agentTimelines = new Map<string, ReturnType<typeof createTimelineStore>>();

/**
 * 子智能体时间线的上限。
 *
 * 每个子智能体一份 store（懒创建），而一次运行里派出去的子智能体只会越来越多——
 * 不设上限就是"派过的每一个子智能体都在内存里留一条完整时间线"（长时间运行的
 * 慢泄漏）。超过上限时回收**最老**那份：它通常是早就跑完、很久没点开过的；
 * 真再点到它，懒创建会造一份新的空时间线（反正内容来自事件流，没有本地持久化）。
 * 登记表整体清空（agents.clear）时另有全量回收，见 stores/agents.ts。
 */
const MAX_AGENT_TIMELINES = 16;

export function timelineFor(agentId?: string): ReturnType<typeof createTimelineStore> {
  if (!agentId) return useTimeline;
  let store = agentTimelines.get(agentId);
  if (!store) {
    while (agentTimelines.size >= MAX_AGENT_TIMELINES) {
      const oldest = agentTimelines.keys().next().value;
      if (typeof oldest !== 'string') break;
      agentTimelines.delete(oldest);
    }
    store = createTimelineStore();
    agentTimelines.set(agentId, store);
  }
  return store;
}

/** 清掉某个子智能体的时间线（子智能体被移除时用）。 */
export function dropAgentTimeline(agentId: string): void {
  agentTimelines.delete(agentId);
}

export const selectTool = (id: string) => (s: TimelineStore) => s.tools[id];
