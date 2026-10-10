import { Spinner } from '@/components/ui/Spinner';
import { cn } from '@/lib/utils';

/**
 * 「ZeroA 正在思考……」占位行。
 *
 * 它解决的问题是**发送之后的空窗期**：用户按下发送，到模型吐出第一个字之间
 * 可能有好几秒（新建会话时第一步还要现起进程，实测 1–3.5 秒）。
 * 这段时间对话区原本一个字都不动，看起来像"点了没反应"——用户会怀疑是不是没发出去。
 *
 * 为什么不做成时间线里的一种 item（stores/timeline.ts 的 TimelineItem）：
 * 它不是一个"记录"，而是"还没有记录"这件事本身。做成 item 就得管它的增删时机、
 * 与历史回放的配合、以及窗口化渲染（只渲染最后 150 条）会不会把它截掉。
 * 放在列表末尾渲染则天然正确：有内容了它自然就不该在。
 *
 * 主会话（ChatTimeline）与子智能体（AgentTranscript）共用同一个组件：
 * 两边的"等它开口"是同一件事，样式与文案不该各写一份。
 */
export function ThinkingRow({ className }: { className?: string }) {
  return (
    <div
      // 自动化/无障碍都读得到这句话：用户要的就是这句文案
      role="status"
      aria-live="polite"
      className={cn('mx-auto flex w-full max-w-[860px] items-center gap-2 text-[13px] text-dim', className)}
    >
      <Spinner className="h-3.5 w-3.5 text-info" />
      <span>
        Zero<span className="font-semibold text-[var(--brand)]">A</span> 正在思考……
      </span>
    </div>
  );
}
