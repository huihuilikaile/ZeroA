// Package bridge 是桌面壳与 Pi 之间的服务层：Wails 绑定唯一注册的 PiService 住在这里。
//
// 为什么单独成包（2026-10 从仓库根迁入）：
// 根目录的 package main 曾同时装着"应用入口"与"全部前端可见方法"，
// 95 个文件平铺、新增功能只能继续往根上堆。迁入本包之后：
//   - 仓库根只剩 main.go 与几个平台文件（窗口/消息框/WebView 配置）；
//   - 业务逻辑仍在 internal/ 各域包，本包只做"绑定方法的形状 + 共享运行状态"；
//   - main.go 通过导出字段注入回调（OnEvent / Quit / OnSessionSwitch 等），
//     反向依赖（bridge 不知道 Wails App 的存在）保持不变。
//
// 注意：PiService 的方法是前端契约——改名必须重新生成绑定
// （wails3 task common:generate:bindings），bindings_test.go 守着这条。
package bridge
