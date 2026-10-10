import { WindowControls } from '@/features/shell/TitleBar';

/**
 * 启动屏：应用起来时先显示它，背后同时做几件事——读配置、启动 ZeroA、恢复会话。
 *
 * 纯文字「ZeroA」，五个字母依次轻轻跳动：
 *   · 动画只动 transform（GPU 合成层，不触发重排重绘）——之前试过渐变扫光，
 *     它在启动最忙的阶段逐帧重绘文字，肉眼可见地卡；
 *   · 不显示进度：各步骤耗时不可预知，百分比是编的，跳动的字母本身就在说"正在加载"；
 *   · leaving = 启动完成：整屏 300ms 淡出并停收事件，下方应用界面接棒，形成过渡
 *    （挂载时机的管理在 AppShell）。
 *
 * 开屏不显示整条标题栏，但右上角始终有窗口三件套（最小化/最大化/关闭），
 * 顶部一条隐形区域仍可拖动窗口——启动不是"锁住窗口"的理由。
 */
const LETTERS = ['Z', 'e', 'r', 'o', 'A'];

export function StartupSplash({ leaving }: { leaving?: boolean }) {
  return (
    <div
      className={`fixed inset-0 z-[90] bg-bg transition-opacity duration-300 ${
        leaving ? 'pointer-events-none opacity-0' : 'opacity-100'
      }`}
      role="status"
      aria-live="polite"
      aria-label="ZeroA 正在启动"
    >
      {/* 顶部隐形条：能拖窗口，右侧是三个窗口按钮（与 TitleBar 同一组组件） */}
      <div
        className="absolute inset-x-0 top-0 flex h-9 items-center justify-end px-1"
        style={{ ['--wails-draggable' as string]: 'drag' }}
      >
        <WindowControls />
      </div>

      <div className="grid h-full place-items-center">
        <span className="flex text-[40px] font-semibold tracking-tight text-ink">
          {LETTERS.map((ch, i) => (
            <span
              key={i}
              className={`splash-letter${ch === 'A' ? ' text-[var(--brand)]' : ''}`}
              style={{ animationDelay: `${i * 120}ms` }}
            >
              {ch}
            </span>
          ))}
        </span>
      </div>
    </div>
  );
}
