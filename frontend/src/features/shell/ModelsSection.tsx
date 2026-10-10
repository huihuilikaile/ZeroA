import { useCallback, useEffect, useMemo, useState } from 'react';
import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';
import { piBridge, errorText } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { reportError } from '@/lib/reportError';
import { cn } from '@/lib/utils';
import { useConfirm } from '@/stores/confirm';
import { useUI } from '@/stores/ui';
import { useAgent } from '@/hooks/useAgent';
import { useModels } from '@/hooks/useModels';

interface ProviderRow {
  provider: string;
  displayName?: string;
  envVar?: string;
  needsKey?: boolean;
  oauthReady?: boolean;
  configured?: boolean;
  hasKey?: boolean;
  builtin?: boolean;
  api?: string;
  baseUrl?: string;
  models?: number;
}
interface ProvidersPayload {
  providers?: ProviderRow[];
  modelsPath?: string;
  /** 内置供应商目录提取到几条（0 = 没读到） */
  builtinCount?: number;
  agentDir?: string;
  agentDirKind?: string;
}

/**
 * 设置里的「模型」分节 —— 布局照 DeepSeek Harness 的设置页：
 *   [提供商行：名字 + 自定义标记 + 凭据状态点 …… 编辑 / 删除]
 *   [ ＋ 添加模型提供商 ]（虚线框）
 *   顶部右上角一个「打开配置文件」
 *
 * 关键取舍：**只列 Pi 已知的提供商**（内置目录 models-store.json + 已配置的
 * models.json + 凭据 auth.json 三处合并），自定义模型的高级写法不进界面——
 * 那些直接点「打开配置文件」改 JSON 更靠谱，也避免我们自造一套字段。
 */
export function ModelsSection({ onOpenConfig }: { onOpenConfig?: (path: string) => void }) {
  const [data, setData] = useState<ProvidersPayload | null>(null);
  const [loading, setLoading] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [editing, setEditing] = useState<ProviderRow | null>(null);
  const [form, setForm] = useState({ provider: '', apiKey: '' });
  const [busy, setBusy] = useState(false);
  const { start } = useAgent();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const result = await piBridge.listProviders();
      if (!result.success) {
        reportError(result.error || '读取模型配置失败', '模型配置');
        return;
      }
      setData(parseData<ProvidersPayload>(result));
    } catch (error) {
      reportError(error, '模型配置');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // Pi 运行时的完整模型目录（get_available_models）：内置提供商有一大批只在这里出现，
  // 光看配置文件会缺一大半——这就是"供应商不全"的根因。
  const { models: runtimeModels } = useModels();

  const providers = useMemo(() => {
    const rows: ProviderRow[] = [...(data?.providers ?? [])];
    const known = new Set(rows.map(row => row.provider));
    const counts = new Map<string, number>();
    for (const model of runtimeModels) {
      counts.set(model.provider, (counts.get(model.provider) ?? 0) + 1);
    }
    for (const [provider, count] of counts) {
      if (known.has(provider)) continue;
      rows.push({ provider, builtin: true, models: count });
    }
    // 已配置的排前面；然后内置的排在自定义后面，其余按名字，保证顺序稳定
    return rows.sort((a, b) => {
      if (Boolean(a.configured) !== Boolean(b.configured)) return a.configured ? -1 : 1;
      if (Boolean(a.builtin) !== Boolean(b.builtin)) return a.builtin ? 1 : -1;
      return a.provider.localeCompare(b.provider);
    });
  }, [data, runtimeModels]);

  /** 列表只显示真的配过（models.json 里定义过）或已有密钥的 */
  const configuredProviders = providers.filter(row => row.configured || row.hasKey);
  /** 添加卡片里当前选中的供应商：用来提示它的环境变量 / OAuth 登录方式 */
  const selectedRow = providers.find(row => row.provider === form.provider);

  function openEditor(row: ProviderRow) {
    setEditing(row);
    setAddOpen(false);
    setForm({ provider: row.provider, apiKey: '' });
  }

  async function save() {
    if (!form.provider.trim()) {
      useUI.getState().showNotice('provider 不能为空');
      return;
    }
    setBusy(true);
    try {
      // Pi 的「登录」就是把密钥写进 auth.json：选提供商 + 填 key 即可。
      // 自定义模型/协议/地址那些高级写法不进界面（点「打开配置文件」改 JSON）。
      const saved = await piBridge.setProviderKey(form.provider.trim(), form.apiKey.trim());
      if (!saved.success) {
        reportError(saved.error || '保存失败', '模型配置');
        return;
      }
      useUI.getState().showNotice('密钥已保存，重启会话后生效');
      setEditing(null);
      setAddOpen(false);
      setForm({ provider: '', apiKey: '' });
      await load();
    } catch (error) {
      reportError(error, '模型配置');
    } finally {
      setBusy(false);
    }
  }

  function removeProvider(row: ProviderRow) {
    useConfirm.getState().ask({
      title: `删除提供商 ${row.provider}？`,
      description: `会从 models.json 里移除 ${row.provider} 的配置（写入前会备份成 models.json.bak）。重启会话后生效。`,
      confirmLabel: '删除',
      danger: true,
      onConfirm: async () => {
        for (const model of await modelsOf(row.provider)) {
          const result = await piBridge.deleteModel(row.provider, model);
          if (!result.success) {
            reportError(result.error || '删除失败', '模型配置');
            return;
          }
        }
        await load();
        useUI.getState().showNotice(`已删除 ${row.provider}，重启会话后生效`);
      },
    });
  }

  /** 读该 provider 下的模型 id（删除提供商时要逐个删掉） */
  async function modelsOf(provider: string): Promise<string[]> {
    const result = await piBridge.listConfiguredProviders();
    const payload = parseData<{ providers?: { provider: string; models?: { id: string }[] }[] }>(result);
    const found = payload?.providers?.find(item => item.provider === provider);
    return (found?.models ?? []).map(model => model.id);
  }

  return (
    <div className="flex flex-col">
      <div className="mb-3 flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <h3 className="text-[13px] font-medium text-ink">模型</h3>
          <p className="mt-0.5 text-[11.5px] leading-[18px] text-mute">
            下面是 ZeroA 已知的提供商，填好 API 密钥即可使用其模型。自定义模型请直接改配置文件。
          </p>
        </div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            const path = data?.modelsPath;
            if (!path) return;
            if (onOpenConfig) onOpenConfig(path);
            else void piBridge.openPath(path).catch(error => reportError(error, '打开配置文件'));
          }}
        >
          打开配置文件
        </Button>
      </div>

      {loading && providers.length === 0 && <p className="py-1 text-[12px] text-mute">读取配置…</p>}

      {/* 内置目录没读到：说清楚原因与出路，别让用户对着空列表发呆。
          正常情况不会出现（提取失败会静默降级，所以这里必须显式提示）。 */}
      {!loading && data?.builtinCount === 0 && (
        <p className="mb-1 rounded-card border border-line px-3 py-2 text-[11.5px] leading-[18px] text-warn">
          没能读取自带 ZeroA 的内置供应商目录（版本或打包布局变了），下面只显示已配置的。
          可以点右上角「打开配置文件」手动添加，或用 scripts/fetch-pi-agent.ps1 重新拉一份 ZeroA 引擎。
        </p>
      )}

      {/* 上面这份列表**永远只列已配置的**：点「添加模型提供商」不该让几十个
          没配置的供应商涌进列表（那正是用户报的 bug）。
          全部内置供应商只在下面「添加」卡片的下拉里出现——那里才是你挑新供应商的地方。 */}
      {configuredProviders.length === 0 && (
        <p className="rounded-card border border-dashed border-line px-3 py-4 text-center text-[12px] text-mute">
          还没有配置任何提供商，点下面的「添加模型提供商」开始
        </p>
      )}

      <div className="flex flex-col gap-2">
        {configuredProviders.map(row => (
          <div key={row.provider} className="rounded-card border border-line px-3 py-2.5">
            <div className="flex items-center gap-2">
              <span className="min-w-0 truncate text-[13px] text-ink">
                {row.displayName || row.provider}
                {row.displayName && row.displayName !== row.provider ? (
                  <span className="ml-1.5 font-mono text-[11px] text-mute">{row.provider}</span>
                ) : null}
              </span>
              {!row.configured && row.builtin && (
                <span className="rounded border border-line px-1.5 py-[1px] text-[10.5px] text-mute">内置</span>
              )}
              {row.configured && !row.builtin && (
                <span className="rounded border border-line px-1.5 py-[1px] text-[10.5px] text-mute">自定义</span>
              )}
              <span
                title={row.hasKey ? '已配置 API 密钥' : '未配置 API 密钥'}
                className={cn('h-2 w-2 rounded-full', row.hasKey ? 'bg-success' : 'border border-line')}
              />
              <span className="flex-1" />
              <Button size="sm" variant="outline" onClick={() => openEditor(row)}>编辑</Button>
              {row.configured && (
                <button
                  type="button"
                  onClick={() => removeProvider(row)}
                  className="rounded-lg px-2 py-1 text-[12px] text-danger hover:bg-danger/10"
                >
                  删除
                </button>
              )}
            </div>
          </div>
        ))}
      </div>

      {addOpen || editing ? (
        <div className="mt-2 rounded-card border border-line bg-panel2 p-3">
          <div className="mb-2 text-[12.5px] text-ink">
            {editing ? `编辑 ${editing.provider}` : '添加模型提供商'}
          </div>
          <div className="flex flex-col gap-2">
            <Field label="模型提供商">
              <select
                value={form.provider}
                onChange={e => setForm(f => ({ ...f, provider: e.target.value }))}
                className="h-8 w-full rounded-lg bg-panel3 px-2.5 text-[12.5px] outline-none"
              >
                <option value="">选择一个提供商…</option>
                {providers.map(row => (
                  <option key={row.provider} value={row.provider}>
                    {row.provider}{row.hasKey ? '（已配置密钥）' : ''}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="API 密钥">
              <Input value={form.apiKey} placeholder="sk-…" onChange={value => setForm(f => ({ ...f, apiKey: value }))} />
            </Field>
          </div>

          {/* 选中某个供应商后才提示它的特殊登录方式：
              · 环境变量：不填 key 也能用（Pi 的 resolve 会回落到环境变量）
              · OAuth：我们不做浏览器授权（Pi 没有对应 RPC，TUI 的 /login 才有），
                所以给一条能照做的指令，而不是含糊的"用命令行完成" */}
          {selectedRow && (selectedRow.envVar || selectedRow.oauthReady) && (
            <p className="mt-2 text-[11.5px] leading-[18px] text-mute">
              {selectedRow.envVar ? `也可以不填密钥，改用环境变量 ${selectedRow.envVar}。` : ''}
              {selectedRow.oauthReady
                ? `${selectedRow.envVar ? ' ' : ''}这个提供商支持 OAuth 登录：在终端运行 pi，用 /login 完成授权（会自动打开浏览器），回到这里重启会话即可。`
                : ''}
            </p>
          )}
          <div className="mt-2.5 flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => { setEditing(null); setAddOpen(false); }}>取消</Button>
            <Button size="sm" variant="default" disabled={busy} onClick={() => void save()}>{busy ? '保存中…' : '保存到配置'}</Button>
          </div>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => { setAddOpen(true); setEditing(null); setForm({ provider: '', apiKey: '' }); }}
          className="mt-2 flex w-full items-center justify-center gap-1.5 rounded-card border border-dashed border-line py-2.5 text-[12.5px] text-dim hover:border-line hover:bg-panel2 hover:text-ink"
        >
          <Icon name="plus" className="h-4 w-4" />
          添加模型提供商
        </button>
      )}

      <div className="mt-3 flex items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => void start()}>重启会话让改动生效</Button>
        {data?.modelsPath && (
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-mute" title={data.modelsPath}>
            {data.modelsPath}
          </span>
        )}
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[11.5px] text-mute">{label}</span>
      {children}
    </label>
  );
}

function Input({ value, placeholder, onChange, readOnly }: {
  value: string;
  placeholder?: string;
  readOnly?: boolean;
  onChange(value: string): void;
}) {
  return (
    <input
      value={value}
      placeholder={placeholder}
      readOnly={readOnly}
      onChange={e => onChange(e.target.value)}
      className={cn(
        'h-8 w-full rounded-lg px-2.5 font-mono text-[12px] outline-none placeholder:text-mute',
        readOnly ? 'bg-panel text-mute' : 'bg-panel3',
      )}
    />
  );
}
