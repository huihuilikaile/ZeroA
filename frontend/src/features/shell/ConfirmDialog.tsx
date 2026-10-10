import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Icon } from '@/components/ui/Icon';
import { useConfirm } from '@/stores/confirm';

/**
 * 统一的确认弹窗（见 stores/confirm.ts）。
 *
 * 删除类操作必须走这里：按钮上写清楚"删什么"，而不是一个含糊的"确定"。
 * 默认焦点放在取消上（data-modal-autofocus 的位置留给取消），
 * 免得连按回车就把东西删了。
 */
export function ConfirmDialog() {
  const request = useConfirm(s => s.request);
  if (!request) return null;

  const close = () => useConfirm.getState().close();
  /** 取消/关闭：先取回调再收弹窗（收完 request 就没了，回调取不到） */
  const cancel = () => {
    const action = request.onCancel;
    close();
    void action?.();
  };

  return (
    <Dialog open onOpenChange={open => { if (!open) cancel(); }}>
      <DialogContent className="w-[min(440px,92vw)]">
        <DialogHeader>
          <Icon name={request.danger ? 'warn' : 'list'} className={request.danger ? 'h-4 w-4 text-danger' : 'h-4 w-4'} />
          <DialogTitle>{request.title}</DialogTitle>
        </DialogHeader>

        <div className="px-4 py-3.5">
          <DialogDescription>{request.description ?? '这个操作不可撤销。'}</DialogDescription>
        </div>

        <DialogFooter>
          {!request.alertOnly && <Button variant="outline" onClick={cancel} autoFocus>取消</Button>}
          <Button
            variant={request.danger ? 'destructive' : 'default'}
            autoFocus={request.alertOnly}
            onClick={() => {
              const action = request.onConfirm;
              close();
              void action?.();
            }}
          >
            {request.confirmLabel ?? (request.alertOnly ? '知道了' : '确定')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
