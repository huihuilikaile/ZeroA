import { create } from 'zustand';

export type Theme = 'light' | 'dark';

/**
 * 设置窗的分区（SettingsDialog 的左栏一项一节）。
 *
 * 独立成类型是为了只有一处定义：设置窗、store 与"从别处跳到某一节"的调用方
 * 都引用它，加一节时编译器会把漏改的地方全部指出来。
 */
export type SettingsSection = 'general' | 'roles' | 'models' | 'session' | 'appearance';

const THEME_KEY = 'pi-gui-theme';
const FONT_KEY = 'pi-gui-font-size';

/** 首屏主题：优先用户上次选择，其次跟随系统 */
function initialTheme(): Theme {
  const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(THEME_KEY) : null;
  if (saved === 'light' || saved === 'dark') return saved;
  const prefersDark = typeof matchMedia !== 'undefined' && matchMedia('(prefers-color-scheme: dark)').matches;
  return prefersDark ? 'dark' : 'light';
}

export interface ExtensionDialog {
  requestId: string;
  method: string; // confirm | select | input | editor
  title: string;
  message: string;
  options?: string[];
  placeholder?: string;
  /** 非空 = 这是某个子智能体的请求，应答要发回它自己的进程 */
  agentId?: string;
}

export type NoticeTone = 'success' | 'warn' | 'error' | 'info';

/**
 * 右上角通知卡片（components/ui/Toast.tsx 渲染）。
 *
 * 两种生命周期：
 *   · 普通提示（showNotice）：停留后自动滑出，最多同时 3 条，新的挤掉最旧的；
 *   · 应用层报错（showError）：常驻，只能手动关（或新一轮操作时被 dismissErrorNotices
 *     收掉）——报错不该"自己悄悄没了"，那是用户错过它的最常见方式。
 */
export interface NoticeItem {
  id: number;
  title?: string;
  text: string;
  tone: NoticeTone;
  /** true = 不自动消失（报错用） */
  sticky: boolean;
}

interface UIStore {
  theme: Theme;
  /** 左侧工作区栏是否展开 */
  sidebarOpen: boolean;
  rawLogOpen: boolean;
  /** 分支树对话框（get_tree） */
  treeOpen: boolean;
  /** 运行命令对话框（Pi 的 bash） */
  bashOpen: boolean;
  showEvents: boolean;
  /** 右侧工作区文件面板是否展开 */
  filesOpen: boolean;
  /** 左栏宽度（可拖拽） */
  sidebarWidth: number;
  /** 右侧文件面板宽度（可拖拽，默认给大一点，看代码才够用） */
  filesWidth: number;
  setSidebarWidth(width: number): void;
  setFilesWidth(width: number): void;
  toggleFiles(): void;
  /** 对话正文字号（px）：14 小 / 15 中 / 17 大 */
  fontSize: number;
  setFontSize(size: number): void;
  /** 空态标题字号（px，夹在 18–64 之间） */
  heroFontSize: number;
  setHeroFontSize(size: number): void;
  /**
   * 运行中按回车怎么投递（照 DSH 的 busy-Enter 设置）：
   *   queue → follow_up，等当前回合彻底结束后再生效；排队的原文显示在
   *           输入框上方的排队面板里，可插入/编辑/删除（默认）；
   *   steer → 在当前回合的下一个检查点插进去。
   * Ctrl/Cmd+Enter 永远走另一种，发送按钮跟随这个设置。
   */
  busyEnter: 'queue' | 'steer';
  setBusyEnter(mode: 'queue' | 'steer'): void;
  settingsOpen: boolean;
  /**
   * 主区域显示哪一页：对话还是角色管理。
   *
   * 角色管理做成**独立页面**而不是设置里的一个分栏：那里要塞下提示词、
   * 工具复选、技能卡片、MCP 服务器与压缩策略，塞进 760×460 的弹窗里
   * 只能什么都看不清。
   */
  page: 'chat' | 'roles' | 'tools' | 'permissions';
  setPage(page: 'chat' | 'roles' | 'tools' | 'permissions'): void;
  /**
   * 设置窗打开后落在哪一节。
   *
   * 放进 store 而不是 SettingsDialog 的 useState：输入框的角色徽标要能
   * "直接打开设置的角色页"（点「管理角色…」），而它跟设置窗是两个组件。
   */
  settingsSection: SettingsSection;
  openSettings(section?: SettingsSection): void;
  dialog: ExtensionDialog | null;
  /** 右上角的通知卡片堆栈（新卡片追加在下方，见 components/ui/Toast.tsx） */
  notices: NoticeItem[];
  /** 启动中：此时显示启动屏（StartupSplash，纯文字无进度——步骤耗时不可预知，百分比是编的） */
  booting: boolean;
  /**
   * 正在切换工作区（重启 Pi + 恢复该工作区的会话）。
   *
   * 这段时间进程是断开又重连的，界面不该把它显示成"未连接/正在连接"——
   * 那是内部状态，用户要的是"我在切文件夹"。对话区据此显示转圈动画。
   */
  switchingWorkspace: boolean;

  setTheme(theme: Theme): void;
  toggleTheme(): void;
  toggleSidebar(): void;
  setRawLogOpen(open: boolean): void;
  setTreeOpen(open: boolean): void;
  setBashOpen(open: boolean): void;
  toggleEvents(): void;
  setSettingsOpen(open: boolean): void;
  toggleSettings(): void;
  openDialog(dialog: ExtensionDialog): void;
  closeDialog(): void;
  /** 弹一条普通提示（右上角，停留后自动滑出）；最多同时 3 条，新的挤掉最旧的 */
  showNotice(text: string, tone?: 'success' | 'warn' | 'error'): void;
  /** 弹一条应用层报错（右上角，常驻，手动关闭；接替以前的输入框上方报错条） */
  showError(title: string, text: string): void;
  /** 关掉某一条通知 */
  dismissNotice(id: number): void;
  /** 新一轮操作开始时收掉所有报错通知（普通提示不动，它们本来就会自己消失） */
  dismissErrorNotices(): void;
  setBooting(booting: boolean): void;
  setSwitchingWorkspace(switching: boolean): void;
  /**
   * 附件投递箱：文件面板的「加为附件」把路径放这里，输入框（Composer）取走。
   *
   * 为什么不直接调 Composer 的方法：文件面板与输入框是两个组件，文件面板
   * 不该知道输入框的内部 state；投递箱是两者之间唯一的约定。
   */
  attachmentInbox: string[];
  enqueueAttachments(paths: string[]): void;
  consumeAttachments(): string[];
  /**
   * 「在侧边栏展示本轮改动」信号：顶栏按钮每点一次 +1，
   * 文件面板据此切回「文件」页（本轮改动小节就在那页顶部）。
   */
  changedFilesFocus: number;
  focusChangedFiles(): void;
}

let noticeId = 0;
/** 每条自动消失的通知各管各的计时器（手动关闭时要能撤掉） */
const noticeTimers = new Map<number, ReturnType<typeof setTimeout>>();

/**
 * 提示的停留与滑出时长，必须和 CSS 里 `.notice` 的动画对上（tokens.css）。
 * 停留 3000ms（DSH 的值）+ 滑出 240ms。
 */
const NOTICE_HOLD_MS = 3000;
const NOTICE_OUT_MS = 240;
/** 普通提示最多同时几条：再多了右上角会盖住一整列内容 */
const NOTICE_MAX = 3;

export const useUI = create<UIStore>((set, get) => ({
  theme: initialTheme(),
  sidebarOpen: true,
  rawLogOpen: false,
  treeOpen: false,
  bashOpen: false,
  showEvents: false,
  filesOpen: typeof localStorage !== 'undefined' && localStorage.getItem('pi-gui-files') === '1',
  sidebarWidth: numberPref('pi-gui-sidebar-w', 260),
  filesWidth: numberPref('pi-gui-files-w', 420),
  fontSize: typeof localStorage !== 'undefined' ? Number(localStorage.getItem(FONT_KEY)) || 15 : 15,
  heroFontSize: numberPref('pi-gui-hero-font', 30),
  // 读偏好前先确认 localStorage 存在：同样的守卫本文件处处都有，
  // 少了它就是"在非浏览器环境（自动化测试、SSR）里一 import 就崩"。
  // 默认排队（queue）：排队原文会显示在输入框上方、可编辑可撤回；
  // 只有用户显式选过 steer 才用插入。
  busyEnter: typeof localStorage !== 'undefined' && localStorage.getItem('pi-gui-busy-enter') === 'steer' ? 'steer' : 'queue',
  settingsOpen: false,
  settingsSection: 'general',
  page: 'chat',
  dialog: null,
  notices: [],
  booting: true,
  switchingWorkspace: false,

  setSidebarWidth: width => {
    numberPrefSave('pi-gui-sidebar-w', width);
    set({ sidebarWidth: width });
  },
  setFilesWidth: width => {
    numberPrefSave('pi-gui-files-w', width);
    set({ filesWidth: width });
  },
  toggleFiles: () => {
    const next = !get().filesOpen;
    try { localStorage.setItem('pi-gui-files', next ? '1' : '0'); } catch { /* 忽略 */ }
    set({ filesOpen: next });
  },
  setHeroFontSize: size => {
    const clamped = Math.min(HERO_FONT_MAX, Math.max(HERO_FONT_MIN, Math.round(size)));
    numberPrefSave('pi-gui-hero-font', clamped);
    applyHeroFontSize(clamped);
    set({ heroFontSize: clamped });
  },
  setBusyEnter: busyEnter => {
    try { localStorage.setItem('pi-gui-busy-enter', busyEnter); } catch { /* 隐私模式忽略 */ }
    set({ busyEnter });
  },
  setFontSize: size => {
    try { localStorage.setItem(FONT_KEY, String(size)); } catch { /* 隐私模式忽略 */ }
    applyFontSize(size);
    set({ fontSize: size });
  },
  setTheme: theme => {
    try { localStorage.setItem(THEME_KEY, theme); } catch { /* 隐私模式下忽略 */ }
    set({ theme });
  },
  toggleTheme: () => get().setTheme(get().theme === 'dark' ? 'light' : 'dark'),

  toggleSidebar: () => set(s => ({ sidebarOpen: !s.sidebarOpen })),
  setRawLogOpen: rawLogOpen => set({ rawLogOpen }),
  setTreeOpen: treeOpen => set({ treeOpen }),
  setBashOpen: bashOpen => set({ bashOpen }),
  toggleEvents: () => set(s => ({ showEvents: !s.showEvents })),
  setSettingsOpen: settingsOpen => set({ settingsOpen }),
  toggleSettings: () => set(s => ({ settingsOpen: !s.settingsOpen })),
  openSettings: section => set(state => ({ settingsOpen: true, settingsSection: section ?? state.settingsSection })),
  setPage: page => set({ page }),

  openDialog: dialog => set({ dialog }),
  closeDialog: () => set({ dialog: null }),

  /** 普通提示：右上角堆叠，停留后自动滑出（时长见上面的常量） */
  showNotice: (text, tone) => {
    const id = ++noticeId;
    set(state => {
      const transient = state.notices.filter(n => !n.sticky);
      // 超上限挤掉最旧的一条（连同它的计时器）
      const overflow = transient.length >= NOTICE_MAX ? transient.slice(0, transient.length - NOTICE_MAX + 1) : [];
      for (const old of overflow) {
        clearTimeout(noticeTimers.get(old.id));
        noticeTimers.delete(old.id);
      }
      const dropped = new Set(overflow.map(o => o.id));
      return { notices: [...state.notices.filter(n => !dropped.has(n.id)), { id, text, tone: tone ?? 'info', sticky: false }] };
    });
    // 卸载时机必须等于"停留 + 滑出"，否则动画还没走完元素就没了
    noticeTimers.set(id, setTimeout(() => get().dismissNotice(id), NOTICE_HOLD_MS + NOTICE_OUT_MS));
  },

  /** 应用层报错：常驻卡片，只能手动关（或被 dismissErrorNotices 收掉） */
  showError: (title, text) => {
    const id = ++noticeId;
    set(state => ({ notices: [...state.notices, { id, title, text, tone: 'error', sticky: true }] }));
  },

  dismissNotice: id => {
    clearTimeout(noticeTimers.get(id));
    noticeTimers.delete(id);
    set(state => ({ notices: state.notices.filter(n => n.id !== id) }));
  },

  dismissErrorNotices: () => set(state => ({ notices: state.notices.filter(n => n.tone !== 'error') })),

  setBooting: booting => set({ booting }),
  setSwitchingWorkspace: switchingWorkspace => set({ switchingWorkspace }),

  attachmentInbox: [],
  enqueueAttachments: paths => set(state => ({ attachmentInbox: [...state.attachmentInbox, ...paths] })),
  consumeAttachments: () => {
    const inbox = get().attachmentInbox;
    if (inbox.length) set({ attachmentInbox: [] });
    return inbox;
  },

  changedFilesFocus: 0,
  focusChangedFiles: () => set(state => ({ changedFilesFocus: state.changedFilesFocus + 1 })),
}));

/** 把字号写到根变量上：对话正文（.md-body / 用户气泡）都读它 */
export function applyFontSize(size: number): void {
  if (typeof document === 'undefined') return;
  document.documentElement.style.setProperty('--chat-font-size', `${size}px`);
  document.documentElement.style.setProperty('--chat-line-height', `${Math.round(size * 1.57)}px`);
}

/** 空态标题字号的上下限（设置里用它夹取值，避免用户填出看不了的界面） */
export const HERO_FONT_MIN = 18;
export const HERO_FONT_MAX = 64;

/** 把空态标题字号写到根变量上（ChatTimeline 的"我们要做些什么？"读它） */
export function applyHeroFontSize(size: number): void {
  if (typeof document === 'undefined') return;
  const clamped = Math.min(HERO_FONT_MAX, Math.max(HERO_FONT_MIN, Math.round(size)));
  document.documentElement.style.setProperty('--hero-font-size', `${clamped}px`);
}
/** 读一个数字偏好（读不到或非法就用兜底值） */
function numberPref(key: string, fallback: number): number {
  if (typeof localStorage === 'undefined') return fallback;
  const value = Number(localStorage.getItem(key));
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function numberPrefSave(key: string, value: number): void {
  try { localStorage.setItem(key, String(Math.round(value))); } catch { /* 忽略 */ }
}