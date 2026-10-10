import { useEffect, useState } from 'react';
import { piBridge } from '@/bridge/pi';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Switch } from '@/components/ui/switch';
import { KV, Panel, PanelTitle } from '@/components/ui/primitives';
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select';
import { ModelsSection } from '@/features/shell/ModelsSection';
import { RoleTransfer } from '@/features/roles/RoleTransfer';
import { Icon } from '@/components/ui/Icon';
import { cn } from '@/lib/utils';
import { reportError, reportCompactResult } from '@/lib/reportError';
import { useAgent } from '@/hooks/useAgent';
import { useModels } from '@/hooks/useModels';
import { fmtTokens } from '@/lib/format';
import { useRuntime, savePref } from '@/stores/runtime';
import { HERO_FONT_MAX, HERO_FONT_MIN, type SettingsSection } from '@/stores/ui';
import { useUI } from '@/stores/ui';


type Section = SettingsSection;

/** 左栏导航项（照 DeepSeek Harness 设置页的分栏：一类一屏） */
const SECTIONS: { key: Section; label: string; icon: 'gear' | 'robot' | 'chat' | 'sun' | 'shield' }[] = [
  { key: 'general', label: '通用设置', icon: 'gear' },
  { key: 'roles', label: '角色', icon: 'shield' },
  { key: 'models', label: '模型', icon: 'robot' },
  { key: 'session', label: '会话', icon: 'chat' },
  { key: 'appearance', label: '外观', icon: 'sun' },
];

/**
 * 设置弹窗 —— 左边一列分类、右边内容（照 DSH 的设置页布局）。
 *
 * 分栏的意义：设置项变多之后，一长条滚动列表找东西很慢；
 * 一类一屏之后「模型」这类有编辑动作的页面也能有自己的头部按钮（打开配置文件）。
 */
export function SettingsDialog() {
  const open = useUI(s => s.settingsOpen);
  const theme = useUI(s => s.theme);
  const showEvents = useUI(s => s.showEvents);
  /** 运行中回车的投递方式（照 DSH 的 busy-Enter 设置） */
  const busyEnter = useUI(s => s.busyEnter);
  const fontSize = useUI(s => s.fontSize);
  const heroFontSize = useUI(s => s.heroFontSize);
  const [section, setSection] = useState<Section>(useUI.getState().settingsSection);
  // 外部（输入框的角色徽标点「管理角色…」）要求打开某一节时跟着跳过去
  const requestedSection = useUI(s => s.settingsSection);
  useEffect(() => {
    if (open) setSection(requestedSection);
  }, [open, requestedSection]);
  /** 快满时自动压缩：默认开启（与 Pi 的默认一致），改动立即下发 */
  const [autoCompaction, setAutoCompaction] = useState(true);
  /** 失败时自动重试：默认开启（与 Pi 的默认一致） */
  const [autoRetry, setAutoRetry] = useState(true);
  /** 是否加载扩展/技能/模板（默认开；关掉的话斜杠命令为空） */
  const [loadResources, setLoadResources] = useState(true);
  /** 投递模式（Pi 默认逐条） */
  const [steeringMode, setSteeringMode] = useState('one-at-a-time');
  const [followUpMode, setFollowUpMode] = useState('one-at-a-time');

  const cwd = useRuntime(s => s.cwd);
  const exeDir = useRuntime(s => s.exeDir);
  const sessionsRoot = useRuntime(s => s.sessionsRoot);
  const piKind = useRuntime(s => s.piKind);
  const piPath = useRuntime(s => s.piPath);
  const piVersion = useRuntime(s => s.piVersion);
  const startModel = useRuntime(s => s.startModel);
  const startThinking = useRuntime(s => s.startThinking);
  const startTools = useRuntime(s => s.startTools);
  const connected = useRuntime(s => s.connected);
  const busy = useRuntime(s => s.busy);
  const sessionId = useRuntime(s => s.sessionId);
  const sessionFile = useRuntime(s => s.sessionFile);
  const messageCount = useRuntime(s => s.messageCount);
  const tokens = useRuntime(s => s.tokens);
  const contextWindow = useRuntime(s => s.contextWindow);
  const logPath = useRuntime(s => s.logPath);

  const { start, applySettings, loadHistory, clearQueue, setModel, setThinkingLevel } = useAgent();
  const { models, groups, supportedLevels, levels } = useModels();

  const thinkingOptions = supportedLevels.length ? supportedLevels : levels.length ? levels : ['low', 'medium', 'high', 'max'];

  function chooseModel(value: string) {
    useRuntime.getState().setStartModel(value);
    if (!connected) return;
    // 只在第一个斜杠处切分：模型 id 自身可能带斜杠
    const slash = value.indexOf('/');
    if (slash > 0) void setModel(value.slice(0, slash), value.slice(slash + 1));
  }

  function chooseThinking(level: string) {
    useRuntime.getState().setStartThinking(level);
    if (connected) void setThinkingLevel(level);
  }

  return (
    <Dialog open={open} onOpenChange={value => useUI.getState().setSettingsOpen(value)}>
      <DialogContent className="w-[min(800px,94vw)] p-0">
        <DialogHeader>
          <DialogTitle>设置</DialogTitle>
          <span className="flex-1" />
          <span className="truncate text-[12px] text-mute">{connected ? '改动会按新配置重启会话' : '启动时生效'}</span>
        </DialogHeader>

        {/* 固定尺寸（照 DSH 的设置面板 760×500）：宽高都定死，
            切分类、开合卡片时窗口纹丝不动；内容超出由右栏自己滚动。 */}
        <div className="flex h-[min(460px,62vh)]">
          {/* 左栏：分类 */}
          <nav className="flex w-[196px] shrink-0 flex-col gap-0.5 p-2.5">
            {SECTIONS.map(item => (
              <button
                key={item.key}
                type="button"
                onClick={() => setSection(item.key)}
                className={cn(
                  'flex items-center gap-2 rounded-lg px-2.5 py-2 text-left text-[13px] transition-colors',
                  section === item.key ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink',
                )}
              >
                <Icon name={item.icon} className="h-4 w-4 text-mute" />
                {item.label}
              </button>
            ))}
          </nav>

          {/* 右栏：内容 */}
          <div className="flex min-w-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-3.5">
            {section === 'general' && (
              <Panel>
                <PanelTitle>扩展与资源</PanelTitle>

                {/* 这一节按用户要求只留这一个开关：模型/强度在工作区输入区就能改，
                    工作目录用左栏「选择文件夹」，工具能力固定为"全部"。 */}
                <label className="flex items-center justify-between gap-3 py-1 text-[13px]">
                  <span className="text-mute">加载扩展 / 技能 / 模板</span>
                  <Switch
                    checked={loadResources}
                    onCheckedChange={checked => {
                      setLoadResources(checked);
                      void piBridge.setLoadResources(checked).then(result => {
                        if (!result.success) {
                          reportError(result.error || '设置失败', '扩展加载');
                          return;
                        }
                        savePref('resources', checked ? '1' : '0');
                        /**
                         * 提示里点名"新建会话"而不是"重启会话"。
                         *
                         * Pi 恢复旧会话时只会补**内置扩展**的工具，不会补已装包提供的工具
                         * （实测：旧会话继续跑只多了 greet/question/mcp，web_search 一直缺席）。
                         * 所以"重启一下就好"是错的——必须新建会话，工具清单才是新的。
                         */
                        useUI.getState().showNotice(
                          checked
                            ? '已开启资源加载：新建一个会话后，web_search 这类扩展工具才会出现在模型手里'
                            : '已关闭资源加载：当前会话不变，新建会话后扩展工具才会消失',
                        );
                        void applySettings();
                      });
                    }}
                    aria-label="加载扩展 / 技能 / 模板"
                  />
                </label>
                <p className="mt-2 text-[11.5px] leading-[18px] text-mute">
                  输入框里的 <span className="font-mono">/</span> 命令面板来自扩展命令、提示模板与技能 ——
                  关掉之后它会是空的。关掉也能加快启动，并便于排查"问题是不是某个扩展引起的"。
                  <span className="text-warn">
                    注意：关掉之后，扩展提供的工具（web_search、fetch_content 等）不会加载，
                    模型手上只剩内置的 read / bash / edit / write；角色页的「能力」预览也会把它们标成不可用。
                  </span>
                  <span className="mt-1 block text-[11.5px] text-mute">
                    改动**对当前会话无效**：工具清单是每次新会话开始时定下的（ZeroA 只会给旧会话补内置扩展的工具，
                    不会补已装包提供的工具）。开关改完请**新建一个会话**。
                  </span>
                </p>
              </Panel>
            )}

            {section === 'general' && (
              <Panel>
                <PanelTitle>运行中按回车</PanelTitle>
                <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                  ZeroA 正在干活时按回车，这条消息怎么投递。输入框右下角的按钮跟随这里的选择，
                  Ctrl+Enter（macOS 为 Cmd+Enter）永远走另一种。
                </p>
                <div className="flex items-center gap-1.5">
                  {([
                    { value: 'steer', label: '插入（steer）', hint: '在下一个检查点接进当前回合' },
                    { value: 'queue', label: '排队（follow_up）', hint: '等当前回合彻底结束后再生效；排队的原文显示在输入框上方，可插入/编辑/删除' },
                  ] as const).map(option => (
                    <button
                      key={option.value}
                      type="button"
                      title={option.hint}
                      aria-pressed={busyEnter === option.value}
                      onClick={() => useUI.getState().setBusyEnter(option.value)}
                      className={cn(
                        'rounded-lg px-2.5 py-1 text-[12px] transition-colors',
                        busyEnter === option.value
                          ? 'bg-panel3 text-ink'
                          : 'text-dim hover:bg-panel2 hover:text-ink',
                      )}
                    >
                      {option.label}
                    </button>
                  ))}
                </div>
              </Panel>
            )}

            {section === 'roles' && (
              /* 角色管理已经独立成页面（信息量太大，塞不进这个 760×460 的弹窗）。
                 设置里只留一个入口，避免两处维护同一份表单。 */
              <Panel>
                <PanelTitle>角色</PanelTitle>
                <p className="mb-3 text-[12.5px] leading-[19px] text-mute">
                  角色 = 提示词 + 工具白名单 + 技能 + 模型/思考级别 + 上下文压缩策略。
                  它有独立页面：左侧列角色，右侧按「基本信息 / 提示词 / 工具 / 技能 / MCP 与联网 / 上下文压缩」分区编辑。
                </p>
                <Button
                  size="sm"
                  onClick={() => {
                    useUI.getState().setSettingsOpen(false);
                    useUI.getState().setPage('roles');
                  }}
                >
                  <Icon name="shield" className="h-3.5 w-3.5" /> 打开角色管理
                </Button>
              </Panel>
            )}

            {section === 'roles' && <RoleTransfer />}

            {section === 'models' && (
              <ModelsSection onOpenConfig={path => void piBridge.openPath(path).catch(error => reportError(error, '打开配置文件'))} />
            )}

            {section === 'session' && (
              <Panel>
                <PanelTitle>当前会话</PanelTitle>
                <KV label="状态">{connected ? '已连接' : '未连接'}</KV>
                <KV label="会话 ID"><code className="font-mono text-[11px]">{sessionId || '—'}</code></KV>
                <KV label="会话文件"><code className="max-w-[320px] truncate font-mono text-[11px]">{sessionFile || '（临时会话）'}</code></KV>
                <KV label="消息数">{messageCount}</KV>
                <KV label="上下文">{contextWindow ? `${fmtTokens(tokens.total)} / ${fmtTokens(contextWindow)}` : '—'}</KV>
                <div className="mt-2 flex flex-wrap gap-1.5">
                  <Button size="sm" variant="ghost" disabled={!connected} onClick={() => void loadHistory()}>重新载入消息</Button>
                  <Button size="sm" variant="ghost" disabled={!connected} onClick={() => void clearQueue()}>清空队列</Button>
                  <Button size="sm" variant="ghost" onClick={() => useUI.getState().setRawLogOpen(true)}>原始事件日志</Button>
                  {/* 会话恢复/工作区记忆出问题时，先看这个文件：它记录了每次启动的决定 */}
                  {logPath && (
                    <Button
                      size="sm"
                      variant="ghost"
                      title={logPath}
                      onClick={() => {
                        void piBridge.openPath(logPath).catch(() =>
                          useUI.getState().showNotice('还没有日志文件，运行一次后生成'));
                      }}
                    >
                      打开诊断日志
                    </Button>
                  )}
                </div>
                {logPath && (
                  <p className="mt-1.5 truncate font-mono text-[10.5px] text-mute" title={logPath}>{logPath}</p>
                )}

                {/* 上下文管理：长会话 token 快满时，用户需要能主动压一次 */}
                <div className="mt-4 border-t border-linesoft pt-3">
                  <div className="mb-1 text-[12.5px] text-ink">上下文压缩</div>
                  <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                    把前面的对话压成摘要以省 token（ZeroA 的 compact）。当前用量：{contextWindow ? `${fmtTokens(tokens.total)} / ${fmtTokens(contextWindow)}` : '未知'}
                  </p>
                  <div className="flex items-center gap-1.5">
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={!connected || busy}
                      onClick={() => {
                        void piBridge.compact('').then(reportCompactResult);
                      }}
                    >
                      立即压缩
                    </Button>
                    <span className="text-[11.5px] text-mute">压缩期间不能发消息</span>
                  </div>
                  <label className="mt-3 flex items-center justify-between gap-3 py-1 text-[13px]">
                    <span className="text-mute">快满时自动压缩</span>
                    <Switch
                      checked={autoCompaction}
                      disabled={!connected}
                      onCheckedChange={checked => {
                        setAutoCompaction(checked);
                        void piBridge.setAutoCompaction(checked).then(result => {
                          if (!result.success) reportError(result.error || '设置失败', '自动压缩');
                        });
                      }}
                      aria-label="快满时自动压缩"
                    />
                  </label>

                  {/* 重试控制：以前界面只能被动看 willRetry 事件滚过去 */}
                  <label className="mt-1 flex items-center justify-between gap-3 py-1 text-[13px]">
                    <span className="text-mute">失败时自动重试</span>
                    <Switch
                      checked={autoRetry}
                      disabled={!connected}
                      onCheckedChange={checked => {
                        setAutoRetry(checked);
                        void piBridge.setAutoRetry(checked).then(result => {
                          if (!result.success) reportError(result.error || '设置失败', '自动重试');
                        });
                      }}
                      aria-label="失败时自动重试"
                    />
                  </label>
                  <p className="text-[11.5px] leading-[18px] text-mute">
                    仅针对瞬时错误（过载 / 限流 / 5xx）。重试等待期间可以在输入区点「停止重试」。
                  </p>
                  <div className="mt-3 flex items-center justify-between gap-3 py-1 text-[13px]">
                    <span className="text-mute">运行中插入的投递</span>
                    <div className="flex gap-1">
                      {(['one-at-a-time', 'all'] as const).map(mode => (
                        <button
                          key={mode}
                          type="button"
                          disabled={!connected}
                          onClick={() => {
                            setSteeringMode(mode);
                            void piBridge.setSteeringMode(mode).then(result => {
                              if (!result.success) reportError(result.error || '设置失败', '投递模式');
                            });
                          }}
                          className={cn(
                            'h-7 rounded-lg px-2.5 text-[12px] transition-colors',
                            steeringMode === mode ? 'bg-panel3 text-ink' : 'text-mute hover:text-dim',
                          )}
                        >
                          {mode === 'all' ? '一次全投' : '逐条投递'}
                        </button>
                      ))}
                    </div>
                  </div>
                  <div className="flex items-center justify-between gap-3 py-1 text-[13px]">
                    <span className="text-mute">排到后面的投递</span>
                    <div className="flex gap-1">
                      {(['one-at-a-time', 'all'] as const).map(mode => (
                        <button
                          key={mode}
                          type="button"
                          disabled={!connected}
                          onClick={() => {
                            setFollowUpMode(mode);
                            void piBridge.setFollowUpMode(mode).then(result => {
                              if (!result.success) reportError(result.error || '设置失败', '投递模式');
                            });
                          }}
                          className={cn(
                            'h-7 rounded-lg px-2.5 text-[12px] transition-colors',
                            followUpMode === mode ? 'bg-panel3 text-ink' : 'text-mute hover:text-dim',
                          )}
                        >
                          {mode === 'all' ? '一次全投' : '逐条投递'}
                        </button>
                      ))}
                    </div>
                  </div>
                  <p className="text-[11.5px] leading-[18px] text-mute">
                  </p>
                </div>
              </Panel>
            )}

            {section === 'appearance' && (
              <Panel>
                <PanelTitle>外观</PanelTitle>
                <label className="flex items-center justify-between gap-3 py-1 text-[13px]">
                  <span className="text-mute">纯黑暗色</span>
                  <Switch checked={theme === 'dark'} onCheckedChange={checked => useUI.getState().setTheme(checked ? 'dark' : 'light')} aria-label="纯黑暗色" />
                </label>
                {/* 空态标题（"我们要做些什么？"）的字号：数字输入 + 上下限夹取 */}
                <div className="flex items-center justify-between gap-3 py-1 text-[13px]">
                  <span className="text-mute">欢迎标题字号</span>
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      aria-label="减小标题字号"
                      onClick={() => useUI.getState().setHeroFontSize(heroFontSize - 2)}
                      disabled={heroFontSize <= HERO_FONT_MIN}
                      className="grid h-6 w-6 place-items-center rounded-lg text-mute hover:bg-panel3 hover:text-ink disabled:opacity-30"
                    >
                      −
                    </button>
                    <input
                      type="number"
                      min={HERO_FONT_MIN}
                      max={HERO_FONT_MAX}
                      value={heroFontSize}
                      onChange={e => useUI.getState().setHeroFontSize(Number(e.target.value))}
                      className="h-7 w-[64px] rounded-lg bg-panel2 px-2 text-center font-mono text-[12px] outline-none"
                    />
                    <button
                      type="button"
                      aria-label="增大标题字号"
                      onClick={() => useUI.getState().setHeroFontSize(heroFontSize + 2)}
                      disabled={heroFontSize >= HERO_FONT_MAX}
                      className="grid h-6 w-6 place-items-center rounded-lg text-mute hover:bg-panel3 hover:text-ink disabled:opacity-30"
                    >
                      ＋
                    </button>
                    <span className="text-[11px] text-mute">px（{HERO_FONT_MIN}–{HERO_FONT_MAX}）</span>
                  </div>
                </div>                <div className="flex items-center justify-between gap-3 py-1 text-[13px]">
                  <span className="text-mute">对话字号</span>
                  <div className="flex gap-1">
                    {([[14, '小'], [15, '中'], [17, '大']] as const).map(([size, label]) => (
                      <button
                        key={size}
                        type="button"
                        onClick={() => useUI.getState().setFontSize(size)}
                        className={cn(
                          'h-7 rounded-lg px-3 text-[12.5px] transition-colors',
                          fontSize === size ? 'bg-panel3 text-ink' : 'text-mute hover:text-dim',
                        )}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </div>
                <label className="flex items-center justify-between gap-3 py-1 text-[13px]">
                  <span className="text-mute">事件映射</span>
                  <Switch checked={showEvents} onCheckedChange={() => useUI.getState().toggleEvents()} aria-label="事件映射" />
                </label>
              </Panel>
            )}

          </div>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => useUI.getState().setSettingsOpen(false)}>取消</Button>
          <Button
            variant="default"
            onClick={() => {
              useUI.getState().setSettingsOpen(false);
              void applySettings();
            }}
          >
            保存并应用
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
