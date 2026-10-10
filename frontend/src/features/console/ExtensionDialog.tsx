import { useEffect, useState } from 'react';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Icon } from '@/components/ui/Icon';
import { useAgent } from '@/hooks/useAgent';
import { useUI } from '@/stores/ui';

/**
 * 扩展 UI 弹窗（extension_ui_request → extension_ui_response）。
 * 这是 Pi 的权限确认通道：扩展要做事之前先问一句，界面必须给出回应，否则对面会一直等。
 * 所以「关掉弹窗」等同于拒绝，而不是什么都不做。
 */
export function ExtensionDialog() {
  const dialog = useUI(s => s.dialog);
  const { respondExtension } = useAgent();
  const [value, setValue] = useState('');

  useEffect(() => setValue(''), [dialog?.requestId]);

  if (!dialog) return null;

  const { requestId, method, title, message, options, placeholder, agentId } = dialog;

  const deny = () => void respondExtension(requestId, { confirmed: false }, agentId);

  return (
    <Dialog open onOpenChange={open => { if (!open) deny(); }}>
      <DialogContent>
        <DialogHeader>
          <Icon name="warn" className="h-4 w-4 text-warn" />
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>

        <div className="flex flex-col gap-3 px-4 py-3.5">
          <DialogDescription>{message}</DialogDescription>
          <p className="font-mono text-[11px] text-mute">method: {method} · id: {requestId}</p>

          {(method === 'input' || method === 'editor') && (
            <textarea
              value={value}
              onChange={e => setValue(e.target.value)}
              placeholder={placeholder ?? '输入内容…'}
              rows={method === 'editor' ? 6 : 2}
              className="w-full resize-y rounded-lg bg-panel2 px-2.5 py-2 font-mono text-[12.5px] leading-[20px] outline-none placeholder:text-mute"
            />
          )}

          {method === 'select' && options?.length ? (
            <div className="flex flex-col gap-1.5">
              {options.map(option => (
                <Button key={option} variant="outline" className="justify-start" onClick={() => void respondExtension(requestId, { value: option, confirmed: true }, agentId)}>
                  {option}
                </Button>
              ))}
            </div>
          ) : null}
        </div>

        <DialogFooter>
          {method === 'confirm' ? (
            <>
              <Button variant="destructive" onClick={deny}>拒绝</Button>
              <Button variant="outline" onClick={() => void respondExtension(requestId, { confirmed: true, always: true }, agentId)}>始终允许</Button>
              <Button variant="default" onClick={() => void respondExtension(requestId, { confirmed: true }, agentId)}>允许一次</Button>
            </>
          ) : (
            <>
              <Button variant="destructive" onClick={deny}>取消</Button>
              <Button
                variant="default"
                disabled={method !== 'select' && !value.trim()}
                onClick={() => void respondExtension(requestId, method === 'select' ? { confirmed: true } : { value, confirmed: true }, agentId)}
              >
                提交
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
