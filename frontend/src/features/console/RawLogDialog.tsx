import { useEffect, useRef, useState } from 'react';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import { useTraces, type TraceFilter } from '@/stores/traces';
import { useUI } from '@/stores/ui';

const FILTERS: { value: TraceFilter; label: string }[] = [
  { value: 'all', label: '全部方向' },
  { value: 'stdin', label: 'stdin → Pi' },
  { value: 'stdout', label: 'stdout ← Pi' },
  { value: 'stderr', label: 'stderr' },
  { value: 'system', label: '进程' },
];

const DIR_LABEL: Record<string, string> = {
  stdin: '→ stdin',
  stdout: '← stdout',
  stderr: 'stderr',
  system: 'system',
};

/**
 * 原始事件日志（调试/学习用，从顶栏「更多」打开）。
 * 主界面只保留对话，不占用标签页；但排查问题时能一眼看到线路上到底发了什么。
 */
export function RawLogDialog() {
  const open = useUI(s => s.rawLogOpen);
  const traces = useTraces(s => s.traces);
  const filter = useTraces(s => s.filter);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (open) bottomRef.current?.scrollIntoView({ block: 'end' });
  }, [traces, open]);

  const visible = traces.filter(t => (filter === 'all' ? true : t.direction === filter));

  return (
    <Dialog open={open} onOpenChange={value => useUI.getState().setRawLogOpen(value)}>
      <DialogContent className="w-[min(880px,94vw)]">
        <DialogHeader>
          <DialogTitle>原始事件日志</DialogTitle>
          <span className="flex-1" />
          <select
            aria-label="筛选方向"
            value={filter}
            onChange={e => useTraces.getState().setFilter(e.target.value as TraceFilter)}
            className="h-7 rounded-lg bg-panel2 px-2 text-[12px] text-dim outline-none"
          >
            {FILTERS.map(f => <option key={f.value} value={f.value}>{f.label}</option>)}
          </select>
          <Button size="sm" variant="ghost" onClick={() => useTraces.getState().clear()}>清空</Button>
        </DialogHeader>

        <div className="max-h-[64vh] overflow-y-auto px-3.5 py-2">
          {visible.length === 0 ? (
            <p className="py-3 text-[12px] text-mute">
              连接后这里会出现真实的 JSONL：stdin 是请求，stdout 是响应与事件，stderr 是诊断。
            </p>
          ) : (
            visible.map((trace, index) => (
              <div key={`${trace.time}-${index}`} className="flex gap-3soft py-[3px] last:border-b-0">
                <span
                  className={cn(
                    'w-[74px] shrink-0 font-mono text-[11.5px]',
                    trace.direction === 'stdin' && 'text-info',
                    trace.direction === 'stdout' && 'text-success',
                    trace.direction === 'stderr' && 'text-danger',
                    trace.direction === 'system' && 'text-warn',
                  )}
                >
                  {DIR_LABEL[trace.direction] ?? trace.direction}
                </span>
                <pre className="min-w-0 flex-1 font-mono text-[11.5px] leading-[18px] break-words whitespace-pre-wrap text-dim">
                  {trace.raw}
                </pre>
              </div>
            ))
          )}
          <div ref={bottomRef} />
        </div>
      </DialogContent>
    </Dialog>
  );
}
