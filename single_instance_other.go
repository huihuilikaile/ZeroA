//go:build !windows

package main

// 非 Windows 平台暂时没有"WebView2 用户数据目录被占用 → 无声挂死"这个故障，
// 单实例拦截只在 Windows 实现（见 single_instance_windows.go）。
func ensureSingleInstance() bool { return true }
