import { Row } from '@/components/ui/Row';
import type { IconName } from '@/components/ui/Icon';
import type { SystemSubtype } from '@/stores/timeline';

const STYLE: Record<SystemSubtype, { icon: IconName; tone: 'default' | 'err' | 'warn'; title: string }> = {
  compaction: { icon: 'db', tone: 'default', title: '上下文已压缩' },
  retry: { icon: 'warn', tone: 'warn', title: '自动重试' },
  error: { icon: 'warn', tone: 'err', title: '错误' },
  info: { icon: 'list', tone: 'default', title: '信息' },
};

/** 系统事件（压缩/重试/错误/进程信息）：DSH 里也是一行折叠行，不额外占版面 */
export function SystemCard({ subtype, title, text, time }: { subtype: SystemSubtype; title: string; text: string; time: string }) {
  const style = STYLE[subtype];
  return <Row icon={style.icon} title={title || style.title} summary={text} meta={time} tone={style.tone} />;
}
