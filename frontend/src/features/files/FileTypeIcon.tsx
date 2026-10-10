/**
 * 文件类型图标（照 VS Code / DSH 的 FileTypeIcon 做法）：
 * 一个圆角色块 + 类型缩写，按扩展名上色。
 *
 * 为什么不用单色线性图标：标签页里一眼要能区分「这是 ts 还是 go」，
 * 颜色是最快的线索；缩写只放 1-3 个字符，窄标签也放得下。
 */
const TYPE_STYLES: { match: RegExp; label: string; color: string }[] = [
  { match: /\.(ts|tsx)$/i, label: 'TS', color: '#3178c6' },
  { match: /\.(js|jsx|mjs|cjs)$/i, label: 'JS', color: '#d9b400' },
  { match: /\.go$/i, label: 'GO', color: '#00add8' },
  { match: /\.(json|jsonc)$/i, label: '{}', color: '#cbcb41' },
  { match: /\.(md|markdown)$/i, label: 'MD', color: '#8a8a8f' },
  { match: /\.css$/i, label: 'CS', color: '#563d7c' },
  { match: /\.(html|htm)$/i, label: '<>', color: '#e34c26' },
  { match: /\.(yml|yaml)$/i, label: 'Y', color: '#cb171e' },
  { match: /\.(py)$/i, label: 'PY', color: '#3572a5' },
  { match: /\.(png|jpe?g|gif|webp|ico|svg)$/i, label: 'IM', color: '#a074c4' },
  { match: /\.(sh|ps1|bat|cmd)$/i, label: '>_', color: '#4eaa25' },
  { match: /^\.git/i, label: 'GI', color: '#f14e32' },
  { match: /\.(txt|log)$/i, label: 'TX', color: '#8a8a8f' },
  { match: /\.(toml|ini|cfg)$/i, label: 'CF', color: '#9c6b3f' },
];

export function FileTypeIcon({ name, className }: { name: string; className?: string }) {
  const style = TYPE_STYLES.find(item => item.match.test(name));
  const label = style?.label ?? '·';
  const color = style?.color ?? 'var(--text-mute)';
  return (
    <span
      aria-hidden="true"
      style={{ background: color }}
      className={`grid h-4 w-4 shrink-0 place-items-center rounded-[4px] text-[8px] font-semibold leading-none text-white ${className ?? ''}`}
    >
      {label}
    </span>
  );
}
