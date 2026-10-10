import { useEffect, useState } from 'react';

/**
 * 窗口逻辑宽度（随窗口大小变化而更新）。
 *
 * 用途：拖拽分隔条时给"另一侧"留出最小宽度。不这么做的话，把文件面板往左拉，
 * 对话区会被一路挤到 0 —— 文件面板的上限必须由"窗口宽度 - 对话区下限"决定，
 * 而不是一个写死的数字。
 */
export function useWindowWidth(): number {
  const [width, setWidth] = useState(() => (typeof window === 'undefined' ? 0 : window.innerWidth));
  useEffect(() => {
    const onResize = () => setWidth(window.innerWidth);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  return width;
}
