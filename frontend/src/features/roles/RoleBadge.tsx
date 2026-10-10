import { Icon } from '@/components/ui/Icon';
import { Spinner } from '@/components/ui/Spinner';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { useAgent } from '@/hooks/useAgent';
import { useConfirm } from '@/stores/confirm';
import { roleNeedsRestart, useRoles } from '@/stores/roles';
import { useRuntime } from '@/stores/runtime';
import { useUI } from '@/stores/ui';
import type { RoleIcon } from '@/types/role';

/** 这些状态表示"Pi 正在干活"：切角色要重启进程，先问一句 */
const BUSY = new Set(['running', 'retrying', 'compacting', 'waiting-ui']);

/**
 * 角色徽标：钉在输入框左下角（与附件 + 同一行，照 DSH 的工具行布局）。
 *
 * 为什么放在这：角色决定了"这一轮用什么提示词、有哪些工具"，它和模型选择
 * 一样属于"发送前要确认的上下文"，所以要跟输入框待在一起，而不是埋进设置里。
 */
export function RoleBadge() {
  const roles = useRoles(s => s.roles);
  const selected = useRoles(s => s.selected);
  const processRole = useRoles(s => s.processRole);
  const status = useRuntime(s => s.status);
  const busy = useRuntime(s => s.busy);
  /** 角色切换全程的标志（重启 + 新会话 + 命名）：按钮转圈代替"待重启生效"文字 */
  const roleSwitching = useRoles(s => s.switching);
  const { switchRole } = useAgent();

  const current = roles.find(role => role.id === selected);
  // 子智能体不进选择器：它们是"被委派的工作单元"，不是人切换的方向。
  // current 仍从全量里查——工作区记忆指向一个子智能体（老数据）时，徽标照样显示名字，
  // 而不是突然变成"无角色"让人摸不着头脑。
  const selectable = roles.filter(role => role.kind !== 'agent');
  // 选中的角色还没生效（进程还是旧角色）：这种情况必须让用户看出来，
  // 否则他会以为提示词已经换了，实际上这一轮还是旧角色的工具集。
  const pending = roleNeedsRestart(selected, processRole);

  function pick(id: string): void {
    if (id === selected && !pending) return;
    const target = roles.find(role => role.id === id);
    const run = () => void switchRole(id);
    if (BUSY.has(useRuntime.getState().status)) {
      useConfirm.getState().ask({
        title: `切换到角色「${target?.name ?? id}」？`,
        description: '切换角色要重启 ZeroA（提示词与工具集只能在启动时设定），'
          + '当前回合会被中断，并开始一个属于新角色的新会话。旧会话保留在左侧历史里。',
        confirmLabel: '切换并新开会话',
        onConfirm: run,
      });
      return;
    }
    run();
  }

  function openSettings(): void {
    // 打开独立的角色管理页面（不再是设置里的一个小分栏）
    useUI.getState().setPage('roles');
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          disabled={busy || roleSwitching}
          aria-label="选择角色"
          title={
            current
              ? `当前角色：${current.name}`
              : '选择角色：为不同方向（CTF / 应急响应 / 取证）配好提示词与工具'
          }
          className={`inline-flex h-7 max-w-[200px] items-center gap-1.5 rounded-full px-2.5 text-[12px] transition-colors disabled:opacity-50 ${
            current ? 'bg-raised text-ink hover:bg-hover' : 'text-mute hover:bg-panel3 hover:text-ink'
          }`}
        >
          <Icon name={(current?.icon as RoleIcon) ?? 'robot'} className={`h-3.5 w-3.5 ${current ? 'text-dim' : 'text-mute'}`} />
          <span className="min-w-0 truncate">{roleSwitching ? '切换中…' : current ? current.name : '无角色'}</span>
          {/* 切换失败的残余信号（选中≠进程角色）保留这个小圆点；切换中由转圈表达 */}
          {pending && !roleSwitching && <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warn" title="尚未生效" />}
          {roleSwitching
            ? <Spinner className="h-3 w-3 shrink-0 text-mute" />
            : <Icon name="chevron" className="h-3 w-3 shrink-0 text-mute" />}
        </button>
      </DropdownMenuTrigger>

      <DropdownMenuContent align="start" className="w-[320px]">
        <div className="px-2 py-1 text-[11px] text-mute">角色</div>
        {selectable.length === 0 && (
          <div className="px-2 py-1.5 text-[12px] text-mute">还没有角色（进入设置 →「角色」新建）</div>
        )}
        {selectable.map(role => (
          <DropdownMenuItem
            key={role.id}
            disabled={Boolean(role.broken)}
            onSelect={() => pick(role.id)}
            className="flex items-start gap-2"
          >
            <Icon name={(role.icon as RoleIcon) ?? 'robot'} className="mt-0.5 h-3.5 w-3.5 shrink-0 text-mute" />
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-1.5">
                <span className="truncate">{role.name}</span>
                {role.id === selected && <Icon name="check" className="h-3 w-3 shrink-0 text-dim" />}
                {role.broken && <span className="shrink-0 text-[11px] text-danger">配置损坏</span>}
              </span>
              {role.description && (
                <span className="mt-0.5 block truncate text-[11px] text-mute">{role.description}</span>
              )}
            </span>
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        {selected && (
          <DropdownMenuItem onSelect={() => pick('')}>
            <Icon name="x" className="h-3.5 w-3.5" /> 不使用角色（用 ZeroA 默认）
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onSelect={openSettings}>
          <Icon name="gear" className="h-3.5 w-3.5" /> 管理角色…
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
