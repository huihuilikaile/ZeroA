import { useCallback, useRef } from 'react';
import { cn } from '@/lib/utils';

/**
 * 可拖拽的分隔条。
 *
 * 为什么不用现成库：这里只需要"按住左右拖 → 改一个宽度"，用 pointer 事件
 * 二十行就够，而且能把"拖拽期间禁止选中文字/保持光标形状"这两件容易忘的事做对。
 *
 * invert：右侧面板用。往左拖 = 变宽，所以位移要取反。
 */
export function Splitter({ getWidth, setWidth, min, max, invert, ariaLabel, onReset }: {
  getWidth(): number;
  setWidth(width: number): void;
  min: number;
  max: number;
  invert?: boolean;
  ariaLabel: string;
  /** 双击回到默认宽度 */
  onReset?(): void;
}) {
  const dragging = useRef(false);
  const startX = useRef(0);
  const startWidth = useRef(0);

  const onPointerDown = useCallback((event: React.PointerEvent<HTMLDivElement>) => {
    dragging.current = true;
    startX.current = event.clientX;
    startWidth.current = getWidth();
    event.currentTarget.setPointerCapture(event.pointerId);
    // 拖拽期间锁住光标与选中：否则会一边拖一边把文字选蓝
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  }, [getWidth]);

  const onPointerMove = useCallback((event: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    const delta = (event.clientX - startX.current) * (invert ? -1 : 1);
    const next = Math.min(max, Math.max(min, startWidth.current + delta));
    setWidth(next);
  }, [getWidth, invert, max, min, setWidth]);

  const endDrag = useCallback((event: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    dragging.current = false;
    event.currentTarget.releasePointerCapture?.(event.pointerId);
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
  }, []);

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={ariaLabel}
      title={`${ariaLabel}（拖动调整，双击复位）`}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onDoubleClick={() => onReset?.()}
      className={cn(
        // 可见部分只有 1px 发丝线，但命中区域给到 6px，不然很难拖
        'group relative z-10 w-1.5 shrink-0 cursor-col-resize',
        'before:absolute before:inset-y-0 before:left-1/2 before:w-px before:-translate-x-1/2 before:bg-transparent',
        'hover:before:bg-line',
      )}
    />
  );
}
