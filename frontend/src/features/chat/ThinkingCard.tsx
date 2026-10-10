import { useState } from 'react';
import { RichText } from '@/components/ui/RichText';
import { Row } from '@/components/ui/Row';

/**
 * 思考：DSH 里同样是一行折叠行（图标 + 「思考」+ 摘要），展开才看全文。
 * 折叠是刻意的：思考通常很长，展开会淹没真正的回答。
 *
 * 摘要取「最后一个段落的首行」而不是第一行：思考往往以"用户想让我…"开头，
 * 真正的结论在最后一段；取最后一段才看得出这次到底想了什么。
 */
export function ThinkingCard({ text, done }: { text: string; done: boolean }) {
  const [open, setOpen] = useState(false);
  const paragraphs = text.split(/\n\s*\n/).filter(part => part.trim());
  const lastParagraph = paragraphs[paragraphs.length - 1] ?? text;
  const summary = (lastParagraph ?? '').split('\n').find(line => line.trim()) ?? '';
  return (
    <Row
      icon="think"
      title="思考"
      summary={summary || (done ? '已完成' : '进行中…')}
      meta={done ? '' : '…'}
      expandable
      expanded={open}
      onToggle={() => setOpen(!open)}
    >
      <div className="mt-1 ml-[22px] text-[13px]">
        {/* 思考内容也按 Markdown 渲染：模型经常在这里写列表和行内代码 */}
        <RichText text={text} />
      </div>
    </Row>
  );
}
