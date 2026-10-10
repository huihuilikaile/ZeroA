import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath, URL } from 'node:url';

// Wails 直接把 frontend/dist 嵌进二进制（//go:embed），所以输出目录保持默认的 dist。
// 别名 @ → src，避免深层相对路径；Tailwind v4 通过官方 Vite 插件接入。
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // 桌面 WebView 只需要现代内核，不必兼容老浏览器
    target: 'es2022',
    sourcemap: false,
  },
});
