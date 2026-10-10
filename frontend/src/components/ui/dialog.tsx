import * as React from 'react';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import { cn } from '@/lib/utils';
import { Icon } from '@/components/ui/Icon';

/** shadcn/ui Dialog（Radix）：负责焦点陷阱、Esc 关闭、遮罩点击关闭 —— 这些自己写很容易漏 */
const Dialog = DialogPrimitive.Root;
const DialogTrigger = DialogPrimitive.Trigger;
const DialogClose = DialogPrimitive.Close;
const DialogPortal = DialogPrimitive.Portal;

function DialogOverlay({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      /* top-9 = 不遮住标题栏：模态窗开着时窗口仍可拖动、可最小化/关闭——
         模态锁住的是"应用内容"，不是"窗口本身" */
      className={cn('fixed inset-x-0 bottom-0 top-9 z-50 bg-black/50 data-[state=open]:animate-in', className)}
      {...props}
    />
  );
}

function DialogContent({
  className,
  children,
  hideClose = false,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Content> & { hideClose?: boolean }) {
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        className={cn(
          'fixed top-1/2 left-1/2 z-50 w-[min(560px,92vw)] -translate-x-1/2 -translate-y-1/2',
          'rounded-card border border-line bg-panel shadow-[var(--shadow)] outline-none',
          'max-h-[86vh] overflow-y-auto',
          className,
        )}
        {...props}
      >
        {children}
        {!hideClose && (
          <DialogPrimitive.Close
            aria-label="关闭"
            className="absolute top-3 right-3 rounded-md p-1 text-mute hover:bg-panel2 hover:text-ink"
          >
            <Icon name="x" className="h-4 w-4" />
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Content>
    </DialogPortal>
  );
}

function DialogHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  /**
   * pr-11 是给右上角 ✕ 让位：关闭按钮 absolute right-3、宽 24px，占据距右缘
   * 12–36px 的区间；头部内容若只按 px-4（16px）收尾就会伸进这 20px 里，
   * 看起来是"文本和按钮叠在一起"。放在共享组件里而不是每个对话框各自
   * 补 padding——逐个补已经漏过（RawLogDialog），以后新对话框也会再漏。
   */
  return <div className={cn('flex items-center gap-2.5 border-b border-line px-4 py-3 pr-11', className)} {...props} />;
}

function DialogFooter({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('flex justify-end gap-2 border-t border-line bg-panel2/60 px-4 py-3', className)} {...props} />;
}

function DialogTitle({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return <DialogPrimitive.Title className={cn('text-[14px] font-medium text-ink', className)} {...props} />;
}

function DialogDescription({ className, ...props }: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return <DialogPrimitive.Description className={cn('text-[13px] leading-[22px] text-dim', className)} {...props} />;
}

export {
  Dialog, DialogTrigger, DialogClose, DialogPortal,
  DialogOverlay, DialogContent, DialogHeader, DialogFooter, DialogTitle, DialogDescription,
};
