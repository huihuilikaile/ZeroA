/** 展示层的格式化小工具（不涉及状态，纯函数） */
import type { AgentStatus } from '@/stores/runtime';

export function duration(startedAt: number, finishedAt?: number): string {
  const ms = (finishedAt ?? Date.now()) - startedAt;
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export function fmtTokens(n: number): string {
  if (!n) return '0';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

export function fmtCost(n: number): string {
  if (!n) return '$0';
  return `$${n < 0.01 ? n.toFixed(5) : n.toFixed(3)}`;
}

/** 文件大小：1 KB 以下按字节，以上按 KB/MB 一位小数（文件树 tooltip 与预览头用） */
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** 附件图标：图片用一个图标，其它类型用文件图标（附件不再只有图片） */
export function attachmentIcon(path: string): 'image' | 'file' {
  return /\.(png|jpe?g|webp|gif)$/i.test(path) ? 'image' : 'file';
}

/** 附件显示名：只取文件名，路径太长会把 chip 撑爆 */
export function attachmentName(path: string): string {
  const parts = path.split(/[\\/]/);
  return parts[parts.length - 1] || path;
}

/**
 * 相对时间：会话列表里显示「刚刚 / 3 分钟前 / 3 天前」。
 * 用相对时间而不是绝对时间，是因为列表要回答的是「哪个是最近的对话」。
 */
export function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (!then || Number.isNaN(then)) return '';
  const minutes = Math.floor((Date.now() - then) / 60_000);
  if (minutes < 1) return '刚刚';
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return new Date(iso).toLocaleDateString();
}

export const STATUS_LABEL: Record<AgentStatus, string> = {
  offline: '未连接',
  idle: '空闲',
  running: '运行中',
  'waiting-ui': '等待确认',
  retrying: '重试中',
  compacting: '压缩中',
  error: '出错',
};

/** 状态对应的圆点颜色（用语义色，两套主题自动适配） */
export const STATUS_DOT: Record<AgentStatus, string> = {
  offline: 'bg-mute',
  idle: 'bg-success',
  running: 'bg-info',
  'waiting-ui': 'bg-warn',
  retrying: 'bg-warn',
  compacting: 'bg-think',
  error: 'bg-danger',
};
