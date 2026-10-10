//go:build !windows

package pi

import "os/exec"

// 非 Windows 平台不需要做任何事，但函数必须存在：
// 这样 command.go 可以无条件调用 configureProcess，平台差异全部收进这两个小文件。
// 这是 Go 里最常见的跨平台写法之一。
func configureProcess(cmd *exec.Cmd) {}
