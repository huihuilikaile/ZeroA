import { useEffect, useState } from 'react';
import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';
import { TitleBar } from '@/features/shell/TitleBar';
import { Sidebar } from '@/features/shell/Sidebar';
import { SettingsDialog } from '@/features/shell/SettingsDialog';
import { ConfirmDialog } from '@/features/shell/ConfirmDialog';
import { StartupSplash } from '@/features/shell/StartupSplash';
import { Notifications } from '@/components/ui/Toast';
import { ChatTimeline } from '@/features/chat/ChatTimeline';
import { RolesPage } from '@/features/roles/RolesPage';
import { ToolsPage } from '@/features/tools/ToolsPage';
import { PermissionsPage } from '@/features/permissions/PermissionsPage';
import { AgentTabs } from '@/features/agents/AgentTabs';
import { AgentTranscript } from '@/features/agents/AgentTranscript';
import { Composer } from '@/features/chat/Composer';
import { FilePanel } from '@/features/files/FilePanel';
import { Splitter } from '@/components/ui/Splitter';
import { useWindowWidth } from '@/hooks/useWindowWidth';
import { RawLogDialog } from '@/features/console/RawLogDialog';
import { TreeDialog } from '@/features/console/TreeDialog';
import { BashDialog } from '@/features/console/BashDialog';
import { ExtensionDialog } from '@/features/console/ExtensionDialog';
import { useAgent } from '@/hooks/useAgent';
import { useCloseGuard } from '@/hooks/useCloseGuard';
import { usePiEvents } from '@/hooks/usePiEvents';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { useRuntime } from '@/stores/runtime';
import { persistedSession, useSessions, workspaceKey } from '@/stores/sessions';
import { useRoles } from '@/stores/roles';
import { applyFontSize, applyHeroFontSize, useUI } from '@/stores/ui';
import { useAgents } from '@/stores/agents';

interface Defaults {
  cwd?: string;
  exe?: string;
  exeDir?: string;
  sessionsRoot?: string;
  mode?: 'real' | 'mock';
  piPath?: string;
  piKind?: string;
  piVersion?: string;
  debug?: boolean;
  /** 上次用的工作区（后端持久化）：重启后回到这里 */
  lastWorkspace?: string;
  /** 侧栏的工作区列表（后端持久化） */
  workspaces?: string[];
  /** 诊断日志路径（会话记忆排查用） */
  logPath?: string;
  /** 默认工作区（exe 同目录的 workspace/）：侧栏里这一行不可删除 */
  defaultWorkspace?: string;
  /** 用户删过的工作区（规范化键）：本地残留要按它清掉 */
  dismissed?: string[];
  /** Pi 配置（settings.json）里的默认模型/提供方/思考级别：进程还没起时模型徽标靠它显示 */
  defaultModel?: string;
  defaultProvider?: string;
  defaultThinkingLevel?: string;
}

/**
 * 应用外壳：标题栏 + 顶栏 + 左栏（工作区） + 对话区 + 输入区。
 *
 * 布局要点（修过 bug，别改回去）：
 *   · 外层固定 h-screen + overflow-hidden，只有对话区滚动；
 *   · 网格行 minmax(0,1fr)，min-h-0 一路传下去；
 *   · 输入区 shrink-0，永远钉在底部。
 *
 * 启动时先显示启动屏（StartupSplash），背后按顺序做四件事：
 *   ① 读取默认工作目录与配置 → ② 启动 Pi → ③ 等模型列表 → ④ 读会话列表
 * 完成后启动屏淡出（leaving 态再挂 320ms），应用界面在底下接棒。
 * 整个过程不调用模型，不花钱。
 */
export function AppShell() {
  usePiEvents();
  // 关窗保护：还有回合在跑时先弹确认框（标题栏按钮与 Alt+F4 走同一套）
  useCloseGuard();
  const { start } = useAgent();

  const theme = useUI(s => s.theme);
  const showEvents = useUI(s => s.showEvents);
  const booting = useUI(s => s.booting);
  /**
   * 启动屏的淡出过渡：booting 变 false 后再挂 320ms（leaving 态，淡出动画 300ms），
   * 应用界面在底下先就位，启动屏淡出去——不是"闪没"。
   */
  const [splashMounted, setSplashMounted] = useState(true);
  useEffect(() => {
    if (booting) {
      setSplashMounted(true);
      return;
    }
    const timer = setTimeout(() => setSplashMounted(false), 320);
    return () => clearTimeout(timer);
  }, [booting]);
  /** 主区域显示哪一页：角色管理是独立页面（见 features/roles/RolesPage.tsx） */
  const page = useUI(s => s.page);
  /** 当前在看的子智能体（'' = 主会话）：有它时换成它的会话视图（输入框也换成它自己的） */
  const activeAgentId = useAgents(s => s.activeId);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  useEffect(() => {
    document.documentElement.dataset.events = showEvents ? 'on' : 'off';
  }, [showEvents]);

  useEffect(() => {
    let cancelled = false;
    const ui = useUI.getState();
    const startedAt = Date.now();
    /**
     * 排队中的会话切换：有回合在跑时后端只把目标排进队列，等这一轮结束才真正执行，
     * 执行完通过 pi:session 告诉界面（见 stores/sessions.ts 的 watch）。
     *
     * 订阅挂在这个启动 useEffect 里、清理函数里注销：React 严格模式会挂载 → 卸载 → 再挂载，
     * 不注销就会出现"同一条事件被处理两遍"。
     */
    const offSession = useSessions.getState().watch();
    // 启动动画的最短展示时长：会话是在启动阶段**预加载**的（见下面：
    // 列表扫描与后端记忆和"启动 Pi"并行做，Pi 一起来就 load_session），
    // 所以这里只保留"别闪一下"的最小值，不再为了走完整轮动画去补时间。
    const MIN_SPLASH_MS = 450;
    (async () => {
      try {
        ui.setBooting(true);

        // 首屏就套用记住的字号（对话正文 + 空态标题），避免加载完再"跳"一下
        applyFontSize(useUI.getState().fontSize);
        applyHeroFontSize(useUI.getState().heroFontSize);
        // 工具能力改为"默认全部"（设置里那一项已移除）：清掉可能残留的收窄值，
        // 否则老用户会被之前选过的"只读"继续限制，而界面上已经找不到那个开关了。
        useRuntime.setState({ startTools: '' });

        // 记住的"是否加载扩展/技能/模板"必须在启动 Pi **之前**推给 Go：
        // 它是启动参数，运行中改不了（改了要重启会话）。默认开，所以只有"关过"才需要推。
        try {
          const raw = localStorage.getItem('pi-gui-prefs');
          const saved = raw ? (JSON.parse(raw) as Record<string, unknown>) : {};
          if (saved.resources === '0') await piBridge.setLoadResources(false);
        } catch {
          /* 隐私模式等场景忽略：默认值就是"加载" */
        }

        const defaults = parseData<Defaults>(await piBridge.getDefaults());
        if (!cancelled && defaults) {
          const runtime = useRuntime.getState();
          /**
           * 上次用的是哪个工作区，**以后端为准**。
           *
           * 这里踩过一个大坑：WebView 的 localStorage 会随 WebView2 用户数据目录
           * 变化整体消失（实测 %LOCALAPPDATA%\EBWebView 里根本没有我们的键）。
           * 以前只信 localStorage，一旦丢失就退回默认目录，用户看到的现象就是
           * "重启后不是我上次点的那份对话"——因为恢复到的是另一个工作区的记忆。
           */
          let remembered = defaults.lastWorkspace || useRuntime.getState().cwd;
          // 用户删过的工作区不能复活：后端会把它从默认目录/上次工作区/记忆里
          // 全部摘掉，这里再按名单清一遍本地残留（localStorage 与后端文件是两套）。
          const dismissed = (defaults.dismissed ?? []).map(workspaceKey);
          useSessions.getState().dropWorkspaces(dismissed);
          if (remembered && dismissed.includes(workspaceKey(remembered))) remembered = '';
          // 目录可能被删/改名：那种情况下不要拿它去启动 Pi，否则每次开机都是一行报错。
          if (remembered) {
            try {
              const info = parseData<{ exists?: boolean; isDir?: boolean }>(await piBridge.statFolder(remembered));
              if (!info?.exists || info.isDir === false) remembered = '';
            } catch {
              remembered = '';
            }
          }
          // 兜底顺序：上次工作区 → 默认工作区（删不掉的那个）→ 后端给的默认目录
          if (!remembered) remembered = defaults.defaultWorkspace || '';
          if (remembered) runtime.setCwd(remembered);
          else if (defaults.cwd) runtime.setCwd(defaults.cwd);
          // 立刻把"当前工作区"告诉后端：文件面板的边界靠它，必须在第一次列目录之前生效
          // （Pi 还没起来时 workspaceDir 是空的，那时按旧逻辑会把新工作区判成越界）。
          const workspaceNow = useRuntime.getState().cwd;
          if (workspaceNow) void piBridge.setActiveWorkspace(workspaceNow).catch(() => { /* 后端不可用时降级 */ });
          useRuntime.setState({
            exeDir: defaults.exeDir ?? '',
            sessionsRoot: defaults.sessionsRoot ?? '',
            mode: defaults.mode === 'mock' ? 'mock' : 'real',
            piKind: defaults.piKind ?? '',
            piPath: defaults.piPath ?? '',
            piVersion: defaults.piVersion ?? '',
            logPath: defaults.logPath ?? '',
            defaultWorkspace: defaults.defaultWorkspace ?? '',
            debug: Boolean(defaults.debug),
          });
          // 侧栏的工作区列表同样以后端为准：localStorage 丢了也还在
          for (const workspace of defaults.workspaces ?? []) {
            useSessions.getState().addWorkspace(workspace);
          }
          // 默认工作区（exe 同目录的 workspace/）始终在列表里：它是那个删不掉的兜底
          if (defaults.defaultWorkspace) useSessions.getState().addWorkspace(defaults.defaultWorkspace);
          if (remembered) useSessions.getState().addWorkspace(remembered);
          // 注意：不再无条件补上 defaults.cwd —— 用户把它当工作区删掉之后，
          // 它就是那个"删了又回来"的源头（以前每次启动都被这行加回来）。
          if (defaults.cwd && !dismissed.includes(workspaceKey(defaults.cwd))) {
            useSessions.getState().addWorkspace(defaults.cwd);
          }
          /**
           * 预填模型徽标：懒启动之后进程要到第一条消息才建，get_state 一直缺位，
           * 不预填的话模型名要聊过一句才出现（用户报的问题）。
           *
           * 口径 = "第一条消息发出去时 Pi 会用哪个模型"：
           * 记住的启动模型（localStorage 的 startModel，会作为 --model 传给 Pi）
           * 优先；没记住过就用 Pi settings.json 里的默认模型。两者都是"将会使用"，
           * 进程一起来 get_state 就会用真实值覆盖，不会长期漂移。
           */
          // 配置默认值无条件留一份：之后每次进入"还没有进程"的视图都要按它重填
          // （见 router 的 enterPendingSession），不能只挂在下面的 if 里——
          // 否则"启动时模型已被别路填上"就永远拿不到配置默认值。
          useRuntime.setState({
            cfgModel: defaults.defaultModel || '',
            cfgProvider: defaults.defaultProvider || '',
            cfgThinking: defaults.defaultThinkingLevel || '',
          });
          if (!useRuntime.getState().model) {
            const pref = useRuntime.getState().startModel;
            const slash = pref.indexOf('/');
            const prefProvider = slash > 0 ? pref.slice(0, slash) : '';
            const prefModel = slash > 0 ? pref.slice(slash + 1) : pref;
            useRuntime.setState({
              model: prefModel || defaults.defaultModel || '',
              provider: prefProvider || defaults.defaultProvider || '',
              thinkingLevel: useRuntime.getState().startThinking || defaults.defaultThinkingLevel || '',
            });
          }
        }
      } catch { /* 拿不到默认值不影响使用 */ }

      if (cancelled) return;

      /**
       * 预加载：会话列表扫描和后端记忆**都不依赖 Pi 进程**（一个是读会话目录，
       * 一个是读 gui-state.json），所以它们和"启动 Pi"并行做。
       *
       * 串行版本要等 start() 返回才开始扫目录，启动就白白多花一倍时间；
       * 并行之后 Pi 一就绪就能直接 load_session，界面出现时对话已经在那儿了。
       */
      const bootCwd = useRuntime.getState().cwd;
      const preloadList = useSessions.getState().refresh();
      const preloadMemory = persistedSession(bootCwd);
      /**
       * 角色必须在 start() **之前**读完：角色是 Pi 的启动参数（提示词/工具集），
       * 启动之后再读就晚了一轮——那会表现成"重启后角色没生效，要再切一次才行"。
       */
      await useRoles.getState().load();

      /**
       * 会话记忆里也包括**角色**：上次打开的那条会话是在哪个角色下做的，
       * 这次就用同一个角色启动（而不是先按工作区角色启动、再重启一次切过去）。
       *
       * 这里只 select（记下选择），真正的启动在下面一次完成——
       * 否则每次开机都要起两次 Pi 进程。
       */
      const memory = await preloadMemory;
      const rememberedRole = memory.sessionPath
        ? useRoles.getState().roleBySession[memory.sessionPath] ?? ''
        : '';
      if (rememberedRole && rememberedRole !== useRoles.getState().selected) {
        await useRoles.getState().select(rememberedRole);
      }

      useUI.getState().setBooting(true);
      /* 静默启动：启动动画已经表达了"正在连接"，起来后再弹一条"已连接"是噪音；
         手动重启（保存设置/模型页按钮）仍然走有声路径，那是用户主动触发的操作 */
      await start(true);
      /**
       * 启动后必须再读一次角色。
       *
       * processRole（后端 activeRole）是"进程此刻在用的角色"，只有 Pi 起来之后才有值；
       * 上面那次 load() 是在启动**之前**读的，那时它是空的。不补这一次，
       * 界面就会永远显示"角色待重启生效"——每次打开都提示一次重启，
       * 而实际上角色早就生效了。
       */
      await useRoles.getState().load();

      if (cancelled) return;
      useUI.getState().setBooting(true);
      await preloadList;

      // 恢复当前工作区最后打开的会话。不能从 activePath 推断：启动 Pi 后 get_state
      // 会给出本次新建的空会话，refresh() 会据此更新高亮，但它不是用户上次的选择。
      // 会话按工作区分别记忆，避免在 A 工作区打开会话后覆盖 B 的恢复目标。
      if (memory.sessionPath) {
        // 列表里找不到也照样打开：列表最多 100 条，而记忆可能指向更早的会话。
        // 后端会校验路径确实在会话目录内，打不开只会留下一行错误，不会切错会话。
        await useSessions.getState().open(memory.sessionPath);
      }

      const remain = MIN_SPLASH_MS - (Date.now() - startedAt);
      if (remain > 0) await new Promise(resolve => setTimeout(resolve, remain));
      if (!cancelled) useUI.getState().setBooting(false);
    })();
    return () => {
      cancelled = true;
      offSession();
    };
  }, [start]);

  const filesOpen = useUI(s => s.filesOpen);
  const sidebarWidth = useUI(s => s.sidebarWidth);
  const filesWidth = useUI(s => s.filesWidth);

  // 对话区的最小宽度 + 分隔条占位：两侧的上限都由它们和窗口宽度算出来，
  // 这样无论怎么拖，对话区都不会被挤没（用户反馈的问题）。
  const windowWidth = useWindowWidth();
  const MIN_CHAT_WIDTH = 420;
  const SPLITTER_PX = 6;
  const gaps = filesOpen ? SPLITTER_PX * 2 : SPLITTER_PX;
  const sidebarMax = Math.max(200, windowWidth - (filesOpen ? filesWidth : 0) - MIN_CHAT_WIDTH - gaps);
  const filesMax = Math.max(260, windowWidth - sidebarWidth - MIN_CHAT_WIDTH - gaps);

  // 窗口变小（或面板刚打开）时把宽度收回到还放得下的范围
  useEffect(() => {
    if (!windowWidth) return;
    const state = useUI.getState();
    if (filesOpen && state.filesWidth > filesMax) state.setFilesWidth(filesMax);
    if (state.sidebarWidth > sidebarMax) state.setSidebarWidth(sidebarMax);
  }, [windowWidth, filesOpen, filesMax, sidebarMax]);

  return (
    <div className="grid h-screen grid-rows-[auto_minmax(0,1fr)] overflow-hidden bg-panel">
      <TitleBar />

      {/* 三栏都可以拖：左栏 | 对话 | 文件面板。宽度记在 ui store 里（下次打开还是这个宽度），
          双击分隔条回到默认值。 */}
      <div className="flex min-h-0 overflow-hidden">
        <div style={{ width: sidebarWidth }} className="h-full min-h-0 shrink-0 overflow-hidden">
          <Sidebar />
        </div>

        <Splitter
          ariaLabel="调整工作区宽度"
          getWidth={() => sidebarWidth}
          setWidth={width => useUI.getState().setSidebarWidth(width)}
          onReset={() => useUI.getState().setSidebarWidth(260)}
          min={200}
          max={sidebarMax}
        />

        <main className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-tl-2xl bg-bg" style={{ minWidth: MIN_CHAT_WIDTH }}>
          {page === 'roles' ? (
            /* 角色管理是独立页面：它要同时放下提示词、工具、技能卡片、MCP 与压缩策略 */
            <RolesPage />
          ) : page === 'tools' ? (
            /* 工具页：本机环境工具的检测结果（方案第二十四节） */
            <ToolsPage />
          ) : page === 'permissions' ? (
            /* 权限页：四层策略、临时授权、以及"能不能真拦住"（方案第十六、十七节） */
            <PermissionsPage />
          ) : (
            <>
              {/* 子智能体标签栏：派了活才出现，点一下切到它的会话 */}
              <AgentTabs />
              {activeAgentId ? (
                /* 子智能体的会话视图：**不渲染主会话的输入框**（那条输入是发给主 Agent 的）。
                   可继续形态的子智能体在它自己的底部有输入框；一次性形态与终态
                   只有一行"不能再发消息"的说明（见 AgentTranscript）。 */
                <AgentTranscript />
              ) : (
                <>
                  <ChatTimeline />
                  <Composer />
                </>
              )}
            </>
          )}
        </main>

        {filesOpen && (
          <>
            <Splitter
              ariaLabel="调整文件面板宽度"
              invert
              getWidth={() => filesWidth}
              setWidth={width => useUI.getState().setFilesWidth(width)}
              onReset={() => useUI.getState().setFilesWidth(420)}
              min={260}
              max={filesMax}
            />
            <div style={{ width: filesWidth }} className="h-full min-h-0 shrink-0 overflow-hidden">
              <FilePanel onClose={() => useUI.getState().toggleFiles()} />
            </div>
          </>
        )}
      </div>

      {splashMounted && <StartupSplash leaving={!booting} />}
      {/* 右上角通知堆栈：普通提示自动滑出，报错常驻可关（见 Toast.tsx） */}
      <Notifications />
      <ExtensionDialog />
      <SettingsDialog />
      <ConfirmDialog />
      <RawLogDialog />
      <TreeDialog />
      <BashDialog />
    </div>
  );
}
