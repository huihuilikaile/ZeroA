import * as React from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { cn } from '@/lib/utils';

/**
 * shadcn/ui 的 Button，主题映射到 DSH 令牌：
 *   primary → label-primary 底 + label-primary-foreground 字（亮色=墨黑，暗色=白）
 * 其余变体只用 bg-layer / border-l 系列，保证两套主题自动成立。
 */
const buttonVariants = cva(
  'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-lg transition-colors disabled:pointer-events-none disabled:opacity-40 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        default: 'bg-accent text-accentink hover:opacity-90',
        secondary: 'bg-panel2 text-ink hover:bg-panel3',
        outline: 'border border-line bg-transparent text-ink hover:bg-panel2',
        ghost: 'text-dim hover:bg-panel2 hover:text-ink',
        destructive: 'bg-danger/10 text-danger hover:bg-danger/15',
        link: 'text-info underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-8 px-3 text-[13px]',
        sm: 'h-7 px-2.5 text-[12px]',
        lg: 'h-9 px-4 text-[13px]',
        icon: 'h-8 w-8',
        'icon-sm': 'h-7 w-7',
      },
    },
    defaultVariants: { variant: 'secondary', size: 'default' },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

export function Button({ className, variant, size, asChild = false, ...props }: ButtonProps) {
  const Comp = asChild ? Slot : 'button';
  return <Comp className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}

export { buttonVariants };
