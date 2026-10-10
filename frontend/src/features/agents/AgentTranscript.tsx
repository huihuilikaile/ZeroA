import { useEffect, useRef, useState } from 'react';
import {
  agentCanCompose,
  agentContinuable,
  agentDisplayName,
  agentElapsedText,
  agentFinished,
  agentVisual,
  useAgents,
  type AgentDelivery,
} from '@/stores/agents';
import { AGENT_DOT_CLASS, AGENT_VISUAL_META, agentModeLabel, agentModeTitle, agentStatusLabel } from '@/features/agents/status';
import { timelineFor } from '@/stores/timeline';
import { TimelineItems } from '@/features/chat/TimelineItems';
import { ThinkingRow } from '@/features/chat/ThinkingRow';
import { PromptBox } from '@/features/chat/PromptBox';
import { Icon } from '@/components/ui/Icon';
import { Spinner } from '@/components/ui/Spinner';
import { Button } from '@/components/ui/button';

/**
 * 子智能体的会话视图（可继续对话）。
 *
 * **复用主会话的解析与渲染**：这里读的是这个子智能体自己的时间线 store
 * （`timelineFor(agentId)`），内容由同一个 router 按 agentId 写进去，
 * 渲染用同一个 `<TimelineItems>`。所以子智能体与主会话的显示行为完全一致
 * ——文本按块合并、工具卡片、思考折叠、settled 分隔线都是同一套。
 *
 * **输入框也复用主会话那套**（features/chat/PromptBox.tsx，用户要求）：
 * 同样的外壳、Enter 发送 / Shift+Enter 换行、同样的发送按钮位置与样式。
 * 这里**不**带主输入框的斜杠命令、附件与模型快切：子智能体只需要"发消息"，
 * 把那些控件塞过来只会让两处行为互相牵连。运行中的"中断当前回合"、
 * 空闲时的"结束并关闭"保留，只是换成同一套按钮样式。
 *
 * 与主会话的区别（用户要求）：
 *   · 顶部只留**一行**状态（谁、什么角色、在跑还是空着、几轮、多久、多少 token）；
 *   · 底部**按形态给输入框**：continuable 且正在跑或空闲时才有，
 *     一次性形态与终态只给一行说明（见下面的 note）。
 *
 * 任务文本不在这里了：它作为一条用户消息出现在会话流里（像"已发送的消息"）。
 * 以前挤在顶部当一大块说明，占地方还挡视线（用户反馈）。
 */
export function AgentTranscript() {
  const activeId = useAgents(s => s.activeId);
  const agent = useAgents(s => (activeId ? s.agents[activeId] : undefined));
  const send = useAgents(s => s.send);
  const interrupt = useAgents(s => s.interrupt);
  const kill = useAgents(s => s.kill);
  const timeline = timelineFor(activeId || undefined);
  const items = timeline(s => s.items);
  const bottomRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);

  /** 输入草稿、投递方式、发送中、受理回执（都是"当前这个人"的临时状态） */
  const [draft, setDraft] = useState('');
  const [delivery, setDelivery] = useState<AgentDelivery>('queue');
  const [sending, setSending] = useState(false);
  const [accepted, setAccepted] = useState('');

  // 换到另一个子智能体：草稿与投递方式跟着换人——不换的话会把上一个人的
  // 半句话发给下一个（它看起来只是"输入框还留着字"，后果却是发错人）
  useEffect(() => {
    setDraft('');
    setAccepted('');
    setDelivery('queue');
  }, [activeId]);

  /**
   * 受理回执只活到"它的状态有动静"为止：一旦它开跑/跑完（status 或 turns 变了），
   * 用户已经能在时间线里看到真实进展，那句"已受理"就该让位了。
   */
  useEffect(() => {
    setAccepted('');
  }, [agent?.status, agent?.turns]);

  /** 它不在跑的时候，"插入当前回合"没有意义：自动退回排队，别等点了发送才失败 */
  useEffect(() => {
    if (agent?.status !== 'running') setDelivery('queue');
  }, [agent?.status]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' });
  }, [items.length, activeId]);

  if (!activeId || !agent) {
    return (
      <div className="flex flex-1 items-center justify-center text-[13px] text-mute">
        这个子智能体已经不在登记表里了（应用重启后登记表会清空）
      </div>
    );
  }

  const visual = agentVisual(agent);
  const meta = AGENT_VISUAL_META[visual];
  const running = visual === 'running';
  /** 能不能给它发消息：可继续 + 已在跑或空着（判定只写一次，见 stores/agents.ts） */
  const canCompose = agentCanCompose(agent);
  /** steer 只在它正跑着时可选：空闲时没有"当前回合"可插 */
  const steerAllowed = agent.status === 'running';

  /**
   * 不能发消息时的说明行。
   *
   * 顺序是"先形态、后终态"：一次性的记录就算已经跑完，"为什么不能发消息"也是形态
   * 决定的——那句"不能再发消息"比一个孤零零的"已结束"更能解释清楚。
   */
  const note = !agentContinuable(agent)
    ? '这是一次性执行的记录，不能再发消息'
    : agentFinished(agent)
      ? '已结束'
      : '正在启动：等它这一轮跑起来就能继续发消息';

  /**
   * 它开跑之后、还没吐第一个字之前，也给一句"正在思考……"（与主会话同一条提示）。
   * 判据与主会话一致：最后一条记录还是我们发过去的那条消息。
   */
  const lastItem = items.length ? items[items.length - 1] : undefined;
  const waitingFirstOutput = running && lastItem?.kind === 'user';

  async function submit(): Promise<void> {
    const text = draft.trim();
    if (!text || sending || !agent) return;
    setSending(true);
    const ok = await send(agent.id, text, delivery);
    setSending(false);
    // 失败时保留草稿：错误已经进了错误条，不该让用户把刚打的一段话再打一遍
    if (!ok) return;
    setDraft('');
    /**
     * 只说一句"已受理"，**不往时间线里插这条消息**。
     *
     * 时间线的唯一来源必须是它自己的事件流：这条消息可能被中断、可能排在别的
     * 消息后面，乐观插入留下的是一条与真实会话对不上的气泡——回看时没人分得清
     * 哪句真的送进去了。
     */
    setAccepted('已受理，等待它的下一轮');
    inputRef.current?.focus();
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 头部只留一行：任务在会话流里，这里不再重复占地方 */}
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-line bg-panel2 px-4 py-1.5">
        <span className="flex items-center gap-1.5">
          <span className={`${AGENT_DOT_CLASS} ${meta.dot}`} aria-hidden />
          <span
            className="rounded bg-panel3 px-1.5 py-[1px] font-mono text-[11px] text-ink"
            title={agent.name ? `id: ${agent.id}` : undefined}
          >
            {agentDisplayName(agent)}
          </span>
        </span>
        <span className="text-[12px] text-dim">{agent.roleId}</span>
        <span className={`text-[11px] ${meta.text}`}>{agentStatusLabel(agent)}</span>
        {running && agent.phase ? (
          <span className="max-w-[280px] truncate font-mono text-[11px] text-caption" title={agent.phase}>
            {agent.phase}
          </span>
        ) : null}
        {/* 轮次取顶层 turns（"正常结束的轮次数"）：它与 usage.turns 不是一回事，
            中断的轮次在 usage 里也算进去了，显示出来会让人以为跑得更多 */}
        <span className="text-[11px] text-caption">{agent.turns} 轮</span>
        <span className="text-[11px] text-caption">{agentElapsedText(agent)}</span>
        {agent.usage?.totalTokens ? (
          <span className="text-[11px] text-caption">{agent.usage.totalTokens.toLocaleString()} tokens</span>
        ) : null}
        <span className="ml-auto shrink-0 text-[10.5px] text-mute" title={agentModeTitle(agent)}>
          {agentModeLabel(agent)}
        </span>
      </div>

      {agent.err ? (
        <div className="shrink-0 border-b border-line bg-panel2 px-4 py-1.5 text-[11.5px] text-danger">
          {agent.err}
        </div>
      ) : null}

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {items.length === 0 ? (
          <div className="mx-auto max-w-[860px] text-[12.5px] text-mute">
            {running ? '正在启动…' : '这个子智能体没有产生任何输出。'}
          </div>
        ) : (
          <TimelineItems items={items} store={timeline} />
        )}
        {/* 开跑但还没出字：与主会话同一条「正在思考……」提示 */}
        {waitingFirstOutput && <ThinkingRow className="mt-3" />}
        <div ref={bottomRef} />
      </div>

      {/* 底部：能继续说话就给输入框，不能就给一行说明（为什么不能）。
          容器样式与主会话输入区保持一致（同样的内边距、不另外铺底色）——
          之前这里多套了一层 bg-panel2，深色/浅色主题下都比主会话亮一块。 */}
      <div className="shrink-0 px-6 pt-1 pb-4">
        {canCompose ? (
          <div className="mx-auto w-full max-w-[860px]">
            {/* 与主会话**同一个**输入区组件：外壳、Enter 发送 / Shift+Enter 换行、
                发送按钮的位置与样式全都一致（用户要求），只是没有斜杠命令与附件。 */}
            <PromptBox
              value={draft}
              onChange={setDraft}
              inputRef={inputRef}
              ariaLabel="给这个子智能体发消息"
              placeholder={
                running
                  ? '它正在跑：回车发送（默认排队等它这一轮结束）…'
                  : '它空着，可以接着上一轮的上下文继续派活…（Enter 发送，Shift+Enter 换行）'
              }
              onSubmit={() => void submit()}
              leading={(
                <>
                  {/* 投递方式：默认排队（不打断它手上的工作）；
                      "插入"只在它正跑着时可选——空闲时没有当前回合可插。
                      按钮样式与主输入框那一排（ghost/secondary、sm）保持一致。 */}
                  {(
                    [
                      { value: 'queue', label: '排队', title: '等它当前这一轮跑完再送（默认，不打断）' },
                      { value: 'steer', label: '插入', title: '插进它当前这一轮的最近检查点（会影响这一轮）' },
                    ] as const
                  ).map(option => {
                    const disabled = option.value === 'steer' && !steerAllowed;
                    return (
                      <Button
                        key={option.value}
                        size="sm"
                        variant={delivery === option.value ? 'secondary' : 'ghost'}
                        disabled={disabled}
                        onClick={() => setDelivery(option.value)}
                        title={disabled ? '它现在没在跑，没有"当前回合"可插入' : option.title}
                      >
                        {option.label}
                      </Button>
                    );
                  })}
                  <span className="text-[11px] text-mute">
                    {delivery === 'steer' ? '插入：它这一轮的下一个检查点' : '排队：等它这一轮结束'}
                  </span>
                </>
              )}
              trailing={(
                <>
                  {/* 中断只停当前这一轮：进程、队列、后续消息都留着（与"结束"是两件事） */}
                  {running ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => void interrupt(agent.id)}
                      title="只中断它当前这一轮：进程不退出，队列与后续消息都保留"
                    >
                      <Icon name="stop" className="h-3.5 w-3.5" />
                      中断当前回合
                    </Button>
                  ) : null}
                  {/* 空闲时把"结束"放在这里：它等价于标签栏的 ✕，但空闲的子智能体
                      不该在标签栏上摆一个误点就销毁进程的按钮 */}
                  {agent.status === 'idle' ? (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => void kill(agent.id)}
                      title="结束它并释放进程，之后不能再发消息（会话记录仍然留着）"
                    >
                      <Icon name="logout" className="h-3.5 w-3.5" />
                      结束并关闭
                    </Button>
                  ) : null}
                </>
              )}
              primary={{
                label: sending ? '发送中…' : '发送',
                title: '发送（Enter）',
                icon: 'send',
                disabled: sending || draft.trim().length === 0,
                onClick: () => void submit(),
              }}
            />

            {/* 状态行：与主输入框一样放在卡片下方。它正跑着时先说"正在思考……"，
                跑完/空闲时回到快捷键说明（受理回执只在刚发出去那一下有意义）。 */}
            <div className="mt-1.5 flex items-center gap-2 px-1 text-[11.5px] text-mute">
              {running ? (
                <span className="flex items-center gap-1.5 text-dim">
                  <Spinner className="h-3 w-3 text-info" />
                  它正在思考……{agentElapsedText(agent) ? ` ${agentElapsedText(agent)}` : ''}
                </span>
              ) : accepted ? (
                <span className="text-success">{accepted}</span>
              ) : (
                <span>Enter 发送 · Shift+Enter 换行</span>
              )}
              <span className="flex-1" />
              <span>投递只代表"已受理"，它的回复会按自己的节奏出现在上面的时间线里</span>
            </div>
          </div>
        ) : (
          <p className="mx-auto max-w-[860px] text-[12px] text-mute">{note}</p>
        )}
      </div>
    </div>
  );
}
