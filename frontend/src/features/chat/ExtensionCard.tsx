import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/primitives';
import { Row } from '@/components/ui/Row';
import { useAgent } from '@/hooks/useAgent';

/**
 * 扩展请求（extension_ui_request）：权限确认。
 * 折叠状态是一行警示行；展开后给出命令原文与三个选择。
 * 选择通过 extension_ui_response 回传，扩展才会继续。
 */
export function ExtensionCard({ requestId, method, title, message, status, agentId, time }: {
  requestId: string;
  method: string;
  title: string;
  message: string;
  status: 'pending' | 'allowed' | 'denied';
  /** 非空 = 子智能体的请求，应答发回它自己的进程 */
  agentId?: string;
  time: string;
}) {
  const { respondExtension } = useAgent();
  const pending = status === 'pending';

  return (
    <Row
      icon="warn"
      title={title}
      summary={pending ? message : `${status === 'allowed' ? '已允许' : '已拒绝'} · ${method}`}
      meta={time}
      tone={pending ? 'warn' : 'default'}
      expandable
      expanded={pending}
    >
      <div className="mb-2 ml-[22px] flex flex-col gap-2">
        <p className="whitespace-pre-wrap text-[13px] leading-[22px] text-dim">{message}</p>
        {pending ? (
          <div className="flex gap-2">
            <Button size="sm" variant="default" onClick={() => void respondExtension(requestId, { confirmed: true }, agentId)}>允许一次</Button>
            <Button size="sm" onClick={() => void respondExtension(requestId, { confirmed: true, always: true }, agentId)}>始终允许</Button>
            <Button size="sm" variant="destructive" onClick={() => void respondExtension(requestId, { confirmed: false }, agentId)}>拒绝</Button>
          </div>
        ) : (
          <span className="font-mono text-[11px] text-mute">
            extension_ui_response · id={requestId} · confirmed={String(status === 'allowed')}
          </span>
        )}
      </div>
    </Row>
  );
}

/** 一轮结束的分隔线：agent_settled 才代表真的空下来 */
export function SettledDivider({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-3 py-1 text-[12px] text-mute">
      <Icon name="check" className="h-3.5 w-3.5" />
      <span>{text}</span>
      <span className="h-px flex-1 bg-line" />
    </div>
  );
}
