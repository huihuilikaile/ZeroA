import { agentVisual, type AgentRecord, type AgentVisual } from '@/stores/agents';

/**
 * 子智能体状态的**呈现**映射（标签栏与会话页共用一份）。
 *
 * 为什么单独一个文件：两处必须显示同一个状态——各写一份配色与文案，迟早出现
 * "标签栏说它空闲、点进去会话页说它在跑"。判断逻辑在 stores/agents.ts 的
 * agentVisual 里，这里只负责"长什么样、写哪几个字"。
 *
 * 为什么不做进度百分比：没人知道一个子智能体还剩几步（它的回合数由模型自己决定）。
 * 编一个 30%/70% 比没有进度更糟——用户会照着它做判断。
 */

/** 状态点的公共尺寸：只有颜色部分交给下面的映射 */
export const AGENT_DOT_CLASS = 'h-1.5 w-1.5 shrink-0 rounded-full';

export const AGENT_VISUAL_META: Record<AgentVisual, { label: string; dot: string; text: string }> = {
  running: { label: '运行中', dot: 'bg-info animate-pulse', text: 'text-info' },
  /**
   * done：最近一轮正常跑完（绿点）。
   *
   * 注意它**不等于结束**——可继续子智能体这时进程还在，能接着说话。
   * 文案上写成"已完成"是照 DSH 的说法（描述"这一轮"），详情页会补一句它还可以继续。
   */
  done: { label: '已完成', dot: 'bg-success', text: 'text-success' },
  /**
   * idle：空着，可以继续说话（灰点，**空心**）。
   *
   * 空心是有意的：实心绿点表示"有结论了"，空心点表示"这里没人干活、但还活着"
   * ——只靠深浅的灰分不出来，形状不同才一眼能扫出来。文案直接把"可继续"写出来，
   * 因为这是用户最需要知道的一条（否则没人会想到还能给它发消息）。
   */
  idle: { label: '空闲·可继续', dot: 'border border-mute', text: 'text-mute' },
  completed: { label: '已完成', dot: 'bg-success', text: 'text-success' },
  failed: { label: '失败', dot: 'bg-danger', text: 'text-danger' },
  cancelled: { label: '超时停止', dot: 'bg-warn', text: 'text-warn' },
  killed: { label: '已停止', dot: 'bg-mute', text: 'text-mute' },
};

/** 状态中文：只有"启动中"需要单独说（视觉上它仍算在跑，所以三元组里没有它） */
export function agentStatusLabel(agent: AgentRecord): string {
  if (agent.status === 'starting') return '启动中';
  return AGENT_VISUAL_META[agentVisual(agent)].label;
}

/** 形态文案：可继续的空闲时还能接着说话；一次性的跑完就没了 */
export function agentModeLabel(agent: AgentRecord): string {
  return agent.mode === 'continuable' ? '可继续' : '一次性';
}

/** 形态的悬停说明（把"这个形态意味着什么"讲清楚，别只留两个字） */
export function agentModeTitle(agent: AgentRecord): string {
  return agent.mode === 'continuable'
    ? '可继续：一轮跑完进程仍在驻留，空闲时可以继续发消息；空闲一段时间后会自动回收（变成已完成）'
    : '一次性：跑完一轮就结算并释放进程，之后不能再发消息';
}
