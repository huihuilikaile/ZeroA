import { useEffect, useMemo, useRef, useState } from 'react';
import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import c from 'highlight.js/lib/languages/c';
import clojure from 'highlight.js/lib/languages/clojure';
import cmake from 'highlight.js/lib/languages/cmake';
import cpp from 'highlight.js/lib/languages/cpp';
import csharp from 'highlight.js/lib/languages/csharp';
import css from 'highlight.js/lib/languages/css';
import dart from 'highlight.js/lib/languages/dart';
import diff from 'highlight.js/lib/languages/diff';
import dos from 'highlight.js/lib/languages/dos';
import elixir from 'highlight.js/lib/languages/elixir';
import go from 'highlight.js/lib/languages/go';
import graphql from 'highlight.js/lib/languages/graphql';
import haskell from 'highlight.js/lib/languages/haskell';
import ini from 'highlight.js/lib/languages/ini';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import kotlin from 'highlight.js/lib/languages/kotlin';
import less from 'highlight.js/lib/languages/less';
import lua from 'highlight.js/lib/languages/lua';
import makefile from 'highlight.js/lib/languages/makefile';
import markdown from 'highlight.js/lib/languages/markdown';
import nix from 'highlight.js/lib/languages/nix';
import php from 'highlight.js/lib/languages/php';
import plaintext from 'highlight.js/lib/languages/plaintext';
import powershell from 'highlight.js/lib/languages/powershell';
import protobuf from 'highlight.js/lib/languages/protobuf';
import python from 'highlight.js/lib/languages/python';
import ruby from 'highlight.js/lib/languages/ruby';
import rust from 'highlight.js/lib/languages/rust';
import scala from 'highlight.js/lib/languages/scala';
import scss from 'highlight.js/lib/languages/scss';
import sql from 'highlight.js/lib/languages/sql';
import swift from 'highlight.js/lib/languages/swift';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';
import { Icon } from '@/components/ui/Icon';
import { cn } from '@/lib/utils';
import { highlightLanguageForPath, languageForPath } from '@/features/files/preview';

/**
 * 代码预览（照 DSH 的文档预览-代码渲染器）。
 *
 * DSH 那套的可见部分都保留了：语言标签 + 复制按钮的横幅、默认显示行号
 * （行号不进复制内容）、按标签页记住的换行开关、内容用自己的滚动区
 * （横幅固定，滚动条从横幅下面开始）。
 *
 * 引擎差异：DSH 用同步 shiki 内核（createHighlighterCoreSync + JS 正则引擎 +
 * CSS 变量主题），逐行渲染 token 数组。这里用 highlight.js——同样是"一次性注册
 * 语言、同步高亮、token 颜色走 CSS 变量"的路子，但体积小得多、不需要 WASM。
 * 为了让行号在换行模式下也对齐，高亮结果按行切开渲染（见 splitHighlightedLines）。
 */

// 只注册这份语言表里会用到的语法：全量注册会让包大好几倍。
// 必须覆盖 preview.ts 的 LANGUAGES 表里的**每一个**语言 id，否则那些文件
// 只有语言标签、没有颜色，看起来就像"高亮没加载上"。
const LANGUAGES: Record<string, unknown> = {
  bash, c, clojure, cmake, cpp, csharp, css, dart, diff, dos, elixir, go,
  graphql, haskell, ini, java, javascript, json, kotlin, less, lua, makefile,
  markdown, nix, php, plaintext, powershell, protobuf, python, ruby, rust,
  scala, scss, sql, swift, typescript, xml, yaml,
};
for (const [name, language] of Object.entries(LANGUAGES)) {
  hljs.registerLanguage(name, language as never);
}

/** 行号只渲染前这么多行：超大文件一次铺几万个节点会把面板拖死。 */
const MAX_RENDERED_LINES = 5000;

/**
 * 把 highlight.js 的输出按行拆开，并补回跨行标签。
 *
 * hljs 的 HTML 里，块注释/多行字符串这类 token 的 `<span>` 会跨过换行；
 * 直接 `split('\n')` 会让后续行丢失起始标签（颜色断掉）。这里维护一个
 * "当前打开着的标签"栈：每行开头补上、结尾闭合，等价于把标签复制到每一行。
 */
function splitHighlightedLines(html: string): string[] {
  const open: string[] = [];
  return html.split('\n').map(raw => {
    const prefix = open.join('');
    const tokens = raw.match(/<[^>]+>|[^<]+/g) ?? [];
    for (const token of tokens) {
      if (token.startsWith('</')) open.pop();
      else if (token.startsWith('<')) open.push(token);
    }
    return prefix + raw + '</span>'.repeat(open.length);
  });
}

export function CodeView({
  code,
  path,
  wrap,
  onToggleWrap,
}: {
  code: string;
  path: string;
  wrap: boolean;
  onToggleWrap(): void;
}) {
  const [copied, setCopied] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);

  const language = languageForPath(path);
  const hljsName = highlightLanguageForPath(path);

  /** 高亮结果按"内容 + 语言"缓存：切换标签、切换行都不该重算一遍。 */
  const lines = useMemo(() => {
    if (code === '') return [''];
    if (!hljsName || !hljs.getLanguage(hljsName)) return code.split('\n');
    try {
      return splitHighlightedLines(hljs.highlight(code, { language: hljsName, ignoreIllegals: true }).value);
    } catch {
      return code.split('\n'); // 高亮失败就当纯文本，绝不让预览挂掉
    }
  }, [code, hljsName]);

  const highlighted = Boolean(hljsName && hljs.getLanguage(hljsName));
  const shown = lines.slice(0, MAX_RENDERED_LINES);
  const clipped = lines.length > shown.length;

  useEffect(() => {
    setCopied(false);
    scrollRef.current?.scrollTo({ top: 0 });
  }, [path]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      /* 没有用户手势时可能被拒，静默即可 */
    }
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 横幅：语言标签 + 换行 + 复制。DSH 的代码预览就是这一条。 */}
      <div className="flex h-7 shrink-0 items-center gap-2 border-b-[0.5px] border-line px-2.5">
        <span className="font-mono text-[11px] text-mute">{language ?? 'text'}</span>
        {clipped && <span className="text-[10.5px] text-warn">只显示前 {MAX_RENDERED_LINES} 行</span>}
        <span className="flex-1" />
        <button
          type="button"
          aria-label={wrap ? '关闭自动换行' : '开启自动换行'}
          aria-pressed={wrap}
          title={wrap ? '关闭自动换行' : '开启自动换行'}
          onClick={onToggleWrap}
          className={cn('grid h-5 w-5 place-items-center rounded text-mute hover:text-ink', wrap && 'text-dim')}
        >
          <Icon name="list" className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          aria-label={copied ? '已复制' : '复制代码'}
          title={copied ? '已复制' : '复制代码'}
          onClick={() => void copy()}
          className="grid h-5 w-5 place-items-center rounded text-mute hover:text-ink"
        >
          <Icon name={copied ? 'check' : 'copy'} className="h-3.5 w-3.5" />
        </button>
      </div>

      {/* 内容区：每行一个 flex，行号跟着自己那一行走——换行模式下也不会错位。
          行号是 aria-hidden 且不进复制内容（复制用的是原始 code）。 */}
      <div ref={scrollRef} data-selectable className="min-h-0 flex-1 overflow-auto py-2">
        {shown.map((line, index) => (
          <div key={index} className="flex min-w-full">
            <span
              aria-hidden
              className="w-9 shrink-0 select-none pr-2 text-right font-mono text-[11px] leading-[17px] text-caption"
            >
              {index + 1}
            </span>
            {highlighted ? (
              <code
                className={cn(
                  'hljs min-w-0 flex-1 pr-3 font-mono text-[11.5px] leading-[17px]',
                  wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre',
                )}
                // hljs 的输出由它自己转义（&<>"），喂进去的是我们读到的文件内容
                dangerouslySetInnerHTML={{ __html: line === '' ? '\u200b' : line }}
              />
            ) : (
              <code
                className={cn(
                  'min-w-0 flex-1 pr-3 font-mono text-[11.5px] leading-[17px] text-dim',
                  wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre',
                )}
              >
                {line === '' ? '\u200b' : line}
              </code>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
