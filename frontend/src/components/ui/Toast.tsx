import { useState } from 'react';
import { createPortal } from 'react-dom';
import { Icon } from '@/components/ui/Icon';
import { useUI, type NoticeItem } from '@/stores/ui';

/**
 * 右上角通知堆栈（接替以前的"顶部居中胶囊"）。
 *
 * 为什么挪到右上角、做成卡片：
 *   · 胶囊只有一条，连续两条提示前一条直接丢；堆栈最多 3 条，新的把旧的往下推；
 *   · 报错（sticky）也走这里：常驻卡片 + 手动关闭，接替输入框上方的报错条——
 *     报错是"应用的状态"，不该钉在输入区占一层布局；
 *   · 卡片可点（有关闭按钮），所以容器本身 pointer-events:none、卡片单独开回来，
 *     缝隙不挡标题栏拖动（老胶囊的教训）。
 *
 * 时长契约：普通提示的滑出动画（tokens.css 的 notice-out）起始延迟就是
 * --notice-hold（默认 3000ms），store 在 hold + 240ms 后卸载，两边不一致
 * 就会出现"已经没了又闪一下"。
 */
export function Notifications() {
  const notices = useUI(s => s.notices);
  const dismiss = useUI(s => s.dismissNotice);
  if (notices.length === 0) return null;

  return createPortal(
    <div className="notice-stack">
      {notices.map(item => (
        <NoticeCard key={item.id} item={item} onClose={() => dismiss(item.id)} />
      ))}
    </div>,
    document.body,
  );
}

function NoticeCard({ item, onClose }: { item: NoticeItem; onClose: () => void }) {
  const icon = item.tone === 'success' ? 'check' : item.tone === 'info' ? 'list' : 'warn';
  return (
    <div className="notice" role="alert" data-tone={item.tone} data-sticky={item.sticky}>
      <span className="notice-icon" aria-hidden>
        <Icon name={icon} className="h-4 w-4" />
      </span>
      <div className="notice-body" data-selectable>
        {item.title && <div className="notice-title">{item.title}</div>}
        <div className="notice-text">{item.text}</div>
      </div>
      {/* 报错（常驻）才给"复制详情"：贴给日志/反馈时不用手抄；普通提示一闪而过，复制没意义 */}
      {item.sticky && <CopyButton text={item.title ? `${item.title}\n${item.text}` : item.text} />}
      <button type="button" aria-label="关闭提示" title="关闭" onClick={onClose} className="notice-close">
        <Icon name="x" className="h-3.5 w-3.5" />
      </button>
    </div>
  );
}

/** 复制报错内容：复制成功后图标短暂变成对勾（与 ToolCard 的复制按钮同一套交互） */
function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      aria-label={done ? '已复制' : '复制报错详情'}
      title={done ? '已复制' : '复制报错详情'}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // WebView2 无用户手势时可能拒绝写剪贴板；复制失败不值得打断用户
        }
        setDone(true);
        setTimeout(() => setDone(false), 1000);
      }}
      className="notice-close"
    >
      <Icon name={done ? 'check' : 'copy'} className="h-3.5 w-3.5" />
    </button>
  );
}
