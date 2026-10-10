/**
 * 「本轮改动」追踪：从**用户发出一条消息**起，到下一次发消息为止，
 * 这条链路里 write/edit 写过的文件，给文件面板的「本轮改动」视图用。
 *
 * 数据来源是事件流（router.ts 在这些点上调这里）：
 *   用户发消息（useAgent.prompt）→ beginTurn    清空上一轮的清单
 *   tool_execution_start         → noteToolStart 记下这次调用想写哪个文件（含是谁写的）
 *   tool_execution_end           → noteToolEnd  成功了才入清单（失败不写盘）
 *   agent_settled                → settleTurn   有动静就 bump refreshNonce
 *
 * 为什么按"用户消息"起算而不是 agent_start：委派链条是
 * 「主会话派活 → 结束 → 子智能体写文件 → 结果投回 → 主会话又开新一轮」——
 * 按 agent_start 清的话，结果回传唤醒的那一轮会把子智能体刚写的文件全部清掉，
 * 界面上就是"明明写了文件，本轮改动却是空的"（实测踩到）。
 * 用户感知里的"这一轮"是从他发消息开始的整条链，清单也按这个边界算。
 *
 * 为什么在 start 记、在 end 判：tool_execution_end 不一定带 toolName，
 * 而失败/被中止的写入不落盘——只有「start 时记住路径 + end 时确认成功」
 * 两个信息拼起来才是真相。
 *
 * 子智能体的写入同样入清单（agentId 非空，面板里分组 + 机器人标记区分）：
 * 它改的也是这个工作区的文件，用户同样想知道。
 *
 * bash 命令不知道碰了哪些文件，只记「跑过」，回合结束统一让面板刷新目录。
 */
import { create } from 'zustand';

/** 一条被改动记录：agentId 空 = 主会话写的，非空 = 那个子智能体写的 */
export interface TouchedFile {
  path: string;
  agentId: string;
}

interface ChangedFilesStore {
  /** 本轮被写过的文件绝对路径（按发生顺序，按路径去重） */
  touched: TouchedFile[];
  /** 目录刷新信号：回合里有写入或跑过 bash 时 +1，面板据此重列 */
  refreshNonce: number;
  clear(): void;
}

export const useChangedFiles = create<ChangedFilesStore>(set => ({
  touched: [],
  refreshNonce: 0,
  clear: () => set({ touched: [] }),
}));

interface PendingWrite {
  tool: string;
  path?: string;
  agentId: string;
}

/** 进行中的工具调用（toolCallId → 想写的路径）；bash 也记，用来判断成功没有 */
const pending = new Map<string, PendingWrite>();
/** 本回合跑成功过 bash（不知道改了什么，回合结束统一刷新目录） */
let bashRan = false;
/** 本回合有过成功写入（settler 据此决定要不要 bump） */
let wrote = false;

/** 路径比较统一成"正斜杠 + 小写"：Windows 大小写不敏感，且手拼的相对路径用的是 / */
export function normPath(path: string): string {
  return path.replace(/\\/g, '/').toLowerCase();
}

/** 把工具参数里的路径换成绝对路径；相对路径按当前工作区拼，拼不出就放弃 */
function absolutePath(raw: unknown, cwd: string): string | undefined {
  if (typeof raw !== 'string') return undefined;
  const path = raw.trim();
  if (!path) return undefined;
  if (/^[a-zA-Z]:[\\/]/.test(path) || path.startsWith('\\\\') || path.startsWith('/')) return path;
  const base = cwd.replace(/[\\/]+$/, '');
  return base ? `${base}/${path}` : undefined;
}

/**
 * 新一轮「本轮」开始：清空上一轮的清单与未完成记录。
 *
 * 调用时机是**用户发出一条新消息**（useAgent.prompt）与切换会话——
 * 不是 agent_start（见文件头注释：委派链里结果回传会再开一轮，不能清）。
 */
export function beginTurn(): void {
  pending.clear();
  bashRan = false;
  wrote = false;
  useChangedFiles.setState({ touched: [] });
}

/** 工具调用开始：write/edit 记下目标路径，bash 记下「有这回事」。agentId 空 = 主会话 */
export function noteToolStart(callId: string, toolName: string, args: unknown, cwd: string, agentId = ''): void {
  if (!callId) return;
  if (toolName === 'write' || toolName === 'edit') {
    const path = absolutePath((args as { path?: unknown } | undefined)?.path, cwd);
    if (path) pending.set(callId, { tool: toolName, path, agentId });
    return;
  }
  if (toolName === 'bash') pending.set(callId, { tool: toolName, agentId });
}

/** 工具调用结束：成功才把文件入清单；失败的写入不落盘 */
export function noteToolEnd(callId: string, failed: boolean): void {
  const entry = pending.get(callId);
  if (!entry) return;
  pending.delete(callId);
  if (failed) return;
  if (entry.tool === 'bash') {
    bashRan = true;
    return;
  }
  if (entry.path) {
    wrote = true;
    const record: TouchedFile = { path: entry.path, agentId: entry.agentId };
    useChangedFiles.setState(state => {
      const rest = state.touched.filter(item => normPath(item.path) !== normPath(record.path));
      return { touched: [...rest, record] };
    });
  }
}

/** 回合彻底结束：有写入或跑过 bash 就通知面板刷新 */
export function settleTurn(): void {
  if (wrote || bashRan) {
    useChangedFiles.setState(state => ({ refreshNonce: state.refreshNonce + 1 }));
  }
  pending.clear();
  bashRan = false;
  wrote = false;
}
