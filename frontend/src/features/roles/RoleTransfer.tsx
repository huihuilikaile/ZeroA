import { useState } from 'react';
import { piBridge } from '@/bridge/pi';
import { Button } from '@/components/ui/button';
import { Panel, PanelTitle } from '@/components/ui/primitives';
import { Icon } from '@/components/ui/Icon';
import { parseData } from '@/rpc/types';
import { reportError } from '@/lib/reportError';
import { useRoles } from '@/stores/roles';
import { useUI } from '@/stores/ui';

/**
 * 角色导入 / 导出。
 *
 * 为什么要做：角色是"一套工作方式"（提示词 + 工具白名单 + 权限 + 模型 + 委派），
 * 用户做出来之后必然想带走或分享。以前只能手拷 `.pi/roles` 目录——而一个角色是
 * **两个文件**（`<id>.json` 定义 + `prompts/<id>.md` 提示词），漏一个就成了空壳角色，
 * 而且不会报错。
 *
 * 导出是一个**自包含 JSON**（定义 + 提示词都在里面），单文件好传、好 diff；
 * 导入同时接受**文件与目录**（目录按 roles-pack 约定读 *.json + prompts/*.md），
 * 因为社区里的角色包大多是目录形式。
 */

interface ExportResult {
  path?: string;
  count?: number;
  ids?: string[];
}

interface ImportResult {
  created?: string[];
  overwritten?: string[];
  failed?: { id?: string; error?: string }[];
}

export function RoleTransfer() {
  const roles = useRoles(s => s.roles);
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [exportPath, setExportPath] = useState('');
  const [importPath, setImportPath] = useState('');
  const [busy, setBusy] = useState<'export' | 'import' | null>(null);
  const [exported, setExported] = useState<ExportResult | null>(null);
  const [imported, setImported] = useState<ImportResult | null>(null);

  /** 没勾过任何角色时默认导出全部：这是最常见的使用方式 */
  const checkedIDs = roles.filter(role => picked[role.id] ?? true).map(role => role.id);

  async function runExport() {
    setBusy('export');
    setExported(null);
    try {
      const result = await piBridge.exportRoles(JSON.stringify({ roleIds: checkedIDs, path: exportPath.trim() }));
      if (!result.success) {
        reportError(result.error || '导出失败');
        return;
      }
      const data = parseData<ExportResult>(result);
      setExported(data ?? {});
      useUI.getState().showNotice(`已导出 ${data?.count ?? 0} 个角色`, 'success');
    } catch (error) {
      reportError(error, '导出角色');
    } finally {
      setBusy(null);
    }
  }

  async function runImport() {
    setBusy('import');
    setImported(null);
    try {
      const result = await piBridge.importRoles(importPath.trim());
      if (!result.success) {
        reportError(result.error || '导入失败');
        return;
      }
      const data = parseData<ImportResult>(result);
      setImported(data ?? {});
      // 角色列表与"当前角色"都要重新读：导入可能新增、也可能覆盖当前正在用的那个
      await useRoles.getState().load();
      const created = data?.created?.length ?? 0;
      const overwritten = data?.overwritten?.length ?? 0;
      const failed = data?.failed?.length ?? 0;
      useUI.getState().showNotice(
        `导入完成：新增 ${created} · 覆盖 ${overwritten}${failed ? ` · 失败 ${failed}` : ''}`,
        failed ? 'warn' : 'success',
      );
    } catch (error) {
      reportError(error, '导入角色');
    } finally {
      setBusy(null);
    }
  }

  return (
    <Panel>
      <PanelTitle>角色导入 / 导出</PanelTitle>
      <p className="mb-3 text-[12px] leading-[18px] text-mute">
        导出成**一个自包含 JSON**（含提示词），方便备份、换机器或分享；
        导入支持这种 JSON，也支持目录形式的角色包（<span className="font-mono">*.json</span> +{' '}
        <span className="font-mono">prompts/&lt;id&gt;.md</span>）。
        同名角色会被**覆盖**，导入后请检查列表。
      </p>

      {/* ── 导出 ─────────────────────────────────────────────── */}
      <div className="mb-4 rounded-xl bg-panel2 p-3">
        <div className="mb-2 flex items-center gap-2">
          <Icon name="logout" className="h-3.5 w-3.5 text-mute" />
          <span className="text-[12.5px] text-ink">导出</span>
          <span className="text-[11.5px] text-mute">已选 {checkedIDs.length} / {roles.length}</span>
        </div>
        <div className="mb-2 flex flex-wrap gap-1.5">
          {roles.map(role => {
            const on = picked[role.id] ?? true;
            return (
              <button
                key={role.id}
                type="button"
                onClick={() => setPicked(prev => ({ ...prev, [role.id]: !on }))}
                className={`rounded-lg px-2 py-1 text-[11.5px] transition-colors ${
                  on ? 'bg-panel3 text-ink' : 'text-mute hover:bg-panel3'
                }`}
              >
                {on ? '✓ ' : ''}{role.name}
              </button>
            );
          })}
        </div>
        <div className="flex items-center gap-2">
          <input
            value={exportPath}
            onChange={event => setExportPath(event.target.value)}
            placeholder="留空 = 存到 .pi/exports/roles-<时间>.json"
            aria-label="导出路径"
            className="h-8 min-w-0 flex-1 rounded-lg bg-panel3 px-2.5 font-mono text-[11.5px] text-ink outline-none placeholder:font-sans"
          />
          <Button size="sm" onClick={() => void runExport()} disabled={busy !== null || checkedIDs.length === 0}>
            {busy === 'export' ? '导出中…' : '导出'}
          </Button>
        </div>
        {exported?.path && (
          <div className="mt-2 flex items-center gap-2 text-[11.5px] text-success">
            <span className="min-w-0 flex-1 truncate font-mono" title={exported.path}>{exported.path}</span>
            <button
              type="button"
              className="shrink-0 rounded px-1.5 py-0.5 text-[11.5px] text-dim hover:bg-panel3 hover:text-ink"
              onClick={() => void piBridge.openPath(exported.path!).catch(error => reportError(error, '打开目录'))}
            >
              打开所在文件夹
            </button>
          </div>
        )}
      </div>

      {/* ── 导入 ─────────────────────────────────────────────── */}
      <div className="rounded-xl bg-panel2 p-3">
        <div className="mb-2 flex items-center gap-2">
          <Icon name="plus" className="h-3.5 w-3.5 text-mute" />
          <span className="text-[12.5px] text-ink">导入</span>
          <span className="text-[11.5px] text-mute">文件或目录都可以</span>
        </div>
        <div className="flex items-center gap-2">
          <input
            value={importPath}
            onChange={event => setImportPath(event.target.value)}
            placeholder="例如 D:\\下载\\roles-pack 或 D:\\下载\\roles-20261006.json"
            aria-label="导入路径"
            className="h-8 min-w-0 flex-1 rounded-lg bg-panel3 px-2.5 font-mono text-[11.5px] text-ink outline-none placeholder:font-sans"
          />
          <Button size="sm" onClick={() => void runImport()} disabled={busy !== null || !importPath.trim()}>
            {busy === 'import' ? '导入中…' : '导入'}
          </Button>
        </div>
        {imported && (
          <div className="mt-2 space-y-1 text-[11.5px]">
            {imported.created?.length ? (
              <p className="text-success">新增：{imported.created.join('、')}</p>
            ) : null}
            {imported.overwritten?.length ? (
              <p className="text-warn">覆盖：{imported.overwritten.join('、')}</p>
            ) : null}
            {imported.failed?.map(item => (
              <p key={item.id} className="text-danger">{item.id}：{item.error}</p>
            ))}
            {!imported.created?.length && !imported.overwritten?.length && !imported.failed?.length && (
              <p className="text-mute">没有导入任何角色。</p>
            )}
            <p className="text-caption">改完角色要**新建会话**才生效（提示词与工具集在启动时确定）。</p>
          </div>
        )}
      </div>
    </Panel>
  );
}
