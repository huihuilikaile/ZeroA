module zeroa

go 1.25.0

require github.com/wailsapp/wails/v3 v3.0.0-beta.17

// golang.org/x/sys 从 indirect 提升为直接依赖：internal/license 用它读注册表
// （MachineGuid）与卷序列号来生成机器码。版本与原先一致，go.sum 无需改动。
require golang.org/x/sys v0.46.0

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
)
