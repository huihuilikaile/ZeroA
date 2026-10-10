import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '@/components/ui/Icon';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { reportError } from '@/lib/reportError';
import { fmtBytes } from '@/lib/format';
import { cn } from '@/lib/utils';
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { useRuntime } from '@/stores/runtime';
import { useUI } from '@/stores/ui';
import { useChangedFiles, normPath } from '@/stores/changedFiles';
import { FileTypeIcon } from '@/features/files/FileTypeIcon';
import { CodeView } from '@/features/files/CodeView';
import { languageForPath, pickPreviewKind, type PreviewKind } from '@/features/files/preview';
import { RichText } from '@/components/ui/RichText';
import { Clipboard, Dialogs } from '@wailsio/runtime';

/** 后端的"这不是文本文件"是稳定的用户可见文案；用它把错误转成"不支持预览"页 */
function looksNotText(message: string): boolean {
  return /不是文本|not\s*text|binary/i.test(message);
}

interface DirEntry { name: string; path: string; isDir: boolean; size?: number; binary?: boolean }
interface DirPayload { path?: string; parent?: string; entries?: DirEntry[] }
/** 搜索命中（后端 internal/files.SearchResult 的形状） */
interface SearchHit { name: string; path: string; rel: string; isDir: boolean; size?: number }

/** 一个标签页：文件树本身也是一页，文件各自一页（照 DSH 的 pane tab） */
type Tab =
  | { id: 'files'; kind: 'files' }
  | {
      id: string;
      kind: 'file';
      path: string;
      name: string;
      /** 预览类型：按扩展名选出来的渲染器 */
      preview: PreviewKind | 'unsupported';
      text?: string;
      dataUrl?: string;
      truncated?: boolean;
      /** 文件总字节数与下一页起点（分页读取用） */
      size?: number;
      nextOffset?: number;
      error?: string;
    };

type FileTab = Extract<Tab, { kind: 'file' }>;

const FILES_TAB: Tab = { id: 'files', kind: 'files' };

/**
 * 工作区面板（右侧栏）。
 *
 * 交互照 DSH 的 ui-sidebar-files + dockkit 标签页：
 *   · 文件树**一次只列一层**（展开哪层请求哪层）
 *   · 点文件 → 在**同一个面板里新开一个标签页**（DSH 是 tabActions 让 file: 查看器认领），
 *     不再把面板切成上下两半
 *   · 标签条：chip + 下拉箭头（列出已开标签）+ `…` 菜单（刷新/打开目录/关闭全部）+ 收起面板
 *   · 「文件」这一页不可关闭；文件页可关，点 chip 切换
 *   · 「本轮改动」小节：agent 回合里 write/edit 碰过的文件（事件流见 stores/changedFiles）
 *   · 搜索：文件名/相对路径模糊匹配（后端整树遍历，跳过 node_modules 等）
 */
export function FilePanel({ onClose }: { onClose(): void }) {
  const cwd = useRuntime(s => s.cwd);
  const exeDir = useRuntime(s => s.exeDir);
  const root = (cwd || exeDir || '').replace(/[\\/]+$/, '');

  const [tabs, setTabs] = useState<Tab[]>([FILES_TAB]);
  const [activeId, setActiveId] = useState('files');
  const [children, setChildren] = useState<Record<string, DirEntry[]>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(false);
  /** 代码换行开关：按标签页记住（DSH 的 per-tab preference starts on），这里面板级共用 */
  const [wrap, setWrap] = useState(true);
  /** Markdown 标签页：渲染 / 源码（默认渲染——看 md 的人多半是要看内容） */
  const [mdRendered, setMdRendered] = useState(true);
  /** 「继续加载」按钮的进行中状态（按标签页） */
  const [loadingMore, setLoadingMore] = useState<Record<string, boolean>>({});
  /** 激活的标签元素：切换时滚进视野（窄面板里 chip 会溢出） */
  const activeChipRef = useRef<HTMLDivElement | null>(null);

  /* 搜索：切换图标 → 输入框；300ms 防抖 + 序号防旧响应覆盖新结果 */
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [hits, setHits] = useState<SearchHit[] | null>(null);
  const [hitsTruncated, setHitsTruncated] = useState(false);
  const [searching, setSearching] = useState(false);
  const searchSeq = useRef(0);
  const searchInputRef = useRef<HTMLInputElement | null>(null);

  /* 「本轮改动」（stores/changedFiles：router 在工具事件里维护） */
  const touched = useChangedFiles(s => s.touched);
  const refreshNonce = useChangedFiles(s => s.refreshNonce);
  /* 顶栏「本轮改动」按钮的聚焦信号：切回「文件」页并打开改动视图 */
  const changedFilesFocus = useUI(s => s.changedFilesFocus);
  /** 文件页的两个视图：目录树 / 只列本轮改动 */
  const [view, setView] = useState<'tree' | 'changed'>('tree');
  /** 主会话是否在跑：改动视图的标题上挂一个呼吸点（进行中实时追加） */
  const running = useRuntime(s => s.status === 'running');

  useEffect(() => {
    activeChipRef.current?.scrollIntoView({ inline: 'nearest', block: 'nearest' });
  }, [activeId]);

  const active = tabs.find(tab => tab.id === activeId) ?? FILES_TAB;

  const loadDir = useCallback(async (dir: string) => {
    try {
      const result = await piBridge.listDir(dir);
      if (!result.success) {
        reportError(result.error || '读取目录失败', '工作区文件');
        return;
      }
      const payload = parseData<DirPayload>(result);
      setChildren(previous => ({ ...previous, [dir]: payload?.entries ?? [] }));
    } catch (error) {
      reportError(error, '工作区文件');
    }
  }, []);

  useEffect(() => {
    if (!root) return;
    setChildren({});
    setExpanded({});
    setTabs([FILES_TAB]);
    setActiveId('files');
    setSearchOpen(false);
    setQuery('');
    setHits(null);
    setLoading(true);
    void loadDir(root).finally(() => setLoading(false));
  }, [root, loadDir]);

  /**
   * 读一页文本进标签页。offset=0 是打开/重读（替换内容），否则是「继续加载」（追加）。
   */
  const readTextIntoTab = useCallback(async (id: string, path: string, offset: number) => {
    const result = await piBridge.readFileTextAt(path, offset, 0);
    if (!result.success) {
      const message = result.error || '读取失败';
      // 后端说"不是文本"→ 换成不支持预览，而不是把一句英文错误丢在空白页上
      setTabs(previous => previous.map(tab => (tab.id === id && tab.kind === 'file'
        ? { ...tab, preview: looksNotText(message) ? 'unsupported' : tab.preview, error: message }
        : tab)));
      return;
    }
    const payload = parseData<{ text?: string; truncated?: boolean; size?: number; nextOffset?: number }>(result);
    setTabs(previous => previous.map(tab => {
      if (tab.id !== id || tab.kind !== 'file') return tab;
      return {
        ...tab,
        text: offset === 0 ? payload?.text ?? '' : (tab.text ?? '') + (payload?.text ?? ''),
        truncated: payload?.truncated,
        size: payload?.size,
        nextOffset: payload?.nextOffset,
        error: undefined,
      };
    }));
  }, []);

  /**
   * 打开一个文件：已有同路径标签就切过去，否则新开一页。
   *
   * 渲染器按扩展名选（照 DSH 的 documentPreviews 注册表）：
   *   · 图片 → 读字节转 data URL，内联显示；
   *   · 已知二进制容器（exe/zip/pdf/字体/音视频…）→ **不发读取请求**，
   *     直接进"无法预览"页（DSH 就是这么做的：没有渲染器的已知二进制后缀不读）；
   *   · 有语言定义或其它 → 按文本读，读不动（非 UTF-8）再落到"无法预览"。
   */
  async function openFile(entry: DirEntry) {
    const id = `file:${entry.path}`;
    if (tabs.some(tab => tab.id === id)) {
      setActiveId(id);
      return;
    }
    const preview = entry.isDir ? 'unsupported' : pickPreviewKind(entry.path);
    setTabs(previous => [...previous, {
      id, kind: 'file', path: entry.path, name: entry.name, preview, size: entry.size,
    }]);
    setActiveId(id);
    if (preview === 'unsupported') return; // 二进制不读

    try {
      if (preview === 'image') {
        const result = await piBridge.readFileDataUrl(entry.path);
        if (!result.success) {
          // 图片读不了（太大、坏文件、不是图片）→ 落到"无法预览"页，
          // 那里会把后端的原话显示出来，并给出"用系统程序打开"的出路。
          const message = result.error || '读取失败';
          setTabs(previous => previous.map(tab => (tab.id === id && tab.kind === 'file'
            ? { ...tab, preview: 'unsupported', error: message }
            : tab)));
          return;
        }
        const payload = parseData<{ dataUrl?: string; size?: number }>(result);
        setTabs(previous => previous.map(tab => (tab.id === id && tab.kind === 'file'
          ? { ...tab, dataUrl: payload?.dataUrl, size: payload?.size ?? tab.size }
          : tab)));
        return;
      }
      await readTextIntoTab(id, entry.path, 0);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setTabs(previous => previous.map(tab => (tab.id === id ? { ...tab, error: message } : tab)));
    }
  }

  /** 「继续加载」：从 nextOffset 再读一页追加到标签页 */
  async function loadMore(tab: FileTab) {
    if (tab.nextOffset == null || loadingMore[tab.id]) return;
    setLoadingMore(previous => ({ ...previous, [tab.id]: true }));
    try {
      await readTextIntoTab(tab.id, tab.path, tab.nextOffset);
    } catch (error) {
      reportError(error, '工作区文件');
    } finally {
      setLoadingMore(previous => ({ ...previous, [tab.id]: false }));
    }
  }

  /**
   * 本轮改动联动：回合里有写入或跑过 bash 时 refreshNonce +1，
   * 这里重列根目录与所有展开的目录，并重读打开着的、被碰过的文件标签。
   */
  const nonceFirst = useRef(true);
  const panelState = useRef({ expanded, tabs, root });
  panelState.current = { expanded, tabs, root };
  useEffect(() => {
    if (nonceFirst.current) {
      nonceFirst.current = false;
      return;
    }
    const { expanded: dirs, tabs: openTabs, root: base } = panelState.current;
    if (!base) return;
    void loadDir(base);
    for (const dir of Object.keys(dirs)) {
      if (dirs[dir]) void loadDir(dir);
    }
    const touchedNow = useChangedFiles.getState().touched.map(item => normPath(item.path));
    for (const tab of openTabs) {
      if (tab.kind !== 'file' || tab.preview === 'unsupported' || tab.preview === 'image') continue;
      if (touchedNow.includes(normPath(tab.path))) void readTextIntoTab(tab.id, tab.path, 0);
    }
  }, [refreshNonce, loadDir, readTextIntoTab]);

  /* 搜索：防抖请求后端，旧响应序号不对就丢 */
  useEffect(() => {
    if (!searchOpen) return;
    const q = query.trim();
    if (!q) {
      setHits(null);
      setHitsTruncated(false);
      setSearching(false);
      return;
    }
    setSearching(true);
    const seq = ++searchSeq.current;
    const timer = setTimeout(() => {
      piBridge.searchFiles(q)
        .then(result => {
          if (seq !== searchSeq.current) return;
          if (!result.success) {
            reportError(result.error || '搜索失败', '文件搜索');
            setHits([]);
            return;
          }
          const payload = parseData<{ results?: SearchHit[]; truncated?: boolean }>(result);
          setHits(payload?.results ?? []);
          setHitsTruncated(Boolean(payload?.truncated));
        })
        .catch(error => {
          if (seq === searchSeq.current) reportError(error, '文件搜索');
        })
        .finally(() => {
          if (seq === searchSeq.current) setSearching(false);
        });
    }, 300);
    return () => clearTimeout(timer);
  }, [query, searchOpen]);

  function closeSearch() {
    searchSeq.current++;
    setSearchOpen(false);
    setQuery('');
    setHits(null);
    setSearching(false);
  }

  /**
   * 搜索命中的是目录：在树里把它展开出来（逐级列目录 + 展开，最后回到树视图）。
   * 逐级请求而不是一次拿全树：面板的规矩就是"一次只列一层"。
   */
  async function revealInTree(hit: SearchHit) {
    const rootNorm = normPath(root);
    const hitNorm = normPath(hit.path);
    if (!rootNorm || !hitNorm.startsWith(`${rootNorm}/`)) return;
    const segments = hitNorm.slice(rootNorm.length + 1).split('/');
    closeSearch();
    let dir = root;
    // 命中是文件时最后一段是文件名，只展开到它的父目录
    const stopAt = hit.isDir ? segments.length : segments.length - 1;
    for (let i = 0; i < stopAt; i++) {
      const segment = segments[i];
      try {
        const result = await piBridge.listDir(dir);
        if (!result.success) return;
        const payload = parseData<DirPayload>(result);
        const entries = payload?.entries ?? [];
        setChildren(previous => ({ ...previous, [dir]: entries }));
        setExpanded(previous => ({ ...previous, [dir]: true }));
        const next = entries.find(e => normPath(e.name) === segment);
        if (!next) return;
        dir = next.path;
      } catch {
        return;
      }
    }
    // dir 此时是命中的目录本身，或文件的父目录：把它列出来并展开
    setExpanded(previous => ({ ...previous, [dir]: true }));
    void loadDir(dir);
  }

  /** 改动视图里的「定位」：切回目录树视图并把这个文件展开出来 */
  function locateInTree(path: string) {
    const name = path.split(/[\\/]/).pop() || path;
    setView('tree');
    void revealInTree({ name, path, rel: path, isDir: false });
  }

  /** ＋：用系统文件选择器挑一个文件，按路径加标签（与在树里点它等价） */
  async function pickFile() {
    try {
      const picked = await Dialogs.OpenFile({ Title: '打开文件', CanChooseFiles: true, CanChooseDirectories: false });
      if (!picked) return;
      const list = Array.isArray(picked) ? picked : [picked];
      const path = String(list.filter(Boolean)[0] ?? '');
      if (!path) return;
      const name = path.split(/[\\/]/).pop() || path;
      await openFile({ name, path, isDir: false });
    } catch (error) {
      if (!/cancel/i.test(String(error))) reportError(error, '打开文件');
    }
  }

  function closeTab(id: string) {
    setTabs(previous => {
      const next = previous.filter(tab => tab.id !== id);
      if (activeId === id) setActiveId(next.length ? next[next.length - 1].id : 'files');
      return next.length ? next : [FILES_TAB];
    });
  }

  function toggleDir(entry: DirEntry) {
    const next = !expanded[entry.path];
    setExpanded(previous => ({ ...previous, [entry.path]: next }));
    if (next && !children[entry.path]) void loadDir(entry.path);
  }

  /* 行内操作（hover 出现，照 Sidebar 会话行的 group-hover 模式，不做右键菜单） */
  function copyPath(path: string) {
    void Clipboard.SetText(path)
      .then(() => useUI.getState().showNotice('已复制路径', 'success'))
      .catch(error => reportError(error, '复制路径'));
  }
  function attach(path: string) {
    useUI.getState().enqueueAttachments([path]);
  }
  function reveal(path: string) {
    void piBridge.revealPath(path).catch(error => reportError(error, '在资源管理器中显示'));
  }

  function rowActions(entry: DirEntry) {
    return (
      <span className="absolute right-1 top-1/2 hidden -translate-y-1/2 items-center gap-0.5 rounded bg-panel3 group-hover/row:flex">
        <button
          type="button"
          title="复制路径"
          aria-label={`复制路径 ${entry.name}`}
          onClick={event => { event.stopPropagation(); copyPath(entry.path); }}
          className="grid h-5 w-5 place-items-center rounded text-mute hover:text-ink"
        >
          <Icon name="copy" className="h-3 w-3" />
        </button>
        {!entry.isDir && (
          <button
            type="button"
            title="加为附件"
            aria-label={`加为附件 ${entry.name}`}
            onClick={event => { event.stopPropagation(); attach(entry.path); }}
            className="grid h-5 w-5 place-items-center rounded text-mute hover:text-ink"
          >
            <Icon name="plus" className="h-3 w-3" />
          </button>
        )}
        <button
          type="button"
          title="在资源管理器中显示"
          aria-label={`在资源管理器中显示 ${entry.name}`}
          onClick={event => { event.stopPropagation(); reveal(entry.path); }}
          className="grid h-5 w-5 place-items-center rounded text-mute hover:text-ink"
        >
          <Icon name="folderPlus" className="h-3 w-3" />
        </button>
      </span>
    );
  }

  function renderLevel(dir: string, depth: number) {
    const entries = children[dir];
    if (!entries) return null;
    return entries.map(entry => (
      <div key={entry.path} className="group/row relative">
        <button
          type="button"
          onClick={() => (entry.isDir ? toggleDir(entry) : void openFile(entry))}
          title={entry.isDir ? entry.path : `${entry.path} · ${fmtBytes(entry.size ?? 0)}`}
          style={{ paddingLeft: 8 + depth * 12 }}
          className={cn(
            'flex w-full items-center gap-1.5 py-[3px] pr-2 text-left text-[12.5px] transition-colors hover:bg-hover',
            active.kind === 'file' && active.path === entry.path ? 'bg-panel3 text-ink' : 'text-dim',
          )}
        >
          {entry.isDir ? (
            <>
              <Icon name="chevron" className={cn('h-3 w-3 shrink-0 text-mute transition-transform', expanded[entry.path] && 'rotate-90')} />
              <Icon name="folder" className="h-3.5 w-3.5 shrink-0 text-info" />
            </>
          ) : (
            <>
              <span className="w-3 shrink-0" />
              <Icon name="file" className={cn('h-3.5 w-3.5 shrink-0', entry.binary ? 'text-warn' : 'text-mute')} />
            </>
          )}
          <span className="min-w-0 flex-1 truncate">{entry.name}</span>
        </button>
        {rowActions(entry)}
        {entry.isDir && expanded[entry.path] && renderLevel(entry.path, depth + 1)}
      </div>
    ));
  }

  /** 「本轮改动」视图里的文件（只显示当前工作区内的），按写入者分组：主会话在前，子智能体按出现顺序 */
  const rootNorm = normPath(root);
  const touchedHere = touched.filter(item => rootNorm && normPath(item.path).startsWith(`${rootNorm}/`));
  const touchedGroups = (() => {
    const groups: { agentId: string; items: typeof touchedHere }[] = [];
    for (const item of touchedHere) {
      const last = groups[groups.length - 1];
      if (last && last.agentId === item.agentId) {
        last.items.push(item);
      } else {
        groups.push({ agentId: item.agentId, items: [item] });
      }
    }
    return groups;
  })();

  /* 顶栏「本轮改动」按钮：面板开着就切回「文件」页并打开改动视图（首次渲染不响应） */
  const focusFirst = useRef(true);
  useEffect(() => {
    if (focusFirst.current) {
      focusFirst.current = false;
      return;
    }
    setActiveId('files');
    setView('changed');
  }, [changedFilesFocus]);

  const activeFile: FileTab | null = active.kind === 'file' ? active : null;
  const activeIsMarkdown = activeFile !== null && languageForPath(activeFile.path) === 'markdown';

  return (
    <div className="flex h-full min-h-0 flex-col bg-panel">
      {/* 头部（照 VS Code / DSH）：左边是面板标题「文件」，右边是打开的文件标签 + 添加按钮。
          标签不是"页面"而是"列"—「文件」是主动作，点它回到文件树。 */}
      <div className="flex h-9 shrink-0 items-center gap-1 px-1.5">
        <button
          type="button"
          onClick={() => setActiveId('files')}
          aria-pressed={active.kind === 'files'}
          className={cn(
            // DSH：withheld close 的 chip 要"安静"——不激活时没有胶囊、没有 hover 底色
            'flex shrink-0 items-center gap-1.5 rounded-lg px-2 py-1 text-[12.5px] transition-colors',
            active.kind === 'files' ? 'bg-panel3 text-ink' : 'text-mute hover:text-dim',
          )}
        >
          <Icon name="folder" className="h-4 w-4 text-info" />
          文件
        </button>

        {/* 打开的文件：带类型图标的 chip，active 高亮，hover 出 × */}
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
          {tabs.filter(tab => tab.kind === 'file').map(tab => {
            const file = tab as FileTab;
            return (
              <Fragment key={file.id}>
              {/* DSH：相邻 chip 之间是一条固定宽度的发丝线，紧邻激活 chip 时留白 */}
              <span aria-hidden className={cn('h-3.5 w-px shrink-0 bg-line', file.id === activeId && 'bg-transparent')} />
              <div
                ref={file.id === activeId ? activeChipRef : undefined}
                className={cn(
                  'group flex min-w-0 shrink-0 items-center gap-1.5 rounded-lg pl-1.5 pr-1 py-1 text-[12px] transition-colors',
                  file.id === activeId ? 'bg-panel3 text-ink' : 'text-mute hover:bg-hover hover:text-dim',
                )}
              >
                <button
                  type="button"
                  onClick={() => setActiveId(file.id)}
                  title={file.path}
                  className="flex min-w-0 items-center gap-1.5"
                >
                  <FileTypeIcon name={file.name} />
                  <span className="max-w-[96px] truncate">{file.name}</span>
                </button>
                <button
                  type="button"
                  aria-label={`关闭 ${file.name}`}
                  onClick={() => closeTab(file.id)}
                  className={cn(
                    'grid h-4 w-4 shrink-0 place-items-center rounded text-mute hover:text-ink',
                    // 激活时也要常显：DSH 的 chip 在 active/hover/focus 三种状态下都露出关闭
                    file.id === activeId ? 'opacity-100' : 'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100',
                  )}
                >
                  <Icon name="x" className="h-3 w-3" />
                </button>
              </div>
              </Fragment>
            );
          })}
        </div>

        {/* ＋：选一个文件加进标签（等价于在树里点它） */}
        <button
          type="button"
          aria-label="打开文件"
          title="打开文件"
          onClick={() => void pickFile()}
          className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute hover:bg-hover hover:text-ink"
        >
          <Icon name="plus" className="h-4 w-4" />
        </button>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label="面板操作"
              className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute hover:bg-hover hover:text-ink"
            >
              <Icon name="list" className="h-3.5 w-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => { setChildren({}); setExpanded({}); void loadDir(root); }}>
              <Icon name="list" className="h-4 w-4" /> 刷新文件树
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => void piBridge.openPath(root).catch(error => reportError(error, '打开文件夹'))}>
              <Icon name="folderPlus" className="h-4 w-4" /> 在资源管理器中打开
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => { setTabs([FILES_TAB]); setActiveId('files'); }}>
              <Icon name="x" className="h-4 w-4" /> 关闭所有文件标签
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <button
          type="button"
          aria-label="收起文件面板"
          onClick={onClose}
          className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute hover:bg-hover hover:text-ink"
        >
          <svg viewBox="0 0 16 16" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="1.4">
            <rect x="1.5" y="2.5" width="13" height="11" rx="2" />
            <path d="M9.5 2.5v11" />
          </svg>
        </button>
      </div>
      {active.kind === 'files' ? (
        <>
          {/* 视图切换：目录树 / 只列本轮改动（顶栏铅笔按钮点开的就是后者） */}
          <div className="mx-2.5 mb-1 mt-0.5 flex shrink-0 gap-0.5 rounded-lg bg-panel2 p-0.5">
            <button
              type="button"
              aria-pressed={view === 'tree'}
              onClick={() => setView('tree')}
              className={cn(
                'flex-1 rounded-md py-[3px] text-[11.5px] transition-colors',
                view === 'tree' ? 'bg-panel text-ink shadow-sm' : 'text-mute hover:text-dim',
              )}
            >
              全部文件
            </button>
            <button
              type="button"
              aria-pressed={view === 'changed'}
              onClick={() => setView('changed')}
              className={cn(
                'flex-1 rounded-md py-[3px] text-[11.5px] transition-colors',
                view === 'changed' ? 'bg-panel text-ink shadow-sm' : 'text-mute hover:text-dim',
              )}
            >
              本轮改动{touchedHere.length > 0 && <span className="text-info"> {touchedHere.length}</span>}
            </button>
          </div>

          {view === 'changed' ? (
            /* 改动视图：只列本轮写过的文件，按写入者分组（主会话在前，子智能体按出现顺序） */
            <div data-selectable className="min-h-0 flex-1 overflow-auto pb-2">
              {touchedGroups.length === 0 ? (
                <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center">
                  <Icon name="file" className="h-6 w-6 text-mute opacity-60" />
                  <p className="text-[12px] text-mute">这一轮还没有改动文件</p>
                  <p className="text-[11px] leading-[16px] text-mute">
                    agent 用 write / edit 写过的文件会列在这里（含子智能体）
                  </p>
                </div>
              ) : (
                touchedGroups.map((group, groupIndex) => (
                  <div key={group.agentId || 'main'}>
                    <div className="flex items-center gap-1.5 px-2.5 pb-0.5 pt-1.5">
                      {group.agentId !== '' && <Icon name="robot" className="h-3 w-3 shrink-0 text-think" />}
                      <span className="min-w-0 flex-1 truncate text-[11px] font-medium text-mute">
                        {group.agentId ? `子智能体 ${group.agentId}` : '主会话'}
                      </span>
                      {groupIndex === 0 && running && (
                        <span title="进行中：文件随写入实时追加" className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-success" />
                      )}
                      {groupIndex === 0 && (
                        <button
                          type="button"
                          onClick={() => useChangedFiles.getState().clear()}
                          className="shrink-0 text-[11px] text-mute hover:text-ink"
                        >
                          清空
                        </button>
                      )}
                    </div>
                    {group.items.map(item => {
                      const name = item.path.split(/[\\/]/).pop() || item.path;
                      /* 目录部分：在工作区内就显示相对路径，外面的显示完整路径 */
                      const rel = normPath(item.path).startsWith(`${rootNorm}/`)
                        ? item.path.slice(root.length + 1)
                        : item.path;
                      const dir = rel.slice(0, rel.length - name.length).replace(/[\\/]+$/, '');
                      return (
                        <div key={item.path} className="group/row relative">
                          <button
                            type="button"
                            title={item.agentId ? `${item.path}（子智能体写入）` : item.path}
                            onClick={() => void openFile({ name, path: item.path, isDir: false })}
                            className="flex w-full items-center gap-1.5 py-[3px] pl-2.5 pr-2 text-left text-[12px] text-dim transition-colors hover:bg-hover"
                          >
                            {item.agentId
                              ? <Icon name="robot" className="h-3.5 w-3.5 shrink-0 text-think" />
                              : <FileTypeIcon name={name} />}
                            <span className="shrink-0 text-ink">{name}</span>
                            {dir && <span className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-mute">{dir}</span>}
                          </button>
                          {/* 行内操作：定位 = 切回目录树并把它展开出来（点行本身就是打开） */}
                          <span className="absolute right-1 top-1/2 hidden -translate-y-1/2 items-center gap-0.5 rounded bg-panel3 group-hover/row:flex">
                            <button
                              type="button"
                              title="在目录树中定位"
                              aria-label={`在目录树中定位 ${name}`}
                              onClick={event => { event.stopPropagation(); locateInTree(item.path); }}
                              className="grid h-5 w-5 place-items-center rounded text-mute hover:text-ink"
                            >
                              <Icon name="folderPlus" className="h-3 w-3" />
                            </button>
                          </span>
                        </div>
                      );
                    })}
                  </div>
                ))
              )}
            </div>
          ) : (
          <>
          <div className="flex h-7 shrink-0 items-center gap-1.5 px-2.5">
            {searchOpen ? (
              <>
                <Icon name="search" className="h-3.5 w-3.5 shrink-0 text-mute" />
                <input
                  ref={searchInputRef}
                  value={query}
                  onChange={event => setQuery(event.target.value)}
                  onKeyDown={event => { if (event.key === 'Escape') closeSearch(); }}
                  placeholder="搜索文件名 / 路径…"
                  aria-label="搜索文件"
                  className="min-w-0 flex-1 bg-transparent text-[12px] text-ink outline-none placeholder:text-mute"
                />
                <button
                  type="button"
                  aria-label="关闭搜索"
                  onClick={closeSearch}
                  className="grid h-5 w-5 shrink-0 place-items-center rounded text-mute hover:text-ink"
                >
                  <Icon name="x" className="h-3 w-3" />
                </button>
              </>
            ) : (
              <>
                <span className="min-w-0 flex-1 truncate text-[11.5px] text-mute" title={root}>
                  {root || '未设置工作目录'}
                </span>
                <button
                  type="button"
                  aria-label="搜索文件"
                  title="搜索文件"
                  onClick={() => { setSearchOpen(true); setTimeout(() => searchInputRef.current?.focus(), 0); }}
                  className="grid h-5 w-5 shrink-0 place-items-center rounded text-mute hover:text-ink"
                >
                  <Icon name="search" className="h-3.5 w-3.5" />
                </button>
              </>
            )}
          </div>

          <div data-selectable className="min-h-0 flex-1 overflow-auto pb-2">
            {searchOpen ? (
              <>
                {searching && <p className="px-3 py-1 text-[12px] text-mute">搜索中…</p>}
                {!searching && hits !== null && hits.length === 0 && query.trim() && (
                  <p className="px-3 py-1 text-[12px] text-mute">没有匹配的文件</p>
                )}
                {(hits ?? []).map(hit => (
                  <div key={hit.path} className="group/row relative">
                    <button
                      type="button"
                      onClick={() => (hit.isDir ? void revealInTree(hit) : void openFile({ name: hit.name, path: hit.path, isDir: false, size: hit.size }))}
                      title={hit.isDir ? `${hit.rel}（点击在树中展开）` : `${hit.rel} · ${fmtBytes(hit.size ?? 0)}`}
                      className="flex w-full items-center gap-1.5 px-2.5 py-[3px] pr-2 text-left text-[12.5px] text-dim transition-colors hover:bg-hover"
                    >
                      {hit.isDir
                        ? <Icon name="folder" className="h-3.5 w-3.5 shrink-0 text-info" />
                        : <FileTypeIcon name={hit.name} />}
                      <span className="min-w-0 flex-1 truncate">
                        <span className="text-ink">{hit.name}</span>
                        <span className="text-mute">{`  ${hit.rel.slice(0, hit.rel.length - hit.name.length)}`}</span>
                      </span>
                    </button>
                    {rowActions({ name: hit.name, path: hit.path, isDir: hit.isDir, size: hit.size })}
                  </div>
                ))}
                {hitsTruncated && <p className="px-3 py-1 text-[11px] text-mute">结果太多，只显示前 200 条</p>}
              </>
            ) : (
              <>
                {loading && <p className="px-3 py-1 text-[12px] text-mute">读取中…</p>}
                {root ? renderLevel(root, 0) : <p className="px-3 py-1 text-[12px] text-mute">还没有工作目录</p>}
              </>
            )}
          </div>
          </>
          )}
        </>
      ) : (
        <>
          <div className="flex h-7 shrink-0 items-center gap-1.5 px-2.5">
            <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-mute" title={active.path}>
              {active.path}
            </span>
            {activeFile?.size != null && (
              <span className="shrink-0 text-[10.5px] text-mute">{fmtBytes(activeFile.size)}</span>
            )}
            {activeIsMarkdown && (
              /* Markdown：渲染 / 源码切换（看内容用渲染，改格式用源码） */
              <span className="flex shrink-0 items-center rounded-md bg-panel3 p-px text-[10.5px]">
                <button
                  type="button"
                  aria-pressed={mdRendered}
                  onClick={() => setMdRendered(true)}
                  className={cn('rounded px-1.5 py-px transition-colors', mdRendered ? 'bg-panel text-ink shadow-sm' : 'text-mute hover:text-dim')}
                >
                  渲染
                </button>
                <button
                  type="button"
                  aria-pressed={!mdRendered}
                  onClick={() => setMdRendered(false)}
                  className={cn('rounded px-1.5 py-px transition-colors', !mdRendered ? 'bg-panel text-ink shadow-sm' : 'text-mute hover:text-dim')}
                >
                  源码
                </button>
              </span>
            )}
            {active.truncated && <span className="shrink-0 text-[10.5px] text-warn">已截断</span>}
            <button
              type="button"
              aria-label="用系统程序打开"
              onClick={() => void piBridge.openPath(active.path).catch(error => reportError(error, '打开文件'))}
              className="shrink-0 text-[11.5px] text-mute hover:text-ink"
            >
              打开
            </button>
          </div>

          {active.preview === 'unsupported' ? (
            /* 无法预览：文件类型图标 + 说明 + 唯一的出路（用系统程序打开）。
               与 DSH 的"不支持预览"空态一致：不显示半截内容，也不假装能渲染。 */
            <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-4 text-center">
              <FileTypeIcon name={active.name} className="h-8 w-8 opacity-70" />
              <p className="text-[12px] leading-[19px] text-mute">
                {active.error && !looksNotText(active.error)
                  ? active.error
                  : '这个文件是二进制内容，无法在侧栏预览。'}
              </p>
              {activeFile?.size != null && (
                <p className="text-[11px] text-mute">{fmtBytes(activeFile.size)}</p>
              )}
              <button
                type="button"
                onClick={() => void piBridge.openPath(active.path).catch(error => reportError(error, '打开文件'))}
                className="flex items-center gap-1.5 rounded-lg bg-panel3 px-2.5 py-1 text-[12px] text-dim transition-colors hover:text-ink"
              >
                <Icon name="file" className="h-3.5 w-3.5" /> 用系统程序打开
              </button>
            </div>
          ) : active.error ? (
            <p className="px-3 py-2 text-[12px] text-danger">{active.error}</p>
          ) : active.preview === 'image' ? (
            /* 图片：整块面板内居中，最多按原始尺寸显示（不放大），
               四周留 12px 内边距（DSH 的静态图片渲染就是"rounded inside a 12px inset"）。 */
            <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto p-3">
              {active.dataUrl ? (
                <img
                  src={active.dataUrl}
                  alt={active.name}
                  title={active.name}
                  className="max-h-full max-w-full rounded-lg object-contain"
                />
              ) : (
                <p className="text-[12px] text-mute">读取中…</p>
              )}
            </div>
          ) : (
            <>
              {activeIsMarkdown && mdRendered ? (
                /* Markdown 渲染态：复用对话正文的 RichText（链接走系统浏览器） */
                <div data-selectable className="min-h-0 flex-1 overflow-auto px-4 py-3">
                  {active.text !== undefined
                    ? <RichText text={active.text} />
                    : <p className="text-[12px] text-mute">读取中…</p>}
                </div>
              ) : active.preview === 'code' ? (
                <CodeView
                  code={active.text ?? ''}
                  path={active.path}
                  wrap={wrap}
                  onToggleWrap={() => setWrap(previous => !previous)}
                />
              ) : (
                <pre data-selectable className="min-h-0 flex-1 overflow-auto px-2.5 pb-2 font-mono text-[11.5px] leading-[17px] text-dim">
                  {active.text ?? '读取中…'}
                </pre>
              )}
              {active.truncated && (
                /* 分页：从 nextOffset 再读一页追加（后端上限 256KB/页） */
                <div className="shrink-0 border-t border-line px-2.5 py-1.5">
                  <button
                    type="button"
                    disabled={Boolean(activeFile && loadingMore[activeFile.id])}
                    onClick={() => activeFile && void loadMore(activeFile)}
                    className="flex w-full items-center justify-center gap-1.5 rounded-lg bg-panel3 px-2.5 py-1 text-[12px] text-dim transition-colors hover:text-ink disabled:opacity-60"
                  >
                    {activeFile && loadingMore[activeFile.id]
                      ? '读取中…'
                      : `继续加载（已显示 ${fmtBytes(activeFile?.nextOffset ?? 0)} / 共 ${fmtBytes(activeFile?.size ?? 0)}）`}
                  </button>
                </div>
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}
