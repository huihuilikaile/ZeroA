import { create } from 'zustand';

/**
 * 全局确认弹窗。
 *
 * 为什么做成全局的：删除会话、移除工作区、删除模型都要"先问一句"，
 * 如果每个地方各写一个 Dialog，很快就会出现三种不同的确认样式与措辞。
 * 这里统一成一处，调用方只提供文案与动作。
 */
export interface ConfirmRequest {
  title: string;
  description?: string;
  confirmLabel?: string;
  /** 危险操作用红色主按钮 */
  danger?: boolean;
  /** 纯告知：只有一个"知道了"，没有确认动作（例如"工作区正在使用"） */
  alertOnly?: boolean;
  onConfirm?(): void | Promise<void>;
  /**
   * 取消/关掉弹窗时调用。
   *
   * 为什么需要它：有些调用点必须在"用户取消"之后**继续做别的事**——
   * 例如点开一条属于别的角色的会话：回合在跑时，切角色会重启进程、打断这一轮，
   * 所以要问一句；无论用户选哪边，那条会话都要照常打开。
   * 没有这个回调，调用方就只能靠 setTimeout 猜用户选了什么。
   */
  onCancel?(): void | Promise<void>;
}

interface ConfirmStore {
  request: ConfirmRequest | null;
  ask(request: ConfirmRequest): void;
  close(): void;
}

export const useConfirm = create<ConfirmStore>(set => ({
  request: null,
  ask: request => set({ request }),
  close: () => set({ request: null }),
}));
