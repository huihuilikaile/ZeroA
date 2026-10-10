import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Icon } from '@/components/ui/Icon';
import { Panel, PanelTitle } from '@/components/ui/primitives';
import { piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import { parseData } from '@/rpc/types';
import { useUI } from '@/stores/ui';

type Decision = 'allow' | 'ask' | 'deny';
/** 单层的取值；abstain = 这一层没表态，不参与取严 */
type LayerDecision = Decision | 'abstain';

interface PermissionSnapshot {
  action: string;
  decision: Decision;
  role: LayerDecision;
  user: LayerDecision;
  global: LayerDecision;
  session: LayerDecision;
  temporary?: { action: string; expiresAt: string; reason?: string };
  enforced: boolean;
  note?: string;
}

interface PermissionPayload {
  actions: string[];
  global: Record<string, Decision>;
  user: Record<string, Decision>;
  role: Record<string, Decision>;
  evaluated: PermissionSnapshot[];
  temporary: { action: string; expiresAt: string; reason?: string }[];
  rolesUsing: string;
}

const ACTION_LABELS: Record<string, string> = {
  'filesystem.read': '读取文件',
  'filesystem.write': '写入文件',
  'shell.execute': '执行命令',
  'network.public': '访问公网',
  'network.target': '访问靶机/内网',
  'process.spawn': '启动进程',
};

/**
 * 四层各自的取值文案。
 *
 * `abstain` = 这一层没有对这个动作表态（弃权），它**不参与取严**。
 * 不能把它显示成"禁止"：那会让人以为"角色禁止读文件"，而最终结果却是允许——
 * 看起来像算错了，其实只是那一层压根没提这件事。
 */
const DECISION_LABELS: Record<Decision | 'abstain', string> = {
  allow: '允许',
  ask: '询问',
  deny: '禁止',
  abstain: '未声明',
};

/**
 * 权限页（方案第十六、十七、二十三节）。
 *
 * 两条设计原则写在这里，因为它决定了整页的样子：
 *
 *  1. 显示的是 **Effective（最终生效值）**，不是"角色申请了什么"——
 *     四层交集之后才是我实际被允许做的事。
 *  2. 对"能不能真的拦住"必须诚实：能被工具开关强制的标「强制」，
 *     只能记录与提示的标「仅记录」。假装拦住了比不拦更危险。
 */
export function PermissionsPage() {
  const [payload, setPayload] = useState<PermissionPayload | null>(null);
  const [busy, setBusy] = useState(false);
  const [minutes, setMinutes] = useState(30);

  async function load(): Promise<void> {
    try {
      const result = await piBridge.getPermissions();
      if (!result.success) {
        reportError(result.error || '读取权限失败', '权限');
        return;
      }
      const data = parseData<PermissionPayload>(result);
      if (data) setPayload(data);
    } catch (error) {
      reportError(error, '权限');
    }
  }

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function setLayer(layer: 'global' | 'user', action: string, decision: Decision): Promise<void> {
    if (!payload) return;
    const next = { ...(payload[layer] ?? {}), [action]: decision };
    setBusy(true);
    try {
      const result = await piBridge.setPermissionPolicy(layer, JSON.stringify(next));
      if (!result.success) {
        reportError(result.error || '保存策略失败', '权限');
        return;
      }
      const data = parseData<PermissionPayload>(result);
      if (data) setPayload(data);
    } catch (error) {
      reportError(error, '权限');
    } finally {
      setBusy(false);
    }
  }

  async function authorize(action: string): Promise<void> {
    setBusy(true);
    try {
      const result = await piBridge.authorizePermission(action, minutes, '界面临时授权');
      if (!result.success) {
        reportError(result.error || '临时授权失败', '权限');
        return;
      }
      const data = parseData<PermissionPayload>(result);
      if (data) setPayload(data);
      useUI.getState().showNotice(`已临时允许「${ACTION_LABELS[action] ?? action}」${minutes} 分钟`, 'success');
    } catch (error) {
      reportError(error, '权限');
    } finally {
      setBusy(false);
    }
  }

  async function revoke(action: string): Promise<void> {
    setBusy(true);
    try {
      const result = await piBridge.revokePermission(action);
      if (!result.success) {
        reportError(result.error || '撤销失败', '权限');
        return;
      }
      const data = parseData<PermissionPayload>(result);
      if (data) setPayload(data);
    } catch (error) {
      reportError(error, '权限');
    } finally {
      setBusy(false);
    }
  }

  const snapshots = payload?.evaluated ?? [];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex shrink-0 items-center gap-3 border-b border-line px-5 py-3">
        <Icon name="shield" className="h-4 w-4 text-mute" />
        <h1 className="text-[14px] font-medium text-ink">权限</h1>
        <span className="text-[12px] text-mute">
          当前角色：{payload?.rolesUsing || '（无，用全局/用户策略）'}
        </span>
        <span className="flex-1" />
        <label className="flex items-center gap-1.5 text-[11.5px] text-mute">
          临时授权时长
          <select
            value={minutes}
            onChange={event => setMinutes(Number(event.target.value))}
            className="h-7 rounded-lg bg-panel2 px-2 text-[12px] text-ink outline-none"
          >
            {[15, 30, 60, 120, 240].map(value => (
              <option key={value} value={value}>{value} 分钟</option>
            ))}
          </select>
        </label>
        <Button size="sm" variant="ghost" onClick={() => useUI.getState().setPage('chat')}>
          <Icon name="logout" className="h-3.5 w-3.5" /> 返回对话
        </Button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        <Panel>
          <PanelTitle>最终生效的权限（四层交集）</PanelTitle>
          <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
            计算方式：<span className="font-mono">角色 ∩ 用户 ∩ 全局 ∩ 会话</span>，取最严的一层。
            没被任何一层提到的动作按「禁止」处理（安全默认）。
            <span className="ml-1 text-dim">提示词不是安全边界，所以权限由这里算出来，而不是写进 Prompt。</span>
          </p>
          <div className="flex flex-col gap-1.5">
            {snapshots.map(snapshot => (
              <div key={snapshot.action} className="flex flex-wrap items-center gap-2 rounded-xl bg-panel2 px-3 py-2">
                <span className="w-[112px] shrink-0 text-[12.5px] text-ink">
                  {ACTION_LABELS[snapshot.action] ?? snapshot.action}
                </span>
                <span
                  className={`w-[52px] shrink-0 rounded px-1.5 py-[1px] text-center text-[11px] ${
                    snapshot.decision === 'allow' ? 'bg-panel3 text-info'
                      : snapshot.decision === 'ask' ? 'bg-panel3 text-warn'
                        : 'bg-panel3 text-danger'
                  }`}
                >
                  {DECISION_LABELS[snapshot.decision]}
                </span>
                {/* 强制能力：诚实地标出"能不能真拦住" */}
                <span
                  title={snapshot.note}
                  className={`shrink-0 rounded px-1.5 py-[1px] text-[10.5px] ${
                    snapshot.enforced ? 'bg-panel3 text-dim' : 'text-caption'
                  }`}
                >
                  {snapshot.enforced ? '可强制' : '仅记录'}
                </span>
                <span className="min-w-0 flex-1 truncate text-[11px] text-caption" title={snapshot.note}>
                  {snapshot.note}
                </span>

                {/* 每层的取值：改哪一层、为什么是这个结果，一眼看清 */}
                <span className="shrink-0 font-mono text-[10.5px] text-caption">
                  角色 <span className={snapshot.role === 'abstain' ? 'text-caption/60' : ''}>{DECISION_LABELS[snapshot.role] ?? '未声明'}</span>
                  {' · '}用户 <span className={snapshot.user === 'abstain' ? 'text-caption/60' : ''}>{DECISION_LABELS[snapshot.user] ?? '未声明'}</span>
                  {' · '}全局 <span className={snapshot.global === 'abstain' ? 'text-caption/60' : ''}>{DECISION_LABELS[snapshot.global] ?? '未声明'}</span>
                </span>

                {/* 全局层：三态直接改 */}
                <span className="flex shrink-0 gap-0.5">
                  {(['allow', 'ask', 'deny'] as const).map(decision => (
                    <button
                      key={decision}
                      type="button"
                      disabled={busy}
                      onClick={() => void setLayer('global', snapshot.action, decision)}
                      className={`rounded px-1.5 py-[1px] text-[10.5px] transition-colors ${
                        snapshot.global === decision ? 'bg-panel3 text-ink' : 'text-mute hover:bg-panel3'
                      }`}
                    >
                      {DECISION_LABELS[decision]}
                    </button>
                  ))}
                </span>

                {/* 临时授权 */}
                {snapshot.temporary ? (
                  <span className="flex shrink-0 items-center gap-1.5">
                    <span className="text-[10.5px] text-warn">
                      临时至 {new Date(snapshot.temporary.expiresAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
                    </span>
                    <Button size="sm" variant="ghost" disabled={busy} onClick={() => void revoke(snapshot.action)}>
                      撤销
                    </Button>
                  </span>
                ) : (
                  snapshot.decision !== 'allow' && snapshot.global !== 'deny' && (
                    <Button size="sm" variant="ghost" disabled={busy} onClick={() => void authorize(snapshot.action)}>
                      临时授权
                    </Button>
                  )
                )}
              </div>
            ))}
          </div>
        </Panel>

        <Panel className="mt-3">
          <PanelTitle>怎么被强制</PanelTitle>
          <ul className="flex flex-col gap-1 text-[11.5px] leading-[18px] text-mute">
            <li>· <span className="text-dim">禁止</span>会体现为：启动 ZeroA 时把这些工具从白名单里排除（<span className="font-mono">--exclude-tools</span>），模型直接看不到它们。</li>
            <li>· <span className="text-dim">询问</span>目前是"保留工具 + 记录审计 + 界面提示"：ZeroA 的工具调用在它自己的进程里，Go 拦不到单次调用。</li>
            <li>· 要真正做到逐次拦截（例如只允许访问授权靶机），需要方案里的 <span className="font-mono">WebToolGateway</span>：把网络访问挪到 Go 侧，由 Go 校验 URL 与目标后转发。</li>
          </ul>
        </Panel>
      </div>
    </div>
  );
}
