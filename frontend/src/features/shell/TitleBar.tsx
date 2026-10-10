import { useState } from 'react';
import { Window } from '@wailsio/runtime';
import { Icon } from '@/components/ui/Icon';
import { cn } from '@/lib/utils';
import { requestClose } from '@/hooks/useCloseGuard';
import { useUI } from '@/stores/ui';

/**
 * 自绘标题栏（窗口是无边框的，见 main.go 的 Frameless: true）。
 *
 * 两条 Wails 约定：
 *   · 拖动：给元素加 CSS 变量 `--wails-draggable: drag`，运行时据此发起拖动；
 *     按钮必须标 `no-drag`，否则点不动。
 *   · 控制：直接用运行时导出的当前窗口对象 Window.Minimise/ToggleMaximise/Close。
 *
 * 为什么自绘：系统标题栏是浅色的，暗色主题下会形成一条刺眼的白边。
 */
export function TitleBar() {
  /** 当前主页面：标题栏上做主导航（方案第二十二节） */
  const page = useUI(s => s.page);

  return (
    <div
      className="flex h-9 select-none items-center gap-2 bg-panel px-3"
      style={{ ['--wails-draggable' as string]: 'drag' }}
    >
      {/* 品牌标记：图标本身就是"Z+A"的连体字形，直接用小尺寸位图即可
          （放在 public/ 里由 Vite 原样拷进产物，不需要 import 与类型声明）。 */}
      <img src="/zeroa-mark-128.png" alt="ZeroA" className="h-5 w-5 rounded-[5px]" draggable={false} />
      <span className="text-[12px] text-dim">
        Zero<span className="font-semibold text-[var(--brand)]">A</span>
      </span>

      {/* 主导航（方案第二十二节）：Chat / Roles / Tools / Permissions。
          放在标题栏而不是侧栏顶部：侧栏已经被工作区与会话占满，
          而这里是"换一屏"的操作，和窗口控制同一层更符合直觉。 */}
      <span className="ml-3 flex items-center gap-0.5" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
        {([
          { key: 'chat', label: '对话', icon: 'chat' },
          { key: 'roles', label: '角色', icon: 'shield' },
          { key: 'tools', label: '工具', icon: 'tool' },
          { key: 'permissions', label: '权限', icon: 'db' },
        ] as const).map(item => (
          <button
            key={item.key}
            type="button"
            onClick={() => useUI.getState().setPage(item.key)}
            className={`flex items-center gap-1.5 rounded-lg px-2 py-1 text-[12px] transition-colors ${
              page === item.key ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
            }`}
          >
            <Icon name={item.icon} className="h-3.5 w-3.5 text-mute" />
            {item.label}
          </button>
        ))}
      </span>

      <span className="flex-1" />

      <WindowControls />
    </div>
  );
}

/**
 * 窗口三件套：最小化 / 最大化（还原）/ 关闭。
 *
 * 抽出来是因为启动屏也要它：开屏不显示整条标题栏（用户只要按钮），
 * 但这三个按钮必须一直在——启动不是"锁住窗口"的理由（见 StartupSplash）。
 */
export function WindowControls() {
  const [maximised, setMaximised] = useState(false);
  return (
    <>
      <WindowButton
        label="最小化"
        onClick={() => void Window.Minimise()}
        icon={<span className="block h-[1px] w-[10px] bg-current" />}
      />
      <WindowButton
        label={maximised ? '还原' : '最大化'}
        onClick={() => {
          void Window.ToggleMaximise();
          setMaximised(v => !v);
        }}
        icon={
          maximised ? (
            <span className="relative block h-[9px] w-[9px]">
              <span className="absolute top-0 right-0 h-[7px] w-[7px] border border-current" />
              <span className="absolute bottom-0 left-0 h-[7px] w-[7px] border border-current bg-panel" />
            </span>
          ) : (
            <span className="block h-[9px] w-[9px] border border-current" />
          )
        }
      />
      <WindowButton
        label="关闭"
        danger
        onClick={requestClose}
        icon={<Icon name="x" className="h-3.5 w-3.5" />}
      />
    </>
  );
}

function WindowButton({
  label,
  onClick,
  icon,
  danger,
}: {
  label: string;
  onClick(): void;
  icon: React.ReactNode;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      style={{ ['--wails-draggable' as string]: 'no-drag' }}
      className={cn(
        // 仿 DSH 的窗口按钮：平时没有底色，只有 hover 才浮起来一点；关闭是红色
        'grid h-8 w-10 place-items-center rounded-md text-dim transition-colors',
        danger ? 'hover:bg-[#e81123] hover:text-white' : 'hover:bg-hover hover:text-ink',
      )}
    >
      {icon}
    </button>
  );
}
