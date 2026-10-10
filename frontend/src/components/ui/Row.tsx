import type { ReactNode } from 'react';
import { Icon, type IconName } from '@/components/ui/Icon';
import { cn } from '@/lib/cn';

/**
 * DisclosureRow —— 直接照 DeepSeek Harness 的一行式事件行实现。
 *
 * 结构与尺寸跟 DSH 的 DisclosureRow.module.css / ToolRow.module.css 对齐：
 *   [16px 图标] 6 [13px/24px 标题] 8 [2×2 点] 8 [摘要，超长省略]
 * 平时是 tertiary 灰，hover 或展开时升到 secondary；chevron 只在 hover/展开时淡入。
 * 整个应用里的「工具 / 思考 / 压缩 / 重试 / 扩展请求」都用它，视觉自然统一。
 */
export function Row({
  icon,
  title,
  summary,
  meta,
  tone = 'default',
  expandable = false,
  expanded = false,
  onToggle,
  children,
}: {
  icon: IconName;
  title: string;
  summary?: ReactNode;
  meta?: ReactNode;
  tone?: 'default' | 'err' | 'warn';
  expandable?: boolean;
  expanded?: boolean;
  onToggle?: () => void;
  children?: ReactNode;
}) {
  const head = (
    <>
      <span className="drow-glyph">
        <Icon name={icon} />
      </span>
      <span className="drow-title">{title}</span>
      {summary !== undefined && summary !== '' && (
        <>
          <span className="drow-dot" />
          <span className="drow-summary">{summary}</span>
        </>
      )}
      {meta !== undefined && <span className="drow-meta ml-2">{meta}</span>}
      {expandable && <Icon name="chevron" className={cn('drow-chev ml-2 h-4 w-4', expanded && 'rotate-180')} />}
    </>
  );

  return (
    <div className="flex flex-col">
      {expandable ? (
        <button
          type="button"
          className={cn('drow', tone === 'err' && 'drow-err', tone === 'warn' && 'drow-warn')}
          aria-expanded={expanded}
          onClick={onToggle}
        >
          {head}
        </button>
      ) : (
        <div className={cn('drow', tone === 'err' && 'drow-err', tone === 'warn' && 'drow-warn')}>{head}</div>
      )}
      {expanded && children}
    </div>
  );
}

/**
 * TerminalBlock —— DSH 的命令/输出卡片。
 * 左留白 30px 专门放运行状态点，头部横幅与输出之间用 0.5px 发丝线分隔。
 */
export function Terminal({
  command,
  status,
  output,
  meta,
  mark = 'ok',
}: {
  command: string;
  status?: string;
  output?: string;
  meta?: ReactNode;
  mark?: 'run' | 'ok' | 'err';
}) {
  return (
    <div className="term rise my-1">
      <span className={cn('term-mark', mark === 'run' && 'bg-info', mark === 'ok' && 'bg-success', mark === 'err' && 'bg-danger')} />
      <div className="term-head" data-body={output ? 'true' : 'false'}>
        <span className="min-w-0 flex-1 truncate">{command}</span>
        {status && (
          <span className={cn('flex-none', mark === 'err' ? 'text-danger' : mark === 'ok' ? 'text-success' : 'text-info')}>{status}</span>
        )}
      </div>
      {output && <div className="term-out max-h-64 overflow-auto">{output}</div>}
      {meta && <div className="px-3.5 pb-2.5 pl-[30px] font-mono text-[11px] text-mute">{meta}</div>}
    </div>
  );
}
