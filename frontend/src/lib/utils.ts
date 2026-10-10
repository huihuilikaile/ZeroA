import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

/**
 * shadcn/ui 约定的 `cn`：clsx 负责条件拼接，tailwind-merge 负责让后写的类覆盖冲突项。
 * 放在 lib/utils.ts 是为了让 `npx shadcn@latest add <component>` 生成的代码能直接 import。
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
