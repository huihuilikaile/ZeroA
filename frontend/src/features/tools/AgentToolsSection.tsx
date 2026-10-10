import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Icon } from '@/components/ui/Icon';
import { Panel, PanelTitle } from '@/components/ui/primitives';
import { reportError } from '@/lib/reportError';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { useUI } from '@/stores/ui';

/**
 * "模型可调用的工具"——**自动加载**，不写死在代码里。
 *
 * 三个来源：
 *   · 内置：Pi 自带的 read/bash/edit…
 *   · 扩展：已装 npm 包注册的工具（web_search、fetch_content…）
 *   · MCP：mcp.json 里每台服务器（ida / jadx / burp…）的工具
 *
 * 真清单只有运行中的会话知道（Pi 把它写进 system 消息的 sections.tools），
 * 所以这一区显示的是"最近一次会话里实际生效的工具集"：
 * 装个新扩展、加台 MCP 服务器，重启会话就自动出现，不需要改代码。
 */

interface AgentTool {
  id: string;
  name: string;
  note?: string;
  kind?: string;
  provider?: string;
  /** 本次会话是否真的启用（false = 注册了但没开） */
  active?: boolean;
}

interface McpServer {
  name: string;
  transport: string;
  target?: string;
  enabled: boolean;
  description?: string;
  toolCount: number;
  resources: number;
  prompts: number;
  cachedAt?: string;
  failed?: boolean;
  capability: string;
  toolNames?: string[];
}

interface PiPackage {
  name: string;
  version?: string;
  description?: string;
  explicit?: boolean;
  scope?: string;
  source?: string;
  /** 这个包提供什么：extensions / skills / prompts / themes（官方发现规则） */
  resources?: string[];
  tools?: string[];
}

export interface AgentToolsPayload {
  piTools: AgentTool[];
  piToolsSource?: string;
  piToolsNote?: string;
  piPackages?: PiPackage[];
  /** "pi list" = 权威来源；"scan" = 兜底扫描（可能漏 git/本地安装的包） */
  packagesSource?: string;
  packagesNote?: string;
  mcpServers?: McpServer[];
  catalogPaths?: string[];
  catalogIssues?: { path: string; message: string }[];
  catalogUserPath?: string;
}

const KIND_LABELS: Record<string, string> = { builtin: '内置', extension: '扩展', mcp: 'MCP' };

export function AgentToolsSection({
  payload,
  onReloaded,
}: {
  payload: AgentToolsPayload | null;
  onReloaded: () => void;
}) {
  const [expanded, setExpanded] = useState<string>('');
  const [busy, setBusy] = useState<string>('');
  const [mcpTools, setMcpTools] = useState<Record<string, { name: string; description?: string }[]>>({});

  const agentTools = payload?.piTools ?? [];
  const servers = payload?.mcpServers ?? [];
  const packages = payload?.piPackages ?? [];
  const issues = payload?.catalogIssues ?? [];
  const paths = payload?.catalogPaths ?? [];

  const grouped = (['builtin', 'extension', 'mcp'] as const).map(kind => ({
    kind,
    items: agentTools.filter(tool => (tool.kind ?? 'extension') === kind),
  }));

  async function toggleServer(server: McpServer): Promise<void> {
    if (expanded === server.name) {
      setExpanded('');
      return;
    }
    setExpanded(server.name);
    if (mcpTools[server.name]) return;
    try {
      const result = await piBridge.listMcpTools(server.name);
      const data = result.success ? parseData<{ tools: { name: string; description?: string }[] }>(result) : null;
      setMcpTools(prev => ({ ...prev, [server.name]: data?.tools ?? [] }));
    } catch (error) {
      reportError(error, '读取 MCP 工具');
    }
  }

  async function rediscover(server: McpServer): Promise<void> {
    setBusy(server.name);
    try {
      const result = await piBridge.rediscoverMcp(server.name);
      if (!result.success) {
        reportError(result.error || '清除缓存失败');
        return;
      }
      useUI.getState().showNotice(`已让「${server.name}」下次重新发现工具`, 'success');
    } catch (error) {
      reportError(error, '重新发现');
    } finally {
      setBusy('');
    }
  }

  async function exportCatalog(): Promise<void> {
    setBusy('export');
    try {
      const result = await piBridge.exportToolCatalog('');
      if (!result.success) {
        reportError(result.error || '导出失败');
        return;
      }
      const data = parseData<{ path: string }>(result);
      useUI.getState().showNotice('已导出工具清单', 'success');
      if (data?.path) {
        void piBridge.openPath(data.path).catch(() => { /* 打不开就算了，路径已提示 */ });
      }
    } catch (error) {
      reportError(error, '导出清单');
    } finally {
      setBusy('');
    }
  }

  async function reloadCatalog(): Promise<void> {
    setBusy('reload');
    try {
      const result = await piBridge.reloadToolCatalog();
      if (!result.success) {
        reportError(result.error || '重新加载失败');
        return;
      }
      const data = parseData<{ tools: number; issues?: unknown[] }>(result);
      useUI.getState().showNotice(
        `清单已重新加载（${data?.tools ?? 0} 条${data?.issues?.length ? ` · ${data.issues.length} 条问题` : ''}）`,
        data?.issues?.length ? 'warn' : 'success',
      );
      onReloaded();
    } catch (error) {
      reportError(error, '重新加载清单');
    } finally {
      setBusy('');
    }
  }

  useEffect(() => {
    // 会话切换后工具集会变，这里不主动轮询：父组件刷新时 payload 会更新
  }, [payload?.piToolsSource]);

  return (
    <div className="space-y-3">
      <Panel>
        <div className="mb-2 flex items-center gap-2">
          <PanelTitle>模型可调用的工具（自动）</PanelTitle>
          <span className="flex-1" />
          <span className="text-[11px] text-caption">
            {agentTools.length} 个 · {payload?.piToolsSource ? '由扩展在会话启动时上报' : '（还没有会话）'}
          </span>
        </div>
        {payload?.piToolsNote && <p className="mb-2 text-[11.5px] leading-[18px] text-warn">{payload.piToolsNote}</p>}
        {payload?.piToolsSource && (
          <p className="mb-2 text-[11px] leading-[17px] text-caption">
            这份清单来自 ZeroA 扩展在会话启动时上报的「真正声明给模型的工具集」（含内置、扩展、MCP）。
            划掉的表示已注册但本次未启用——ZeroA 默认只开 read/bash/edit/write，其它要么在设置里启用，要么由角色白名单指定。
          </p>
        )}
        {agentTools.length === 0 && !payload?.piToolsNote && (
          <p className="text-[12px] text-mute">这一区会自动填上：发一条消息启动 ZeroA 后即可看到。</p>
        )}
        <div className="space-y-2">
          {grouped.map(group =>
            group.items.length === 0 ? null : (
              <div key={group.kind}>
                <p className="mb-1 text-[11.5px] text-mute">
                  {KIND_LABELS[group.kind]} · {group.items.length}
                </p>
                <div className="flex flex-wrap gap-1.5">
                  {group.items.map(tool => (
                    <span
                      key={tool.id}
                      title={tool.note}
                      className={`inline-flex items-center gap-1.5 rounded-lg px-2 py-1 text-[11.5px] ${
                        tool.active === false ? 'bg-panel2 text-mute line-through' : 'bg-panel2 text-ink'
                      }`}
                    >
                      <span className="font-mono">{tool.id}</span>
                      {tool.active === false && <span className="text-[10px] text-caption">未启用</span>}
                      {tool.provider && <span className="text-[10px] text-caption">{tool.provider}</span>}
                    </span>
                  ))}
                </div>
              </div>
            ),
          )}
        </div>
        {packages.length > 0 && (
          <div className="mt-2 space-y-0.5">
            <p className="text-[11px] text-caption">
              已装扩展包（来源：{payload?.packagesSource === 'pi list' ? 'pi list' : '扫描目录'}）：
            </p>
            {packages.map(pkg => (
              <p key={pkg.name} className="text-[11px] leading-[17px] text-mute">
                <span className="font-mono text-ink">{pkg.name}</span>
                {pkg.version && <span className="text-caption">@{pkg.version}</span>}
                {pkg.resources?.length ? <span className="text-caption"> · 提供 {pkg.resources.join('/')}</span> : null}
                {pkg.tools?.length ? <span className="text-dim"> · 工具 {pkg.tools.join(' ')}</span> : null}
              </p>
            ))}
            {payload?.packagesNote && <p className="text-[11px] text-warn">{payload.packagesNote}</p>}
          </div>
        )}
      </Panel>

      <Panel>
        <PanelTitle>MCP 服务器（来自 mcp.json）</PanelTitle>
        {servers.length === 0 && (
          <p className="text-[12px] text-mute">
            还没有配置 MCP 服务器。到「角色 → MCP 与联网」里加一台（例如 IDA 的 http://127.0.0.1:13391/mcp）。
          </p>
        )}
        <div className="space-y-2">
          {servers.map(server => (
            <div key={server.name} className="rounded-xl bg-panel2 p-2.5">
              <div className="flex items-center gap-2">
                <span className="font-mono text-[12px] text-ink">{server.name}</span>
                <span className="rounded bg-panel3 px-1.5 py-0.5 text-[10px] text-dim">
                  {server.transport === 'stdio' ? 'stdio' : 'http'}
                </span>
                <span className={`text-[11.5px] ${server.failed || server.toolCount === 0 ? 'text-warn' : 'text-success'}`}>
                  {server.enabled
                    ? server.failed
                      ? '上次发现失败'
                      : `${server.toolCount} 个工具${server.resources ? ` · ${server.resources} 资源` : ''}`
                    : '已停用'}
                </span>
                <span className="flex-1" />
                {server.toolCount > 0 && (
                  <button
                    type="button"
                    className="rounded px-1.5 py-0.5 text-[11px] text-dim hover:bg-panel3 hover:text-ink"
                    onClick={() => void toggleServer(server)}
                  >
                    {expanded === server.name ? '收起' : '看工具'}
                  </button>
                )}
                <button
                  type="button"
                  disabled={busy === server.name}
                  className="rounded px-1.5 py-0.5 text-[11px] text-dim hover:bg-panel3 hover:text-ink disabled:opacity-40"
                  onClick={() => void rediscover(server)}
                  title="清掉这台服务器的工具缓存，下次会话重新发现"
                >
                  重新发现
                </button>
              </div>
              <p className="mt-1 truncate font-mono text-[10.5px] text-caption" title={server.target}>
                {server.target}
              </p>
              {server.description && <p className="mt-1 text-[11px] leading-[17px] text-mute">{server.description}</p>}
              {server.cachedAt && <p className="mt-1 text-[10.5px] text-caption">缓存时间 {server.cachedAt}</p>}
              {expanded === server.name && (
                <div className="mt-2 max-h-[220px] overflow-y-auto rounded-lg bg-panel3 p-2">
                  {(mcpTools[server.name] ?? []).map(tool => (
                    <p key={tool.name} className="text-[11px] leading-[17px]">
                      <span className="font-mono text-ink">{tool.name}</span>
                      {tool.description && <span className="text-caption"> — {tool.description}</span>}
                    </p>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      </Panel>

      <Panel>
        <div className="mb-2 flex items-center gap-2">
          <PanelTitle>工具清单来源</PanelTitle>
          <span className="flex-1" />
          <Button size="sm" variant="outline" disabled={busy === 'export'} onClick={() => void exportCatalog()}>
            <Icon name="db" className="h-3.5 w-3.5" /> 导出清单
          </Button>
          <Button size="sm" variant="outline" disabled={busy === 'reload'} onClick={() => void reloadCatalog()}>
            <Icon name="refresh" className="h-3.5 w-3.5" /> 重新加载
          </Button>
        </div>
        <p className="mb-2 text-[11.5px] leading-[18px] text-mute">
          本机工具清单来自 JSON：<span className="font-mono">{payload?.catalogUserPath ?? '<数据目录>/tool-catalog.json'}</span>
          （可选；也可以放到 <span className="font-mono">&lt;工作区&gt;/.pi/tool-catalog.json</span> 让团队共享）。
          改完保存点「重新加载」即可生效，<b>不用重启</b>。同 id 覆盖字段、新 id 追加、
          <span className="font-mono">"disabled": true</span> 删除、<span className="font-mono">"mode": "replace"</span> 整份替换。
        </p>
        {paths.length > 0 && (
          <p className="mb-1 text-[11px] text-caption">
            已加载：{paths.map(path => path.split(/[\\/]/).pop()).join(' + ')}
          </p>
        )}
        {issues.length > 0 ? (
          <div className="rounded-lg bg-panel2 p-2">
            {issues.map((issue, index) => (
              <p key={`${issue.path}-${index}`} className="text-[11.5px] leading-[18px] text-warn">
                <span className="font-mono">{issue.path}</span>：{issue.message}
              </p>
            ))}
          </div>
        ) : (
          <p className="text-[11px] text-caption">没有发现问题。</p>
        )}
      </Panel>
    </div>
  );
}
