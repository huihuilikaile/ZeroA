import { useEffect } from 'react';
import { Window } from '@wailsio/runtime';
import { Events, PI_CLOSE_REQUEST } from '@/bridge/events';
import { piBridge } from '@/bridge/pi';
import { useConfirm } from '@/stores/confirm';
import { useRuntime, type AgentStatus } from '@/stores/runtime';

/** 这些状态都表示"Pi 正忙"，关窗会打断它 */
const BUSY_STATUS: AgentStatus[] = ['running', 'retrying', 'compacting', 'waiting-ui'];

export function isBusyStatus(status: AgentStatus): boolean {
  return BUSY_STATUS.includes(status);
}

/**
 * 关窗前的确认（两处入口共用同一套文案）：
 *   · 标题栏的关闭按钮（自绘窗口，见 TitleBar.tsx）；
 *   · Go 侧的 WindowClosing 钩子——Alt+F4、任务栏关闭都会走到那里，
 *     它发现还有回合在跑时会拦下关闭并发 pi:close-request 让前端弹窗。
 *
 * 两条路最终都走这里：确认后调 ForceClose —— Go 先把 Pi 进程收掉，再退出应用。
 */
export function requestClose(): void {
  const runtime = useRuntime.getState();
  if (!isBusyStatus(runtime.status)) {
    // 没有在跑的回合：直接关，ServiceShutdown 会收掉 Pi 进程
    void Window.Close();
    return;
  }
  useConfirm.getState().ask({
    title: 'ZeroA 正在运行',
    description: '当前回合还没结束。关闭应用会中断这次生成，并同时结束 ZeroA 进程（已经生成的内容会保留在会话里）。',
    confirmLabel: '强制关闭',
    danger: true,
    onConfirm: () => void piBridge.forceClose().catch(() => void Window.Close()),
  });
}

/** 订阅 Go 的关闭请求：把"被拦下的关闭"接到同一个确认框上 */
export function useCloseGuard(): void {
  useEffect(() => {
    const off = Events.On(PI_CLOSE_REQUEST, () => requestClose());
    return () => { off(); };
  }, []);

  /**
   * 把"是否在跑"上报给 Go：关窗钩子在 Go 侧，它需要一个能问的状态。
   * 直接订阅 runtime.status，状态一变就推一次。
   */
  useEffect(() => {
    const push = (status: AgentStatus) => {
      void piBridge.setRunningWork(isBusyStatus(status)).catch(() => { /* 关窗保护降级为只有按钮那条路 */ });
    };
    push(useRuntime.getState().status);
    return useRuntime.subscribe(state => push(state.status));
  }, []);
}
