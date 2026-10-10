import { AssistantText, ToolCard, UserBubble, type TimelineHook } from '@/features/chat/ToolCard';
import { ThinkingCard } from '@/features/chat/ThinkingCard';
import { SystemCard } from '@/features/chat/SystemCard';
import { ExtensionCard, SettledDivider } from '@/features/chat/ExtensionCard';
import { useTimeline, type TimelineItem } from '@/stores/timeline';

/**
 * 时间线渲染：主会话与子智能体**共用这一份**。
 *
 * 抽出来的理由：子智能体原来自己攒了一份简化记录（每个文本 delta 一条），
 * 结果是"一个字一个字往外蹦"——因为它没走主会话那套块合并。现在两条路都用
 * 同一份 store 形状与同一组组件，行为自然一致。
 *
 * store 以参数传入（zustand 的 create 返回的就是 hook），所以这里不绑定
 * 任何具体会话：谁传进来就渲染谁。
 */

/** 每条记录对应的 RPC 事件名（打开"事件映射"时显示，方便对照协议学习） */
export function eventLabel(item: TimelineItem): string {
  switch (item.kind) {
    case 'user': return 'stdin · {"type":"prompt"}';
    case 'assistant': return 'agent_start → message_start → message_update → tool_execution_* → message_end';
    case 'system': return item.subtype === 'compaction' ? 'compaction_start → compaction_end'
      : item.subtype === 'retry' ? 'auto_retry_start → auto_retry_end'
        : item.subtype === 'error' ? 'error' : 'system';
    case 'extension': return 'extension_ui_request → extension_ui_response';
    case 'settled': return 'agent_settled';
  }
}

function AssistantItem({ item, store }: { item: Extract<TimelineItem, { kind: 'assistant' }>; store: TimelineHook }) {
  const streaming = item.status === 'streaming';
  return (
    <article className="rise flex flex-col gap-1.5">
      <span className="evtag">{eventLabel(item)}</span>
      {/* DSH 的对话没有头像和气泡：正文就是段落，事件是折叠行 */}
      {item.blocks.map((block, index) => {
        if (block.kind === 'thinking') return <ThinkingCard key={index} text={block.text} done={block.done} />;
        if (block.kind === 'tool') return <ToolCard key={block.toolCallId || index} toolCallId={block.toolCallId} store={store} />;
        if (!block.text && !streaming) return null;
        return <AssistantText key={index} text={block.text} streaming={streaming && !block.done} />;
      })}
      {item.error && <p className="text-[13px] leading-[22px] text-danger">{item.error}</p>}
    </article>
  );
}

export interface TimelineItemsProps {
  items: TimelineItem[];
  /** 读哪个时间线：默认主会话；子智能体传自己的 store */
  store?: TimelineHook;
  /** 用户消息的节点引用（主会话用它做"跳到这一轮"）；子智能体不需要 */
  registerAnchor?: (id: number, el: HTMLElement | null) => void;
  /** 分支按钮：只有主会话有（子智能体不能分支） */
  forkFor?: (item: Extract<TimelineItem, { kind: 'user' }>) => (() => void) | undefined;
}

export function TimelineItems({ items, registerAnchor, forkFor, store = useTimeline }: TimelineItemsProps) {
  return (
    <div className="mx-auto flex max-w-[860px] flex-col gap-3">
      {items.map(item => {
        switch (item.kind) {
          case 'user':
            return (
              <article
                key={item.id}
                ref={el => registerAnchor?.(item.id, el)}
                className="flex flex-col items-end"
              >
                <span className="evtag">{eventLabel(item)}</span>
                <UserBubble text={item.text} attachments={item.attachments} onFork={forkFor?.(item)} />
              </article>
            );
          case 'assistant': return <AssistantItem key={item.id} item={item} store={store} />;
          case 'system': return <SystemCard key={item.id} {...item} />;
          case 'extension': return <ExtensionCard key={item.id} {...item} />;
          case 'settled': return <SettledDivider key={item.id} text={item.text} />;
        }
      })}
    </div>
  );
}
