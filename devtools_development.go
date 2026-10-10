//go:build !production

package main

// 开发构建保留 DevTools，便于检查 WebView；正式构建由 production tag 关闭。
const devToolsEnabled = true
