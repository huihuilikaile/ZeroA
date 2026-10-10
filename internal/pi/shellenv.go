package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// 本文件解决一个实测踩到的问题：**模型实际用的 shell 和配置里写的不是同一个**。
//
// 现象（在本机复现过）：
//   · settings.json 写着 "shellPath": "E:\\Git\\bin\\bash.exe"；
//   · 但模型执行 bash 时跑在 C:\Windows\System32\bash.exe（WSL 启动器）里：
//     uname -s 返回 Linux，python 不存在（只有 python3）；
//   · 把 shellPath 改成不存在的路径后 Pi 既不报错也不改变行为
//     ——说明这个值在 RPC 模式下没有被消费。
//
// 后果不只是"shell 不对"：环境清单告诉模型"本机有 python"，而它实际执行的地方
// 没有 python，于是模型照着清单写命令、一条条失败（用户看到的是"模型很笨"）。
//
// 这里的做法是在启动 Pi 之前，把**配置里那个 shell 所在目录前置到 PATH**：
// Pi 若读了 shellPath，一切照旧；若没读，它回退到"在 PATH 里找 bash"，
// 也会先找到配置的那个而不是 WSL。两条路都覆盖，不依赖对 Pi 内部行为的假设。

// ShellDirFor 返回应当前置到 PATH 的目录（找不到返回空串）。
//
// 导出给界面层用：环境清单需要如实告诉模型「bash 工具跑在哪个 shell 里」，
// 否则清单（Go 在 Windows PATH 上检测）与执行环境（Pi 的 bash）可能各说各话。
func ShellDirFor(agentDir string) string { return shellDirFor(agentDir) }

// shellDirFor 返回应当前置到 PATH 的目录（找不到返回空串）。
//
// 顺序：
//  1. 配置目录 settings.json 里的 shellPath（用户/界面明确选择的那一个）；
//  2. 常见 Git Bash 安装位置（只在文件真的存在时才用）。
func shellDirFor(agentDir string) string {
	if path := configuredShellPath(agentDir); path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return filepath.Dir(path)
		}
	}
	for _, candidate := range fallbackBashPaths() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return filepath.Dir(candidate)
		}
	}
	return ""
}

// configuredShellPath 读 <agentDir>/settings.json 的 shellPath。
//
// 只读这一个键，不引整个设置体系：这里的职责是"给 PATH 挑一个目录"，
// 读失败（文件不存在/不是 JSON）就当没配——启动 Pi 不能因为设置文件坏了而失败。
func configuredShellPath(agentDir string) string {
	trimmed := strings.TrimSpace(agentDir)
	if trimmed == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(trimmed, "settings.json"))
	if err != nil {
		return ""
	}
	var parsed struct {
		ShellPath string `json:"shellPath"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.ShellPath)
}

// fallbackBashPaths 是"没有配置时值得一试"的 Git Bash 位置。
//
// 不猜 System32\bash.exe：那正是我们要避开的 WSL 启动器。
func fallbackBashPaths() []string {
	var paths []string
	if programFiles := strings.TrimSpace(os.Getenv("ProgramFiles")); programFiles != "" {
		paths = append(paths, filepath.Join(programFiles, "Git", "bin", "bash.exe"))
	}
	if programFilesX86 := strings.TrimSpace(os.Getenv("ProgramFiles(x86)")); programFilesX86 != "" {
		paths = append(paths, filepath.Join(programFilesX86, "Git", "bin", "bash.exe"))
	}
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		paths = append(paths, filepath.Join(localAppData, "Programs", "Git", "bin", "bash.exe"))
	}
	return paths
}

// prependPath 把 dir 放到环境块里 PATH 的最前面（Windows 上环境变量名不区分大小写）。
//
// env 是 "KEY=VALUE" 的切片（os.Environ() 的形状）。PATH 不存在时补一条，
// 这样即使调用方给了一个精简过的环境块，结果也是对的。
func prependPath(env []string, dir string) []string {
	const key = "PATH="
	replaced := false
	next := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if len(entry) >= len(key) && strings.EqualFold(entry[:len(key)], key) {
			if !replaced {
				next = append(next, key+dir+string(os.PathListSeparator)+entry[len(key):])
				replaced = true
			}
			// 重复的 PATH 只保留第一条（Windows 上重复键的取值不确定）
			continue
		}
		next = append(next, entry)
	}
	if !replaced {
		next = append(next, key+dir)
	}
	return next
}

/**
 * BundledToolDirs 返回随应用分发的工具目录（按 PATH 优先级排序）。
 *
 * 为什么需要：分发形态是"用户不用装 Python 和工具"，那些东西放在
 * <exe目录>/tools/ 下。但**放在那儿不等于能用**——模型敲 `python` 或 `ffuf` 时，
 * 解析靠的是 PATH。所以启动 Pi 前要把这几个目录前置进去，
 * 让"清单里说可用"和"模型真能敲到"是同一件事。
 *
 * 顺序：python 在前（`python` 是最常用的），然后命令行工具，最后 node。
 * 目录不存在就跳过——开发机（bin/ 直接跑）没有 tools/ 也不会受影响。
 */
func BundledToolDirs() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	dir := filepath.Dir(exe)
	var out []string
	for _, name := range []string{"python", "bin", "node"} {
		candidate := filepath.Join(dir, "tools", name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			out = append(out, candidate)
		}
	}
	return out
}
