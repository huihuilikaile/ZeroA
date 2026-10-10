//go:build production

package main

// 发布包不暴露 WebView DevTools；需要调试时使用 wails3 dev 的非 production 构建。
const devToolsEnabled = false
