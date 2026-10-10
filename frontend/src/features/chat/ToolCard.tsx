import { useState } from 'react';
import { Icon } from '@/components/ui/Icon';
import { RichText } from '@/components/ui/RichText';
import { attachmentIcon, attachmentName } from '@/lib/format';
import { Row, Terminal } from '@/components/ui/Row';
import { cn } from '@/lib/cn';
import { duration } from '@/lib/format';
import { selectTool, useTimeline } from '@/stores/timeline';

/**
 * 终端输出的中部折叠（照 DSH 的 head-tail-cap）：头 8 行 + 尾 8 行，中间省略。
 *
 * 为什么不按字符截断：只留开头会丢掉最有用的结尾（编译错误、"测试失败"往往在最后）。
 * 头尾都留，中间折叠，才既省空间又看得见结论。
 */
const HEAD_LINES = 8;
const TAIL_LINES = 8;
/** 极端兜底：再长也不整份塞进 DOM（完整内容在「原始事件日志」里能看） */
const HARD_LIMIT = 200_000;

function capOutput(text: string): string {
  const bounded = text.length > HARD_LIMIT ? text.slice(0, HARD_LIMIT) : text;
  const lines = bounded.split('\n');
  if (lines.length <= HEAD_LINES + TAIL_LINES) return bounded;
  const hidden = lines.length - HEAD_LINES - TAIL_LINES;
  return [
    ...lines.slice(0, HEAD_LINES),
    `… 中间省略 ${hidden} 行（完整输出见顶栏「更多 → 原始事件日志」）…`,
    ...lines.slice(-TAIL_LINES),
  ].join('\n');
}

/** 工具摘要：DSH 的 ToolRow 用「工具名 · 参数摘要」一行；这里跟随同样的信息层级 */
function toolGlyph(name: string): 'terminal' | 'tool' {
  return /bash|shell|term|exec/i.test(name) ? 'terminal' : 'tool';
}

/**
 * 参数摘要：事件里 args 是原始 JSON（`{"command":"go test ./..."}`），
 * 直接塞进 24px 的行里又长又难读，所以按常见的几个键提取成一句人话；
 * 提取不到就退回紧凑 JSON（去掉引号与花括号）。
 */
function summarizeArgs(raw: string): string {
  if (!raw) return '';
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    for (const key of ['command', 'cmd', 'path', 'file_path', 'filePath', 'url', 'query', 'pattern']) {
      const value = parsed[key];
      if (typeof value === 'string' && value.trim()) return value;
    }
    const first = Object.values(parsed).find(value => typeof value === 'string' && value.trim());
    if (typeof first === 'string') return first;
  } catch { /* 不是 JSON 就用原文 */ }
  return raw.replace(/^\{|\}$/g, '').replace(/"/g, '');
}

/**
 * 工具卡片默认读**主会话**的时间线。
 *
 * store 可注入是因为子智能体有自己的时间线：不给它传 store 的话，
 * 子智能体里的工具卡片会去主会话的 tools 表里查自己的 toolCallId，
 * 查不到就渲染成"没有对应记录"（这个 bug 真的出现过）。
 */
export type TimelineHook = typeof useTimeline;

export function ToolCard({ toolCallId, store = useTimeline }: { toolCallId: string; store?: TimelineHook }) {
  const tool = store(selectTool(toolCallId));
  const [manual, setManual] = useState<boolean | null>(null);

  if (!tool) {
    return <Row icon="tool" title="工具调用" summary={`${toolCallId || '(缺少 id)'} 没有对应记录`} tone="warn" />;
  }

  // 运行中默认展开，结束后自动收起；用户手动点过就听用户的
  const expanded = manual ?? tool.status === 'running';
  const output = capOutput(tool.result);
  // 三种终态各有标记：成功=ok、失败=err、被停止=run（中性，不染红）
  const mark = tool.status === 'running' ? 'run' : tool.status === 'error' ? 'err' : tool.status === 'aborted' ? 'run' : 'ok';
  const statusText =
    tool.status === 'running' ? '运行中'
      : tool.status === 'error' ? '失败'
        : tool.status === 'aborted' ? '已中止'
          : '成功';

  return (
    <Row
      icon={toolGlyph(tool.name)}
      title={tool.name}
      summary={summarizeArgs(tool.args) || '—'}
      meta={duration(tool.startedAt, tool.finishedAt)}
      tone={tool.status === 'error' ? 'err' : 'default'}
      expandable
      expanded={expanded}
      onToggle={() => setManual(!expanded)}
    >
      <div className="ml-[22px]">
        <Terminal
          command={summarizeArgs(tool.args) || tool.name}
          status={statusText}
          mark={mark}
          output={output || undefined}
          meta={
            <>
              toolCallId: {tool.id} · 更新 {tool.updates} 次{tool.isError ? ' · isError: true' : ''}
              {tool.result.length > output.length ? ` · 原始 ${tool.result.length.toLocaleString()} 字符` : ''}
            </>
          }
        />
      </div>
    </Row>
  );
}

/** 复制按钮：DSH 里消息下方有一排小动作，这里只做最有用的"复制" */
function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      aria-label={done ? '已复制' : '复制'}
      title={done ? '已复制' : '复制'}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // WebView2 在没有用户手势时可能拒绝写剪贴板；失败就什么都不做，
          // 不弹错误（复制失败不值得打断用户）
        }
        setDone(true);
        setTimeout(() => setDone(false), 1000);
      }}
      className="text-mute opacity-0 transition-opacity group-hover:opacity-100 hover:text-ink"
    >
      <Icon name={done ? 'check' : 'copy'} className="h-3.5 w-3.5" />
    </button>
  );
}

/** 助手正文：Markdown 渲染（DSH 里就是纯段落，没有气泡） */
export function AssistantText({ text, streaming }: { text: string; streaming: boolean }) {
  if (!text) return null;
  return (
    <div className="group flex flex-col gap-0.5">
      <RichText text={text} streaming={streaming} />
      {streaming && <span className="caret" />}
      {!streaming && (
        <div className="flex h-4 items-center">
          <CopyButton text={text} />
        </div>
      )}
    </div>
  );
}

/** 用户消息：DSH 用一块浅色气泡承载，靠右；带附件时在气泡上方列出来 */
export function UserBubble({ text, attachments, onFork }: {
  text: string;
  attachments?: string[];
  /** 有 entryId 时给出「分支」：从这条消息开一条新分支（Pi 的 fork） */
  onFork?: () => void;
}) {
  return (
    <div className={cn('group flex max-w-[min(560px,88%)] flex-col items-end gap-0.5 self-end')}>
      {attachments && attachments.length > 0 && (
        <div className="flex flex-wrap justify-end gap-1.5">
          {attachments.map(path => (
            <span key={path} className="inline-flex items-center gap-1.5 rounded-lg bg-panel3 px-2 py-1 text-[11.5px] text-dim">
              <Icon name={attachmentIcon(path)} className="h-3.5 w-3.5 text-mute" />
              <span className="max-w-[200px] truncate" title={path}>{attachmentName(path)}</span>
            </span>
          ))}
        </div>
      )}
      <div className="rounded-card bg-panel2 px-3.5 py-2.5">
        <p className="whitespace-pre-wrap text-[length:var(--chat-font-size,15px)] leading-[var(--chat-line-height,23px)]">{text}</p>
      </div>
      <div className="flex h-4 items-center gap-2">
        <CopyButton text={text} />
        {onFork && (
          <button
            type="button"
            onClick={onFork}
            title="从这条消息开一条新分支（原分支保留）"
            className="text-[11px] text-mute opacity-0 transition-opacity group-hover:opacity-100 hover:text-ink"
          >
            分支
          </button>
        )}
      </div>
    </div>
  );
}
