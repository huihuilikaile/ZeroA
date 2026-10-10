package sessionstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"zeroa/internal/pi"
)

// 本文件负责「目录解析」：会话根目录、工作目录、exe 目录的取值规则。
//
// 配置目录的解析统一在 internal/pi（桌面端与命令行入口必须算出同一个目录）。

// ProjectPiDir 返回「项目级配置目录」：工作目录下的 .pi。
//
// 注意它和用户级的区别（文档明确写了）：项目级配置要等"项目信任"之后才加载，
// 唯一的例外是 sessionDir —— Pi 在读信任之前就会看它，否则找不到会话。
// 我们的会话列表因此也要同时看这两处。
func ProjectPiDir(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	return filepath.Join(ResolveCwd(cwd), ".pi")
}

// SessionsRoot 返回 Pi 存放会话的根目录。
//
// 与 Pi 自己的决定顺序保持一致（见 1.0.2 的 docs/settings.md、docs/environment-variables.md）：
//  1. 环境变量 PI_CODING_AGENT_SESSION_DIR（被 --session-dir 覆盖，但我们不传那个参数）
//  2. 项目级 .pi/settings.json 的 sessionDir（文档指明它在信任之前就会被读）
//  3. 用户级 <agent-dir>/settings.json 的 sessionDir
//  4. 默认 <agent-dir>/sessions
func SessionsRoot() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); dir != "" {
		return dir
	}
	dir := pi.AgentDir()
	if dir == "" {
		return ""
	}
	if configured := sessionDirFrom(filepath.Join(dir, "settings.json")); configured != "" {
		return configured
	}
	return filepath.Join(dir, "sessions")
}

// sessionDirFrom 读一个 settings.json 里的 sessionDir（没有就返回空）。
func sessionDirFrom(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var settings struct {
		SessionDir string `json:"sessionDir"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return ""
	}
	return strings.TrimSpace(settings.SessionDir)
}

// ExeDir 返回可执行文件所在目录：工作目录的默认值就是它。
// 这样双击 exe 启动时，Pi 默认在程序所在目录里干活，而不是某个系统目录。
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// ResolveCwd 统一「工作目录」的取值：空 → exe 同目录 → 若还取不到就用进程当前目录。
//
// 一个例外：exe 放在 bin/dist/out/build 这类**构建输出目录**里时，取它的上一级。
// 否则默认工作区会变成 "bin"，Pi 就会在一堆编译产物里读写文件——
// 这不是用户说的「exe 同目录」想要的效果，而是构建布局的副作用。
func ResolveCwd(cwd string) string {
	if trimmed := strings.TrimSpace(cwd); trimmed != "" {
		if absolute, err := filepath.Abs(trimmed); err == nil {
			return absolute
		}
		return trimmed
	}
	if dir := ExeDir(); dir != "" {
		if parent := WorkspaceFallback(dir); parent != "" {
			return parent
		}
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// WorkspaceFallback 在 exe 位于构建输出目录时返回其父目录，否则返回空串。
func WorkspaceFallback(dir string) string {
	switch strings.ToLower(filepath.Base(dir)) {
	case "bin", "dist", "out", "build", "release", "target":
		parent := filepath.Dir(dir)
		if info, err := os.Stat(parent); err == nil && info.IsDir() {
			return parent
		}
	}
	return ""
}

// NormalizeDir 把两个路径规整到可比较的形式。
// Windows 上大小写与分隔符都不敏感，不统一就会把同一个目录认成两个。
func NormalizeDir(path string) string {
	/**
	 * 空输入必须原样返回空——**先判空，再 Clean**。
	 *
	 * 这里踩过一个很难查的坑：`filepath.Clean("")` 返回的是 `"."` 而不是空串，
	 * 所以"cleaned == \"\"" 这个判断永远不会命中，空输入会一路走到 filepath.Abs，
	 * 变成**进程的当前工作目录**。后果是：
	 *   · Cwd 读不到会话文件时返回的不是空，而是 app 的工作目录；
	 *   · 于是"删除工作区"的守卫把"要删的目录"和"app 的 cwd"比成相等——
	 *     一个会话都没有的工作区被判定成"还有会话被 Pi 打开着"（用户实测报的）。
	 * 这个函数是全应用的工作区键规范化入口，任何空值被悄悄变成 cwd 都会波及
	 * 记忆、边界检查与列表去重，所以修在这里而不是在调用点补判空。
	 */
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "" || cleaned == "." {
		return ""
	}
	cleaned = strings.ReplaceAll(cleaned, "/", string(filepath.Separator))
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	return strings.ToLower(filepath.ToSlash(cleaned))
}
