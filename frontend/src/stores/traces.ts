import { create } from 'zustand';
import type { Trace } from '@/rpc/types';

/** 观察器里一条记录最多保留多少字符（工具输出动辄几十 KB，全量渲染会拖慢界面） */
const DISPLAY_LIMIT = 20000;
const MAX_TRACES = 500;

function forDisplay(trace: Trace): Trace {
  if (trace.raw.length <= DISPLAY_LIMIT) return trace;
  return {
    ...trace,
    raw: `${trace.raw.slice(0, DISPLAY_LIMIT)}\n…（原始 ${trace.raw.length} 字符，界面只保留前 ${DISPLAY_LIMIT}）`,
  };
}

export type TraceFilter = 'all' | 'stdin' | 'stdout' | 'stderr' | 'system';

interface TraceStore {
  traces: Trace[];
  filter: TraceFilter;
  add(trace: Trace): void;
  setFilter(filter: TraceFilter): void;
  clear(): void;
}

export const useTraces = create<TraceStore>(set => ({
  traces: [],
  filter: 'all',
  // 从头部裁剪，所以渲染时用稳定 key（这是把裸 JSONL 放进 React 列表的经典坑）
  add: trace => set(s => ({ traces: [...s.traces.slice(-(MAX_TRACES - 1)), forDisplay(trace)] })),
  setFilter: filter => set({ filter }),
  clear: () => set({ traces: [] }),
}));
