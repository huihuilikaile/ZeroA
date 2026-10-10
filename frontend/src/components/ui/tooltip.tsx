import type { ReactNode } from 'react';

/**
 * 悬浮提示被整体关掉了（用户要求：鼠标悬浮不要弹文字）。
 *
 * 保留这三个导出而不是删掉组件，是为了不动各处调用点：把 Tooltip 变成透明包装，
 * UI 结构、props、asChild 的用法都还在，只是不再渲染任何浮层。
 * 键盘用户仍然有 aria-label（按钮上都写了），所以可访问性不依赖 tooltip。
 */
export function TooltipProvider({ children }: { children: ReactNode; delayDuration?: number }) {
  return <>{children}</>;
}

export function Tooltip({ children }: { children: ReactNode }) {
  return <>{children}</>;
}

export function TooltipTrigger({ children }: { children: ReactNode; asChild?: boolean }) {
  return <>{children}</>;
}

export function TooltipContent(_: { children?: ReactNode; sideOffset?: number }) {
  return null;
}
