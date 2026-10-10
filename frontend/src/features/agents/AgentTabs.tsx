import {
  agentDisplayName,
  agentElapsedText,
  agentVisual,
  runningAgentCount,
  useAgents,
  type AgentRecord,
  type AgentVisual,
} from '@/stores/agents';
import { AGENT_DOT_CLASS, AGENT_VISUAL_META, agentStatusLabel } from '@/features/agents/status';
import { useUI } from '@/stores/ui';
import { Icon } from '@/components/ui/Icon';

/**
 * 会话顶部的子智能体标签栏（用户要求）。
 *
 * 只在**真的有子智能体**时出现：平时不占地方，派了活才让用户知道
 * "现在有别的进程在替你干活，点一下能看它在干什么"。
 *
 * 每个标签显示：短名（没有就用 id）、角色、**三态之一**的状态点、状态中文、耗时。
 * 三态指的是：
 *   · 运行中（实心蓝点，脉冲）；
 *   · 已完成（实心绿点：最近一轮正常跑完，但它还活着）；
 *   · 空闲·可继续（**空心**灰点：没在跑，也没失败，可以继续说话）。
 * 后面还有失败/超时/已停止三个终态，颜色区分。
 *
 * 为什么不做进度百分比：一个子智能体还剩几步是模型自己决定的，没人知道。
 * 编一个 30%/70% 比什么都显示更糟——用户会照着它判断要不要再等。
 *
 * 为什么 idle 不给「✕」：那个按钮会 KillAgent，**直接销毁进程**；而空闲的子智能体
 * 是活着、可以继续问的。把"再问一句"和"结束它"挤在同一个位置上，误点的代价是
 * 丢掉一个还在驻留的会话——所以 idle 时只留"点进详情去操作"这一条路。
 */
export function AgentTabs() {
  const order = useAgents(s => s.order);
  const agents = useAgents(s => s.agents);
  const activeId = useAgents(s => s.activeId);
  const setActive = useAgents(s => s.setActive);
  const kill = useAgents(s => s.kill);
  /** 正在跑的数量与状态点同源（见 stores/agents.ts 的 runningAgentCount） */
  const runningCount = useAgents(runningAgentCount);
  /** 输入框里的角色徽标点了会切页；这里只关心当前页 */
  const page = useUI(s => s.page);

  if (page !== 'chat' || order.length === 0) return null;

  return (
    <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b border-line bg-panel px-3 py-1.5">
      {/* 计数放最左端：先给总量、再给"其中几个还在跑"。只有颜色点的话，
          派出去十几个子智能体时没人能一眼数出还有几个没回来。 */}
      <span
        className="mr-1 flex shrink-0 items-center gap-1.5 px-1 text-[11.5px] text-mute"
        title="派出去的子智能体总数，以及其中此刻还在跑的"
      >
        <Icon name="robot" className="h-3.5 w-3.5" />
        子智能体 {order.length} · 运行 {runningCount}
      </span>

      <button
        type="button"
        onClick={() => setActive('')}
        className={`flex shrink-0 items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] transition-colors ${
          activeId === '' ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
        }`}
      >
        <Icon name="chat" className="h-3.5 w-3.5 text-mute" />
        主会话
      </button>

      {order.map(id => {
        const agent = agents[id];
        if (!agent) return null;
        const visual = agentVisual(agent);
        const meta = AGENT_VISUAL_META[visual];
        const running = visual === 'running';
        const active = activeId === id;
        const name = agentDisplayName(agent);
        return (
          <div
            key={id}
            /* 外层只管底色 + 把「✕」放在标签右侧：✕ 是**兄弟节点**而不是嵌在
               标签按钮里（按钮套按钮是无效结构，键盘操作也会打架） */
            className={`flex shrink-0 items-center rounded-lg transition-colors ${
              active ? 'bg-panel3' : 'hover:bg-panel2'
            }`}
          >
            <button
              type="button"
              onClick={() => setActive(id)}
              title={tabTitle(agent, visual)}
              className={`flex items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] transition-colors ${
                active ? 'text-ink' : 'text-dim hover:text-ink'
              }`}
            >
              <span className={`${AGENT_DOT_CLASS} ${meta.dot}`} aria-hidden />
              {/* 短名优先、id 兜底；id 仍然留在悬停提示里，排查问题时要它 */}
              <span className="max-w-[140px] truncate">{name}</span>
              {/* 角色降级成次要信息：它是"谁派的活"，不是"这是谁" */}
              <span className="font-mono text-[10.5px] text-mute">{agent.roleId}</span>
              <span className={`text-[11px] ${meta.text}`}>{agentStatusLabel(agent)}</span>
              {running && agent.phase ? (
                <span className="max-w-[180px] truncate font-mono text-[10.5px] text-caption">{agent.phase}</span>
              ) : null}
              <span className="text-[10.5px] text-caption">{agentElapsedText(agent)}</span>
            </button>
            {/* 结束入口只在"正在跑"时出现：它此刻确实该有个能按停的地方 */}
            {running && (
              <button
                type="button"
                aria-label={`结束并停止 ${name}`}
                title="停止这个子智能体（结束并释放进程，之后不能再发消息）"
                onClick={() => void kill(id)}
                className="mr-1 rounded px-1 text-[11px] text-mute hover:bg-panel2 hover:text-danger"
              >
                ✕
              </button>
            )}
          </div>
        );
      })}
    </div>
  );
}

/**
 * 悬停提示：把"点它会看到什么、它现在什么形态"讲清楚。
 *
 * 形态必须写出来——同样一个灰点，一次性形态的子智能体是"已经结束了"，
 * 可继续的是"还能接着聊"，只有提示里能说清这个区别。
 */
function tabTitle(agent: AgentRecord, visual: AgentVisual): string {
  return [
    `${agentDisplayName(agent)}${agent.name ? `（${agent.id}）` : ''} · 角色 ${agent.roleId}`,
    agent.mode === 'continuable'
      ? '可继续：一轮跑完进程仍在驻留，空闲时还能给它发消息'
      : '一次性：跑完一轮即释放进程，之后不能再发消息',
    visual === 'idle' ? '它现在空着（不是失败），点开可以继续发消息' : '',
    visual === 'done' ? '最近一轮正常跑完；它还活着，点开可以继续发消息' : '',
    `任务：${agent.task}`,
    agent.err ? `错误：${agent.err}` : '',
  ]
    .filter(Boolean)
    .join('\n');
}
