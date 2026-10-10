import { useEffect } from 'react';
import { Events, PI_AGENT_STATE, PI_EVENT, PI_TRACE } from '@/bridge/events';
import { routeEnvelope, routeTrace } from '@/rpc/router';
import type { Envelope, Trace } from '@/rpc/types';
import { useSessions } from '@/stores/sessions';
import { useAgents, type AgentRecord } from '@/stores/agents';
import { timelineFor } from '@/stores/timeline';
import { useUI } from '@/stores/ui';

/**
 * 全应用唯一的 Pi 事件订阅点。
 *
 * 两条通道分开：
 *   pi:event  → 类型化事件，喂给 router 更新状态
 *   pi:trace  → 原始 JSONL，喂给观察器（学习用）
 *
 * Events.On 返回注销函数，正好是 useEffect 的清理函数：
 * 组件卸载或 React 严格模式重复挂载时都会注销，不会出现"一个事件被处理两遍"。
 */
export function usePiEvents(): void {
  useEffect(() => {
    const offEvent = Events.On(PI_EVENT, event => {
      const envelope = event.data as Envelope | undefined;
      if (!envelope) return;

      /**
       * 全部交给 router —— 分流在它内部按 agentId 完成。
       *
       * 子智能体的事件带着自己的 id：router 会把它们写进**那个子智能体自己的**
       * 时间线（用的是同一段解析代码），并且不碰父会话的运行状态。
       * 在这里提前拦截反而会把"复用主会话解析"这条线切断。
       */
      routeEnvelope(envelope);

      // agent_settled 之后会话文件才有新内容，这时刷新左侧对话列表最合适。
      // 放在这里而不是每个组件里，是为了「只有一处订阅」这条规则。
      if (envelope.type === 'agent_settled') {
        // 回合结束后刷一次 get_state：新会话这时才有稳定的 sessionFile，
        // 侧栏高亮和"当前会话"显示都靠它。
        //
        // 记忆不在这里写：会话归属由 Go 侧按会话文件头判定（见 rememberCurrentSession），
        // 前端此刻的 cwd 可能已经切到别的工作区，用它当键会把记忆记错地方。
        void (async () => {
          try {
            const { piBridge } = await import('@/bridge/pi');
            const { routeResult } = await import('@/rpc/router');
            routeResult(await piBridge.getState());
          } catch {
            // 进程可能恰好在 settled 后退出；不影响已显示的回答，也不制造未处理 Promise。
          } finally {
            try { await useSessions.getState().refresh(); } catch { /* refresh 内部已报告错误 */ }
          }
        })();
      }
    });
    const offTrace = Events.On(PI_TRACE, event => {
      const trace = event.data as Trace | undefined;
      if (trace) routeTrace(trace);
    });
    // 子智能体的状态通道：谁被派出去、在跑什么、跑完没有。
    // 和 Pi 的事件流分开是有意的——它是我们自己的管理事件，不是协议事件。
    const offAgent = Events.On(PI_AGENT_STATE, event => {
      const data = event.data as { kind?: string; payload?: string } | undefined;
      if (!data?.payload) return;
      try {
        const parsed = JSON.parse(data.payload) as {
          agent?: AgentRecord;
          kind?: string;
          count?: number;
          reason?: string;
        };
        if (parsed.agent) {
          const agent = parsed.agent;
          const known = Boolean(useAgents.getState().agents[agent.id]);
          useAgents.getState().upsert(agent);
          /**
           * 把"派给它的任务"作为**一条用户消息**放进它的会话。
           *
           * 语义上就是这样：这条任务确实是从父 Agent 发过去的。放进会话流之后，
           * 它跟着一起滚动、样式和用户发的消息一致（用户要求：不要再在顶部占一大块挡视线）。
           * 只在第一次见到这个子智能体时放一次；真实 Pi 稍后也会把同一条 prompt
           * 作为 message 事件回放，router 那边按文本去重（见 addInjectedUser）。
           */
          if (!known && agent.task) {
            timelineFor(agent.id).getState().addUser(agent.task);
          }
        }
        /**
         * 主 Agent 给出候选 flag 后自动停掉子智能体：必须告诉用户。
         * 否则用户只会看到"子智能体突然全变成已停止"，以为出错了。
         */
        if (data.kind === 'delegation:auto-stop') {
          useUI.getState().showNotice(
            `已停止 ${parsed.count ?? 0} 个子智能体：主 Agent 已得出候选 flag，不再需要它们`,
            'warn',
          );
        }
      } catch {
        /* 结构化事件解析失败不影响主流程 */
      }
    });
    return () => {
      offEvent();
      offTrace();
      offAgent();
    };
  }, []);
}
