import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

/* 交互组件统一由 shadcn/ui（Radix）提供，这里只做转出，
   让老的 import 路径继续可用；纯展示组件保留在本文件。 */
export { Button } from '@/components/ui/button';
export { Badge } from '@/components/ui/badge';
export { Switch } from '@/components/ui/switch';

/** 面板容器：检查器里的分组 */
export function Panel({ className, children }: { className?: string; children: ReactNode }) {
  return <section className={cn('pb-3 last:border-b-0', className)}>{children}</section>;
}

export function PanelTitle({ children }: { children: ReactNode }) {
  return <h3 className="mb-2 text-[12px] font-normal text-mute">{children}</h3>;
}

/** 键值行：左边次级文字、右边主文字（DSH 的信息行风格） */
export function KV({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 py-[2px] text-[13px]">
      <span className="shrink-0 text-mute">{label}</span>
      <span className="min-w-0 truncate text-right text-ink">{children}</span>
    </div>
  );
}

/** 细进度条：纯展示，不需要 Radix */
export function Progress({ value, max, className }: { value: number; max: number; className?: string }) {
  const pct = max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0;
  return (
    <div className={cn('h-1.5 w-full overflow-hidden rounded-full bg-panel3', className)}>
      <div className="h-full rounded-full bg-info transition-[width] duration-300" style={{ width: `${pct}%` }} />
    </div>
  );
}
