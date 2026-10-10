package bridge

import "zeroa/internal/sessionstore"

/**
 * 会话存储域（目录解析、文件扫描、索引缓存、历史读取）已经迁入
 * internal/sessionstore。本文件只留别名：主包内既有调用点与全部测试
 * 继续用原来的名字，行为一行不变。
 *
 * 与 roles_alias.go 同一个套路：实现搬走、名字留下，等调用点自然演化，
 * 不在一次重构里同时改"代码在哪"和"代码怎么写"。
 */
type (
	sessionInfo   = sessionstore.Info
	sessionHeader = sessionstore.Header
	sessionEntry  = sessionstore.Entry
)

var (
	// 目录解析
	sessionsRoot      = sessionstore.SessionsRoot
	exeDir            = sessionstore.ExeDir
	resolveCwd        = sessionstore.ResolveCwd
	workspaceFallback = sessionstore.WorkspaceFallback
	normalizeDir      = sessionstore.NormalizeDir

	// 扫描与索引
	scanAllSessions        = sessionstore.ScanAll
	scanWorkspaceSessions  = sessionstore.ScanWorkspace
	scanAllSessionsCached  = sessionstore.ScanAllCached
	invalidateSessionIndex = sessionstore.InvalidateIndex
	scanSessionFile        = sessionstore.ScanFile

	// 头部与历史
	sessionCwd         = sessionstore.Cwd
	sessionHeaderCwd   = sessionstore.HeaderCwd
	readSessionHeader  = sessionstore.ReadHeader
	readSessionEntries = sessionstore.ReadEntries
	sessionMessages    = sessionstore.Messages
	newestSessionFor   = sessionstore.NewestFor
)

func init() {
	// 会话存储域的诊断日志并进 gui-log.txt（与会话恢复链路同一处排查）。
	sessionstore.Configure(Logf)
}
