import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Icon } from '@/components/ui/Icon';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { reportError } from '@/lib/reportError';
import { useRoles } from '@/stores/roles';
import type { RoleIcon } from '@/types/role';
import { cn } from '@/lib/utils';
import { TimelineItems } from '@/features/chat/TimelineItems';
import { ThinkingCard } from '@/features/chat/ThinkingCard';
import { ThinkingRow } from '@/features/chat/ThinkingRow';
import { SystemCard } from '@/features/chat/SystemCard';
import { Spinner } from '@/components/ui/Spinner';
import { ExtensionCard, SettledDivider } from '@/features/chat/ExtensionCard';
import { useAgent } from '@/hooks/useAgent';
import { useRuntime } from '@/stores/runtime';
import { useSessions } from '@/stores/sessions';
import { useTimeline, type TimelineItem } from '@/stores/timeline';
import { useUI } from '@/stores/ui';
import { normPath, useChangedFiles } from '@/stores/changedFiles';

/** 窗口化渲染的步长：默认只渲染最后 150 条，点一次「显示更早的」多放 150 条 */
const WINDOW_STEP = 150;

export function ChatTimeline() {
  const activeRoleList = useRoles(s => s.roles);
  const selectedRole = useRoles(s => s.selected);
  const activeRole = activeRoleList.find(role => role.id === selectedRole);
  const items = useTimeline(s => s.items);
  const connected = useRuntime(s => s.connected);
  /**
   * 当前在看哪条会话进程。
   *
   * 它是"该问谁要可分支消息"的一部分：换了会话就必须重新问一次
   * （entryId 是**那条会话**里的游标，拿着上一条会话的 entryId 去 fork
   *  会分支出别的东西来）。所以它出现在下面那个 effect 的依赖里。
   */
  const runtimeId = useRuntime(s => s.runtimeId);
  const cwd = useRuntime(s => s.cwd);
  const exeDir = useRuntime(s => s.exeDir);
  /** 本回合还在等第一次输出（发送中 / 已开始但一个字都还没吐）：显示"正在思考……" */
  const sending = useRuntime(s => s.sending);
  const status = useRuntime(s => s.status);
  const filesOpen = useUI(s => s.filesOpen);
  const switching = useUI(s => s.switchingWorkspace);
  /**
   * 可分支的用户消息（Pi 的 get_fork_messages 给 entryId）。
   * 会话是一棵只追加的树，entryId 是稳定游标；界面上按**文本**把它配回具体那条消息。
   * 每次时间线变化都重新取一次：发完新消息、分支之后列表都会变。
   */
  const [forkTargets, setForkTargets] = useState<{ entryId: string; text: string }[]>([]);
  const { forkFrom } = useAgent();
  useEffect(() => {
    if (!connected) return;
    /**
     * 组件已卸载（或依赖又变了一次）之后，这次请求回来就不该再 setState。
     *
     * 切换会话时这个组件不会重挂载，但请求会连发几次：先到的旧响应若在后到的
     * 之后落地，界面上的分支按钮就会配到**别的会话**的消息上（点了分支出来的是
     * 另一条会话的分支）。alive 这个开关保证只有最后一次请求能写状态。
     */
    let alive = true;
    void piBridge.getForkMessages()
      .then(result => {
        if (!alive || !result.success) return;
        const payload = parseData<{ messages?: { entryId: string; text: string }[] }>(result);
        setForkTargets(payload?.messages ?? []);
      })
      .catch(() => { /* 拿不到就不显示分支按钮，不影响对话 */ });
    return () => { alive = false; };
  }, [connected, runtimeId, items.length]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  /** 每一轮用户消息的节点：右侧会话条用它做"跳到这一轮" */
  const anchorsRef = useRef<Map<number, HTMLElement>>(new Map());
  const [activeAnchor, setActiveAnchor] = useState(0);
  const debug = useRuntime(s => s.debug);
  /** 调试指标：scrollTop / scrollHeight / clientHeight / 距底距离 / 是否跟随 */
  const [metrics, setMetrics] = useState({ top: 0, height: 0, client: 0, stick: true });

  /** 把当前滚动状态记下来（调试模式下会渲染成一行文本，供自动化读取） */
  function captureMetrics(el: HTMLElement) {
    setMetrics({
      top: Math.round(el.scrollTop),
      height: Math.round(el.scrollHeight),
      client: Math.round(el.clientHeight),
      stick: stick.current,
    });
  }
  const anchors = items.filter(item => item.kind === 'user');

  /**
   * 窗口化渲染：items 很多时只渲染**最后** WINDOW_STEP 条。
   *
   * 为什么需要：时间线是纯 DOM 渲染（没有虚拟列表），一条记录里可能有 Markdown 正文
   * 与好几个工具卡片，长会话几百条就是上千个组件——切换会话时"全量挂载"的开销
   * 比取数据本身还大。只渲染尾部既符合"打开就停在最新位置"的直觉，
   * 也不影响流式追加：新内容永远落在渲染范围里。
   */
  const [visibleCount, setVisibleCount] = useState(WINDOW_STEP);
  /**
   * 换了会话就重置：上一条会话点开的"更早"不该带到这一条上。
   *
   * 两个依赖都要：sessionFile 是"会话文件换了"（包括热切换切回来），
   * runtimeId 是"进程换了"（新建会话时 sessionFile 还是空的，只有它一定变）。
   */
  const sessionFile = useRuntime(s => s.sessionFile);
  useEffect(() => { setVisibleCount(WINDOW_STEP); }, [sessionFile, runtimeId]);
  const hiddenCount = Math.max(0, items.length - visibleCount);
  const visibleItems = hiddenCount > 0 ? items.slice(hiddenCount) : items;

  /**
   * 展开更早的内容。
   *
   * 展开是在**顶部插入**节点，浏览器不会替我们保持视口：不补差值的话，
   * 用户点一下按钮，眼前的内容会被整体顶下去一大截（看着像跳到了别处）。
   * 所以先记下"距底距离"，渲染完再按它还原——视口里的内容停在原地。
   */
  const keepBottomGap = useRef<number | null>(null);
  function showEarlier() {
    const el = scrollRef.current;
    if (el) keepBottomGap.current = el.scrollHeight - el.scrollTop;
    setVisibleCount(count => count + WINDOW_STEP);
  }
  useLayoutEffect(() => {
    const el = scrollRef.current;
    const gap = keepBottomGap.current;
    if (!el || gap === null) return;
    keepBottomGap.current = null;
    el.scrollTop = el.scrollHeight - gap;
  }, [visibleCount]);

  /** 待跳转的锚点：目标还在折叠区里时先把它放出来，等 DOM 出现再滚（见 jumpTo） */
  const pendingJump = useRef<number | null>(null);

  /** 滚到某一轮：会话条上点一下就跳过去 */
  function jumpTo(id: number) {
    const node = anchorsRef.current.get(id);
    if (node) {
      node.scrollIntoView({ block: 'start', behavior: 'smooth' });
      return;
    }
    /**
     * 目标落在折叠区里：先展开到它，再滚过去。
     *
     * 取舍：另一条路是"把落在折叠区的锚点禁掉"，实现更短，但右侧会话条的意义
     * 就是"任意一轮都能跳"——长会话里最需要导航的恰恰是早期那几轮，禁掉等于废了导航。
     * 展开的代价只是多渲染 150 条，而且用户点的本来就是"我要看更早的那一轮"。
     */
    const index = items.findIndex(item => item.id === id);
    if (index < 0) return;
    // 目标其实在可见范围里（只是节点还没挂上）：只等下一次渲染，不折腾窗口大小
    if (index >= items.length - visibleCount) return;
    pendingJump.current = id;
    // 只增不减：绝不能在"点了锚点"时把用户已经展开的窗口收回去
    setVisibleCount(count => Math.max(count, items.length - index));
  }
  useLayoutEffect(() => {
    const id = pendingJump.current;
    if (id === null) return;
    const node = anchorsRef.current.get(id);
    if (!node) return; // 目标还没渲染出来（可见条数仍不够）：留着下次再滚
    pendingJump.current = null;
    node.scrollIntoView({ block: 'start' });
  }, [visibleCount]);

  /** 当前在哪一轮：取最靠近视口顶部的那条锚点 */
  function updateActiveAnchor(container: HTMLElement) {
    let best = 0;
    anchors.forEach((anchor, index) => {
      const node = anchorsRef.current.get(anchor.id);
      if (!node) return;
      if (node.offsetTop - container.scrollTop <= 80) best = index;
    });
    setActiveAnchor(best);
  }
  const workspace = (cwd || exeDir || '').replace(/[\\/]+$/, '');

  /** 本轮改动（当前工作区内）：顶栏按钮的徽标计数与空态提示都用它 */
  const touched = useChangedFiles(s => s.touched);
  const workspaceNorm = normPath(workspace);
  const touchedCount = workspaceNorm
    ? touched.filter(item => normPath(item.path).startsWith(`${workspaceNorm}/`)).length
    : 0;

  /** 顶栏「本轮改动」按钮：打开文件面板并切到改动视图（空清单也开——空态会解释什么会出现在这里） */
  function showChangedFiles() {
    const ui = useUI.getState();
    if (!ui.filesOpen) ui.toggleFiles();
    ui.focusChangedFiles();
  }

  /** 导出会话 HTML：Pi 的 export_html 会把当前会话写成一份自包含的 HTML */
  async function exportHtml() {
    // 空会话先拦下来，别把 Pi 的英文错误丢给用户。
    // 注意不能只判断 items.length：错误行本身也是 timeline item，
    // 报过一次错之后 length 就永远 > 0，预检会形同虚设。
    // 只认真正的对话内容——用户消息或助手回复。
    if (!items.some(item => item.kind === 'user' || item.kind === 'assistant')) {
      useUI.getState().showNotice('当前会话还没有消息，先聊一轮再导出');
      return;
    }
    try {
      const result = await piBridge.exportHTML('');
      if (!result.success) {
        // Pi 在"没有可导出内容"时会这么回，翻成人话
        const raw = result.error || '导出失败';
        reportError(/nothing to export/i.test(raw) ? '这个会话还没有可导出的内容，先聊一轮' : raw, '导出会话');
        return;
      }
      const payload = parseData<{ path?: string }>(result);
      useUI.getState().showNotice(payload?.path ? `已导出：${payload.path}` : '已导出会话 HTML', 'success');
    } catch (error) {
      reportError(error, '导出会话');
    }
  }

  // 贴底策略（踩过坑的地方，改动前先读完）：
  //   · 刚发出用户消息 → 强制贴底
  //   · 流式追加内容   → 用户本来就在底部时持续跟随
  //
  // ⚠️ stick 只能由「用户主动往上滚」关闭。早期版本在 onScroll 里写成
  //     stick = (scrollHeight - scrollTop - clientHeight < 80)
  // 看着合理，实际在流式下会自锁失效：我们刚滚到底 → 浏览器还没派发 scroll 事件时
  // 新 delta 又让内容长高 → 事件派发时算出的间距已 > 80 → stick 被判 false，
  // 从此再也不跟随。现象就是"发完消息不往下滚"。
  const lastItem = items.length ? items[items.length - 1] : undefined;
  /**
   * 还在等这一轮的第一次输出。
   *
   * 判定用两件事，缺一不可：
   *   · **最后一条记录是用户消息**——一旦助手气泡、工具卡片或系统行出现，
   *     就说明模型已经开口了，这句话就该让位（这是"模型还没吐第一个字"的准确表达，
   *     比"agent_start 到了没"更贴近用户看到的东西：agent_start 只是"回合开始了"，
   *     之后模型还可能安静地想很久）；
   *   · 本回合仍在进行（sending = prompt 还在路上，包含懒启动起进程的那 1–3.5 秒；
   *     running/retrying/compacting = 回合已经跑起来）。
   */
  const waitingFirstOutput = Boolean(
    lastItem?.kind === 'user'
    && (sending || status === 'running' || status === 'retrying' || status === 'compacting'),
  );
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (lastItem?.kind === 'user') stick.current = true;
    if (stick.current) el.scrollTop = el.scrollHeight;
    // 采集给调试探针：流式期间也要能看到"是否跟随、离底多远"
    setMetrics({
      top: Math.round(el.scrollTop),
      height: Math.round(el.scrollHeight),
      client: Math.round(el.clientHeight),
      stick: stick.current,
    });
  }, [items, lastItem]);

  // 高度变化也要跟随（图片加载、字体切换、表格重排不一定走 items 更新）
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const inner = el.firstElementChild;
    if (!inner) return;
    const observer = new ResizeObserver(() => {
      if (stick.current) el.scrollTop = el.scrollHeight;
    });
    observer.observe(inner);
    return () => observer.disconnect();
  }, []);

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      {/* 会话标题栏：文件夹图标 + 工作区名（点击打开该文件夹）+ 导出 HTML */}
      <div className="flex h-9 shrink-0 items-center gap-2 px-4">
        <button
          type="button"
          title={workspace ? `打开文件夹：${workspace}` : '还没有工作目录'}
          disabled={!workspace}
          onClick={() => {
            void piBridge.openPath(workspace).catch(error => reportError(error, '打开文件夹'));
          }}
          className="inline-flex min-w-0 items-center gap-1.5 text-[12.5px] text-dim hover:text-ink disabled:opacity-50"
        >
          <Icon name="folder" className="h-4 w-4 text-info" />
          <span className="min-w-0 truncate">{workspace ? workspace.split(/[\\/]/).pop() : '未设置工作目录'}</span>
        </button>
        <span className="flex-1" />
        {debug && (
          <span className="shrink-0 font-mono text-[10.5px] text-warn" title="PI_GUI_DEBUG=1">
            {`top=${metrics.top} h=${metrics.height} c=${metrics.client} gap=${Math.max(0, metrics.height - metrics.top - metrics.client)} stick=${metrics.stick ? 1 : 0}`}
          </span>
        )}

        {/* 本轮改动：点击在右侧文件面板里看这一轮写过的文件（有改动时带数量徽标） */}
        <button
          type="button"
          aria-label="本轮改动"
          title="本轮改动：在文件面板中查看这一轮写过的文件"
          onClick={showChangedFiles}
          className="relative grid h-6 w-6 shrink-0 place-items-center rounded text-mute transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="pencil" className="h-4 w-4" />
          {touchedCount > 0 && (
            <span className="absolute -right-1 -top-1 grid min-w-[13px] place-items-center rounded-full bg-info px-0.5 text-[9px] font-medium leading-[13px] text-panel">
              {touchedCount}
            </span>
          )}
        </button>

        {/* 顺序按用户要求调换：导出在左、文件面板开关在右 */}
        <button
          type="button"
          aria-label="导出 HTML"
          onClick={() => void exportHtml()}
          disabled={!connected}
          title="把当前会话导出成 HTML 文件（ZeroA 的 export_html）"
          className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute transition-colors hover:bg-hover hover:text-ink disabled:opacity-40"
        >
          <Icon name="file" className="h-4 w-4" />
        </button>

        {/* 工作区文件面板开关（照 DSH 的分栏按钮） */}
        <button
          type="button"
          aria-label="工作区文件"
          aria-pressed={filesOpen}
          title="工作区文件"
          onClick={() => useUI.getState().toggleFiles()}
          className={cn(
            'grid h-6 w-6 shrink-0 place-items-center rounded transition-colors',
            filesOpen ? 'bg-panel3 text-ink' : 'text-mute hover:bg-hover hover:text-ink',
          )}
        >
          <svg viewBox="0 0 16 16" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.4">
            <rect x="1.5" y="2.5" width="13" height="11" rx="2" />
            <path d="M9.5 2.5v11" />
          </svg>
        </button>
      </div>

      <div
        // 整块对话内容允许选中/复制（配合 tokens.css 的白名单）
        data-selectable
        // ⚠️ 这个 ref 曾经漏挂过：没有它 scrollRef.current 恒为 null，
        // 自动滚动就成了空操作（现象是"发完消息不往下滚"）。改动这行时务必保留。
        ref={scrollRef}
        className="min-h-0 flex-1 overflow-y-auto px-6 pt-4 pb-6"
        onScroll={e => {
          const el = e.currentTarget;
          // 只在真的滚到底时重新打开跟随；绝不在这里关闭它（原因见上方注释）
          if (el.scrollHeight - el.scrollTop - el.clientHeight < 24) stick.current = true;
          updateActiveAnchor(el);
        }}
        onWheel={e => {
          // 只有"用户主动往上滚"才停止跟随
          if (e.deltaY < 0) stick.current = false;
          captureMetrics(e.currentTarget);
        }}
      >
      {switching ? (
        /* 切换工作区：重启进程 + 恢复该目录的会话都在进行中。
           这里必须是转圈动画，而不是空态里那句"当前未连接"——用户点的是一个文件夹，
           界面却告诉他"未连接"，看起来像出错。 */
        <div className="mx-auto flex min-h-full max-w-[620px] flex-col items-center justify-center gap-3 px-4 text-center">
          <Spinner className="h-6 w-6 text-info" />
          <p className="text-[13px] text-mute">正在切换工作区…</p>
        </div>
      ) : items.length === 0 ? (
        /* 空态：只留一句大标题 + 当前角色。整块在滚动区里垂直居中。
           标题后面垫一团品牌色的柔光：空态是首屏也是门面，全灰太素，
           一点点品牌色把视线锚在标题上，又不抢内容。 */
        <div className="relative isolate mx-auto flex min-h-full max-w-[620px] flex-col items-center justify-center px-4 text-center">
          <div
            aria-hidden
            className="pointer-events-none absolute left-1/2 top-1/2 -z-10 h-64 w-64 -translate-x-1/2 -translate-y-1/2 rounded-full opacity-[0.14] blur-3xl dark:opacity-[0.2]"
            style={{ background: 'radial-gradient(circle, var(--brand) 0%, transparent 70%)' }}
          />
          <h2
            className="font-medium tracking-tight text-ink"
            style={{ fontSize: 'var(--hero-font-size, 30px)', lineHeight: 1.25 }}
          >
            我是 Zero<span className="font-semibold text-[var(--brand)]">A</span>，我们要做什么？
          </h2>
          {/* 当前角色：空态是"准备开始干活"的地方，这里说清"以什么身份干活"
              最有用——否则用户切了角色却不知道这一轮会用哪套提示词与工具。
              key 按角色 id 重挂载：切换成功后文本带 rise 动画出现一次，代替成功弹窗。 */}
          {activeRole && (
            <p key={activeRole.id} className="rise mt-5 flex items-center gap-1.5 text-[12.5px] text-dim">
              <Icon name={(activeRole.icon as RoleIcon) ?? 'robot'} className="h-3.5 w-3.5 text-mute" />
              <span>当前角色：{activeRole.name}</span>
              {activeRole.description && <span className="text-mute">· {activeRole.description}</span>}
            </p>
          )}
        </div>
      ) : (
        /* 渲染复用 TimelineItems：子智能体走的是同一个组件（见 AgentTranscript）。
           外面包一层 div 不是可有可无：下面那个 ResizeObserver 观察的是滚动容器的
           firstElementChild，包一层才能把"折叠提示 + 列表"一起纳入观察范围。 */
        <div>
          {/* 折叠提示必须在列表**顶部**（"更早的"就在上面），而且只在真有折叠时出现：
              空会话与错误态走的是上面两个分支，根本到不了这里。 */}
          {hiddenCount > 0 && (
            <div className="mx-auto mb-3 flex max-w-[860px] items-center justify-center">
              <button
                type="button"
                onClick={showEarlier}
                title={`还有 ${hiddenCount} 条更早的记录没有渲染（这一次放出 ${Math.min(WINDOW_STEP, hiddenCount)} 条）`}
                className="rounded-full border border-line bg-panel2 px-3 py-1 text-[12px] text-dim transition-colors hover:bg-hover hover:text-ink"
              >
                显示更早的 {Math.min(WINDOW_STEP, hiddenCount)} 条
              </button>
            </div>
          )}
          <TimelineItems
            items={visibleItems}
            registerAnchor={(id, el) => {
              if (el) anchorsRef.current.set(id, el);
              else anchorsRef.current.delete(id);
            }}
            forkFor={item => {
              // 只有 Pi 认可"可分支"的消息才给按钮（entryId 由 get_fork_messages 提供）
              const target = forkTargets.find(candidate => candidate.text === item.text);
              return target ? () => void forkFrom(target.entryId) : undefined;
            }}
          />
          {/* 「ZeroA 正在思考……」：发出去到模型吐第一个字之间的空窗期（见 ThinkingRow）。
              放在列表末尾而不是空态里：会话已经有内容时它同样要出现。 */}
          {waitingFirstOutput && <ThinkingRow className="mt-3" />}
        </div>
      )}
      </div>

      {/* 右侧会话条（照 DSH）：每一轮用户消息一条短横线，当前所在的那轮最宽最亮，
          点一下直接跳到那一轮。轮次少的时候不显示（没有导航价值）。 */}
      {anchors.length > 1 && (
        <div className="pointer-events-none absolute top-1/2 right-2 z-10 flex -translate-y-1/2 flex-col items-end gap-1.5">
          {anchors.map((anchor, index) => (
            <button
              key={anchor.id}
              type="button"
              onClick={() => jumpTo(anchor.id)}
              title={anchor.text.slice(0, 40) || `第 ${index + 1} 轮`}
              aria-label={`跳到第 ${index + 1} 轮`}
              className={cn(
                'pointer-events-auto h-[3px] rounded-full transition-all',
                index === activeAnchor ? 'w-4 bg-ink' : 'w-2.5 bg-line hover:bg-caption',
              )}
            />
          ))}
        </div>
      )}
    </div>
  );
}
