// Package sessionstore 是会话存储域：会话目录解析、会话文件扫描与解析、
// 列表索引缓存、历史读取（含分支还原）。
//
// Pi 的会话就是磁盘上只追加的 JSONL（pi 的 docs/session-format.md），
// 这个包把"读懂它"这件事收在一处：不依赖 Wails、不依赖 PiService，
// 唯一的外部注入是诊断日志（应用层装配，见 main 的 sessions_alias.go）。
package sessionstore

// host 是应用层注入的能力。只有日志：目录解析统一走 internal/pi
// （桌面端与命令行入口必须算出同一个目录），不需要再注入。
var host struct {
	Logf func(format string, args ...any)
}

func init() {
	host.Logf = func(string, ...any) {}
}

// Configure 由应用层（main）在启动时调用；空值保留默认（丢弃日志）。
func Configure(logf func(format string, args ...any)) {
	if logf != nil {
		host.Logf = logf
	}
}
