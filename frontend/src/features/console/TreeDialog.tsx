import { useEffect, useState } from 'react';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { reportError } from '@/lib/reportError';
import { cn } from '@/lib/utils';
import { useUI } from '@/stores/ui';
import { useRuntime } from '@/stores/runtime';
import { useAgent } from '@/hooks/useAgent';

interface TreeNode {
  entry: {
    id: string;
    type: string;
    parentId?: string | null;
    role?: string;
    text?: string;
    /** 真实 Pi 的 get_tree 给的是原始记录：role/content 嵌在 message 里 */
    message?: { role?: string; content?: unknown };
  };
  children?: TreeNode[];
  label?: string;
}

/** content 是块数组时取最后一个 text 块（与 mockpi/离线建树的口径一致）；兼容裸字符串旧形态 */
function messageText(content: unknown): string {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  let text = '';
  for (const block of content) {
    if (block && typeof block === 'object' && (block as { type?: string }).type === 'text') {
      const value = (block as { text?: unknown }).text;
      if (typeof value === 'string') text = value;
    }
  }
  return text;
}

/**
 * 两种数据源给两种形态：离线建树与 mockpi 把 role/text 展平在 entry 上，
 * 真实 Pi 进程给原始记录（嵌在 entry.message 里）。解析后统一补成展平形态，
 * 渲染层只认一种，不为数据源分叉。
 */
function normalizeTree(nodes: TreeNode[]) {
  for (const node of nodes) {
    const message = node.entry.message;
    if (message && node.entry.role === undefined && node.entry.text === undefined) {
      node.entry.role = message.role ?? '';
      node.entry.text = messageText(message.content);
    }
    if (node.children) normalizeTree(node.children);
  }
}

/**
 * 分支树对话框。
 *
 * 为什么需要它：会话是一棵**只追加的树**，`get_messages` 只给当前分支 ——
 * 分支之后就看不见"另一条分支还在那儿"。`get_tree` 给的是整棵树 + leafId（当前节点），
 * 这里把它画成缩进的树，并允许**在任意历史节点上再分一次**（对应 Pi 的 fork）。
 *
 * 数据是拉取式的（打开时取一次），不做实时订阅：树的形状只在 prompt/fork/clone 后变，
 * 而这些都是用户主动动作，重新打开即可看到最新状态。
 */
export function TreeDialog() {
  const open = useUI(s => s.treeOpen);
  const [tree, setTree] = useState<TreeNode[]>([]);
  const [leafId, setLeafId] = useState('');
  const [loading, setLoading] = useState(false);
  const { forkFrom } = useAgent();
  // 没有进程时树是从磁盘 .jsonl 离线建出来的（只读查看）：
  // fork 要改写会话状态、必须走进程，所以这种时候不给「从这里分支」按钮，
  // 免得点下去弹一张红卡。
  const hasRuntime = useRuntime(s => s.runtimeId !== '');

  useEffect(() => {
    if (!open) return;
    setLoading(true);
    void piBridge.getTree()
      .then(result => {
        if (!result.success) {
          reportError(result.error || '读取分支树失败', '分支树');
          return;
        }
        const payload = parseData<{ tree?: TreeNode[]; leafId?: string }>(result);
        const nextTree = payload?.tree ?? [];
        normalizeTree(nextTree);
        setTree(nextTree);
        setLeafId(payload?.leafId ?? '');
      })
      .catch(error => reportError(error, '分支树'))
      .finally(() => setLoading(false));
  }, [open]);

  /** 节点摘要：用户消息显示正文，助手消息显示类型 */
  function summary(node: TreeNode): string {
    const text = (node.entry.text ?? '').replace(/\s+/g, ' ').trim();
    if (text) return text.length > 48 ? `${text.slice(0, 48)}…` : text;
    return node.entry.role === 'assistant' ? '助手回复' : (node.entry.type ?? '条目');
  }

  function renderNodes(nodes: TreeNode[], depth: number) {
    return nodes.map(node => (
      <div key={node.entry.id}>
        <div
          className={cn(
            'group flex items-center gap-1.5 rounded-lg py-1 pr-2 hover:bg-hover',
            node.entry.id === leafId && 'bg-panel3',
          )}
          style={{ paddingLeft: 8 + depth * 16 }}
        >
          <Icon
            name={node.entry.role === 'user' ? 'chat' : 'robot'}
            className={cn('h-3.5 w-3.5 shrink-0', node.entry.id === leafId ? 'text-info' : 'text-mute')}
          />
          <span className={cn('min-w-0 flex-1 truncate text-[12.5px]', node.entry.id === leafId ? 'text-ink' : 'text-dim')} title={node.entry.text ?? node.entry.id}>
            {summary(node)}
          </span>
          {node.entry.id === leafId && <span className="shrink-0 text-[10.5px] text-info">当前</span>}
          {/* 在任意用户消息上再分一次（对应 Pi 的 fork）；无进程时离线只读，不给按钮 */}
          {hasRuntime && node.entry.role === 'user' && node.entry.id !== leafId && (
            <button
              type="button"
              onClick={() => {
                void forkFrom(node.entry.id);
                useUI.getState().setTreeOpen(false);
              }}
              className="shrink-0 text-[11px] text-mute opacity-0 transition-opacity group-hover:opacity-100 hover:text-ink"
            >
              从这里分支
            </button>
          )}
        </div>
        {node.children && node.children.length > 0 && renderNodes(node.children, depth + 1)}
      </div>
    ));
  }

  return (
    <Dialog open={open} onOpenChange={value => useUI.getState().setTreeOpen(value)}>
      <DialogContent className="w-[min(720px,94vw)]">
        <DialogHeader>
          <DialogTitle>分支树</DialogTitle>
          <span className="flex-1" />
          <span className="text-[12px] text-mute">{tree.length > 1 ? `${tree.length} 个根` : '单根'}</span>
        </DialogHeader>

        <div data-selectable className="max-h-[60vh] min-h-[160px] overflow-y-auto px-2 py-2">
          {loading && <p className="px-2 py-1 text-[12px] text-mute">读取中…</p>}
          {!loading && tree.length === 0 && <p className="px-2 py-1 text-[12px] text-mute">这个会话还没有条目</p>}
          {renderNodes(tree, 0)}
        </div>

        <div className="flex items-center gap-2 px-4 pb-3 pt-1">
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-mute">{leafId ? `leaf=${leafId}` : ''}</span>
          <Button size="sm" variant="ghost" onClick={() => useUI.getState().setTreeOpen(false)}>关闭</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
