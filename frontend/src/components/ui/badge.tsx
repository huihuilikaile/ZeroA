import * as React from 'react';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-full border px-2 py-[1px] text-[11px] leading-5 whitespace-nowrap',
  {
    variants: {
      variant: {
        default: 'border-line text-dim',
        info: 'border-transparent bg-info/10 text-info',
        success: 'border-transparent bg-success/10 text-success',
        danger: 'border-transparent bg-danger/10 text-danger',
        warning: 'border-transparent bg-warn/10 text-warn',
      },
    },
    defaultVariants: { variant: 'default' },
  },
);

export function Badge({
  className,
  variant,
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & VariantProps<typeof badgeVariants>) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}
