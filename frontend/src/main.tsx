import React from 'react';
import { createRoot } from 'react-dom/client';
import { TooltipProvider } from '@/components/ui/tooltip';
import { ErrorBoundary } from '@/components/ui/ErrorBoundary';
import { AppShell } from '@/app/AppShell';
import '@/styles/tokens.css';

/**
 * 前端入口。
 * StrictMode 在开发模式下会重复挂载组件——这也是事件监听必须返回注销函数的原因
 * （否则同一条事件会被处理两遍，消息会翻倍）。
 *
 * 最外层套错误边界：React 渲染期抛异常会卸载整棵树，界面上就是"整窗空白"，
 * 用户没法区分"程序坏了"和"某一处在报错"。有边界至少能看到错误文本。
 * 分区的边界在设置页里（见 SettingsDialog），这层是最后一道。
 */
createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary where="界面">
      <TooltipProvider delayDuration={200}>
        <AppShell />
      </TooltipProvider>
    </ErrorBoundary>
  </React.StrictMode>,
);
