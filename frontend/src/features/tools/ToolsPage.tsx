import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Icon } from '@/components/ui/Icon';
import { Panel, PanelTitle } from '@/components/ui/primitives';
import { reportError } from '@/lib/reportError';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { useUI } from '@/stores/ui';
import { AgentToolsSection, type AgentToolsPayload } from '@/features/tools/AgentToolsSection';

/** 一条工具（定义 + 本机检测结果） */
interface ToolStatus {
  id: string;
  name: string;
  type: string;
  direction?: string;
  note?: string;
  /** 谁提供它：扩展工具写包名（如 pi-web-access） */
  provider?: string;
  /** true = 模型直接调用的工具（扩展注册）；false = 环境里用 bash 调用的程序 */
  agentTool?: boolean;
  capabilities: string[] | null;
  risk: { shell: boolean; filesystem: string; network: boolean };
  available: boolean;
  path?: string;
  version?: string;
  detail?: string;
}

interface ToolConflict {
  tool: string;
  first: string;
  second: string;
  effect: string;
}

interface ToolListPayload extends AgentToolsPayload {
  tools: ToolStatus[];
  capabilities: string[];
  /** 装了多个扩展抢同一个工具名：Pi 会拒绝加载后一个扩展 */
  conflicts?: ToolConflict[];
  available: number;
  missing: number;
}

/** 方向中文名的兜底：真正的名字来自 tool-catalog.json 的 directions[].label */
const DIRECTION_LABELS: Record<string, string> = {
  base: '通用', web: 'Web', pwn: 'Pwn', reverse: '逆向',
  crypto: '密码学', forensics: '取证', ir: '应急响应',
};

const TYPE_LABELS: Record<string, string> = {
  builtin: 'ZeroA 内置', command: '命令', 'python-package': 'Python 包',
  'node-package': 'Node 包', application: '程序', extension: '扩展工具',
  service: '服务', mcp: 'MCP',
};

/**
 * 工具页（方案第二十四节）：显示这台机器上实际有什么。
 *
 * 刻意只做"检测 + 展示"：第一版不自动下载安装（方案明确排除），
 * 因为自动安装会让"为什么这台机器突然多了个可执行文件"变成一个查不清的问题。
 */
export function ToolsPage() {
  const [payload, setPayload] = useState<ToolListPayload | null>(null);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState<'all' | 'available' | 'missing'>('all');

  async function load(force: boolean): Promise<void> {
    setLoading(true);
    try {
      const result = force ? await piBridge.detectTools(true) : await piBridge.listTools();
      if (!result.success) {
        reportError(result.error || '读取工具清单失败', '工具');
        return;
      }
      const data = parseData<ToolListPayload>(result);
      if (data) setPayload(data);
    } catch (error) {
      reportError(error, '工具');
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const tools = payload?.tools ?? [];
  const shown = tools.filter(tool =>
    filter === 'all' ? true : filter === 'available' ? tool.available : !tool.available);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex shrink-0 items-center gap-3 border-b border-line px-5 py-3">
        <Icon name="tool" className="h-4 w-4 text-mute" />
        <h1 className="text-[14px] font-medium text-ink">工具</h1>
        <span className="text-[12px] text-mute">
          可用 {payload?.available ?? '-'} · 不可用 {payload?.missing ?? '-'} · 共 {tools.length}
        </span>
        <span className="flex-1" />
        {(['all', 'available', 'missing'] as const).map(key => (
          <button
            key={key}
            type="button"
            onClick={() => setFilter(key)}
            className={`rounded-lg px-2.5 py-1 text-[12px] transition-colors ${
              filter === key ? 'bg-panel3 text-ink' : 'text-dim hover:bg-panel2 hover:text-ink'
            }`}
          >
            {key === 'all' ? '全部' : key === 'available' ? '可用' : '缺失'}
          </button>
        ))}
        <Button size="sm" variant="outline" disabled={loading} onClick={() => void load(true)}>
          <Icon name="search" className="h-3.5 w-3.5" /> {loading ? '检测中…' : '重新检测'}
        </Button>
        <Button size="sm" variant="ghost" onClick={() => useUI.getState().setPage('chat')}>
          <Icon name="logout" className="h-3.5 w-3.5" /> 返回对话
        </Button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        {(payload?.conflicts ?? []).length > 0 && (
          <div className="mb-3 rounded-xl border border-[color-mix(in_srgb,var(--danger)_35%,transparent)] bg-[color-mix(in_srgb,var(--danger)_8%,transparent)] px-3 py-2.5">
            <div className="flex items-center gap-2">
              <Icon name="warn" className="h-3.5 w-3.5 shrink-0 text-danger" />
              <span className="text-[12.5px] font-medium text-danger">扩展工具名冲突</span>
            </div>
            {(payload?.conflicts ?? []).map(conflict => (
              <div key={conflict.tool} className="mt-1 text-[11.5px] leading-[18px] text-dim">
                <span className="font-mono text-ink">{conflict.tool}</span> 同时由{' '}
                <span className="font-mono">{conflict.first}</span> 与{' '}
                <span className="font-mono">{conflict.second}</span> 提供。{conflict.effect}
              </div>
            ))}
          </div>
        )}

        {/* 自动发现的两块：模型可调用的工具（会话驱动）+ MCP 服务器（mcp.json 驱动） */}
        <AgentToolsSection payload={payload} onReloaded={() => void load(false)} />

        <Panel>
          <PanelTitle>本机环境工具</PanelTitle>
          <p className="text-[11.5px] leading-[18px] text-mute">
            这里的工具是**本机环境工具**，不是 Agent 可调用的工具接口——
            Volatility、PyCryptodome 这类东西不会注册成 LLM 工具，模型通过 <span className="font-mono">bash</span> 调用它们。
            标了「模型可直接调用」的是扩展注册的工具（如 web_search）——角色声明 <span className="font-mono">web.search</span> 就会把它们并进工具白名单；其余是环境里的程序。角色声明的是「能力」（如 <span className="font-mono">forensics.memory</span>），由能力解析映射成下面这些工具；
            本机没有的会明确标出来，角色仍然能启动。
          </p>
        </Panel>

        {/* 按方向分组 */}
        {Object.entries(
          shown.reduce<Record<string, ToolStatus[]>>((groups, tool) => {
            const key = tool.direction || 'other';
            groups[key] = [...(groups[key] ?? []), tool];
            return groups;
          }, {}),
        ).map(([direction, items]) => (
          <div key={direction} className="mt-4">
            <div className="mb-1.5 flex items-center gap-2">
              <span className="text-[12.5px] text-dim">{DIRECTION_LABELS[direction] ?? direction}</span>
              <span className="text-[11px] text-caption">
                {items.filter(item => item.available).length}/{items.length} 可用
              </span>
            </div>
            <div className="grid grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-1.5">
              {items.map(tool => (
                <div
                  key={tool.id}
                  className={`flex items-start gap-2 rounded-xl border px-3 py-2 ${
                    tool.available ? 'border-line bg-panel2' : 'border-line bg-panel2 opacity-70'
                  }`}
                >
                  <Icon
                    name={tool.available ? 'check' : 'x'}
                    className={`mt-0.5 h-3.5 w-3.5 shrink-0 ${tool.available ? 'text-info' : 'text-caption'}`}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-[12.5px] text-ink">{tool.name}</span>
                      <span className="shrink-0 text-[10.5px] text-caption">{TYPE_LABELS[tool.type] ?? tool.type}</span>
                      {tool.agentTool && (
                        <span className="shrink-0 rounded bg-panel3 px-1.5 py-[1px] text-[10px] text-info">模型可直接调用</span>
                      )}
                      {tool.provider && (
                        <span className="shrink-0 text-[10px] text-caption">来自 {tool.provider}</span>
                      )}
                      {tool.version && <span className="truncate text-[10.5px] text-caption">{tool.version}</span>}
                    </div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-1">
                      {/* 后端 nil 切片会变成 null（没声明能力的工具），一律按空数组读 */}
                      {(tool.capabilities ?? []).slice(0, 3).map(capability => (
                        <span key={capability} className="rounded bg-panel3 px-1.5 py-[1px] font-mono text-[10px] text-mute">
                          {capability}
                        </span>
                      ))}
                      {(tool.capabilities ?? []).length > 3 && (
                        <span className="text-[10px] text-caption">+{(tool.capabilities ?? []).length - 3}</span>
                      )}
                    </div>
                    {(tool.path || tool.detail) && (
                      <div className="mt-0.5 truncate font-mono text-[10.5px] text-caption" title={tool.path || tool.detail}>
                        {tool.path || tool.detail}
                      </div>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
