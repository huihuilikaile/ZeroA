import type { ClipboardEvent, KeyboardEvent, ReactNode, RefObject } from 'react';
import { Icon, type IconName } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';

/**
 * 「输入区」的可复用外壳：主会话（features/chat/Composer.tsx）与子智能体
 * （features/agents/AgentTranscript.tsx）共用同一份。
 *
 * 为什么抽出来：子智能体原本自己写了一个简易 textarea——外观、快捷键、
 * 按钮位置全都是第二套。用户的要求是"子智能体也用主输入框那套"，
 * 而"两套各写一遍"必然漂移（改了一边忘了另一边，两边的回车行为慢慢就不一样了）。
 *
 * 边界（有意为之）：
 *   · 它**只负责输入区本身**——外壳、textarea、Enter/Shift+Enter、主按钮与插槽。
 *     斜杠命令、附件、模型快切、拖拽投递这些是**主会话特有的**，留在 Composer 里，
 *     通过插槽放进来。子智能体只需要"发消息"，不该被塞进一堆它用不到的控件。
 *   · 空稿能不能发由调用方判断（onSubmit 里自己 guard）：两边规则不同
 *     （主会话还要看 switching/sending，子智能体要看到底有没有内容）。
 */
export interface PromptBoxPrimary {
  label: string;
  title?: string;
  icon: IconName;
  variant?: 'default' | 'secondary' | 'outline' | 'ghost' | 'destructive' | 'link';
  disabled?: boolean;
  onClick: () => void;
}

export interface PromptBoxProps {
  value: string;
  onChange(value: string): void;
  /** 回车提交（Shift+Enter 换行；输入法组合中的回车不算） */
  onSubmit(): void;
  /**
   * Ctrl/Cmd+Enter 提交。
   * 主会话用它实现 busy-Enter 的"另一种投递方式"（排队 ⇄ 插入）；不给就等同 onSubmit。
   */
  onAlternateSubmit?(): void;
  /** 其余按键仍然交给调用方（主会话的"连按两次 Esc 停止"在这里） */
  onKeyDown?(event: KeyboardEvent<HTMLTextAreaElement>): void;
  onPaste?(event: ClipboardEvent<HTMLTextAreaElement>): void;
  placeholder?: string;
  ariaLabel: string;
  /** 输入框**上方**的内容：附件 chips、斜杠命令面板、拖拽提示… */
  top?: ReactNode;
  /** 控制行左侧：加号、角色徽标、投递方式… */
  leading?: ReactNode;
  /** 控制行右侧、主按钮**左边**的内容：模型快切… */
  trailing?: ReactNode;
  /** 主按钮：主会话是"发送/停止/排队"，子智能体是"发送" */
  primary: PromptBoxPrimary;
  /**
   * 输入框高度（默认与主输入框一致：2 行 / 62px）。
   *
   * 状态行（"Enter 发送 · Shift+Enter 换行"那类）**不在**这里：它在卡片外面，
   * 两边的文案完全不同（主会话要显示轮次、耗时、角色…），
   * 由调用方自己渲染——这里只管"输入区"这一块。
   */
  height?: number;
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  /** 拖拽落点标记：Go 侧的 DropTargetDetails 会回传这个 id（主会话用它给更具体的提示） */
  dropTargetId?: string;
}

export function PromptBox({
  value, onChange, onSubmit, onAlternateSubmit, onKeyDown, onPaste,
  placeholder, ariaLabel, top, leading, trailing, primary,
  height = 62, inputRef, dropTargetId,
}: PromptBoxProps) {
  return (
    <div
      data-file-drop-target={dropTargetId}
      /**
       * 挡住浏览器的默认行为：把文件拖到窗口上时，WebView 默认会**导航到该文件**
       * （界面直接变成一张图片/一段文本）。主会话的附件走 Wails 的原生投递，
       * 这里只需要阻止"打开文件"这个默认动作。
       */
      onDragOver={event => {
        event.preventDefault();
        if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
      }}
      onDrop={event => event.preventDefault()}
      className="rounded-[18px] border border-line bg-panel2 px-3.5 py-2.5 shadow-[var(--shadow)]"
    >
      {top}

      <textarea
        ref={inputRef}
        value={value}
        onChange={event => onChange(event.target.value)}
        onKeyDown={event => {
          /**
           * 中日韩输入法选词时按回车是"确认候选词"，不能当发送：
           * 少了这个守卫，用中文输入法按回车选字会把半截句子发出去。
           */
          if (event.nativeEvent.isComposing || event.keyCode === 229) return;
          if (event.key === 'Enter' && !event.shiftKey) {
            if (event.altKey) return; // Alt+Enter 留给应用快捷键
            event.preventDefault();
            if ((event.ctrlKey || event.metaKey) && onAlternateSubmit) onAlternateSubmit();
            else onSubmit();
            return;
          }
          onKeyDown?.(event);
        }}
        onPaste={onPaste}
        rows={2}
        aria-label={ariaLabel}
        placeholder={placeholder}
        style={{ height, minHeight: height }}
        className="w-full min-w-0 resize-y bg-transparent text-[14px] leading-[22px] outline-none placeholder:text-mute"
      />

      {/* 控制行：左侧是与本次发送相关的上下文动作，右侧是模型/状态与主按钮。
          主按钮固定在右下角（照 DSH 的"一个座位"），两处输入框因此长得一样。 */}
      <div className="mt-1.5 flex flex-wrap items-center gap-3 px-0.5 pt-1 pb-0.5">
        <div className="flex min-w-0 items-center gap-3">{leading}</div>
        <div className="ml-auto flex flex-none items-center gap-3">
          {trailing}
          <Button
            size="sm"
            variant={primary.variant ?? 'default'}
            disabled={primary.disabled}
            onClick={primary.onClick}
            // 禁用时不显示 tooltip（禁用的按钮给提示只会吵）
            title={primary.disabled ? undefined : primary.title}
          >
            <Icon name={primary.icon} className="h-3.5 w-3.5" />
            {primary.label}
          </Button>
        </div>
      </div>
    </div>
  );
}
