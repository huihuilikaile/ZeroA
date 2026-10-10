import { useState } from 'react';
import { cn } from '@/lib/utils';
import { fmtTokens } from '@/lib/format';
import { useRuntime } from '@/stores/runtime';

/**
 * 上下文用量环 + 分项面板 —— 照 DSH 的 ContextMeter：
 *   · 收起：14px 环（2px 描边）+ 「当前上下文 N%」
 *   · 展开：标题行 + `~已用 / 上限` + 分段条 + 分项图例
 *
 * 分项只留**输入 / 输出**（用户要求）：缓存命中的 token 与花费更适合账单，
 * 不是"这段对话还要占多少上下文"的直观组成；摆在这里反而让人以为
 * 缓存命中也在占上下文。需要花费时看侧栏的会话统计。
 */
const RADIUS = 5.5;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

export function ContextMeter() {
  const [open, setOpen] = useState(false);
  const tokens = useRuntime(s => s.tokens);
  const contextWindow = useRuntime(s => s.contextWindow);
  const contextTokens = useRuntime(s => s.contextTokens);
  const contextPercent = useRuntime(s => s.contextPercent);

  // 没有上限就先不显示（DSH 同样在缺少 route capacity 时渲染为空）
  if (!contextWindow) return null;

  /**
   * 用 Pi 的 contextUsage 而不是 tokens.total。
   *
   * tokens.total 是**整个会话的累计用量**：聊上几十轮之后它可能是 500k，
   * 而真实上下文占用只有 60k——拿它画"上下文已用"，进度条会永远顶格。
   * Pi 自己判断"该不该压缩"用的是 contextUsage，那才是这个环该显示的数。
   * 拿不到（例如压缩刚结束）时退回累计值，至少不是 0。
   */
  const used = contextTokens || tokens.total;
  /**
   * Pi 给的 percent 是**原始浮点**（实测 0.6969000000000001），直接渲染会出现一长串小数。
   * 统一取整；小于 1% 但非零时显示 1%——免得"已经用了 900 tokens"被显示成 0%，
   * 那正是用户报的"重新打开显示 0"的观感。
   */
  const percent = contextPercent
    ? Math.max(1, Math.round(contextPercent))
    : Math.min(100, Math.round((used / contextWindow) * 100));
  const parts = [
    { key: 'input', label: '输入', value: tokens.input, cls: 'bg-info' },
    { key: 'output', label: '输出', value: tokens.output, cls: 'bg-success' },
  ];
  const total = parts.reduce((sum, part) => sum + part.value, 0) || 1;

  return (
    <span className="relative inline-flex">
      <button
        type="button"
        aria-label={`上下文已用 ${percent}%`}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className={cn(
          'inline-flex h-7 items-center gap-1.5 rounded-lg px-2 text-[12px] text-mute transition-colors hover:bg-panel3 hover:text-dim',
          open && 'bg-panel3 text-dim',
        )}
      >
        <svg viewBox="0 0 14 14" width="14" height="14" aria-hidden>
          <circle cx="7" cy="7" r={RADIUS} fill="none" stroke="var(--line)" strokeWidth="2" />
          <circle
            cx="7" cy="7" r={RADIUS} fill="none"
            stroke="var(--info)" strokeWidth="2" strokeLinecap="round"
            strokeDasharray={`${(CIRCUMFERENCE * percent) / 100} ${CIRCUMFERENCE}`}
            transform="rotate(-90 7 7)"
          />
        </svg>
        {percent}%
      </button>

      {open && (
        <div
          role="dialog"
          aria-label="上下文用量"
          className="absolute bottom-9 left-0 z-40 w-[292px] rounded-card border border-line bg-raised p-3 shadow-[var(--shadow)]"
        >
          {/*
            上面那行是**当前上下文占用**（ZeroA 的 contextUsage，决定还能聊多久），
            下面分项是**本次会话累计用量**——两个口径，不能混着看。
          */}
          <div className="flex items-baseline gap-1.5">
            <span className="text-[13px] text-dim">当前上下文</span>
            <span className="text-[15px] font-medium text-ink">{percent}%</span>
            <span className="ml-auto font-mono text-[12px] text-mute">
              ~{fmtTokens(used)} / {fmtTokens(contextWindow)}
            </span>
          </div>

          <div className="mt-2.5 flex h-1.5 w-full overflow-hidden rounded-full bg-panel3">
            {parts.map(part => (
              <div key={part.key} className={cn('h-full', part.cls)} style={{ width: `${(part.value / total) * 100}%` }} />
            ))}
          </div>

          <div className="mt-2.5 mb-1 text-[11.5px] text-caption">本次会话累计</div>

          <dl className="flex flex-col gap-1">
            {parts.map(part => (
              <div key={part.key} className="flex items-center justify-between text-[12px]">
                <dt className="flex items-center gap-2 text-mute">
                  <span className={cn('h-2 w-2 rounded-full', part.cls)} aria-hidden />
                  {part.label}
                </dt>
                <dd className="font-mono text-dim">~{fmtTokens(part.value)}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </span>
  );
}
