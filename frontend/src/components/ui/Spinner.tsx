import { cn } from '@/lib/utils';

/**
 * 转圈动画。
 *
 * 用 border + animate-spin 画，而不是引入图标库的 spinner：
 * 这样颜色跟着 currentColor 走，两套主题下都自动适配，也不需要额外资源。
 */
export function Spinner({ className }: { className?: string }) {
  return (
    <span
      role="status"
      aria-label="加载中"
      className={cn(
        'inline-block animate-spin rounded-full border-2 border-current border-t-transparent',
        className,
      )}
    />
  );
}
