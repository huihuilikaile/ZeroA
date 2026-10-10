import { useEffect, useState } from 'react';
import { Clipboard, Dialogs } from '@wailsio/runtime';
import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';
import { relativeTime } from '@/lib/format';
import { reportError, reportCompactResult } from '@/lib/reportError';
import { roleById, useRoles } from '@/stores/roles';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { useAgent } from '@/hooks/useAgent';
import { isBusyStatus } from '@/hooks/useCloseGuard';
import { useRuntime, type AgentStatus } from '@/stores/runtime';
import { useTimeline } from '@/stores/timeline';
import { useConfirm } from '@/stores/confirm';
import { activityKey, useSessionActivity, type SessionActivity } from '@/stores/sessionActivity';
import { persistedSession, sameWorkspace, sessionsOf, useSessions, workspaceKey } from '@/stores/sessions';
import { useUI } from '@/stores/ui';

/** 从路径里取最后一段当工作区名（DSH 的工作区行就是这么显示的） */
function baseName(path: string): string {
  const trimmed = (path || '').replace(/[\\/]+$/, '');
  const parts = trimmed.split(/[\\/]/);
  return parts[parts.length - 1] || trimmed || '工作区';
}

/** 折叠状态持久化的键：分组默认展开，用户折叠过哪几个要记住 */
const COLLAPSE_KEY = 'pi-gui-collapsed';

function loadCollapsed(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(COLLAPSE_KEY);
    const parsed = raw ? (JSON.parse(raw) as unknown) : null;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    return Object.fromEntries(
      Object.entries(parsed as Record<string, unknown>).filter((entry): entry is [string, boolean] => entry[1] === true),
    );
  } catch {
    return {};
  }
}

/**
 * 左栏 —— 结构照 DeepSeek Harness 的 ui-sidebar + ui-workspace：
 *   工作区标题（搜索 / 选择文件夹）→ 每个工作区一节，各自列出自己的会话记录。
 *
 * 三件事的数据来源不同，别混：
 *   · 对话列表 = 读会话目录（Pi 没有列会话的 RPC），按 cwd 分组；
 *   · 新建/切换/改名 = RPC（new_session / switch_session / set_session_name）；
 *   · 选择文件夹 = 系统目录选择器（Wails 的 Dialogs.OpenFile），选完即切工作区。
 */
/**
 * 会话行右侧的状态指示（用户要求）：
 *   · 这条会话的 Pi **正在跑任务** → 一个**圆环**，环上画出**上下文占用百分比**；
 *   · **跑完了但它不是当前正在看的那条** → 一个**绿点**（"有结果了，你还没看"）；
 *   · 点进去（成为当前会话）之后绿点消失（见 openSession 里的 markSeen）。
 *
 * 环的画法与输入框上方那个上下文环保持一套（viewBox 14 / R5.5 / strokeDasharray），
 * 只是更小、不带百分比文字——侧栏一行放不下那么多东西，数字放 title 里。
 */
function SessionActivityMark({ activity, isCurrent }: { activity?: SessionActivity; isCurrent: boolean }) {
  if (!activity?.running) {
    // 绿点只在"不是当前会话"时出现：用户正看着的那条不需要提示它结束了
    if (!activity?.done || isCurrent) return null;
    return (
      <span
        title="已完成，点开查看"
        aria-label="会话已完成"
        className="h-1.5 w-1.5 flex-none rounded-full bg-success"
      />
    );
  }  const percent = Math.max(0, Math.min(100, Math.round(activity.percent)));
  const radius = 5.5;
  const circumference = 2 * Math.PI * radius;
  return (
    <span
      title={percent > 0 ? `正在运行 · 上下文 ${percent}%` : '正在运行'}
      aria-label={percent > 0 ? `正在运行，上下文已用 ${percent}%` : '正在运行'}
      className="flex-none"
    >
      <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden>
        <circle cx="7" cy="7" r={radius} fill="none" stroke="var(--line)" strokeWidth="2" />
        {percent > 0 && (
          <circle
            cx="7" cy="7" r={radius} fill="none"
            stroke="var(--info)" strokeWidth="2" strokeLinecap="round"
            strokeDasharray={`${(circumference * percent) / 100} ${circumference}`}
            transform="rotate(-90 7 7)"
          />
        )}
      </svg>
    </span>
  );
}

/**
 * 当前这条会话的指示数据**不能只信活动表**。
 *
 * 活动表靠"切换结果"登记 runtime↔路径（见 rpc/router.ts 的 finishSessionSwitch），
 * 而启动时前端没有参与任何切换：第一条 get_state 只是把 runtime 认了下来，
 * 表里于是可能没有它的记录——可它完全可能正在跑（例如恢复一条没跑完的会话）。
 * 前台的状态就在 runtime store 里，直接看它最准，也不必等事件把表填上。
 */
function liveActivity(
  activity: SessionActivity | undefined,
  status: AgentStatus,
  contextPercent: number,
): SessionActivity {
  return {
    runtimeId: activity?.runtimeId ?? '',
    /**
     * 在跑 = 表里说在跑，**或者**前台这条的状态还忙着。
     * 忙的判据用 isBusyStatus（running/retrying/compacting/waiting-ui）：
     * 等用户确认扩展弹框时这一轮并没有收尾，环不该消失——那会让人以为活干完了。
     */
    running: Boolean(activity?.running) || isBusyStatus(status),
    /**
     * 当前会话永远不显示绿点：绿点的意思是"有新结果，你还没看"，
     * 而用户正看着这一条——挂上它只会让人以为还有别的事没处理。
     */
    done: false,
    // 百分比用前台这份（stats 给的权威口径，比表里最后一次事件更新得更及时）。
    // 取整规则与输入框上方那个环一致：小于 1% 但非零时显示 1%，
    // 免得"已经用了 900 tokens"被画成"没占用"（见 features/chat/ContextMeter.tsx）。
    percent: contextPercent > 0 ? Math.max(1, Math.round(contextPercent)) : (activity?.percent ?? 0),
  };
}

export function Sidebar() {
  const cwd = useRuntime(s => s.cwd);
  /**
   * 会话活动表：**订阅整张表一次**，行内再按路径查。
   *
   * 为什么不在每行里 `useSessionActivity(s => s.byPath[key])`：那个 hook 要写在
   * `items.map(...)` 里，行数会变——React 不允许条件/循环里调 hook。
   * 订阅整表只在这张表变化时重渲染侧栏，开销可以忽略（它只有几行数据）。
   */
  const activityByKey = useSessionActivity(s => s.byPath);
  /** 前台这条会话的状态与上下文占用：当前行/占位行用它（见 liveActivity） */
  const status = useRuntime(s => s.status);
  const contextPercent = useRuntime(s => s.contextPercent);
  const exeDir = useRuntime(s => s.exeDir);
  const sessionFile = useRuntime(s => s.sessionFile);
  const byWorkspace = useSessions(s => s.byWorkspace);
  const workspaces = useSessions(s => s.workspaces);
  const { create, open, rename, remove, refresh, refreshMany, addWorkspace, removeWorkspace, switchWorkspace } = useSessions();
  /** 当前会话是否还没命名 + 它的路径（画"新建会话"占位行用） */
  const currentNamed = useSessions(s => s.currentNamed);
  const activePath = useSessions(s => s.activePath);
  const { refreshState, refreshStats, loadHistory, switchRole } = useAgent();
  const theme = useUI(s => s.theme);
  /**
   * 正在切换（换会话 / 换工作区 / 起停进程）。
   *
   * 它读的是 **runtime store**：切换的"在飞"状态只有一处记账
   * （见 rpc/router.ts 的 beginSessionSwitch / finishSessionSwitch），
   * 组件自己再存一份布尔量迟早会与真实状态不一致——那种不一致的表现就是
   * "点了一下没反应"或者"切完了按钮还灰着"。
   */
  const switching = useRuntime(s => s.switching || s.busy);
  const defaultWorkspace = useRuntime(s => s.defaultWorkspace);
  /** 会话的角色徽标：同工作区里不同角色的会话要能分辨 */
  const roles = useRoles(s => s.roles);
  const roleBySession = useRoles(s => s.roleBySession);

  const [filterOpen, setFilterOpen] = useState(false);
  const [keyword, setKeyword] = useState('');
  const [renaming, setRenaming] = useState<{ path: string; value: string } | null>(null);
  /**
   * 每个工作区的会话列表是否被折叠（默认展开）。
   *
   * 与 DSH 一致：分组默认展开，折叠状态**存在本地**——"我展开了就显示"这件事
   * 应该在下次打开时还成立，而不是每次都被重置成展开。
   */
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>(loadCollapsed);

  /** 折叠状态写回本地（写不进去也不影响使用） */
  function toggleCollapsed(folder: string, next: boolean) {
    setCollapsed(prev => {
      const map = { ...prev, [folder]: next };
      try { localStorage.setItem(COLLAPSE_KEY, JSON.stringify(map)); } catch { /* 忽略 */ }
      return map;
    });
  }

  const active = cwd || exeDir;
  // 顺序必须稳定：以持久化的 workspaces 为准，当前目录不在列表里时才追加，
  // 绝不因为"谁是当前"而重排——否则一切换工作区，整行就跳到别的位置去了。
  // 比较用 sameWorkspace：后端给的是规范化键，磁盘路径是另一种写法，只比小写会认成两个。
  const activeKnown = workspaces.some(item => sameWorkspace(item, active));
  const folders = activeKnown ? workspaces : [...workspaces, active].filter(Boolean);
  const isExpanded = (folder: string) => searching || collapsed[folder] !== true;
  const query = keyword.trim().toLowerCase();
  /** 搜索时跨分组展示：匹配项来自所有"已加载"的分组（展开即加载）。 */
  const searching = query !== '';

  /**
   * 按需加载：**展开的每个工作区各自加载自己的会话**。
   *
   * 这是照着 DSH 的 ui-workspace 做的——"Each Workspace keeps its own Sessions"，
   * 分组各自成节、各自带行操作。以前只渲染当前工作区那一组，别的文件夹点开是空的，
   * 用户要的是"展开了就显示"。
   *
   * 默认全部展开，所以这里会把所有分组一次性取回；后端用 ListSessionsBatch
   * 一次扫描搞定，不是每个目录各扫一遍。
   */
  useEffect(() => {
    const pending = folders.filter(folder => {
      if (!isExpanded(folder)) return false;         // 折叠的不加载
      const group = sessionsOf(byWorkspace, folder);
      return !group.loaded && !group.loading;
    });
    if (pending.length === 0) return;
    void refreshMany(pending);
  }, [folders, collapsed, byWorkspace, refreshMany]);

  /**
   * 切工作区。
   *
   * 一进程一会话之后这一步**不再重启 Pi**：后端只是把"当前会话"换成新工作区里
   * 最近用过的那条（没有就冷启动一条），甲工作区正在跑的任务不受影响。
   * 所以这里既不用 start()，也不能清时间线——每条会话的内容留在它自己的视图里。
   *
   * 返回是否真的切过去了：调用方（打开别的目录里的会话、在那个目录新建会话）
   * 要靠它决定要不要继续往下走。
   */
  async function switchFolder(folder: string): Promise<boolean> {
    if (sameWorkspace(folder, active)) return true;
    if (switching) return false;

    // 目录不存在就不要拿着无效路径去起进程：那只会得到一句含糊的"找不到文件"。
    // 检查失败与"不存在"两种情况的错误都进对话区（见 lib/reportError.ts）。
    try {
      const result = await piBridge.statFolder(folder);
      const info = parseData<{ exists?: boolean; isDir?: boolean }>(result);
      if (!info?.exists) {
        reportError(`工作区目录不存在：${folder}`, '切换工作区');
        return false;
      }
      if (info.isDir === false) {
        reportError(`这个路径不是文件夹：${folder}`, '切换工作区');
        return false;
      }
    } catch (error) {
      reportError(error, '检查工作区');
      return false;
    }

    addWorkspace(folder);
    useRuntime.getState().setCwd(folder);
    // 工作区是"重启后回到哪里"的依据，必须落到后端文件：localStorage 可能整体丢失。
    await piBridge.setActiveWorkspace(folder).catch(() => { /* 后端不可用时仍可用本次会话 */ });
    // 对话区在这段时间显示转圈动画（换目录要冷启动进程时是实打实的等待）
    useUI.getState().setSwitchingWorkspace(true);
    try {
      if (!await switchWorkspace(folder)) return false;
      /**
       * 切完再读一次角色。
       *
       * 角色是"每个工作区各自记住"的（后端 lastRoleByWorkspace），而
       * useRoles.selected 是上一次 load() 的快照。不重新读一次，界面上显示的
       * 就是**上一个工作区的角色**（进程实际用的是哪个另外算，见 RoleBadge 的
       * "待重启生效"判断）。
       */
      await useRoles.getState().load();
      /**
       * 每个工作区各自恢复上次的会话。
       *
       * switchWorkspace 只保证"落在正确的目录里"，恢复哪一条得问后端记忆
       * （没有显式记忆时它会给出该目录最近一次会话，所以第二个及以后的工作区
       * 同样有记忆）。列表里找不到也照样打开：列表最多 100 条，记忆可能更早。
       */
      const memory = await persistedSession(folder);
      if (memory.sessionPath) await open(memory.sessionPath);
      await refresh(folder);
      useUI.getState().showNotice(`已切到 ${baseName(folder)}`, 'success');
      return true;
    } finally {
      useUI.getState().setSwitchingWorkspace(false);
    }
  }

  /**
   * 点开会话。
   *
   * 会话可能属于**别的工作区**（侧栏现在同时列出所有分组）：那种情况下必须先把
   * 工作区切过去再打开——会话进程是按工作目录起的，在错误的目录里打开它，
   * 模型看到的文件树与相对路径全都不对。
   */
  async function openSession(folder: string, sessionPath: string) {
    if (!sameWorkspace(folder, active)) {
      // 切换会顺带恢复该工作区的"上次会话"，随后再打开用户点的这一条。
      const ok = await switchFolder(folder);
      if (!ok && !sameWorkspace(useRuntime.getState().cwd, folder)) return; // 切换失败就别再往下走
    }
    /**
     * ⚠️ 这里曾经有一段"这条会话属于角色 X → 先 switchRole 再打开"的逻辑，
     * 以及"回合在跑时弹确认框（中断并切换 / 只打开会话）"的兜底。
     *
     * **一进程一会话之后整段都删掉了**，理由：
     *   · 打开会话 = 换跟哪个进程说话，**不重启任何东西**；角色是那条会话的进程
     *     启动时就带上的，切过去它本来就在自己的角色里（冷启动时由 Go 侧按
     *     会话记住的角色起进程，见 navigateLocked 里的 roleForSession）。
     *   · 旧的 switchRole 会走 Stop()（把所有会话进程一起杀掉）+ Start()，
     *     那才是"点一下会话就把正在跑的回合弄没"的元凶——现在它只会出现在
     *     用户**主动**从角色菜单切换时（RoleBadge 有确认框）。
     * 保留这段注释是为了别有人再把它加回来：点会话不该有破坏力。
     */
    // 用户点了它：绿点立刻消失（不等切换成功——他的意图就是"我要看这条"）
    useSessionActivity.getState().markSeen(sessionPath);
    await open(sessionPath);
  }

  /**
   * 删除一个工作区（用户要求）。
   *
   * 三层保护：
   *   · 默认工作区（exe 同目录的 workspace/）不给删——它是"永远留着的那块地方"；
   *   · 正在使用的工作区不给删：先弹一个"正在使用"的模态说明为什么，
   *     只有它里面的会话全部停下来（也就是切走）之后才删得掉；
   *   · 其余情况弹确认框，说清楚"删的是会话记录，磁盘目录不动"。
   */
  function deleteFolder(folder: string) {
    if (sameWorkspace(folder, defaultWorkspace)) {
      useConfirm.getState().ask({
        title: '默认工作区不能删除',
        description: `${folder} 是应用自带的默认工作区，留着它才能保证任何时候都有地方可以干活。`,
        alertOnly: true,
      });
      return;
    }
    if (sameWorkspace(folder, active)) {
      useConfirm.getState().ask({
        title: '工作区正在使用',
        description: `ZeroA 当前就在「${baseName(folder)}」里工作，它名下的会话还在运行。\n`
          + '先切换到别的工作区，等这里的会话全部停下来，再回来删除。',
        alertOnly: true,
      });
      return;
    }
    const group = sessionsOf(byWorkspace, folder);
    useConfirm.getState().ask({
      title: `删除工作区「${baseName(folder)}」？`,
      description: `会删掉它的 ${group.items.length} 条会话记录（.jsonl），并从侧栏移除。\n`
        + `磁盘上的目录 ${folder} 不会被删除。删掉就找不回来了。`,
      confirmLabel: '删除工作区',
      danger: true,
      onConfirm: async () => {
        try {
          const result = await piBridge.deleteWorkspace(folder);
          if (!result.success) {
            // 后端是权威判定：它拒绝时把原因交给输入框上方的报错条，
            // 而不是再弹一个只有"知道了"的框（那是模态的滥用）。
            reportError(result.error || '删除工作区失败', '删除工作区');
            return;
          }
          const payload = parseData<{ sessions?: number }>(result);
          removeWorkspace(folder);
          useUI.getState().showNotice(`已删除工作区（${payload?.sessions ?? 0} 条会话）`, 'success');
        } catch (error) {
          // 删除工作区的失败要显示在输入框上方（应用层报错），不是弹窗
          reportError(error, '删除工作区');
        }
      },
    });
  }

  /** 在某个文件夹里新建会话：不是当前工作区就先切过去，再 new_session */
  async function newSessionIn(folder: string) {
    if (!await switchFolder(folder)) return;
    /**
     * ⚠️ 判据是"启动参数就绪"，**不是**"此刻有没有进程"。
     *
     * 懒启动之后一个进程都没有是正常状态（刚进应用、刚打开一条历史会话），
     * 而新建会话本来就不需要进程（进程等第一条消息才建）。
     * 以前这里读 connected：进程崩过一次之后它是 false，于是"新建"变成
     * 静默返回——用户看到的就是"点了没反应"，而那正是这一版要根治的问题。
     */
    if (!useRuntime.getState().paramsReady) {
      // 真的还没准备好（启动没跑完/授权没过）：说出来，别让它表现成"没反应"
      useUI.getState().showNotice('还没准备好，等启动完成后再试');
      return;
    }
    await create();
    // 新会话立刻记上"它属于当前角色"：这样它的角色记忆从第一条消息之前就存在，
    // 下次打开这条会话能直接切回同一个角色（不用等到切角色那一步才写）。
    const roleId = useRoles.getState().selected;
    const sessionPath = useSessions.getState().activePath;
    if (roleId && sessionPath) void useRoles.getState().rememberSessionRole(sessionPath, roleId);
  }

  /**
   * 删除会话要弹窗确认。
   *
   * 正在使用的那个也允许删（用户要求）：后端会先让 Pi 成功切到新会话，再删旧文件，
   * 避免失败时丢失历史，也避免它继续往已经不存在的文件里写。
   */
  function confirmRemoveSession(folder: string, path: string, title: string) {
    const isActive = path === sessionFile;
    useConfirm.getState().ask({
      title: `删除会话「${title || path.split(/[\\/]/).pop()}」？`,
      description: isActive
        ? '这是当前正在使用的会话：删除后会立刻开一个新会话，界面也会清空。文件删掉就找不回来了。'
        : '会话文件（.jsonl）会被删除，删掉就找不回来了。',
      confirmLabel: '删除会话',
      danger: true,
      onConfirm: async () => {
        // 传 folder：这条会话可能属于非当前工作区分组，刷新要落在正确的分组上
        await remove(path, folder);
        if (isActive) {
          useTimeline.getState().reset();
          useRuntime.getState().setRunStartedAt(0);
        }
      },
    });
  }

  /** 选择文件夹：系统选择器 → 切工作区 */
  async function pickFolder() {
    let picked: string | string[] | null = null;
    try {
      picked = await Dialogs.OpenFile({
        Title: '选择工作区文件夹',
        CanChooseDirectories: true,
        CanChooseFiles: false,
        Directory: active || undefined,
      });
    } catch (error) {
      // 用户点「取消」不是错误：Wails 会把 cancelled by user 当异常抛出来。
      // 直接吞掉，否则输入框上方会冒一条莫名其妙的报错条。
      const message = String(error);
      if (/cancel/i.test(message)) return;
      reportError(message, '选择文件夹');
      return;
    }
    const path = Array.isArray(picked) ? picked[0] : picked;
    if (!path) return;
    addWorkspace(path);
    await switchFolder(path);
  }

  return (
    <aside className="flex h-full min-h-0 flex-col bg-panel">
      {/* 工作区标题行吸顶：会话多了以后列表自己滚，标题不被顶走 */}
      <div className="flex shrink-0 items-center justify-between px-3.5 py-2.5">
        <span className="text-[12px] text-mute">工作区</span>
        <span className="flex items-center gap-0.5">
          <IconAction icon="search" label="搜索对话" active={filterOpen} onClick={() => setFilterOpen(v => !v)} />
          <IconAction icon="folderPlus" label="选择文件夹" disabled={switching} onClick={() => void pickFolder()} />
        </span>
      </div>

      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-2.5 py-2">
        {filterOpen && (
          <input
            autoFocus
            value={keyword}
            onChange={e => setKeyword(e.target.value)}
            placeholder="按标题过滤…"
            className="mb-1.5 h-7 w-full rounded-lg bg-panel3 px-2.5 text-[12px] outline-none placeholder:text-mute"
          />
        )}

        <div className="flex flex-col gap-0.5">
          {folders.map(folder => {
            const isActive = sameWorkspace(folder, active);
            const expanded = collapsed[folder] !== true;
            return (
              <div key={folder}>
                <div className={cn('group flex items-center gap-0.5 rounded-lg', isActive && 'bg-panel2/60')}>
                  {/* 折叠箭头：只管展开/收起，不切工作区 */}
                  <button
                    type="button"
                    aria-label={expanded ? '折叠会话' : '展开会话'}
                    aria-expanded={expanded}
                    onClick={() => toggleCollapsed(folder, expanded)}
                    className="grid h-6 w-5 shrink-0 place-items-center text-mute hover:text-ink"
                  >
                    <Icon name="chevron" className={cn('h-3.5 w-3.5 transition-transform', !expanded && '-rotate-90')} />
                  </button>

                  <button
                    type="button"
                    title={folder}
                    disabled={switching}
                    onClick={() => void switchFolder(folder)}
                    className={cn(
                      'flex min-w-0 flex-1 items-center gap-2 rounded-lg px-1 py-1.5 text-left text-[13px] transition-colors',
                      isActive ? 'text-ink' : 'text-dim hover:bg-panel2 hover:text-ink',
                    )}
                  >
                    <Icon name="folder" className={cn('h-4 w-4 shrink-0', isActive ? 'text-info' : 'text-mute')} />
                    <span className="min-w-0 flex-1 truncate">{baseName(folder)}</span>
                  </button>

                  {/* 文件夹右侧：新建会话 + 删除工作区。
                      默认工作区不给删（exe 同目录那一块永远留着），所以那一行不放删除按钮。 */}
                  <IconAction
                    icon="plus"
                    label={`在 ${baseName(folder)} 新建会话`}
                    disabled={switching}
                    onClick={() => void newSessionIn(folder)}
                  />
                  {sameWorkspace(folder, defaultWorkspace) ? (
                    <span
                      title={`${folder} 是默认工作区，不能删除`}
                      className="grid h-6 w-6 shrink-0 place-items-center text-caption"
                    >
                      <Icon name="shield" className="h-3.5 w-3.5" />
                    </span>
                  ) : (
                    <IconAction
                      icon="trash"
                      label={`删除工作区 ${baseName(folder)}`}
                      disabled={switching}
                      onClick={() => deleteFolder(folder)}
                    />
                  )}
                </div>

                {/* 每个展开的工作区各自列出自己的会话（照 DSH 的 ui-workspace：
                    "Each Workspace keeps its own Sessions"）。以前只渲染当前工作区
                    那一组，别的文件夹展开是空的——用户要的是"展开了就显示"。 */}
                {expanded && (() => {
                  const group = sessionsOf(byWorkspace, folder);
                  const rows = group.items.filter(
                    item => !query
                      || item.title.toLowerCase().includes(query)
                      || (item.name ?? '').toLowerCase().includes(query),
                  );
                  // 这个工作区里有没有"刚新建、还没落盘成列表行"的会话
                  const draftHere = currentNamed === false
                    && sameWorkspace(folder, cwd)
                    && !group.items.some(item => item.path === activePath);
                  return (
                    <div className="mt-0.5 ml-5 flex flex-col border-l border-line pl-2">
                      {/*
                        刚新建、还没发消息的会话：这里显示一条占位行。
                        没有它的话，用户点完"新建会话"看到的是"这个目录还没有对话记录"
                        ——明明会话已经建好甚至在跑了（用户报过这个）。
                      */}
                      {draftHere && (
                        <div className="group my-0.5 flex items-center gap-2 rounded-lg bg-panel3 px-2 py-1.5">
                          <Icon name="chat" className="h-3.5 w-3.5 flex-none text-mute" />
                          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">新建会话</span>
                          {/*
                            占位行还没有落盘的会话文件，活动表里查不到它（表是按路径建的键）：
                            它是不是在跑只能看**前台**这条的状态。而它完全可能正在跑——
                            用户刚点完"新建会话"就发了第一条消息，那时会话文件还没生成。
                          */}
                          <SessionActivityMark
                            activity={isBusyStatus(status)
                              ? {
                                runtimeId: '',
                                running: true,
                                done: false,
                                // 取整规则同 liveActivity：非零至少显示 1%
                                percent: contextPercent > 0 ? Math.max(1, Math.round(contextPercent)) : 0,
                              }
                              : undefined}
                            isCurrent
                          />
                          <span className="flex-none text-[10.5px] text-caption">待命名</span>
                        </div>
                      )}

                      {group.loading && group.items.length === 0 && !draftHere ? (
                        <p className="py-1.5 text-[12px] text-mute">读取会话…</p>
                      ) : rows.length === 0 && !draftHere ? (
                        <p className="py-1.5 text-[12px] leading-[18px] text-mute">
                          {group.items.length === 0
                            ? (searching ? '没有匹配的对话' : '这个目录还没有对话记录')
                            : '没有匹配的对话'}
                        </p>
                      ) : null}

                      {rows.map(item => {
                        /**
                         * "是不是当前这条会话"用**规范化后的路径**比。
                         *
                         * 列表来自目录扫描、sessionFile 来自 get_state，说的是同一个文件，
                         * 但磁盘路径出现过 `C:\a\b.jsonl` 与 `c:/a/b.jsonl` 两种写法
                         * （理由同 stores/sessions.ts 的 workspaceKey）。只比字符串的话，
                         * 当前这条会被当成"别的会话"，于是它自己跑完时会挂上一个绿点
                         * ——提示用户去看他正看着的那条会话。
                         */
                        const isCurrent = Boolean(sessionFile) && activityKey(item.path) === activityKey(sessionFile);
                        if (renaming?.path === item.path) {
                          return (
                            <input
                              key={item.path}
                              autoFocus
                              value={renaming.value}
                              onChange={e => setRenaming({ path: item.path, value: e.target.value })}
                              onKeyDown={e => {
                                if (e.key === 'Enter') {
                                  void rename(item.path, renaming.value, folder);
                                  setRenaming(null);
                                }
                                if (e.key === 'Escape') setRenaming(null);
                              }}
                              onBlur={() => setRenaming(null)}
                              className="my-0.5 h-7 rounded-lg bg-panel3 px-2 text-[12.5px] outline-none"
                            />
                          );
                        }

                        // 这条会话是在哪个角色下产生的（侧栏徽标用）。
                        // 两种写法都试：后端按规范化路径去重，但历史记录里可能残留
                        // 早期版本写下的不同拼写（大小写/分隔符）。
                        const sessionRole = roleById(
                          roles,
                          roleBySession[item.path] ?? roleBySession[workspaceKey(item.path)] ?? '',
                        );
                        /**
                         * 状态指示的数据：后台那些行查活动表，**当前这一行**再叠一层
                         * 前台实时状态（见 liveActivity 的说明——启动时表里可能还没有它）。
                         */
                        const stored = activityByKey[activityKey(item.path)];
                        const activity = isCurrent ? liveActivity(stored, status, contextPercent) : stored;
                        return (
                          <div key={item.path} className="group flex items-center">
                            <button
                              type="button"
                              disabled={switching}
                              onClick={() => {
                                /**
                                 * 点这一行 = "我要看它"：绿点**立刻**消失，不等切换做完。
                                 *
                                 * 等切换的话，绿点会在"切工作区/切角色"那几秒里继续挂着，
                                 * 用户会以为自己的点击没生效；切换万一失败（或要等一个确认框），
                                 * 那个点还会一直留着。
                                 */
                                useSessionActivity.getState().markSeen(item.path);
                                void openSession(folder, item.path);
                              }}
                              title={item.path}
                              className={cn(
                                'flex min-w-0 flex-1 items-center gap-2 rounded-lg px-2 py-1.5 text-left transition-colors',
                                isCurrent ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink',
                              )}
                            >
                              <Icon name="chat" className="h-3.5 w-3.5 flex-none text-mute" />
                              <span className="min-w-0 flex-1 truncate text-[12.5px]">{item.title || '新建会话'}</span>
                              {/* 状态指示：在跑 = 上下文占用圆环；跑完且不是当前会话 = 绿点 */}
                              <SessionActivityMark activity={activity} isCurrent={isCurrent} />
                              {/* 角色徽标：同一个工作区里可能既有 CTF 会话又有应急会话，
                                  没有这个标记它们看起来完全一样 */}
                              {sessionRole && (
                                <span
                                  title={`角色：${sessionRole.name}`}
                                  className="flex-none text-[10.5px] text-caption group-hover:hidden"
                                >
                                  {sessionRole.name}
                                </span>
                              )}
                              <span className="flex-none text-[11px] text-caption group-hover:hidden">{relativeTime(item.updatedAt)}</span>
                            </button>
                            <span className="hidden flex-none gap-0.5 group-hover:flex">
                              <IconAction icon="tag" label="重命名" onClick={() => setRenaming({ path: item.path, value: item.name || item.title })} />
                              <IconAction icon="x" label="删除" onClick={() => confirmRemoveSession(folder, item.path, item.title)} />
                            </span>
                          </div>
                        );
                      })}
                    </div>
                  );
                })()}
              </div>
            );
          })}
        </div>
      </div>
      {/* 左下角工具条：查询状态 / 主题 / 设置 / 更多（原来在右上角，按用户要求挪到这里） */}
      <div className="flex shrink-0 items-center gap-0.5 px-2 py-1.5">
        <IconAction icon="db" label="查询状态" onClick={() => void refreshState()} />
        <IconAction icon={theme === 'dark' ? 'sun' : 'moon'} label="切换主题" onClick={useUI.getState().toggleTheme} />
        <IconAction icon="gear" label="设置" onClick={() => useUI.getState().setSettingsOpen(true)} />
        <span className="flex-1" />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="icon-sm" variant="ghost" aria-label="更多" className="h-6 w-6">
              <Icon name="chevron" className="h-3.5 w-3.5" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" side="top">
            <DropdownMenuItem onSelect={() => void loadHistory()}>
              <Icon name="db" className="h-4 w-4" /> 重新载入会话消息
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => void refreshStats()}>
              <Icon name="list" className="h-4 w-4" /> 刷新用量统计
            </DropdownMenuItem>
            {/* 长会话的常见动作：token 快满时手动压一次上下文 */}
            <DropdownMenuItem
              onSelect={() => {
                void piBridge.compact('').then(reportCompactResult);
              }}
            >
              <Icon name="db" className="h-4 w-4" /> 压缩上下文
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            {/* 分支树：会话是只追加的树，分支之后要看"另一条还在不在"就靠它 */}
            <DropdownMenuItem
              onSelect={() => {
                void piBridge.getLastAssistantText().then(result => {
                  if (!result.success) {
                    reportError(result.error || '读取失败', '复制回答');
                    return;
                  }
                  const text = parseData<{ text?: string }>(result)?.text ?? '';
                  if (!text) {
                    useUI.getState().showNotice('还没有可复制的回答');
                    return;
                  }
                  void Clipboard.SetText(text);
                  useUI.getState().showNotice(`已复制上一条回答（${text.length} 字）`, 'success');
                });
              }}
            >
              <Icon name="copy" className="h-4 w-4" /> 复制上一条回答
            </DropdownMenuItem>            <DropdownMenuItem onSelect={() => useUI.getState().setBashOpen(true)}>
              <Icon name="terminal" className="h-4 w-4" /> 运行命令
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => useUI.getState().setTreeOpen(true)}>
              <Icon name="chat" className="h-4 w-4" /> 分支树
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => useUI.getState().setRawLogOpen(true)}>
              <Icon name="terminal" className="h-4 w-4" /> 原始事件日志
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => useUI.getState().toggleEvents()}>
              <Icon name="tag" className="h-4 w-4" /> 事件映射
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => useTimeline.getState().reset()}>
              <Icon name="x" className="h-4 w-4" /> 清空时间线
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </aside>
  );
}

function IconAction({
  icon,
  label,
  onClick,
  active,
  disabled,
}: {
  icon: 'search' | 'gear' | 'folderPlus' | 'tag' | 'x' | 'plus' | 'db' | 'sun' | 'moon' | 'trash' | 'shield';
  label: string;
  onClick(): void;
  active?: boolean;
  disabled?: boolean;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          size="icon-sm"
          variant="ghost"
          aria-label={label}
          disabled={disabled}
          className={cn('h-6 w-6', active && 'bg-panel3 text-ink')}
          onClick={onClick}
        >
          <Icon name={icon} className="h-3.5 w-3.5" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
