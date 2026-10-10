import { memo, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Browser } from '@wailsio/runtime';
import { Icon } from '@/components/ui/Icon';
import { cn } from '@/lib/utils';

/**
 * 对话正文的 Markdown 渲染。
 *
 * 三个决定，都是踩过坑之后的选择：
 *
 * 1. **用 react-markdown + remark-gfm，不自己写解析器**。
 *    自己写行内代码还行，但表格、嵌套列表、引用、链接这些边界情况会一直出问题；
 *    这一层不值得自研。安全上 react-markdown 默认不渲染原始 HTML，正好合意。
 *
 * 2. **流式期间限流**：解析整段 Markdown 是 O(文本长度)，每个 delta 都重解析
 *    在长回复里会明显掉帧。所以流式时只在文本"跨过整十字符"或收尾时才重渲染
 *    （memo + 量化 key），观感上仍是逐字出现，代价却降下来了。
 *
 * 3. **链接交给系统浏览器**：桌面应用里点链接不该在 WebView 里跳转，
 *    否则整个界面被替换成网页、还回不来。
 */
export const RichText = memo(function RichText({ text, streaming = false }: { text: string; streaming?: boolean }) {
  if (!text) return null;
  // 流式期间按 10 字符量化，避免每个字符都重新解析一遍
  const quantised = streaming ? text.slice(0, Math.floor(text.length / 10) * 10) || text : text;
  return (
    <div className="md-body">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ href, children }) => (
            <a
              href={href}
              title={href}
              onClick={event => {
                event.preventDefault();
                if (href && /^https?:\/\//i.test(href)) void Browser.OpenURL(href);
              }}
            >
              {children}
            </a>
          ),
          pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
        }}
      >
        {quantised}
      </ReactMarkdown>
    </div>
  );
});

/** 代码块：带语言标签与复制按钮（Markdown 里最常用的就是贴命令和代码） */
function CodeBlock({ children }: { children?: React.ReactNode }) {
  const [done, setDone] = useState(false);
  const text = extractText(children);
  const language = extractLanguage(children);

  return (
    <div className="md-code group/code">
      <div className="flex items-center gap-2 border-b-[0.5px] border-line px-3 py-1.5">
        <span className="font-mono text-[11px] text-mute">{language || 'code'}</span>
        <span className="flex-1" />
        <button
          type="button"
          aria-label={done ? '已复制' : '复制代码'}
          title={done ? '已复制' : '复制代码'}
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(text);
            } catch { /* 没有用户手势时可能被拒，静默即可 */ }
            setDone(true);
            setTimeout(() => setDone(false), 1000);
          }}
          className="text-mute opacity-0 transition-opacity group-hover/code:opacity-100 hover:text-ink"
        >
          <Icon name={done ? 'check' : 'copy'} className="h-3.5 w-3.5" />
        </button>
      </div>
      <pre className="overflow-x-auto px-3 py-2.5 font-mono text-[12px] leading-[20px]">{children}</pre>
    </div>
  );
}

/** 从 React 节点里抽出纯文本（复制用） */
function extractText(node: React.ReactNode): string {
  if (node == null || node === false) return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(extractText).join('');
  const element = node as { props?: { children?: React.ReactNode } };
  return extractText(element.props?.children);
}

/** 代码块的语言标签：react-markdown 会给 <code className="language-ts"> */
function extractLanguage(node: React.ReactNode): string {
  const element = (Array.isArray(node) ? node[0] : node) as { props?: { className?: string } } | undefined;
  const className = element?.props?.className ?? '';
  const match = /language-([\w-]+)/.exec(className);
  return match ? match[1] : '';
}

/** 小工具：把行内代码渲染得跟 Markdown 一致（工具行等处复用） */
export function InlineCode({ children, className }: { children: React.ReactNode; className?: string }) {
  return <code className={cn('rounded bg-panel3 px-1 py-[1px] font-mono text-[12px]', className)}>{children}</code>;
}
