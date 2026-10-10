import { useState } from 'react';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { reportError } from '@/lib/reportError';
import { useUI } from '@/stores/ui';

interface BashResult { output?: string; exitCode?: number; cancelled?: boolean; truncated?: boolean }

/**
 * 运行命令对话框（Pi 的 bash / abort_bash）。
 *
 * 与"模型回合里调用 bash 工具"不同：这是**用户主动**跑一条命令，
 * 命令与输出会进会话上下文（所以模型下一轮能看到），适合看环境、跑测试。
 *
 * 刻意不做成终端模拟器：这里只需要"跑一条、看结果、能中止"。
 * 输出是纯文本，长输出由容器滚动。
 */
export function BashDialog() {
  const open = useUI(s => s.bashOpen);
  const [command, setCommand] = useState('');
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<BashResult | null>(null);
  const [elapsed, setElapsed] = useState(0);

  async function run() {
    const trimmed = command.trim();
    if (!trimmed || running) return;
    setRunning(true);
    setResult(null);
    const startedAt = Date.now();
    try {
      const response = await piBridge.bash(trimmed);
      if (!response.success) {
        reportError(response.error || '命令执行失败', '运行命令');
        return;
      }
      setResult(parseData<BashResult>(response));
    } catch (error) {
      reportError(error, '运行命令');
    } finally {
      setElapsed(Date.now() - startedAt);
      setRunning(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={value => useUI.getState().setBashOpen(value)}>
      <DialogContent className="w-[min(720px,94vw)]">
        <DialogHeader>
          <DialogTitle>运行命令</DialogTitle>
          <span className="flex-1" />
          <span className="text-[12px] text-mute">命令与输出会进会话上下文</span>
        </DialogHeader>

        <div className="flex flex-col gap-2 px-4 py-1">
          <div className="flex items-center gap-2">
            <input
              value={command}
              autoFocus
              placeholder="例如 go test ./..."
              onChange={e => setCommand(e.target.value)}
              onKeyDown={e => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return;
                if (e.key === 'Enter') {
                  e.preventDefault();
                  void run();
                }
              }}
              className="h-8 min-w-0 flex-1 rounded-lg bg-panel2 px-2.5 font-mono text-[12.5px] outline-none placeholder:text-mute"
            />
            <Button size="sm" variant="default" disabled={running || !command.trim()} onClick={() => void run()}>
              {running ? '执行中…' : '运行'}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={!running}
              onClick={() => {
                void piBridge.abortBash().then(response => {
                  if (!response.success) reportError(response.error || '中止失败', '运行命令');
                });
              }}
            >
              中止
            </Button>
          </div>

          <div data-selectable className="min-h-[140px] rounded-lg bg-panel2 px-2.5 py-2">
            {!result && !running && <p className="text-[12px] text-mute">还没有运行任何命令</p>}
            {running && <p className="text-[12px] text-mute">执行中…</p>}
            {result && (
              <>
                <div className="mb-1 flex items-center gap-2 text-[11.5px] text-mute">
                  <span>退出码 {result.exitCode ?? 0}</span>
                  <span>· {(elapsed / 1000).toFixed(2)}s</span>
                  {result.cancelled && <span className="text-warn">· 已中止</span>}
                  {result.truncated && <span className="text-warn">· 输出已截断</span>}
                </div>
                <pre className="max-h-[46vh] overflow-auto font-mono text-[12px] leading-[18px] text-dim">{result.output ?? ''}</pre>
              </>
            )}
          </div>
        </div>

        <div className="flex items-center gap-2 px-4 pb-3 pt-1">
          <span className="min-w-0 flex-1 truncate text-[11px] text-mute">
            ZeroA 的 bash：直接执行并写入会话（等价于你在终端里跑）
          </span>
          <Button size="sm" variant="ghost" onClick={() => useUI.getState().setBashOpen(false)}>关闭</Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
