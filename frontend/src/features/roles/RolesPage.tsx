import { useEffect, useMemo, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Icon, type IconName } from '@/components/ui/Icon';
import { Panel, PanelTitle } from '@/components/ui/primitives';
import { piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import { parseData } from '@/rpc/types';
import { RoleBadge } from '@/features/roles/RoleBadge';
import { useCapabilities, groupSkills } from '@/stores/capabilities';
import { useConfirm } from '@/stores/confirm';
import { useRoles } from '@/stores/roles';
import { useUI } from '@/stores/ui';
import type { Delegation, Role, RoleIcon, RoleView } from '@/types/role';
import { humanBytes } from '@/types/capabilities';
import { ROLE_ICONS } from '@/types/role';

/** 页签：一类信息一屏，避免一个长表单滚到底 */
type Tab = 'basics' | 'prompt' | 'tools' | 'skills' | 'capabilities' | 'permissions' | 'mcp' | 'context' | 'delegation';

const TABS: { key: Tab; label: string; icon: IconName }[] = [
  { key: 'basics', label: '基本信息', icon: 'robot' },
  { key: 'prompt', label: '提示词', icon: 'chat' },
  { key: 'capabilities', label: '能力', icon: 'search' },
  { key: 'tools', label: '工具', icon: 'tool' },
  { key: 'skills', label: '技能', icon: 'list' },
  { key: 'permissions', label: '权限', icon: 'shield' },
  { key: 'mcp', label: 'MCP 与联网', icon: 'db' },
  { key: 'context', label: '上下文与循环', icon: 'think' },
  { key: 'delegation', label: '子智能体', icon: 'flag' },
];

/** 权限动作（与 Go 的 permission.Actions 一致） */
const PERMISSION_ACTIONS: { action: string; label: string; hint: string }[] = [
  { action: 'filesystem.read', label: '读取文件', hint: '禁止时模型看不到 read/grep/ls/find' },
  { action: 'filesystem.write', label: '写入文件', hint: '禁止时去掉 write/edit' },
  { action: 'shell.execute', label: '执行命令', hint: '禁止时去掉 bash/powershell' },
  { action: 'network.public', label: '访问公网', hint: '禁止时去掉联网工具' },
  { action: 'network.target', label: '访问靶机/内网', hint: '由授权目标控制，Go 拦不到单次请求' },
  { action: 'process.spawn', label: '启动进程', hint: 'ZeroA 的 bash 能起任意进程，只能记录' },
];

const DECISION_TEXT: Record<string, string> = { allow: '允许', ask: '询问', deny: '禁止' };

/**
 * 角色管理页（独立页面，占满主区域）。
 *
 * 为什么不做成设置里的分栏：一个角色有提示词、工具白名单、技能、MCP、压缩策略
 * 五组配置，挤在 760×460 的弹窗里只能看见一小块。独立页面之后左栏列角色、
 * 右栏分区编辑，一屏能看完一个方向的全部定义。
 */
export function RolesPage() {
  const roles = useRoles(s => s.roles);
  const selected = useRoles(s => s.selected);
  const save = useRoles(s => s.save);
  const remove = useRoles(s => s.remove);
  const reset = useRoles(s => s.reset);
  const fetchPrompt = useRoles(s => s.fetchPrompt);
  const capabilities = useCapabilities();

  const [editingId, setEditingId] = useState('');
  const [tab, setTab] = useState<Tab>('basics');
  const [draft, setDraft] = useState<Role>({ id: '', name: '' });
  const [prompt, setPrompt] = useState('');
  // 右键菜单：复制为子智能体 / 复制为角色（见 listMenu 的渲染）
  const [listMenu, setListMenu] = useState<{ x: number; y: number; role: Role } | null>(null);

  const editing = roles.find(role => role.id === editingId);
  const skillGroups = useMemo(() => groupSkills(capabilities.skills), [capabilities.skills]);
  // 普通角色与子智能体分两栏：前者是"人切换的方向"，后者是"被委派的工作单元"
  const normalRoles = roles.filter(role => role.kind !== 'agent');
  const agents = roles.filter(role => role.kind === 'agent');
  // 子智能体不能再派人（只有一层）：编辑子智能体时不显示"子智能体"页签
  const visibleTabs = draft.kind === 'agent' ? TABS.filter(item => item.key !== 'delegation') : TABS;

  // 编辑对象换成子智能体时，若正停在"子智能体"页签就退回基本信息
  useEffect(() => {
    if (draft.kind === 'agent' && tab === 'delegation') setTab('basics');
  }, [draft.kind, tab]);

  // 右键菜单的关闭：点任意处 / Escape。只挂 click，不挂 contextmenu——
  // 后者会在"右键另一个条目"时把刚打开的菜单立刻关掉（冒泡到 window）。
  useEffect(() => {
    if (!listMenu) return;
    const close = () => setListMenu(null);
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close();
    };
    window.addEventListener('click', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('click', close);
      window.removeEventListener('keydown', onKey);
    };
  }, [listMenu]);

  // 进入页面时拉一次能力清单（技能扫描要遍历目录，只在进来时做一次）
  useEffect(() => {
    void capabilities.load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 选中角色时把内容读进编辑器
  useEffect(() => {
    if (!editing) return;
    setDraft({ ...editing });
    let alive = true;
    void fetchPrompt(editing.id).then(text => {
      if (alive) setPrompt(text);
    });
    return () => {
      alive = false;
    };
  }, [editingId, editing, fetchPrompt]);

  function newRole(): void {
    setEditingId('new');
    setDraft({ id: '', name: '', icon: 'robot', contextFiles: true });
    setPrompt('');
    setTab('basics');
  }

  function newAgent(): void {
    setEditingId('new');
    setDraft({ id: '', name: '', icon: 'flag', kind: 'agent', contextFiles: true });
    setPrompt('');
    setTab('basics');
  }

  function copyRole(role: Role): void {
    setEditingId('new');
    setDraft({ ...role, id: `${role.id}-copy`, name: `${role.name} 副本`, builtin: false });
    void fetchPrompt(role.id).then(setPrompt);
    setTab('basics');
  }

  /**
   * 复制为子智能体：把一份角色定义转成"只能被委派"的独立子智能体。
   *
   * delegation 必须剥掉——子智能体不能再派子智能体（只有一层），
   * 留着只会让界面显示一个永远不会生效的配置。
   */
  function copyAsAgent(role: Role): void {
    setEditingId('new');
    setDraft({
      ...role,
      id: `${role.id}-agent`,
      name: `${role.name}（子智能体）`,
      kind: 'agent',
      builtin: false,
      delegation: undefined,
    });
    void fetchPrompt(role.id).then(setPrompt);
    setTab('basics');
  }

  /** 反过来：把子智能体复制回普通角色（比如想把它当主会话方向用时） */
  function copyAsRole(role: Role): void {
    setEditingId('new');
    setDraft({ ...role, id: `${role.id}-role`, name: `${role.name}（角色）`, kind: undefined, builtin: false });
    void fetchPrompt(role.id).then(setPrompt);
    setTab('basics');
  }

  async function submit(): Promise<void> {
    const role: Role = { ...draft, id: draft.id.trim().toLowerCase(), name: draft.name.trim(), builtin: undefined };
    const ok = await save(role, prompt);
    if (ok) {
      setEditingId(role.id);
      setDraft(role);
    }
  }

  function confirmDelete(role: Role): void {
    const isAgent = role.kind === 'agent';
    useConfirm.getState().ask({
      title: `删除${isAgent ? '子智能体' : '角色'}「${role.name}」？`,
      description: isAgent
        ? '会同时删掉它的提示词文件。引用它的委派白名单会保留这个 id，派发时会报"子智能体不存在"。'
        : '会同时删掉它的提示词文件。正在被某个工作区使用的角色不能删除。',
      confirmLabel: '删除',
      danger: true,
      onConfirm: async () => {
        const ok = await remove(role.id);
        if (ok && editingId === role.id) setEditingId('');
      },
    });
  }

  function toggleTool(tool: string): void {
    // undefined = 不限制；一旦点过就变成显式白名单，"全选"等价于列出全部工具
    const current = draft.tools ?? capabilities.piTools.map(item => item.id);
    const next = current.includes(tool) ? current.filter(item => item !== tool) : [...current, tool];
    setDraft({ ...draft, tools: next });
  }

  function setAllTools(on: boolean): void {
    setDraft({ ...draft, tools: on ? capabilities.piTools.map(item => item.id) : [] });
  }

  function toggleSkill(id: string): void {
    const current = draft.skills ?? [];
    const next = current.includes(id) ? current.filter(item => item !== id) : [...current, id];
    setDraft({ ...draft, skills: next });
  }

  /** 委派白名单的勾选；空数组 = 一个都不能派（Go 端语义，不是"不限制"） */
  function toggleAllowedRole(id: string): void {
    const current = draft.delegation?.allowedRoles ?? [];
    const next = current.includes(id) ? current.filter(item => item !== id) : [...current, id];
    setDraft({ ...draft, delegation: { ...(draft.delegation ?? { enabled: true }), allowedRoles: next } });
  }

  /** 改委派配置的某个字段（0/空串 = 恢复默认，不写进 JSON） */
  function patchDelegation(patch: Partial<Delegation>): void {
    setDraft({ ...draft, delegation: { ...(draft.delegation ?? { enabled: true }), ...patch } });
  }

  function setSkillsForCategory(items: { id: string }[], on: boolean): void {
    const ids = items.map(item => item.id);
    const current = draft.skills ?? [];
    const next = on
      ? [...new Set([...current, ...ids])]
      : current.filter(id => !ids.includes(id));
    setDraft({ ...draft, skills: next });
  }

  const toolsUnlimited = draft.tools == null;
  const activeTools = draft.tools ?? [];
  const activeSkills = draft.skills ?? [];
  const skillsUnlimited = draft.skills == null;

  /** 左栏条目：普通角色与子智能体共用一个渲染，右键给出"复制为另一类型" */
  function renderItem(role: RoleView) {
    return (
      <button
        key={role.id}
        type="button"
        onClick={() => setEditingId(role.id)}
        onContextMenu={event => {
          event.preventDefault();
          setListMenu({ x: event.clientX, y: event.clientY, role });
        }}
        className={`flex items-start gap-2 rounded-xl px-2.5 py-2 text-left transition-colors ${
          role.id === editingId ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
        }`}
      >
        <Icon name={(role.icon as RoleIcon) ?? 'robot'} className="mt-0.5 h-3.5 w-3.5 shrink-0 text-mute" />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="truncate text-[12.5px]">{role.name}</span>
            {role.id === selected && role.kind !== 'agent' && <Icon name="check" className="h-3 w-3 shrink-0 text-dim" />}
          </span>
          <span className="mt-0.5 block text-[11px] leading-[16px] text-mute">
            {role.broken ? <span className="text-danger">配置损坏</span> : (
              <>
                {role.tools == null ? '工具：默认' : `工具：${role.tools.length} 个`}
                {role.skills != null && ` · 技能：${role.skills.length}`}
                {role.thinkingLevel && ` · ${role.thinkingLevel}`}
              </>
            )}
          </span>
        </span>
        {role.builtin && <Icon name="shield" className="mt-0.5 h-3 w-3 shrink-0 text-caption" />}
      </button>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 页头：标题 + 当前角色 + 返回对话 */}
      <header className="flex shrink-0 items-center gap-3 border-b border-line px-5 py-3">
        <Icon name="shield" className="h-4 w-4 text-mute" />
        <h1 className="text-[14px] font-medium text-ink">角色管理</h1>
        <span className="text-[12px] text-mute">
          {normalRoles.length} 个角色 · {agents.length} 个子智能体 · 当前：{roles.find(role => role.id === selected)?.name ?? '未选择'}
        </span>
        <span className="flex-1" />
        <Button
          size="sm"
          variant="outline"
          onClick={() => useUI.getState().openSettings('roles')}
          title="导入或导出角色（设置 → 角色）"
        >
          <Icon name="db" className="h-3.5 w-3.5" /> 导入 / 导出
        </Button>
        <Button size="sm" variant="outline" onClick={newRole}>
          <Icon name="plus" className="h-3.5 w-3.5" /> 新建角色
        </Button>
        <Button size="sm" variant="outline" onClick={newAgent} title="子智能体只能被角色委派，不会出现在输入框的角色选择器里">
          <Icon name="plus" className="h-3.5 w-3.5" /> 新建子智能体
        </Button>
        <Button size="sm" variant="ghost" onClick={() => useUI.getState().setPage('chat')}>
          <Icon name="logout" className="h-3.5 w-3.5" /> 返回对话
        </Button>
      </header>

      <div className="flex min-h-0 flex-1">
        {/* 左：角色 / 子智能体两栏列表 */}
        <aside className="flex w-[240px] shrink-0 flex-col gap-1 overflow-y-auto border-r border-line p-3">
          <p className="px-2 pb-0.5 text-[11px] text-caption">角色（输入框可选）</p>
          {normalRoles.map(renderItem)}
          {normalRoles.length === 0 && <p className="px-2 py-1 text-[12px] text-mute">还没有角色</p>}
          <p className="px-2 pb-0.5 pt-3 text-[11px] text-caption">子智能体（只能被委派）</p>
          {agents.map(renderItem)}
          {agents.length === 0 && (
            <p className="px-2 py-1 text-[12px] leading-[17px] text-mute">
              还没有子智能体：点右上角「新建子智能体」，或右键一个角色「复制为子智能体」
            </p>
          )}
        </aside>

        {/* 右：编辑器 */}
        <section className="flex min-w-0 flex-1 flex-col">
          {!editingId ? (
            <div className="flex flex-1 flex-col items-center justify-center gap-2 text-center">
              <Icon name="shield" className="h-6 w-6 text-caption" />
              <p className="text-[13px] text-mute">左侧选一个角色开始编辑，或点「新建角色」</p>
              <p className="max-w-[420px] text-[11.5px] leading-[18px] text-caption">
                角色 = 提示词 + 工具白名单 + 技能 + 模型/思考级别 + 压缩策略。
                切换角色会重启 ZeroA 并开始一个新会话（ZeroA 的提示词与工具集只能在启动时确定）。
              </p>
            </div>
          ) : (
            <>
              {/* 页签：flex-wrap + nowrap——字号调大后多出来的换到第二行，
                  而不是把每个页签压成一字一行的竖排 */}
              <nav className="flex shrink-0 flex-wrap items-center gap-1 border-b border-line px-4 py-2">
                {visibleTabs.map(item => (
                  <button
                    key={item.key}
                    type="button"
                    onClick={() => setTab(item.key)}
                    className={`flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-lg px-2.5 py-1.5 text-[12.5px] transition-colors ${
                      tab === item.key ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
                    }`}
                  >
                    <Icon name={item.icon} className="h-3.5 w-3.5 shrink-0 text-mute" />
                    {item.label}
                  </button>
                ))}
                <span className="flex-1" />
                {editing?.builtin && <span className="whitespace-nowrap text-[11px] text-mute">内置角色（可改不可删）</span>}
              </nav>

              <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
                {editing?.broken && (
                  <p className="mb-3 rounded-lg bg-[color-mix(in_srgb,var(--danger)_10%,transparent)] px-3 py-2 text-[11.5px] text-danger">
                    {editing.broken}
                  </p>
                )}

                {tab === 'basics' && (
                  <div className="flex max-w-[720px] flex-col gap-4">
                    {draft.kind === 'agent' && (
                      <p className="rounded-lg bg-panel2 px-3 py-2 text-[11.5px] leading-[18px] text-mute">
                        这是<span className="text-ink">子智能体</span>：只能被其他角色委派
                        （在它们的「子智能体」页签里勾选），不会出现在输入框的角色选择器里，也不能再派自己的子智能体。
                      </p>
                    )}
                    <Panel>
                      <PanelTitle>身份</PanelTitle>
                      <div className="grid grid-cols-[88px_1fr] items-center gap-x-3 gap-y-2">
                        <label className="text-[12px] text-mute">ID</label>
                        <input
                          value={draft.id}
                          disabled={editingId !== 'new'}
                          onChange={event => setDraft({ ...draft, id: event.target.value })}
                          placeholder="ctf（小写字母、数字、-、_）"
                          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none disabled:opacity-60"
                        />
                        <label className="text-[12px] text-mute">名称</label>
                        <input
                          value={draft.name}
                          onChange={event => setDraft({ ...draft, name: event.target.value })}
                          placeholder="CTF 竞赛"
                          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                        <label className="text-[12px] text-mute">说明</label>
                        <input
                          value={draft.description ?? ''}
                          onChange={event => setDraft({ ...draft, description: event.target.value })}
                          placeholder="这个角色负责什么方向"
                          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                        <label className="text-[12px] text-mute">图标</label>
                        <div className="flex flex-wrap gap-1">
                          {ROLE_ICONS.map(icon => (
                            <button
                              key={icon}
                              type="button"
                              aria-label={`图标 ${icon}`}
                              onClick={() => setDraft({ ...draft, icon })}
                              className={`grid h-7 w-7 place-items-center rounded-lg ${
                                (draft.icon ?? 'robot') === icon ? 'bg-panel3 text-ink' : 'text-mute hover:bg-panel2'
                              }`}
                            >
                              <Icon name={icon} className="h-3.5 w-3.5" />
                            </button>
                          ))}
                        </div>
                      </div>
                    </Panel>

                    <Panel>
                      <PanelTitle>模型与思考级别</PanelTitle>
                      <div className="grid grid-cols-[88px_1fr] items-center gap-x-3 gap-y-2">
                        <label className="text-[12px] text-mute">模型</label>
                        <input
                          value={draft.model ?? ''}
                          onChange={event => setDraft({ ...draft, model: event.target.value })}
                          placeholder="留空 = 用界面里选的模型（provider/id）"
                          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                        <label className="text-[12px] text-mute">思考级别</label>
                        <select
                          value={draft.thinkingLevel ?? ''}
                          onChange={event => setDraft({ ...draft, thinkingLevel: event.target.value || undefined })}
                          className="h-8 rounded-lg bg-panel2 px-2 text-[12.5px] text-ink outline-none"
                        >
                          <option value="">跟随界面</option>
                          <option value="low">low</option>
                          <option value="medium">medium</option>
                          <option value="high">high</option>
                          <option value="max">max</option>
                        </select>
                        <label className="text-[12px] text-mute">作用域模型</label>
                        <input
                          value={(draft.scopedModels ?? []).join(', ')}
                          onChange={event => setDraft({
                            ...draft,
                            scopedModels: event.target.value.trim() === ''
                              ? undefined
                              : event.target.value.split(',').map(item => item.trim()).filter(Boolean),
                          })}
                          placeholder="cycle_model 只在这几个里循环，逗号分隔"
                          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                      </div>
                    </Panel>

                    <Panel>
                      <PanelTitle>会话</PanelTitle>
                      <label className="flex items-center gap-2 text-[12px] text-mute">
                        <input
                          type="checkbox"
                          checked={draft.contextFiles !== false}
                          onChange={event => setDraft({ ...draft, contextFiles: event.target.checked })}
                        />
                        加载工作区的 AGENTS.md 等目录上下文
                      </label>
                      <label className="mt-2 grid grid-cols-[88px_1fr] items-center gap-x-3">
                        <span className="text-[12px] text-mute">会话名前缀</span>
                        <input
                          value={draft.sessionNamePrefix ?? ''}
                          onChange={event => setDraft({ ...draft, sessionNamePrefix: event.target.value })}
                          placeholder="[CTF]（切换到这个角色后，新会话会用这个前缀命名）"
                          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                      </label>
                    </Panel>
                  </div>
                )}

                {tab === 'prompt' && (
                  <div className="flex max-w-[860px] flex-col gap-3">
                    <Panel>
                      <PanelTitle>角色提示词</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        默认**追加**到 ZeroA 的编码助手提示之后；勾选下面的开关才会整体替换。
                        写"工作流"比写"人设"有用：先说清先做什么、交付什么。
                        改动要重启 ZeroA 才生效（切换角色即重启）。
                      </p>
                      <textarea
                        value={prompt}
                        onChange={event => setPrompt(event.target.value)}
                        rows={22}
                        placeholder={'你是……\n\n工作流：\n1. 先侦察……\n2. 最小复现……\n3. 交付物：……'}
                        className="w-full resize-y rounded-lg bg-panel2 px-3 py-2.5 font-mono text-[12.5px] leading-[20px] text-ink outline-none"
                      />
                      <label className="mt-2 flex items-center gap-2 text-[12px] text-mute">
                        <input
                          type="checkbox"
                          checked={Boolean(draft.replaceSystemPrompt)}
                          onChange={event => setDraft({ ...draft, replaceSystemPrompt: event.target.checked })}
                        />
                        用它整体替换 ZeroA 的基础系统提示（默认追加）
                      </label>
                    </Panel>
                  </div>
                )}

                {tab === 'tools' && (
                  <div className="flex max-w-[900px] flex-col gap-4">
                    <Panel>
                      <div className="mb-2 flex items-center gap-2">
                        <PanelTitle>ZeroA 工具</PanelTitle>
                        <span className="flex-1" />
                        <Button size="sm" variant="ghost" onClick={() => setAllTools(true)}>全选</Button>
                        <Button size="sm" variant="ghost" onClick={() => setAllTools(false)}>全不选</Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => setDraft({ ...draft, tools: undefined })}
                          title="留空 = 不限制（ZeroA 的默认工具集）"
                        >
                          不限制
                        </Button>
                      </div>
                      <p className="mb-2 text-[11.5px] text-mute">
                        {toolsUnlimited
                          ? '当前：不限制（用 ZeroA 默认的 read/bash/edit/write）'
                          : activeTools.length === 0
                            ? '当前：全部禁用（--no-tools）'
                            : `当前：白名单 ${activeTools.length} 个`}
                      </p>
                      <div className="grid grid-cols-[repeat(auto-fill,minmax(210px,1fr))] gap-1.5">
                        {capabilities.piTools.map(tool => {
                          const on = !toolsUnlimited && activeTools.includes(tool.id);
                          return (
                            <button
                              key={tool.id}
                              type="button"
                              onClick={() => toggleTool(tool.id)}
                              className={`flex items-start gap-2 rounded-xl border px-2.5 py-2 text-left transition-colors ${
                                on ? 'border-[color-mix(in_srgb,var(--info)_45%,transparent)] bg-panel3' : 'border-line hover:bg-panel2'
                              }`}
                            >
                              <Icon name={on ? 'check' : 'tool'} className={`mt-0.5 h-3.5 w-3.5 shrink-0 ${on ? 'text-info' : 'text-mute'}`} />
                              <span className="min-w-0">
                                <span className="block font-mono text-[12px] text-ink">{tool.name}</span>
                                <span className="mt-0.5 block text-[11px] leading-[15px] text-mute">{tool.note}</span>
                              </span>
                            </button>
                          );
                        })}
                      </div>
                    </Panel>

                    <Panel>
                      <PanelTitle>方向工具线索（选填，进提示词）</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        这些是命令行工具，**不是** ZeroA 工具，没法用开关控制；它们的作用是让模型知道这台机器上有什么。
                        点方向后面的按钮把它们追加到角色提示词里。
                      </p>
                      <div className="flex flex-col gap-1.5">
                        {capabilities.toolHints.map(group => (
                          <div key={group.direction} className="flex items-center gap-2 rounded-lg bg-panel2 px-2.5 py-1.5">
                            <span className="w-[68px] shrink-0 text-[12px] text-dim">{group.label}</span>
                            <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-mute">
                              {group.tools.join(' · ')}
                            </span>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => {
                                setPrompt(`${prompt}\n\n## 可用工具\n${group.tools.map(tool => `- ${tool}`).join('\n')}\n`);
                                setTab('prompt');
                              }}
                            >
                              插入提示词
                            </Button>
                          </div>
                        ))}
                      </div>
                    </Panel>
                  </div>
                )}

                {tab === 'skills' && (
                  <div className="flex max-w-[980px] flex-col gap-4">
                    <Panel>
                      <div className="mb-2 flex items-center gap-2">
                        <PanelTitle>技能（按方向，勾选即启用）</PanelTitle>
                        <span className="flex-1" />
                        <span className="text-[11.5px] text-mute">
                          {skillsUnlimited
                            ? '当前：不限制（加载所有已发现技能）'
                            : `当前：勾选 ${activeSkills.length} 个`}
                        </span>
                        <Button size="sm" variant="ghost" onClick={() => setDraft({ ...draft, skills: undefined })}>不限制</Button>
                        <Button size="sm" variant="ghost" onClick={() => setDraft({ ...draft, skills: [] })}>全不用</Button>
                      </div>
                      <p className="mb-3 text-[11.5px] leading-[18px] text-mute">
                        技能目录：<span className="font-mono">{capabilities.skillsRoot || '（未知）'}</span>
                        {!capabilities.skillsExist && '（还不存在：用下面的「导入技能」拷一批进来）'}
                        。技能只在需要时才被读取全文，勾选它不会立刻占用上下文。
                      </p>
                      <div className="mb-3 flex items-center gap-2">
                        <Button size="sm" variant="outline" onClick={() => void capabilities.openPath(capabilities.skillsRoot)}>
                          <Icon name="folder" className="h-3.5 w-3.5" /> 打开技能目录
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={async () => {
                            // 选一个目录导入：技能包通常是"外面套一层 + 方向目录"
                            const picked = await import('@wailsio/runtime').then(m => m.Dialogs.OpenFile({
                              Title: '选择要导入的技能目录（里面应有 SKILL.md）',
                              CanChooseDirectories: true,
                              CanChooseFiles: false,
                            })).catch(() => null);
                            const dir = Array.isArray(picked) ? picked[0] : picked;
                            if (dir) await capabilities.importSkills(String(dir));
                          }}
                        >
                          <Icon name="folderPlus" className="h-3.5 w-3.5" /> 导入技能
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => void capabilities.loadSkills()}>重新扫描</Button>
                      </div>

                      {skillGroups.length === 0 && (
                        <p className="rounded-lg bg-panel2 px-3 py-2 text-[12px] text-mute">
                          还没有发现技能。点「导入技能」把技能包（含 SKILL.md 的目录）拷进技能目录。
                        </p>
                      )}

                      {skillGroups.map(group => {
                        const allOn = group.items.every(item => activeSkills.includes(item.id));
                        return (
                          <div key={group.category} className="mb-3">
                            <div className="mb-1.5 flex items-center gap-2">
                              <span className="text-[12px] text-dim">{group.label}</span>
                              <span className="text-[11px] text-caption">{group.items.length} 个</span>
                              <span className="flex-1" />
                              <button
                                type="button"
                                onClick={() => setSkillsForCategory(group.items, !allOn)}
                                className="text-[11.5px] text-mute hover:text-ink"
                              >
                                {allOn ? '全不选' : '全选'}
                              </button>
                            </div>
                            <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-1.5">
                              {group.items.map(skill => {
                                const on = activeSkills.includes(skill.id);
                                return (
                                  <button
                                    key={skill.id}
                                    type="button"
                                    title={skill.description}
                                    onClick={() => toggleSkill(skill.id)}
                                    className={`flex items-start gap-2 rounded-xl border px-2.5 py-2 text-left transition-colors ${
                                      on ? 'border-[color-mix(in_srgb,var(--info)_45%,transparent)] bg-panel3' : 'border-line hover:bg-panel2'
                                    }`}
                                  >
                                    <Icon name={on ? 'check' : 'list'} className={`mt-0.5 h-3.5 w-3.5 shrink-0 ${on ? 'text-info' : 'text-mute'}`} />
                                    <span className="min-w-0 flex-1">
                                      <span className="flex items-center gap-1.5">
                                        <span className="truncate font-mono text-[12px] text-ink">{skill.id}</span>
                                        {skill.broken && <Icon name="warn" className="h-3 w-3 shrink-0 text-warn" />}
                                      </span>
                                      <span className="mt-0.5 line-clamp-2 block text-[11px] leading-[15px] text-mute">
                                        {skill.description || '（没有 description：模型无法判断何时该用）'}
                                      </span>
                                      <span className="mt-1 block text-[10.5px] text-caption">
                                        {skill.files} 个文件 · {humanBytes(skill.bytes)}
                                        {skill.hasReferences && ' · 含参考资料'}
                                        {skill.license && ` · ${skill.license}`}
                                      </span>
                                    </span>
                                    {on && (
                                      <span
                                        role="button"
                                        tabIndex={0}
                                        title="打开技能目录"
                                        onClick={event => {
                                          event.stopPropagation();
                                          void capabilities.openPath(skill.dir);
                                        }}
                                        className="mt-0.5 text-caption hover:text-ink"
                                      >
                                        <Icon name="folder" className="h-3 w-3" />
                                      </span>
                                    )}
                                  </button>
                                );
                              })}
                            </div>
                          </div>
                        );
                      })}
                    </Panel>
                  </div>
                )}

                {tab === 'capabilities' && (
                  <CapabilitiesTab
                    roleId={editingId}
                    selected={draft.capabilities ?? []}
                    onToggle={capability => {
                      const current = draft.capabilities ?? [];
                      setDraft({
                        ...draft,
                        capabilities: current.includes(capability)
                          ? current.filter(item => item !== capability)
                          : [...current, capability],
                      });
                    }}
                    onSet={capabilities => setDraft({ ...draft, capabilities })}
                  />
                )}

                {tab === 'permissions' && (
                  <div className="flex max-w-[860px] flex-col gap-4">
                    <Panel>
                      <PanelTitle>这个角色申请的权限（角色层）</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        最终生效的是「角色 ∩ 用户 ∩ 全局 ∩ 会话」四层取最严，
                        所以这里选「允许」不代表就真的允许——要看去「权限」页看最终值。
                        <span className="ml-1 text-dim">提示词永远不是安全边界。</span>
                      </p>
                      <div className="flex flex-col gap-1.5">
                        {PERMISSION_ACTIONS.map(item => {
                          const current = draft.permissions?.[item.action];
                          return (
                            <div key={item.action} className="flex flex-wrap items-center gap-2 rounded-xl bg-panel2 px-3 py-2">
                              <span className="w-[110px] shrink-0 text-[12.5px] text-ink">{item.label}</span>
                              <span className="flex gap-0.5">
                                {(['', 'allow', 'ask', 'deny'] as const).map(decision => (
                                  <button
                                    key={decision || 'unset'}
                                    type="button"
                                    onClick={() => {
                                      const next = { ...(draft.permissions ?? {}) };
                                      if (decision === '') delete next[item.action];
                                      else next[item.action] = decision;
                                      setDraft({ ...draft, permissions: next });
                                    }}
                                    className={`rounded px-2 py-[2px] text-[11.5px] transition-colors ${
                                      (current ?? '') === decision ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel3'
                                    }`}
                                  >
                                    {decision === '' ? '不声明' : DECISION_TEXT[decision]}
                                  </button>
                                ))}
                              </span>
                              <span className="min-w-0 flex-1 truncate text-[11px] text-caption">{item.hint}</span>
                            </div>
                          );
                        })}
                      </div>
                    </Panel>
                  </div>
                )}

                {tab === 'mcp' && (
                  <div className="flex max-w-[900px] flex-col gap-4">
                    <Panel>
                      <PanelTitle>联网能力</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        ZeroA 内置工具里**没有**联网工具（read/bash/edit/write/grep/ls/find…），
                        web_search / fetch 这类能力来自扩展包。当前已装的包：
                      </p>
                      <div className="flex flex-wrap gap-1.5">
                        {capabilities.packages.length === 0 && <span className="text-[12px] text-mute">（没有安装任何包）</span>}
                        {capabilities.packages.map(name => (
                          <span key={name} className="rounded-lg bg-panel2 px-2 py-1 font-mono text-[11.5px] text-dim">{name}</span>
                        ))}
                      </div>
                      <p className={`mt-2 text-[12px] ${capabilities.webCapable ? 'text-dim' : 'text-warn'}`}>
                        {capabilities.webCapable
                          ? '已具备联网能力（工具由扩展注册，会话里的 system 消息能看到实际清单）。'
                          : '没有检测到提供联网能力的包：模型无法搜索或抓取网页。'}
                      </p>
                      {!capabilities.adapterReady && (
                        <p className="mt-1 text-[11.5px] text-mute">
                          也没有装 MCP 适配器（pi-mcp-adapter）：下面的服务器仍然可用 ZeroA 内置的 MCP 支持加载。
                        </p>
                      )}
                    </Panel>

                    <Panel>
                      <div className="mb-2 flex items-center gap-2">
                        <PanelTitle>MCP 服务器</PanelTitle>
                        <span className="flex-1" />
                        <span className="font-mono text-[11px] text-caption">{capabilities.mcpConfig}</span>
                        <Button size="sm" variant="outline" onClick={() => void capabilities.openPath(capabilities.mcpConfig)}>
                          <Icon name="folder" className="h-3.5 w-3.5" /> 打开
                        </Button>
                      </div>
                      <div className="flex flex-col gap-1.5">
                        {capabilities.mcpServers.length === 0 && (
                          <p className="rounded-lg bg-panel2 px-3 py-2 text-[12px] text-mute">
                            还没有配置 MCP 服务器。加一个之后，它的工具会以 <span className="font-mono">mcp__&lt;服务器&gt;__&lt;工具&gt;</span> 的形式出现。
                          </p>
                        )}
                        {capabilities.mcpServers.map(server => (
                          <div key={server.name} className="flex items-start gap-2 rounded-xl bg-panel2 px-3 py-2">
                            <Icon name={server.transport === 'http' ? 'db' : 'terminal'} className="mt-0.5 h-3.5 w-3.5 shrink-0 text-mute" />
                            <span className="min-w-0 flex-1">
                              <span className="flex items-center gap-2">
                                <span className="font-mono text-[12px] text-ink">{server.name}</span>
                                <span className="text-[10.5px] text-caption">{server.transport}</span>
                                <span className="text-[10.5px] text-caption">{server.exposure}</span>
                                {server.enabled === false && <span className="text-[10.5px] text-warn">已停用</span>}
                              </span>
                              <span className="mt-0.5 block truncate font-mono text-[11px] text-mute">
                                {server.command ? `${server.command} ${(server.args ?? []).join(' ')}` : server.url}
                              </span>
                              {server.description && <span className="mt-0.5 block text-[11px] text-mute">{server.description}</span>}
                            </span>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => void capabilities.saveMcp({ ...server, enabled: server.enabled === false })}
                            >
                              {server.enabled === false ? '启用' : '停用'}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              className="text-danger"
                              onClick={() => {
                                useConfirm.getState().ask({
                                  title: `删除服务器「${server.name}」？`,
                                  description: '只删这条配置，其他内容不动。',
                                  confirmLabel: '删除',
                                  danger: true,
                                  onConfirm: () => void capabilities.removeMcp(server.name),
                                });
                              }}
                            >
                              删除
                            </Button>
                          </div>
                        ))}
                      </div>
                      <AddMcpServer onSave={server => void capabilities.saveMcp(server)} />
                    </Panel>
                  </div>
                )}

                {tab === 'context' && (
                  <div className="flex max-w-[760px] flex-col gap-4">
                    <Panel>
                      <PanelTitle>自动压缩（走 RPC，切换角色即时生效）</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        上下文快满时自动压缩早期消息。这项走 <span className="font-mono">set_auto_compaction</span>，
                        不是启动参数，所以**不需要重启**就能改。方向不同要求也不同：
                        取证要尽量留原始证据，CTF 解题可以早点压。
                      </p>
                      <div className="flex items-center gap-1.5">
                        {[
                          { value: undefined, label: '跟随全局设置' },
                          { value: true, label: '总是自动压缩' },
                          { value: false, label: '关掉自动压缩' },
                        ].map(option => (
                          <button
                            key={String(option.value)}
                            type="button"
                            aria-pressed={draft.autoCompaction === option.value}
                            onClick={() => setDraft({ ...draft, autoCompaction: option.value })}
                            className={`rounded-lg px-2.5 py-1 text-[12px] transition-colors ${
                              draft.autoCompaction === option.value ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
                            }`}
                          >
                            {option.label}
                          </button>
                        ))}
                      </div>
                    </Panel>

                    <Panel>
                      <PanelTitle>任务循环（方案第十九节）</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        ZeroA 负责一轮之内的 LLM/工具循环；这里管的是**一轮结束之后**要不要继续、
                        卡住了怎么办。第一版只做方案点名的几件事：轮次上限、时长上限、卡住计数、手动停止。
                        留空 = 不限制（默认行为：什么都不做，完全由你驱动）。
                      </p>
                      <div className="grid grid-cols-[120px_1fr] items-center gap-x-3 gap-y-2">
                        <label className="text-[12px] text-mute">最多轮次</label>
                        <input
                          type="number"
                          min={0}
                          value={draft.loop?.maxRounds ?? 0}
                          onChange={event => setDraft({
                            ...draft,
                            loop: { ...(draft.loop ?? {}), maxRounds: Number(event.target.value) || undefined },
                          })}
                          placeholder="0 = 不限制"
                          className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                        <label className="text-[12px] text-mute">最长时长（秒）</label>
                        <input
                          type="number"
                          min={0}
                          value={draft.loop?.maxDurationSeconds ?? 0}
                          onChange={event => setDraft({
                            ...draft,
                            loop: { ...(draft.loop ?? {}), maxDurationSeconds: Number(event.target.value) || undefined },
                          })}
                          placeholder="0 = 不限制"
                          className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                        <label className="text-[12px] text-mute">到达上限</label>
                        <select
                          value={draft.loop?.onLimit ?? 'stop'}
                          onChange={event => setDraft({ ...draft, loop: { ...(draft.loop ?? {}), onLimit: event.target.value } })}
                          className="h-8 w-[220px] rounded-lg bg-panel2 px-2 text-[12.5px] text-ink outline-none"
                        >
                          <option value="stop">停下来告诉我</option>
                          <option value="summarize">先让它总结，再停</option>
                        </select>
                        <label className="text-[12px] text-mute">重复阈值</label>
                        <input
                          type="number"
                          min={2}
                          value={draft.loop?.stuck?.threshold ?? 3}
                          onChange={event => setDraft({
                            ...draft,
                            loop: {
                              ...(draft.loop ?? {}),
                              stuck: {
                                enabled: draft.loop?.stuck?.enabled ?? true,
                                threshold: Number(event.target.value) || 3,
                                window: draft.loop?.stuck?.window ?? 6,
                              },
                            },
                          })}
                          className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                        />
                        <label className="text-[12px] text-mute">卡住检测</label>
                        <label className="flex items-center gap-2 text-[12px] text-mute">
                          <input
                            type="checkbox"
                            checked={Boolean(draft.loop?.stuck?.enabled)}
                            onChange={event => setDraft({
                              ...draft,
                              loop: {
                                ...(draft.loop ?? {}),
                                stuck: {
                                  enabled: event.target.checked,
                                  threshold: draft.loop?.stuck?.threshold ?? 3,
                                  window: draft.loop?.stuck?.window ?? 6,
                                },
                              },
                            })}
                          />
                          同一组工具调用反复出现时介入（最近 6 条里重复 N 次）
                        </label>
                        <label className="text-[12px] text-mute">卡住时</label>
                        <select
                          value={draft.loop?.onStuck ?? 'replan'}
                          onChange={event => setDraft({ ...draft, loop: { ...(draft.loop ?? {}), onStuck: event.target.value } })}
                          className="h-8 w-[220px] rounded-lg bg-panel2 px-2 text-[12.5px] text-ink outline-none"
                        >
                          <option value="replan">发一条"换个思路"</option>
                          <option value="stop">直接停下来</option>
                        </select>
                      </div>
                    </Panel>

                    <Panel>
                      <PanelTitle>手动压缩时附加的指令</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        点「压缩上下文」时作为 <span className="font-mono">compact</span> 的
                        <span className="font-mono"> customInstructions</span> 发给 ZeroA：
                        告诉它哪些必须留下、哪些可以丢。
                      </p>
                      <textarea
                        value={draft.compactionInstructions ?? ''}
                        onChange={event => setDraft({ ...draft, compactionInstructions: event.target.value })}
                        rows={5}
                        placeholder="保留 flag、利用链与关键命令输出；压缩掉试探与失败尝试。"
                        className="w-full resize-y rounded-lg bg-panel2 px-3 py-2 text-[12.5px] leading-[19px] text-ink outline-none"
                      />
                      <div className="mt-2 flex items-center gap-2">
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={async () => {
                            const instructions = (draft.compactionInstructions ?? '').trim();
                            const result = await import('@/bridge/pi').then(m =>
                              m.piBridge.compact(instructions ? instructions : ''));
                            const { reportCompactResult } = await import('@/lib/reportError');
                            reportCompactResult(result);
                          }}
                        >
                          立即用这个指令压缩
                        </Button>
                        <span className="text-[11.5px] text-mute">会立即向当前会话发一次 compact</span>
                      </div>
                    </Panel>
                  </div>
                )}

                {tab === 'delegation' && (
                  <div className="flex max-w-[760px] flex-col gap-4">
                    <Panel>
                      <PanelTitle>允许这个角色派子智能体</PanelTitle>
                      <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                        子智能体是这个角色派出的独立 ZeroA 进程，按<span className="text-ink">子角色自己的定义</span>干活。
                        这里配的是<span className="text-ink">带外策略</span>：不放进工具参数，模型自己改不了；
                        实际上限还要与会话授权取交集。不勾 = 这个角色完全不能派人（安全默认）。
                        取消勾选只关掉能力，下面的配置会保留，勾回来即恢复。
                      </p>
                      <label className="flex items-center gap-2 text-[12px] text-mute">
                        <input
                          type="checkbox"
                          checked={Boolean(draft.delegation?.enabled)}
                          onChange={event =>
                            setDraft({
                              ...draft,
                              delegation: event.target.checked
                                ? { ...(draft.delegation ?? {}), enabled: true }
                                : draft.delegation
                                  ? { ...draft.delegation, enabled: false }
                                  : undefined,
                            })
                          }
                        />
                        允许派子智能体
                      </label>
                    </Panel>

                    {draft.delegation?.enabled && (
                      <>
                        <Panel>
                          <PanelTitle>能派哪些子智能体</PanelTitle>
                          <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                            白名单，只能派勾中的子智能体。<span className="text-ink">一个都不勾 = 不允许派任何子智能体</span>
                            （不是"不限制"）。子智能体用的是它自己的提示词 / 工具 / 模型，与它无关的别勾。
                            普通角色不会出现这里——要派一个新助手，先在左栏右键它「复制为子智能体」。
                          </p>
                          <div className="flex flex-wrap gap-1.5">
                            {roles.filter(role => role.id !== draft.id && role.kind === 'agent').map(role => {
                              const on = (draft.delegation?.allowedRoles ?? []).includes(role.id);
                              return (
                                <button
                                  key={role.id}
                                  type="button"
                                  aria-pressed={on}
                                  onClick={() => toggleAllowedRole(role.id)}
                                  className={`rounded-lg px-2.5 py-1 text-[12px] transition-colors ${
                                    on ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
                                  }`}
                                >
                                  {role.name}
                                </button>
                              );
                            })}
                            {roles.filter(role => role.kind === 'agent').length === 0 && (
                              <p className="text-[12px] leading-[18px] text-mute">
                                还没有可派的子智能体：点右上角「新建子智能体」，或在左栏右键一个角色「复制为子智能体」。
                              </p>
                            )}
                          </div>
                        </Panel>

                        <Panel>
                          <PanelTitle>协作方式</PanelTitle>
                          <div className="grid grid-cols-[120px_1fr] items-center gap-x-3 gap-y-2">
                            <label className="text-[12px] text-mute">派出模式</label>
                            <select
                              value={draft.delegation.mode ?? 'background'}
                              onChange={event => patchDelegation({ mode: event.target.value as Delegation['mode'] })}
                              className="h-8 w-[280px] rounded-lg bg-panel2 px-2 text-[12.5px] text-ink outline-none"
                            >
                              <option value="background">后台跑，父继续干活（默认）</option>
                              <option value="blocking">等结果（⚠️ 暂未实现，保存时会警告）</option>
                            </select>
                            <label className="text-[12px] text-mute">结果投递</label>
                            <select
                              value={draft.delegation.deliverOn ?? 'turn_end'}
                              onChange={event => patchDelegation({ deliverOn: event.target.value as Delegation['deliverOn'] })}
                              className="h-8 w-[280px] rounded-lg bg-panel2 px-2 text-[12.5px] text-ink outline-none"
                            >
                              <option value="turn_end">回合结束后一起给我（默认）</option>
                              <option value="checkpoint">子智能体一完成立刻插话</option>
                            </select>
                            <label className="text-[12px] text-mute">子智能体形态</label>
                            <select
                              value={draft.delegation.lifecycle ?? 'continuable'}
                              onChange={event => patchDelegation({ lifecycle: event.target.value as Delegation['lifecycle'] })}
                              className="h-8 w-[280px] rounded-lg bg-panel2 px-2 text-[12.5px] text-ink outline-none"
                            >
                              <option value="continuable">跑完保留进程，可继续发消息（默认）</option>
                              <option value="one-shot">跑完即释放，省内存</option>
                            </select>
                            <label className="text-[12px] text-mute">派出前先审计</label>
                            <label className="flex items-center gap-2 text-[12px] text-mute">
                              <input
                                type="checkbox"
                                checked={Boolean(draft.delegation.requireAudit)}
                                onChange={event => patchDelegation({ requireAudit: event.target.checked || undefined })}
                              />
                              父会话至少调过一次工具才允许派人
                            </label>
                            <label className="text-[12px] text-mute">拿到 flag 后</label>
                            <div className="flex items-center gap-1.5">
                              {[
                                { value: undefined, label: '默认（停掉）' },
                                { value: true, label: '停掉子智能体' },
                                { value: false, label: '让它们跑完' },
                              ].map(option => (
                                <button
                                  key={String(option.value)}
                                  type="button"
                                  aria-pressed={draft.delegation?.stopOnFlag === option.value}
                                  onClick={() => patchDelegation({ stopOnFlag: option.value })}
                                  className={`rounded-lg px-2.5 py-1 text-[12px] transition-colors ${
                                    draft.delegation?.stopOnFlag === option.value
                                      ? 'bg-panel3 text-ink'
                                      : 'text-dim hover:bg-panel2 hover:text-ink'
                                  }`}
                                >
                                  {option.label}
                                </button>
                              ))}
                            </div>
                          </div>
                        </Panel>

                        <Panel>
                          <PanelTitle>预算（0 = 用默认值）</PanelTitle>
                          <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
                            按"这条会话"计：累计派出数与同时在跑数都有限额，防止批量委派把机器跑满。
                          </p>
                          <div className="grid grid-cols-[140px_1fr] items-center gap-x-3 gap-y-2">
                            <label className="text-[12px] text-mute">累计最多派几个</label>
                            <input
                              type="number"
                              min={0}
                              value={draft.delegation.maxChildren ?? 0}
                              onChange={event => patchDelegation({ maxChildren: Number(event.target.value) || undefined })}
                              placeholder="0 = 默认"
                              className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                            />
                            <label className="text-[12px] text-mute">同时最多跑几个</label>
                            <input
                              type="number"
                              min={0}
                              value={draft.delegation.maxParallel ?? 0}
                              onChange={event => patchDelegation({ maxParallel: Number(event.target.value) || undefined })}
                              placeholder="0 = 默认"
                              className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                            />
                            <label className="text-[12px] text-mute">单个时限（秒）</label>
                            <input
                              type="number"
                              min={0}
                              value={draft.delegation.maxDurationSeconds ?? 0}
                              onChange={event => patchDelegation({ maxDurationSeconds: Number(event.target.value) || undefined })}
                              placeholder="0 = 不限时"
                              className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                            />
                            <label className="text-[12px] text-mute">空闲回收（秒）</label>
                            <input
                              type="number"
                              min={0}
                              value={draft.delegation.idleTimeoutSeconds ?? 0}
                              onChange={event => patchDelegation({ idleTimeoutSeconds: Number(event.target.value) || undefined })}
                              placeholder="0 = 默认 10 分钟"
                              className="h-8 w-[140px] rounded-lg bg-panel2 px-2.5 text-[12.5px] text-ink outline-none"
                            />
                          </div>
                        </Panel>
                      </>
                    )}
                  </div>
                )}
              </div>

              {/* 底部操作条 */}
              <footer className="flex shrink-0 items-center gap-2 border-t border-line px-4 py-2.5">
                <Button size="sm" onClick={() => void submit()}>{draft.kind === 'agent' ? '保存子智能体' : '保存角色'}</Button>
                {editing && <Button size="sm" variant="ghost" onClick={() => copyRole(editing)}>复制一份</Button>}
                {editing?.builtin && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      useConfirm.getState().ask({
                        title: `把「${editing.name}」恢复成默认？`,
                        description: '你改过的提示词与工具都会被内置版本覆盖。',
                        confirmLabel: '恢复默认',
                        danger: true,
                        onConfirm: async () => {
                          const ok = await reset(editing.id);
                          if (ok) setEditingId(editing.id);
                        },
                      });
                    }}
                  >
                    恢复默认
                  </Button>
                )}
                <span className="flex-1" />
                {draft.id && draft.id !== selected && draft.kind !== 'agent' && (
                  <span className="mr-1 text-[11.5px] text-mute">保存后在输入框左下角切换到这个角色</span>
                )}
                {draft.id && draft.kind === 'agent' && (
                  <span className="mr-1 text-[11.5px] text-mute">保存后在其他角色的「子智能体」页签里勾选它</span>
                )}
                {editing && !editing.builtin && (
                  <Button size="sm" variant="ghost" className="text-danger" onClick={() => confirmDelete(editing)}>
                    {editing.kind === 'agent' ? '删除子智能体' : '删除角色'}
                  </Button>
                )}
              </footer>
            </>
          )}
        </section>
      </div>

      {/* 左栏右键菜单：复制为另一类型。手写而不用 DropdownMenu——
          Radix 的 DropdownMenu 要常驻 trigger，右键位置触发得用 ContextMenu（没装）；
          菜单只有一项，自己管定位与关闭更直接（点任意处 / Escape 关闭，见上面的 effect）。 */}
      {listMenu && (
        <div
          className="fixed z-50 min-w-[180px] rounded-lg border border-line bg-raised p-1 text-ink shadow-[var(--shadow)]"
          style={{
            left: Math.min(listMenu.x, window.innerWidth - 190),
            top: Math.min(listMenu.y, window.innerHeight - 48),
          }}
        >
          {listMenu.role.kind === 'agent' ? (
            <button
              type="button"
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] outline-none select-none hover:bg-hover"
              onClick={() => {
                setListMenu(null);
                copyAsRole(listMenu.role);
              }}
            >
              <Icon name="robot" className="h-3.5 w-3.5 text-mute" /> 复制为角色
            </button>
          ) : (
            <button
              type="button"
              className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] outline-none select-none hover:bg-hover"
              onClick={() => {
                setListMenu(null);
                copyAsAgent(listMenu.role);
              }}
            >
              <Icon name="flag" className="h-3.5 w-3.5 text-mute" /> 复制为子智能体
            </button>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * 能力页签（方案第八、十、二十三节）。
 *
 * 这一屏回答的是"这个角色要什么本事、这台机器给不给得起"：
 * 左边是可声明的能力清单，右边是**编译预览**——Go 侧真的跑一遍
 * CapabilityResolver 之后得到了什么（哪些能力有工具、哪些没有、会禁用哪些工具）。
 * 显示的是 Effective，不是"角色申请了什么"。
 */
function CapabilitiesTab({
  roleId,
  selected,
  onToggle,
  onSet,
}: {
  roleId: string;
  selected: string[];
  onToggle(capability: string): void;
  onSet(capabilities: string[]): void;
}) {
  const [known, setKnown] = useState<string[]>([]);
  const [preview, setPreview] = useState<{
    // 后端 nil 切片在 JSON 里是 null：这里全部按"可能为 null"声明，读的时候用 ?? []
    resolutions?: { capability: string; available?: { name: string }[] | null; missing?: { name: string }[] | null }[];
    environment?: { commands?: Record<string, string>; pythonPackages?: string[]; applications?: string[]; missing?: string[] };
    warnings?: string[];
    effective?: { excludeTools?: string[] };
  } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void (async () => {
      try {
        const result = await piBridge.listTools();
        if (result.success) {
          const data = parseData<{ capabilities: string[] }>(result);
          setKnown(data?.capabilities ?? []);
        }
      } catch (error) {
        reportError(error, '能力');
      }
    })();
  }, []);

  async function refreshPreview(): Promise<void> {
    if (roleId === 'new') {
      useUI.getState().showNotice('先保存角色，才能预览编译结果');
      return;
    }
    setBusy(true);
    try {
      const result = await piBridge.resolveRole(roleId);
      if (!result.success) {
        reportError(result.error || '编译预览失败', '能力');
        return;
      }
      setPreview(parseData(result) ?? null);
    } catch (error) {
      reportError(error, '能力');
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    if (roleId && roleId !== 'new') void refreshPreview();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roleId]);

  return (
    <div className="flex max-w-[1000px] flex-col gap-4">
      <Panel>
        <div className="mb-2 flex items-center gap-2">
          <PanelTitle>角色需要的能力</PanelTitle>
          <span className="flex-1" />
          <span className="text-[11.5px] text-mute">已选 {selected.length} 项</span>
          <Button size="sm" variant="ghost" onClick={() => onSet([])}>清空</Button>
        </div>
        <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
          写**能力**而不是工具名：能力会由解析器映射到本机实际存在的程序
          （装了 Ghidra 就给 Ghidra，只装了 radare2 就给 radare2）。
          **只有勾选的能力才会给这个角色**——它的提示词里只会出现这些能力对应的工具，没勾选的不会给。
          本机没有的能力会明确标出来，角色仍然可以启动——只是它可能做不到这件事。
        </p>
        <div className="flex flex-wrap gap-1.5">
          {known.length === 0 && <span className="text-[12px] text-mute">（读取能力清单中…）</span>}
          {known.map(capability => {
            const on = selected.includes(capability);
            return (
              <button
                key={capability}
                type="button"
                onClick={() => onToggle(capability)}
                className={`rounded-lg border px-2 py-1 font-mono text-[11.5px] transition-colors ${
                  on ? 'border-[color-mix(in_srgb,var(--info)_45%,transparent)] bg-panel3 text-ink' : 'border-line text-mute hover:bg-panel2'
                }`}
              >
                {capability}
              </button>
            );
          })}
        </div>
      </Panel>

      <Panel>
        <div className="mb-2 flex items-center gap-2">
          <PanelTitle>编译预览（这台机器上实际会用什么）</PanelTitle>
          <span className="flex-1" />
          <Button size="sm" variant="outline" disabled={busy} onClick={() => void refreshPreview()}>
            {busy ? '编译中…' : '重新编译'}
          </Button>
        </div>
        {!preview && <p className="text-[12px] text-mute">保存角色后点「重新编译」看结果。</p>}
        {preview && (
          <div className="flex flex-col gap-2">
            {(preview.resolutions ?? []).length === 0 && (
              <p className="text-[12px] text-mute">这个角色没有声明能力。</p>
            )}
            {(preview.resolutions ?? []).map(resolution => {
              const available = resolution.available ?? [];
              const missing = resolution.missing ?? [];
              return (
              <div key={resolution.capability} className="flex flex-wrap items-center gap-2 rounded-lg bg-panel2 px-2.5 py-1.5">
                <span className="shrink-0 font-mono text-[11.5px] text-dim">{resolution.capability}</span>
                {available.length > 0 ? (
                  available.map(tool => (
                    <span key={tool.name} className="rounded bg-panel3 px-1.5 py-[1px] text-[11px] text-ink">{tool.name}</span>
                  ))
                ) : (
                  <span className="text-[11px] text-warn">本机没有对应工具</span>
                )}
                {missing.length > 0 && (
                  <span className="text-[10.5px] text-caption">
                    （也可以：{missing.map(tool => tool.name).join(' / ')}）
                  </span>
                )}
              </div>
              );
            })}
            {(preview.warnings ?? []).length > 0 && (
              <div className="rounded-lg bg-[color-mix(in_srgb,var(--warn)_10%,transparent)] px-2.5 py-2">
                {(preview.warnings ?? []).map(warning => (
                  <div key={warning} className="text-[11.5px] text-warn">· {warning}</div>
                ))}
              </div>
            )}
            <div className="grid grid-cols-2 gap-2 text-[11.5px] text-mute">
              <div>
                <div className="mb-0.5 text-caption">可用命令</div>
                <div className="font-mono text-[11px] text-dim">
                  {Object.keys(preview.environment?.commands ?? {}).join('、') || '（无）'}
                </div>
              </div>
              <div>
                <div className="mb-0.5 text-caption">Python 包 / 桌面程序</div>
                <div className="font-mono text-[11px] text-dim">
                  {[...(preview.environment?.pythonPackages ?? []), ...(preview.environment?.applications ?? [])].join('、') || '（无）'}
                </div>
              </div>
            </div>
            {(preview.effective?.excludeTools ?? []).length > 0 && (
              <div className="text-[11.5px] text-mute">
                权限导致的工具禁用：
                <span className="ml-1 font-mono text-[11px] text-warn">{(preview.effective?.excludeTools ?? []).join('、')}</span>
              </div>
            )}
          </div>
        )}
      </Panel>
    </div>
  );
}

/** 新增 MCP 服务器的小表单：默认给 stdio（最常见） */function AddMcpServer({ onSave }: { onSave(server: import('@/types/capabilities').McpServer): void }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState('');
  const [command, setCommand] = useState('');
  const [args, setArgs] = useState('');
  const [url, setUrl] = useState('');
  const [exposure, setExposure] = useState('codemode');
  const [description, setDescription] = useState('');

  if (!open) {
    return (
      <Button size="sm" variant="outline" className="mt-2" onClick={() => setOpen(true)}>
        <Icon name="plus" className="h-3.5 w-3.5" /> 添加服务器
      </Button>
    );
  }

  return (
    <div className="mt-3 flex flex-col gap-2 rounded-xl border border-line p-3">
      <div className="grid grid-cols-[72px_1fr] items-center gap-x-3 gap-y-2">
        <label className="text-[12px] text-mute">名称</label>
        <input value={name} onChange={event => setName(event.target.value)} placeholder="search"
          className="h-8 rounded-lg bg-panel2 px-2.5 font-mono text-[12px] text-ink outline-none" />
        <label className="text-[12px] text-mute">命令</label>
        <input value={command} onChange={event => setCommand(event.target.value)} placeholder="npx（stdio 传输）"
          className="h-8 rounded-lg bg-panel2 px-2.5 font-mono text-[12px] text-ink outline-none" />
        <label className="text-[12px] text-mute">参数</label>
        <input value={args} onChange={event => setArgs(event.target.value)} placeholder="-y @modelcontextprotocol/server-xxx"
          className="h-8 rounded-lg bg-panel2 px-2.5 font-mono text-[12px] text-ink outline-none" />
        <label className="text-[12px] text-mute">或 URL</label>
        <input value={url} onChange={event => setUrl(event.target.value)} placeholder="https://example.com/mcp（HTTP 传输）"
          className="h-8 rounded-lg bg-panel2 px-2.5 font-mono text-[12px] text-ink outline-none" />
        <label className="text-[12px] text-mute">暴露方式</label>
        <select value={exposure} onChange={event => setExposure(event.target.value)}
          className="h-8 rounded-lg bg-panel2 px-2 text-[12px] text-ink outline-none">
          <option value="codemode">codemode（脚本里调用，默认）</option>
          <option value="deferred">deferred（tool_search 加载）</option>
          <option value="direct">direct（直接声明给模型）</option>
          <option value="hidden">hidden（注册但不可达）</option>
        </select>
        <label className="text-[12px] text-mute">说明</label>
        <input value={description} onChange={event => setDescription(event.target.value)} placeholder="一句话：这个服务器提供什么"
          className="h-8 rounded-lg bg-panel2 px-2.5 text-[12px] text-ink outline-none" />
      </div>
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          onClick={() => {
            onSave({
              name: name.trim(),
              command: command.trim() || undefined,
              args: args.trim() ? args.split(/\s+/).filter(Boolean) : undefined,
              url: url.trim() || undefined,
              exposure,
              description: description.trim() || undefined,
            });
            setOpen(false);
            setName(''); setCommand(''); setArgs(''); setUrl(''); setDescription('');
          }}
        >
          保存
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>取消</Button>
      </div>
    </div>
  );
}

export { RoleBadge };
