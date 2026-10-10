import { useEffect, useRef, useState } from 'react';
import { Dialogs } from '@wailsio/runtime';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { Icon } from '@/components/ui/Icon';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { ContextMeter } from '@/features/chat/ContextMeter';
import { PromptBox } from '@/features/chat/PromptBox';
import { RoleBadge } from '@/features/roles/RoleBadge';
import { useRoles } from '@/stores/roles';
import { reportError } from '@/lib/reportError';
import { useAgent } from '@/hooks/useAgent';
import { attachmentName } from '@/lib/format';
import { isImagePath } from '@/features/files/preview';
import { Events, PI_FILES_DROPPED } from '@/bridge/events';
import { cn } from '@/lib/utils';
import { useModels } from '@/hooks/useModels';
import { useRuntime } from '@/stores/runtime';
import { useTimeline } from '@/stores/timeline';
import { useUI } from '@/stores/ui';

/**
 * 输入区（照 DSH 的 composer 布局）：
 *   · 未连接时直接发送 → 自动启动 Pi 再发，不需要先点「启动」
 *   · 运行中：回车 = 插入（steer），另有「停止」（abort）与「排到后面」（follow_up）
 *   · 左下角是模型快切 + 上下文用量环；连接参数（模式/目录/入口）在设置页
 *
 * 草稿**按会话（runtime）分开存**：切到另一条会话时，输入框里该是那一条
 * 上次没发出去的东西，而不是这一条打了一半的句子（一个进程一条会话之后，
 * 两边的草稿本来就分属两个上下文）。做法是外层按 runtimeId 换 key 重挂载
 * 内层组件，内层读写自己那份草稿——比在每个 setState 里带上 id 可靠得多，
 * 卸载时也不会把草稿写错地方。
 */
const drafts = new Map<string, { text: string; files: string[] }>();
/** 草稿最多记这么多条：Map 不设上限就是"开过的每一条会话都留一份草稿" */
const DRAFT_LIMIT = 20;

function rememberDraft(runtimeId: string, draft: { text: string; files: string[] }): void {
  drafts.delete(runtimeId);
  drafts.set(runtimeId, draft);
  while (drafts.size > DRAFT_LIMIT) {
    const oldest = drafts.keys().next().value;
    if (typeof oldest !== 'string') break;
    drafts.delete(oldest);
  }
}

/** 附件缩略图缓存：path → 缩小后的 dataUrl，切会话 / 重挂载不重复读盘 */
const thumbnailCache = new Map<string, string>();
/** 缓存里缩略图的最长边（px） */
const THUMB_EDGE = 96;

/**
 * 把原图压成小缩略图再缓存：原图 dataUrl 最大约 11MB（8MB 上限的 base64），
 * 不压直接缓存，一天几十张截图就是几百 MB 内存。
 * gif / svg 保留原图：canvas 会杀掉动画，矢量图本身也很小。
 */
async function makeThumbnail(dataUrl: string, mime: string): Promise<string> {
  if (mime === 'image/gif' || mime === 'image/svg+xml') return dataUrl;
  const image = new Image();
  await new Promise<void>((resolve, reject) => {
    image.onload = () => resolve();
    image.onerror = () => reject(new Error('图片解码失败'));
    image.src = dataUrl;
  });
  const longest = Math.max(image.naturalWidth, image.naturalHeight);
  if (longest <= THUMB_EDGE) return dataUrl;
  const scale = THUMB_EDGE / longest;
  const canvas = document.createElement('canvas');
  canvas.width = Math.max(1, Math.round(image.naturalWidth * scale));
  canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
  const context = canvas.getContext('2d');
  if (!context) return dataUrl;
  context.drawImage(image, 0, 0, canvas.width, canvas.height);
  return canvas.toDataURL('image/png'); // png 保透明底；96px 只有几十 KB
}

/** 图片附件的缩略图；读不到（文件已删 / 超过 8MB / 不是图片）退回通用文件图标。 */
function AttachmentThumb({ path }: { path: string }) {
  const [thumb, setThumb] = useState<string | undefined>(thumbnailCache.get(path));
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (thumb !== undefined || failed) return;
    let alive = true;
    void (async () => {
      try {
        const result = await piBridge.readAttachmentImage(path);
        const payload = result.success ? parseData<{ dataUrl?: string; mime?: string }>(result) : null;
        if (!payload?.dataUrl) throw new Error(result.error || '读取失败');
        const small = await makeThumbnail(payload.dataUrl, payload.mime ?? '');
        thumbnailCache.set(path, small);
        if (alive) setThumb(small);
      } catch {
        if (alive) setFailed(true);
      }
    })();
    return () => {
      alive = false;
    };
  }, [path, thumb, failed]);
  if (thumb === undefined) return <Icon name="file" className="h-3.5 w-3.5 text-mute" />;
  return <img src={thumb} alt="" className="h-7 w-7 shrink-0 rounded object-cover" />;
}

export function Composer() {
  // key 换掉 = 内层组件重新挂载：state 从这条会话自己的草稿里取
  const id = useRuntime(s => s.runtimeId);
  return <SessionComposer key={id} runtimeId={id} />;
}

function SessionComposer({ runtimeId }: { runtimeId: string }) {
  const [text, setText] = useState(drafts.get(runtimeId)?.text ?? '');
  const [files, setFiles] = useState<string[]>(drafts.get(runtimeId)?.files ?? []);
  useEffect(() => { rememberDraft(runtimeId, { text, files }); }, [runtimeId, text, files]);
  /** 有文件正被拖到窗口上方：显示"松开即可添加"的投放提示 */
  const [dragging, setDragging] = useState(false);
  /**
   * 斜杠命令面板：文本以 `/` 开头且还没输入空格时，列出可用命令。
   * 命令来自 Pi 的 get_commands（扩展命令 / 提示模板 / 技能），
   * 选中后把名字插进输入框，发送时 Pi 自己展开。
   */
  const [commands, setCommands] = useState<{ name: string; description?: string; source?: string }[]>([]);
  // 单独订阅连接状态：这段代码位于下面 connected 的声明之前（顺序不能反）
  const commandSourceReady = useRuntime(s => s.connected);
  useEffect(() => {
    if (!commandSourceReady) return;
    void piBridge.getCommands()
      .then(result => {
        if (!result.success) return;
        const payload = parseData<{ commands?: { name: string; description?: string; source?: string }[] }>(result);
        setCommands(payload?.commands ?? []);
      })
      .catch(() => { /* 读不到命令不影响输入 */ });
    // 依赖连接状态：连上之前 get_commands 没意义（命令表来自 Pi）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [commandSourceReady]);

  const slashQuery = /^\/(\S*)$/.exec(text)?.[1];
  const slashMatches = slashQuery === undefined
    ? []
    : commands.filter(item => item.name.toLowerCase().includes(slashQuery.toLowerCase())).slice(0, 8);

  function insertCommand(name: string) {
    setText(`/${name} `);
    inputRef.current?.focus();
  }
  const connected = useRuntime(s => s.connected);
  const status = useRuntime(s => s.status);
  const sending = useRuntime(s => s.sending);
  /**
   * 切换会话进行中：它属于"忙"的一种——那一刻这条消息发给谁还不确定，
   * 所以发送/插入/排队统统禁用（见下面 submit 里的守卫）。
   */
  const switching = useRuntime(s => s.switching);
  const busy = useRuntime(s => s.busy || s.switching);
  const workspaceSwitching = useUI(s => s.switchingWorkspace);
  const showEvents = useUI(s => s.showEvents);
  const lastDuration = useRuntime(s => s.lastDuration);
  const runStartedAt = useRuntime(s => s.runStartedAt);
  /** 运行中回车的投递方式（设置里改，见 stores/ui.ts 的 busyEnter） */
  const busyEnter = useUI(s => s.busyEnter);
  /** 当前角色：状态行要显示它，因为它随"重启 Pi"变化 */
  const roles = useRoles(s => s.roles);
  const selectedRole = useRoles(s => s.selected);
  const activeRole = roles.find(role => role.id === selectedRole);
  const model = useRuntime(s => s.model);
  const provider = useRuntime(s => s.provider);
  /** 排队中的消息原文（queue_update 提供）：显示在输入框上方的排队面板 */
  const queueSteering = useRuntime(s => s.queueSteering);
  const queueFollowUp = useRuntime(s => s.queueFollowUp);
  /** 排队面板展开/收起：运行中视线在生成区，面板允许收成一行 */
  const [queueOpen, setQueueOpen] = useState(true);
  const { prompt, steer, followUp, abort, clearQueue, removeQueued, steerQueued, setModel, setThinkingLevel, refreshState } = useAgent();
  // 模型列表直接来自 Pi 的配置（get_available_models），见 useModels 的说明
  const { groups, currentLabel, models, supportedLevels, levels, fromCache } = useModels();
  const tokens = useRuntime(s => s.tokens);
  const thinkingLevel = useRuntime(s => s.thinkingLevel);
  const thinkingOptions = supportedLevels.length ? supportedLevels : levels;
  const inputRef = useRef<HTMLTextAreaElement>(null);
  /** 上一次按 Escape 的时刻：用于"连按两次停止" */
  const lastEscapeAt = useRef(0);

  /**
   * "正在干活"的三种状态：模型在出字、在自动重试、在压缩。
   *
   * **waiting-ui 不算**：那是扩展在等用户点确认（Pi 已经停下来等我们回
   * extension_ui_response 了）。把它算进"运行中"会让界面变成一个假的运行态——
   * 右上角转圈、右下角是"停止"，而实际上需要用户去点那个确认框。
   */
  const running = status === 'running' || status === 'retrying' || status === 'compacting';
  /** 等用户应答扩展的确认框：单独表达，别混进"运行中" */
  const awaitingUser = status === 'waiting-ui';
  const canSubmit = !sending && !busy && text.trim().length > 0;

  // 运行中每秒走一次的计时：让人知道它已经跑了多久（回合结束后换成"上次 Xs"）
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(timer);
  }, [running]);
  const elapsed = running && runStartedAt
    ? (() => {
        const seconds = (now - runStartedAt) / 1000;
        return seconds < 10 ? `${seconds.toFixed(1)}s` : `${Math.round(seconds)}s`;
      })()
    : '';

  // 上一轮的 token 速度：运行时前后各取一次 output token 快照，
  // 差值 ÷ 这轮耗时。放本地 state 而不是全局 store——它只是显示用的派生值。
  const [tokPerSec, setTokPerSec] = useState(0);
  const runSnapshot = useRef<{ output: number; at: number } | null>(null);
  useEffect(() => {
    if (running) {
      if (!runSnapshot.current) runSnapshot.current = { output: tokens.output, at: Date.now() };
      return;
    }
    const snapshot = runSnapshot.current;
    if (!snapshot) return;
    runSnapshot.current = null;
    const delta = tokens.output - snapshot.output;
    const seconds = (Date.now() - snapshot.at) / 1000;
    if (delta > 0 && seconds > 0.5) setTokPerSec(delta / seconds);
  }, [running, tokens.output]);

  /** 轮次 = 用户发过多少条消息（对话轮次的直观口径） */
  const turns = useTimeline(s => s.items.filter(item => item.kind === 'user').length);

  // 连接后取一次可选模型（用于左下角的快切）
  const modelReady = connected && models.length > 0;

  function submit(mode: 'prompt' | 'steer' | 'followUp') {
    // 切换中一律不投递：这条消息属于哪条会话此刻还没定下来
    if (busy) return;
    const value = text.trim();
    if (!value && files.length === 0) return;
    if (mode === 'prompt' && (running || !canSubmit)) return;
    setText('');
    // 附件只跟第一条 prompt 走：steer / follow_up 是"补一句话"，不带图
    const attached = mode === 'prompt' ? files : [];
    setFiles([]);
    if (mode === 'steer') void steer(value);
    else if (mode === 'followUp') void followUp(value);
    else void prompt(value, attached);
  }

  /**
   * 运行中按回车怎么投递：排队（follow_up）还是插入（steer）。
   *
   * 照 DSH 的 busy-Enter 设置：它决定普通回车走 Queue 还是 Steer，
   * Ctrl/Cmd+Enter 走另一种；发送按钮与回车保持同一个模式。
   */
  const runningMode: 'steer' | 'followUp' = busyEnter === 'queue' ? 'followUp' : 'steer';
  const otherMode: 'steer' | 'followUp' = runningMode === 'steer' ? 'followUp' : 'steer';

  /** 主按钮：空闲=发送；运行中空稿=停止；运行中有内容=按设置排队/插入 */
  const hasDraft = text.trim().length > 0 || files.length > 0;
  const primary = running
    ? hasDraft
      ? {
          label: runningMode === 'followUp' ? '排队' : '插入',
          title: runningMode === 'followUp'
            ? '排队（follow_up）：等当前回合彻底结束后再生效；Ctrl+Enter 改为插入'
            : '插入（steer）：在当前回合的下一个检查点接进去；Ctrl+Enter 改为排队',
          icon: 'send' as const,
          variant: 'default' as const,
          // 切换中不给投递：这条消息属于哪条会话此刻还没定下来
          disabled: switching,
          run: () => submit(runningMode),
        }
      : {
          label: '停止',
          title: '停止当前回合（不杀进程，已生成的内容保留）',
          icon: 'stop' as const,
          variant: 'destructive' as const,
          disabled: false,
          run: () => void abort(),
        }
    : {
        label: '发送',
        title: '发送（Enter）',
        icon: 'send' as const,
        variant: 'default' as const,
        disabled: !canSubmit,
        run: () => submit('prompt'),
      };

  /**
   * 连按两次 Escape 停止当前回合（照 DSH：两次独立 Escape、间隔 500ms 内）。
   * 排队中的消息保持不变——停止只停当前回合。
   */
  function handleEscape(): void {
    if (!running) return;
    const now = Date.now();
    if (now - lastEscapeAt.current < 500) {
      lastEscapeAt.current = 0;
      void abort();
      return;
    }
    lastEscapeAt.current = now;
  }

  /**
   * 面板上的「编辑」：把排队消息拿回输入框改。
   *
   * 拿走 = 从队列里删掉（removeQueued）+ 填回输入框；改完再发送就是一条
   * 新的排队消息。Pi 没有"改队列里某一条"的 RPC，这是语义最清楚的等价做法。
   * 输入框里已有草稿时不覆盖：把草稿先拼在前面，免得打了一半的话被吞掉。
   */
  function editQueued(kind: 'steering' | 'followUp', index: number, queuedText: string) {
    setText(previous => {
      const draft = previous.trim();
      return draft ? `${previous.replace(/\s+$/, '')}\n${queuedText}` : queuedText;
    });
    void removeQueued(kind, index);
    inputRef.current?.focus();
  }

  /**
   * 选附件：任意类型都能选。
   *
   * 不再区分类型：图片、文档、目录一视同仁，Go 侧（attachments.go）会把
   * **绝对路径**写进消息，模型用 read 工具去看——这是 Pi 里读文件的正常方式。
   */
  async function pickFiles() {
    try {
      const picked = await Dialogs.OpenFile({
        Title: '选择附件（任意类型；发送时把绝对路径交给模型）',
        CanChooseFiles: true,
        // 只要文件：两个都开时 Windows 会弹成"选择文件夹"（用户反馈的 bug）
        CanChooseDirectories: false,
        AllowsMultipleSelection: true,
      });
      if (!picked) return;
      const list = (Array.isArray(picked) ? picked : [picked]).filter(Boolean);
      addAttachments(list, '选择文件');
    } catch (error) {
      // 用户点取消不是错误（Wails 会把 cancelled 当异常抛出）
      if (!/cancel/i.test(String(error))) reportError(error, '选择附件');
    }
  }

  /**
   * 附件统一入口：查重、限量、给个提示。
   *
   * 拖拽、粘贴、加号选文件三条路都汇到这里——三条路各自判断限量和查重，
   * 迟早会出现"拖进来 12 个只显示 8 个且不说明原因"这种糊涂账。
   */
  function addAttachments(paths: string[], source: string) {
    const clean = paths.map(item => item.trim()).filter(Boolean);
    if (clean.length === 0) return;
    setFiles(previous => {
      const merged = [...previous];
      for (const path of clean) {
        if (!merged.includes(path)) merged.push(path);
      }
      const capped = merged.slice(0, 8);
      if (merged.length > 8) {
        useUI.getState().showNotice(`最多 8 个附件，已忽略后面的 ${merged.length - 8} 个`, 'warn');
      } else {
        useUI.getState().showNotice(`${source}：已添加 ${clean.length} 个附件`);
      }
      return capped;
    });
  }

  /**
   * 从资源管理器拖进来的文件。
   *
   * 走 Wails 的原生投递（见 main.go 的 EnableFileDrop）：DOM 的 drop 事件在
   * WebView2 里拿不到文件**路径**，只有 File 对象，而我们的附件机制要的是路径。
   * 所以这里订阅 Go 转发过来的 pi:files-dropped，里面是真实路径。
   *
   * 落点判断：窗口任意位置都可以拖（用户不用瞄得很准）；如果拖到了明确标了
   * data-file-drop-target 的区域（输入框），额外给一句更具体的提示。
   */
  useEffect(() => {
    const off = Events.On(PI_FILES_DROPPED, event => {
      const payload = event.data as { files?: string[]; target?: { id?: string } } | undefined;
      const list = payload?.files ?? [];
      setDragging(false);
      if (list.length === 0) return;
      addAttachments(list, '拖入文件');
    });
    /**
     * 拖拽悬停提示 + 兜底拦掉默认行为。
     *
     * WebView2 在 EnableFileDrop=true 时 JS 的 dragenter/dragover/drop 都会触发，
     * 而**不 preventDefault 的话浏览器会导航到那个文件**（界面直接变成图片或文本）。
     * 输入框自己有处理，但用户很可能拖到对话区甚至标题栏上——那些地方只会带来
     * 一次"界面被替换"的惊吓。所以统一挡在 window 上。
     *
     * 提示的显隐用"时间戳 + 轮询"，不用 dragenter/dragleave 配对计数：
     * 配对是不可靠的——拖拽中途离开窗口、拖到别的应用、拖拽被系统取消，
     * leave 都可能不来，计数一旦多算一次，提示就**永久挂在屏幕上**
     * （实测踩到：一次漏掉的 leave 让提示再也不消失）。
     * dragover 在拖拽期间会持续触发，"最近 400ms 内还有 dragover"就是
     * "拖拽仍在进行"的最可靠信号。
     */
    let lastDragAt = 0;
    let decayTimer = 0;
    const markDragging = () => {
      lastDragAt = Date.now();
      setDragging(true);
      if (!decayTimer) {
        decayTimer = window.setInterval(() => {
          if (Date.now() - lastDragAt > 400) {
            setDragging(false);
            window.clearInterval(decayTimer);
            decayTimer = 0;
          }
        }, 200);
      }
    };
    const onEnter = (e: DragEvent) => {
      e.preventDefault();
      if (!e.dataTransfer?.types?.includes('Files')) return; // 只关心文件
      markDragging();
    };
    const onOver = (e: DragEvent) => {
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
      if (e.dataTransfer?.types?.includes('Files')) markDragging();
    };
    const onDrop = (e: DragEvent) => {
      e.preventDefault();
      lastDragAt = 0;
      setDragging(false);
    };
    // 拖拽被中断（Esc、拖到别的应用）时也要收干净
    const onEnd = () => {
      lastDragAt = 0;
      setDragging(false);
    };
    window.addEventListener('dragenter', onEnter);
    window.addEventListener('dragover', onOver);
    window.addEventListener('drop', onDrop);
    window.addEventListener('dragend', onEnd);
    window.addEventListener('blur', onEnd);
    return () => {
      off?.();
      if (decayTimer) window.clearInterval(decayTimer);
      window.removeEventListener('dragenter', onEnter);
      window.removeEventListener('dragover', onOver);
      window.removeEventListener('drop', onDrop);
      window.removeEventListener('dragend', onEnd);
      window.removeEventListener('blur', onEnd);
    };
  }, []);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  /**
   * 附件投递箱：文件面板的「加为附件」把路径放进 ui store，这里取走。
   *
   * 订阅 inbox 本身（而不是轮询）：enqueue 会换数组引用，变化即触发。
   */
  const attachmentInbox = useUI(s => s.attachmentInbox);
  useEffect(() => {
    if (attachmentInbox.length === 0) return;
    const paths = useUI.getState().consumeAttachments();
    if (paths.length) addAttachments(paths, '文件面板');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attachmentInbox]);

  return (
    <div className="shrink-0 px-6 pt-1 pb-4">
      <div className="mx-auto w-full max-w-[min(1040px,92%)]">
        <PromptBox
          /* 拖拽落点的标记：Go 侧的 DropTargetDetails 会回传这个 id，
             前端据此知道"用户是冲着输入框来的"（见上面的提示文案） */
          dropTargetId="composer"
          value={text}
          onChange={setText}
          inputRef={inputRef}
          ariaLabel="输入消息"
          height={62}
          onSubmit={() => submit(running ? runningMode : 'prompt')}
          /* 运行中普通回车走 busy-Enter 设置的投递方式，Ctrl/Cmd+Enter 走另一种 */
          onAlternateSubmit={() => submit(running ? otherMode : 'prompt')}
          onKeyDown={event => {
            // 连按两次 Escape 停止当前回合（照 DSH）
            if (event.key === 'Escape' && running) handleEscape();
          }}
          /**
           * 粘贴附件：图片、复制的文件都收。
           *
           * 三种来源要分别处理：
           *   · 截图/复制图片 → clipboardData.items 里是 image/*（老路径）
           *   · 在资源管理器里复制文件再粘 → clipboardData.files 里有 File，但**没有路径**，
           *     所以把内容读成 dataURL 交给 Go 落盘，再当普通附件用
           *   · 纯文本 → 保持浏览器默认行为（不要抢）
           */
          onPaste={e => {
            const data = e.clipboardData;
            if (!data) return;
            const filesFromClipboard = Array.from(data.files ?? []);
            const imageItem = Array.from(data.items ?? []).find(item => item.type.startsWith('image/'));
            const targets: File[] = filesFromClipboard.length
              ? filesFromClipboard
              : imageItem?.getAsFile()
                ? [imageItem.getAsFile() as File]
                : [];
            if (targets.length === 0) return; // 文本粘贴走默认行为
            e.preventDefault();
            void (async () => {
              const added: string[] = [];
              for (const file of targets) {
                // 空文件（某些来源会给出 0 字节的占位）直接跳过，避免落一堆空壳
                if (file.size === 0) continue;
                const dataUrl = await new Promise<string>((resolve, reject) => {
                  const reader = new FileReader();
                  reader.onload = () => resolve(String(reader.result));
                  reader.onerror = () => reject(new Error(`读取 ${file.name} 失败`));
                  reader.readAsDataURL(file);
                }).catch(error => {
                  reportError(error, '附件');
                  return '';
                });
                if (!dataUrl) continue;
                const result = await piBridge.savePastedFile(file.name || 'pasted.png', dataUrl);
                if (!result.success) {
                  reportError(result.error || '粘贴的文件保存失败', '附件');
                  continue;
                }
                const payload = parseData<{ path?: string }>(result);
                if (payload?.path) added.push(payload.path);
              }
              if (added.length) addAttachments(added, '粘贴内容');
            })();
          }}
          placeholder={
            awaitingUser
              ? '扩展在等你确认：先点上面的「允许 / 拒绝」，再继续输入…'
              : running
                ? runningMode === 'followUp'
                  ? '运行中：回车排队（显示在上方，可插入/编辑/删除），Ctrl+Enter 直接插入…'
                  : '运行中：回车插入一条 steering 消息，Ctrl+Enter 排队…'
                : '给 ZeroA 一个任务…（Enter 发送，Shift+Enter 换行）'
          }
          /* 输入框上方的三块：拖拽提示、附件 chips、斜杠命令面板。
             它们都是"主会话特有"的东西，所以留在 Composer 里、用插槽放进来。 */
          top={(
            <>
              {/* 拖拽提示：有文件被拖到窗口上方时，明确告诉用户"这里能接、松开就行" */}
              {dragging && (
                <div className="mb-2 flex items-center gap-2 rounded-lg border border-dashed border-[color-mix(in_srgb,var(--info)_45%,transparent)] bg-[color-mix(in_srgb,var(--info)_8%,transparent)] px-3 py-2">
                  <Icon name="plus" className="h-3.5 w-3.5 shrink-0 text-info" />
                  <span className="text-[12.5px] text-dim">
                    松开即可把文件添加为附件（发送时把**绝对路径**交给模型，它用 read 去看）
                  </span>
                </div>
              )}

              {/* 排队面板：运行中发送的消息排在这里（follow_up），回合结束后依次生效。
                  原文来自 Pi 的 queue_update 事件；每行提供插入（立即 steer）、
                  编辑（拿回输入框）、删除。steering 队列里的消息也会列出
                  （它们将在下一个检查点插进去），同样可编辑可删。
                  头部可点：收成一行（运行中视线在生成区，面板别一直占着输入区上方）。 */}
              {(queueSteering.length > 0 || queueFollowUp.length > 0) && (
                <div className="mb-2 flex flex-col overflow-hidden rounded-lg border border-line bg-raised">
                  <div className={`flex items-center gap-2 px-2.5 py-1.5 ${queueOpen ? 'border-b border-line' : ''}`}>
                    <button
                      type="button"
                      onClick={() => setQueueOpen(open => !open)}
                      aria-expanded={queueOpen}
                      title={queueOpen ? '收起排队面板' : '展开排队面板'}
                      className="flex min-w-0 flex-1 items-center gap-2 text-left"
                    >
                      <Icon name="chevron" className={`h-3.5 w-3.5 shrink-0 text-mute transition-transform ${queueOpen ? 'rotate-90' : ''}`} />
                      <Icon name="list" className="h-3.5 w-3.5 shrink-0 text-mute" />
                      <span className="text-[11.5px] text-mute">
                        排队 {queueSteering.length + queueFollowUp.length} 条 · 当前回合结束后依次生效
                      </span>
                    </button>
                    <button
                      type="button"
                      onClick={() => void clearQueue()}
                      title="clear_queue：清空全部排队消息"
                      className="shrink-0 text-[11.5px] text-mute transition-colors hover:text-danger"
                    >
                      全部清空
                    </button>
                  </div>
                  {queueOpen && (<>
                  {queueSteering.map((queuedText, index) => (
                    <div key={`steer-${index}`} className="flex items-center gap-1.5 px-2.5 py-1.5">
                      <span className="shrink-0 rounded border border-line px-1 text-[10px] text-warn">插话</span>
                      <span className="min-w-0 flex-1 truncate text-[12.5px] text-dim" title={queuedText}>{queuedText}</span>
                      <span className="shrink-0 text-[10.5px] text-mute">下一个检查点生效</span>
                      <button
                        type="button"
                        aria-label={`编辑排队消息 ${index + 1}`}
                        title="拿回输入框编辑"
                        onClick={() => editQueued('steering', index, queuedText)}
                        className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute transition-colors hover:bg-hover hover:text-ink"
                      >
                        <Icon name="pencil" className="h-3 w-3" />
                      </button>
                      <button
                        type="button"
                        aria-label={`删除排队消息 ${index + 1}`}
                        title="从队列里移除"
                        onClick={() => void removeQueued('steering', index)}
                        className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute transition-colors hover:bg-hover hover:text-danger"
                      >
                        <Icon name="trash" className="h-3 w-3" />
                      </button>
                    </div>
                  ))}
                  {queueFollowUp.map((queuedText, index) => (
                    <div key={`follow-${index}`} className="flex items-center gap-1.5 px-2.5 py-1.5">
                      <span className="shrink-0 rounded border border-line px-1 text-[10px] text-info">排队</span>
                      <span className="min-w-0 flex-1 truncate text-[12.5px] text-dim" title={queuedText}>{queuedText}</span>
                      <button
                        type="button"
                        title="不等回合结束：现在就插进当前回合（steer）"
                        onClick={() => void steerQueued('followUp', index)}
                        className="inline-flex h-6 shrink-0 items-center gap-1 rounded px-1.5 text-[11px] text-mute transition-colors hover:bg-hover hover:text-ink"
                      >
                        <Icon name="send" className="h-3 w-3" /> 插入
                      </button>
                      <button
                        type="button"
                        aria-label={`编辑排队消息 ${index + 1}`}
                        title="拿回输入框编辑"
                        onClick={() => editQueued('followUp', index, queuedText)}
                        className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute transition-colors hover:bg-hover hover:text-ink"
                      >
                        <Icon name="pencil" className="h-3 w-3" />
                      </button>
                      <button
                        type="button"
                        aria-label={`删除排队消息 ${index + 1}`}
                        title="从队列里移除"
                        onClick={() => void removeQueued('followUp', index)}
                        className="grid h-6 w-6 shrink-0 place-items-center rounded text-mute transition-colors hover:bg-hover hover:text-danger"
                      >
                        <Icon name="trash" className="h-3 w-3" />
                      </button>
                    </div>
                  ))}
                  </>)}
                </div>
              )}

              {/* 附件区：附件以 chip 形式排在输入框上方，图片显示缩略图，可单独移除 */}
              {files.length > 0 && (
                <div className="mb-2 flex flex-wrap gap-1.5">
                  {files.map(path => (
                    <span key={path} className="inline-flex items-center gap-1.5 rounded-lg bg-panel3 px-2 py-1 text-[11.5px] text-dim">
                      {isImagePath(path) ? <AttachmentThumb path={path} /> : <Icon name="file" className="h-3.5 w-3.5 text-mute" />}
                      <span className="max-w-[220px] truncate" title={path}>{attachmentName(path)}</span>
                      <button
                        type="button"
                        aria-label={`移除附件 ${path}`}
                        onClick={() => setFiles(previous => previous.filter(item => item !== path))}
                        className="text-mute hover:text-danger"
                      >
                        <Icon name="x" className="h-3 w-3" />
                      </button>
                    </span>
                  ))}
                </div>
              )}

              {/* 斜杠命令面板：文本以 / 开头且还没打空格时列出可用命令。
                  数据来自 Pi 的 get_commands（扩展命令 / 提示模板 / 技能），
                  点一条就把 /名字 插进输入框，发送时 Pi 自己展开。 */}
              {/* 输入了 / 但一条命令都没有：说清原因，别让用户以为"按了没反应"。
                  命令来自扩展命令、提示模板与技能 —— 关掉资源加载时必然是空的。 */}
              {slashQuery !== undefined && commands.length === 0 && (
                <div className="mb-1.5 rounded-lg border border-line bg-raised px-2.5 py-2 text-[11.5px] leading-[18px] text-mute">
                  还没有可用命令。命令来自 ZeroA 的扩展命令、提示模板与技能 ——
                  在「设置 → 通用设置」里确认「加载扩展 / 技能 / 模板」是开着的，然后重启会话。
                </div>
              )}

              {slashMatches.length > 0 && (
                <div className="mb-1.5 flex flex-col overflow-hidden rounded-lg border border-line bg-raised">
                  {slashMatches.map(item => (
                    <button
                      key={`${item.source ?? ''}:${item.name}`}
                      type="button"
                      onClick={() => insertCommand(item.name)}
                      className="flex items-center gap-2 px-2.5 py-1.5 text-left text-[12.5px] text-dim hover:bg-hover hover:text-ink"
                    >
                      <span className="font-mono text-ink">/{item.name}</span>
                      {item.source && <span className="rounded border border-line px-1 text-[10px] text-mute">{item.source}</span>}
                      {item.description && <span className="min-w-0 flex-1 truncate text-mute">{item.description}</span>}
                    </button>
                  ))}
                </div>
              )}
            </>
          )}
          /* 控制行左侧：加号（选文件/粘贴图片）、角色徽标、队列与重试动作 */
          leading={(
            <>
              <button
                type="button"
                aria-label="添加附件"
                title="添加附件（也可以直接把图片粘贴到输入框里）"
                disabled={busy}
                onClick={() => void pickFiles()}
                className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-raised text-ink transition-colors hover:bg-hover disabled:cursor-default disabled:opacity-50"
              >
                <Icon name="plus" className="h-3.5 w-3.5" />
              </button>

              {/* 角色徽标：和附件按钮同一行（照 DSH 的 tools 行，左边放与本次发送
                  相关的上下文动作）。角色决定这一轮的提示词与工具集，属于
                  "发送前要确认的上下文"，所以贴着输入框而不是埋进设置里。 */}
              <RoleBadge />

              {/* 重试等待中：给一个明确的出口（abort 停当前回合，abort_retry 停"还要再试"）。
                  「停止当前回合」已经并进右下角那个主按钮里了，这里只留重试专用的动作。 */}
              {running && status === 'retrying' && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    void piBridge.abortRetry().then(result => {
                      if (!result.success) reportError(result.error || '停止重试失败', '重试');
                      else useUI.getState().showNotice('已停止重试');
                    });
                  }}
                  title="abort_retry：取消等待，不再重试"
                >
                  停止重试
                </Button>
              )}
            </>
          )}
          /* 控制行右侧：模型 + 推理强度合成一个按钮（照 DSH）：点开分成"模型"与"强度"两节。
              徽标永远显示（懒启动后进程要到第一条消息才建，模型名不能等）；
              点开切换只要有启动参数模板就行（connected）：有进程走 set_model，
              没进程由后端改写启动模板（第一条消息懒启动时生效），两条路对用户是一回事。 */
          trailing={connected ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  aria-label="模型与推理强度"
                  className="inline-flex h-7 max-w-[260px] items-center gap-1 rounded-lg px-2 text-[12px] text-dim transition-colors hover:bg-panel3 hover:text-ink"
                >
                  <span className="min-w-0 truncate">{currentLabel}</span>
                  {thinkingLevel && <span className="shrink-0 text-mute">· {thinkingLevel}</span>}
                  <Icon name="chevron" className="h-3.5 w-3.5 shrink-0 text-mute" />
                </button>
              </DropdownMenuTrigger>
              {/* 两行 + 子菜单（照 DSH）：一行是模型、一行是推理等级，
                  各自右边显示当前值，悬停展开对应的列表 */}
              <DropdownMenuContent align="end" className="w-[300px] p-1.5">
                <DropdownMenuSub>
                  <DropdownMenuSubTrigger>
                    <span>模型</span>
                    <span className="flex-1" />
                    <span className="max-w-[170px] truncate text-mute">{currentLabel}</span>
                  </DropdownMenuSubTrigger>
                  <DropdownMenuSubContent className="max-h-[340px] overflow-y-auto">
                    {/* 无进程时清单来自磁盘缓存：标出来，滞后时知道为什么 */}
                    {fromCache && models.length > 0 && (
                      <div className="px-2 pb-1 pt-0.5 text-[10.5px] text-mute">
                        清单来自缓存 · 发出第一条消息后刷新
                      </div>
                    )}
                    {Object.entries(groups).map(([groupProvider, items]) => (
                      <div key={groupProvider}>
                        <div className="px-2 py-1 text-[10.5px] text-mute">{groupProvider}</div>
                        {items.map(item => (
                          <DropdownMenuItem
                            key={`${item.provider}/${item.id}`}
                            onSelect={() => {
                            // 与下面推理等级同一口径：选择即记住为启动参数
                            // （懒启动的新会话与下次启动都按它），再应用到当前。
                            useRuntime.getState().setStartModel(`${item.provider}/${item.id}`);
                            void setModel(item.provider, item.id);
                          }}
                            className={cn('justify-between', item.provider === provider && item.id === model && 'bg-panel3 text-ink')}
                          >
                            <span className="min-w-0 flex-1 truncate">{item.name || item.id}</span>
                            {item.reasoning && <span className="text-[11px] text-mute">推理</span>}
                          </DropdownMenuItem>
                        ))}
                      </div>
                    ))}
                  </DropdownMenuSubContent>
                </DropdownMenuSub>

                <DropdownMenuSub>
                  <DropdownMenuSubTrigger>
                    <span>推理等级</span>
                    <span className="flex-1" />
                    <span className="text-mute">{thinkingLevel || '默认'}</span>
                  </DropdownMenuSubTrigger>
                  <DropdownMenuSubContent>
                    {thinkingOptions.map(level => (
                      <DropdownMenuItem
                        key={level}
                        onSelect={() => {
                          useRuntime.getState().setStartThinking(level);
                          void setThinkingLevel(level);
                        }}
                        className={cn(level === thinkingLevel && 'bg-panel3 text-ink')}
                      >
                        {level}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuSubContent>
                </DropdownMenuSub>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : currentLabel ? (
            <span
              className="inline-flex h-7 max-w-[260px] items-center gap-1 rounded-lg px-2 text-[12px] text-mute"
              title="发送第一条消息后可切换模型与推理等级"
            >
              <span className="min-w-0 truncate">{currentLabel}</span>
              {thinkingLevel && <span className="shrink-0">· {thinkingLevel}</span>}
            </span>
          ) : null}
          /* 主按钮（照 DSH 的"一个座位"）：空闲=发送；运行中空稿=停止；
              运行中有内容=按 busy-Enter 设置排队或插入。
              禁用时不显示 tooltip（DSH 同理：禁用的按钮给提示只会吵）。 */
          primary={{
            label: primary.label,
            title: primary.title,
            icon: primary.icon,
            variant: primary.variant,
            disabled: primary.disabled,
            onClick: primary.run,
          }}
        />

        {/* 卡片下方的状态行（照 DSH）：上下文占用 · 轮次 · 上一轮速度 */}
        <div className="mt-1.5 flex items-center gap-2.5 px-2 text-[11.5px] text-mute">
          <ContextMeter />
          <span>{turns} 轮</span>
          {useRuntime.getState().debug && <span className="font-mono text-warn">{`cmds=${commands.length} q=${slashQuery ?? '-'} hit=${slashMatches.length}`}</span>}
          {tokPerSec > 0 && <span>{tokPerSec.toFixed(1)} tok/s</span>}
          <span className="flex-1" />
          {/* 当前角色放在状态行：它会随切换变化，用户需要一直看得见 */}
          {activeRole && (
            <span>{activeRole.name}</span>
          )}
          <span>
            {workspaceSwitching
              ? '正在切换工作区…'
              : switching
                ? '正在切换会话…'
                : running
                  ? `运行中${elapsed ? ` ${elapsed}` : ''} · 回车${runningMode === 'followUp' ? '排队' : '插入'} · 连按两次 Esc 停止`
                  : sending
                    ? 'ZeroA 正在思考…'
                    /* 待创建/已连接的常态不解释机制（"还没起进程"之类的话用户不需要），
                       只说怎么发；真没连接时才提示发送即启动。 */
                    : connected
                      ? `Enter 发送 · Shift+Enter 换行${lastDuration ? ` · 上次 ${lastDuration < 1 ? `${Math.round(lastDuration * 1000)}ms` : `${lastDuration.toFixed(1)}s`}` : ''}`
                      : '发送第一条消息时启动 ZeroA'}
          </span>
        </div>
      </div>
    </div>
  );
}
