import * as React from 'react';
import * as SwitchPrimitive from '@radix-ui/react-switch';
import { cn } from '@/lib/utils';

/** shadcn/ui Switch（Radix）：role=switch、键盘空格切换、aria-checked 全由 Radix 保证 */
function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        'peer inline-flex h-[19px] w-[34px] shrink-0 items-center rounded-full transition-colors outline-none',
        'bg-panel3 data-[state=checked]:bg-accent disabled:opacity-40',
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          'pointer-events-none block h-[13px] w-[13px] translate-x-[3px] rounded-full bg-caption transition-transform',
          'data-[state=checked]:translate-x-[18px] data-[state=checked]:bg-accentink',
        )}
      />
    </SwitchPrimitive.Root>
  );
}

export { Switch };
