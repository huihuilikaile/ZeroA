import { useUI } from '@/stores/ui';

/** 上一次报的错，用来压掉重复：同一条错误在 2 秒内只记一行。 */
let lastText = '';
let lastAt = 0;

/**
 * 统一的"应用层报错"出口。
 *
 * 规则（用户要求）：**报错以常驻通知卡片的形式出现在窗口右上角，不再往对话区
 * 塞红行、也不再钉在输入框上方**。理由：应用层错误（切会话失败、读目录失败、
 * 删除工作区被拒…）不是对话内容，混在消息流里既打断阅读，又容易被当成模型
 * 说的话；而它也不属于输入区（那是"准备下一步操作"的地方）。右上角卡片常驻、
 * 可手动关闭，新一轮操作开始时被统一收掉（dismissErrorNotices）。
 * 模型自己的错误仍然留在助手消息上（`message.errorMessage` 由时间线渲染），
 * 那是对话的一部分。
 *
 * 去重是必须的：同一个失败往往走两条路回来——调用方 catch 一次，
 * `pi:event` 里那条 kind=response 的失败又被 router 报一次。
 */
/**
 * 判断一条错误是不是"中止"造成的。
 *
 * 中止不是错误：用户按了停止、会话被切走、我们主动 kill 了子智能体，
 * Pi 都会回一句 "This operation was aborted"。把它显示成红字，
 * 用户每次停止都会看到"出错了"，像是操作失败。
 *
 * 独立成函数是因为它不止一处需要：事件分支、RPC 返回值、调用方 catch
 * 三条路都可能把这句话带上来。
 */
export function isAbortError(error: unknown): boolean {
  const text = error instanceof Error ? error.message : String(error ?? '');
  return /abort/i.test(text) && /operation was aborted|aborted by|已中止|已取消/i.test(text);
}
export function reportError(error: unknown, context?: string): void {
  const text = error instanceof Error ? error.message : String(error);
  /**
   * 最后一道防线：中止类错误**永远不进错误条**。
   *
   * 它们从三条路进来（协议事件、RPC 返回、调用方 catch），在每一条路上
   * 分别判断迟早会漏一个——这里统一挡住。
   */
  if (isAbortError(error)) return;
  const now = Date.now();
  if (text === lastText && now - lastAt < 2000) {
    return; // 两秒内的同一条错误，只记第一行
  }
  lastText = text;
  lastAt = now;
  useUI.getState().showError(context ? `出错了：${context}` : '出错了', text);
}

/**
 * compact 应答的统一出口（侧栏菜单 / 设置页 / 角色页三处共用）。
 *
 * "Nothing to compact (session too small)" 不是错误：会话还太短，Pi 没有
 * 可压的东西——用户点了「压缩上下文」只是想试试，回一张红色错误卡像操作
 * 失败。降级为普通提示；真失败（进程没起来、压缩中途出错）才进错误卡。
 */
export function reportCompactResult(result: { success: boolean; error?: string }): void {
  if (result.success) {
    useUI.getState().showNotice('已请求压缩上下文', 'success');
    return;
  }
  if (/nothing to compact/i.test(result.error ?? '')) {
    useUI.getState().showNotice('会话还太短，没有可压缩的内容', 'warn');
    return;
  }
  reportError(result.error || '压缩失败', '上下文压缩');
}
