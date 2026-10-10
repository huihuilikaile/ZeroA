import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';

interface Props {
  children: ReactNode;
  /** 出错时显示在哪里：设置页里的一节，还是整个应用 */
  where: string;
}

interface State {
  error: Error | null;
}

/**
 * 错误边界：**React 渲染期的一个异常会卸载整棵树**。
 *
 * 这条不是为了好看，是踩过：角色页因为 `null.length` 抛了一次类型错误，
 * 结果整个窗口变成纯空白——用户看到的是"应用坏了"，而实际只是设置里
 * 某一节的一个字段读错。有了边界，坏掉的是那一块，报错也留在屏幕上。
 *
 * 刻意不做"自动重试"：能自愈的错误不需要边界，需要边界的错误重试也没用。
 * 用户点了"重试"我们就重建子树，能好就好，不能好至少还能看见错误。
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // 控制台留一份完整栈：界面上的摘要够用户看，栈够开发者查
    console.error(`[${this.props.where}] 渲染出错`, error, info.componentStack);
  }

  render(): ReactNode {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="flex flex-col gap-2 rounded-xl border border-[color-mix(in_srgb,var(--danger)_35%,transparent)] bg-[color-mix(in_srgb,var(--danger)_8%,transparent)] px-3 py-2.5">
        <div className="flex items-center gap-2">
          <Icon name="warn" className="h-3.5 w-3.5 shrink-0 text-danger" />
          <span className="text-[12.5px] font-medium text-danger">{this.props.where}出错了</span>
        </div>
        <div data-selectable className="max-h-[160px] overflow-y-auto break-words font-mono text-[11.5px] leading-[18px] text-dim">
          {error.message || String(error)}
        </div>
        <div>
          <Button size="sm" variant="outline" onClick={() => this.setState({ error: null })}>
            重试
          </Button>
        </div>
      </div>
    );
  }
}
