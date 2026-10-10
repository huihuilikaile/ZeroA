package bridge

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 从会话里自动加载"模型当前能调用的工具"。
//
// 为什么这么做（而不是写死一份清单）：
// Pi 的工具集是**运行期**决定的——内置工具、已装扩展注册的工具、MCP 服务器工具
// 三者合起来才是真清单，其中后两者随"装了什么"变化，代码里永远枚举不完。
// 而 Pi 自己会把结果写进会话首条 system 消息的 `sections.tools`：
//
//	<tools>
//	- bash: Execute bash commands (ls, grep, find, etc.)
//	- mcp__ida: MCP namespace proxy for ida
//	- web_search: Use for web research questions...
//	</tools>
//
// 所以直接解析它：装一个新扩展、加一个 MCP 服务器，重启会话就自动出现，
// 不需要改任何代码，也不会出现"清单里没有、实际能用"的错位。

// piToolInfo 是 / 会话里的一条工具。
type piToolInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	// Kind 只做界面分组：builtin / extension / mcp
	Kind string `json:"kind"`
	// Provider 是扩展包名（能认出来时）
	Provider string `json:"provider,omitempty"`
	// Active 表示"本次会话真的启用了"（false = 注册了但没启用）
	Active bool `json:"active,omitempty"`
}

type sessionToolsCache struct {
	mu       sync.Mutex
	path     string
	modTime  time.Time
	tools    []piToolInfo
	loadedAt time.Time
}

var sessionToolsMu sync.Mutex
var sessionToolsCached = sessionToolsCache{}

// sessionTools 返回"最近一次会话里 Pi 暴露的工具清单"。
//
// 优先用当前会话文件（最准），没有就读最近修改过的那条会话——这样即使还没开始
// 新会话，界面也能显示上次真实生效的工具集。返回的第二值是来源路径（空 = 没找到）。
func (s *PiService) sessionTools() ([]piToolInfo, string) {
	/**
	 * 首选：扩展上报的 pi-tools.json。
	 *
	 * 为什么它是首选（实测教训）：会话系统提示里的 "Available tools" 只是**精选片段**，
	 * 连默认启用的 read/edit/write 都不在里面——按它显示"模型可调用的工具"会又缺又错。
	 * 真正权威的是 Pi 进程里的 getActiveTools()，只有扩展拿得到，所以由我们的扩展
	 * 在 session_start 时落一份文件（见 extensions/tool-catalog.ts）。
	 */
	if reported, path := reportedPiTools(); len(reported) > 0 {
		return reported, path
	}

	// 退路：解析会话的 sections.tools（老会话、或扩展没装时报这个）
	s.mu.Lock()
	current := s.sessionFile
	s.mu.Unlock()

	path := strings.TrimSpace(current)
	if path == "" || !fileExists(path) {
		path = latestSessionFile()
	}
	if path == "" {
		return nil, ""
	}

	sessionToolsMu.Lock()
	defer sessionToolsMu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return nil, ""
	}
	if sessionToolsCached.path == path && sessionToolsCached.modTime.Equal(info.ModTime()) && !sessionToolsCached.loadedAt.IsZero() {
		return sessionToolsCached.tools, sessionToolsCached.path
	}
	tools := parseSessionTools(path)
	sessionToolsCached = sessionToolsCache{path: path, modTime: info.ModTime(), tools: tools, loadedAt: time.Now()}
	return tools, path
}

// piToolsReport 对应扩展写出的 pi-tools.json。
type piToolsReport struct {
	Version     int    `json:"version"`
	CapturedAt  string `json:"capturedAt"`
	PID         int    `json:"pid"`
	ActiveCount int    `json:"activeCount"`
	Tools       []struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Description string `json:"description"`
		Exposure    string `json:"exposure"`
		Active      bool   `json:"active"`
	} `json:"tools"`
}

// reportedPiTools 读扩展上报的工具清单（带 mtime 缓存的简易实现）。
func reportedPiTools() ([]piToolInfo, string) {
	dir := agentDir()
	if dir == "" {
		return nil, ""
	}
	path := filepath.Join(dir, "pi-tools.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	var report piToolsReport
	if json.Unmarshal(raw, &report) != nil || len(report.Tools) == 0 {
		return nil, ""
	}
	out := make([]piToolInfo, 0, len(report.Tools))
	for _, tool := range report.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		kind := classifyPiTool(tool.Name)
		note := tool.Description
		if tool.Exposure == "deferred" || tool.Exposure == "codemode" {
			note = strings.TrimSpace(note + "（非直接暴露：" + tool.Exposure + "）")
		}
		if !tool.Active {
			note = strings.TrimSpace(note + "（本次未启用）")
		}
		out = append(out, piToolInfo{
			ID:       tool.Name,
			Name:     tool.Name,
			Note:     note,
			Kind:     kind,
			Provider: "",
			Active:   tool.Active,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, path
}

// invalidateSessionTools 在切换会话后调用：下次数工具时必须重新读文件。
func invalidateSessionTools() {
	sessionToolsMu.Lock()
	sessionToolsCached = sessionToolsCache{}
	sessionToolsMu.Unlock()
}

// latestSessionFile 找最近修改过的会话文件（递归，跨工作区分组）。
func latestSessionFile() string {
	root := sessionsRoot()
	if root == "" {
		return ""
	}
	var newest string
	var newestTime time.Time
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(newestTime) {
			newest, newestTime = path, info.ModTime()
		}
		return nil
	})
	return newest
}

// parseSessionTools 从会话文件里解析首条 system 消息的 sections.tools。
func parseSessionTools(path string) []piToolInfo {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// 系统消息那一行可能很长（技能段落几 KB），把缓冲调大
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var line struct {
			Type    string `json:"type"`
			Message struct {
				Role     string            `json:"role"`
				Sections map[string]string `json:"sections"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.Type != "message" || line.Message.Role != "system" {
			continue
		}
		section := line.Message.Sections["tools"]
		if strings.TrimSpace(section) == "" {
			continue
		}
		return parseToolsSection(section)
	}
	return nil
}

// parseToolsSection 解析 `<tools> - 名字: 说明 … </tools>`。
func parseToolsSection(section string) []piToolInfo {
	var out []piToolInfo
	for _, raw := range strings.Split(section, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		name, note := line, ""
		if at := strings.Index(line, ":"); at > 0 {
			name = strings.TrimSpace(line[:at])
			note = strings.TrimSpace(line[at+1:])
		}
		if name == "" || name == "tools" || strings.HasPrefix(name, "<") {
			continue
		}
		out = append(out, piToolInfo{ID: name, Name: name, Note: note, Kind: classifyPiTool(name)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// classifyPiTool 按名字给工具归类：界面上"内置 / 扩展 / MCP"三组要分开显示。
func classifyPiTool(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "mcp", strings.HasPrefix(lower, "mcp__"), strings.HasPrefix(lower, "mcp_"):
		return "mcp"
	case isBuiltinPiTool(lower):
		return "builtin"
	default:
		return "extension"
	}
}

// builtinPiToolNames 只用于**界面归类**（把工具分组显示成"内置/扩展/MCP"），
// 不参与任何逻辑：工具到底有没有、叫什么，一切以会话里的实际清单为准。
// 这份名单缺失或过时最多导致"分组不完美"，不会造成功能错误。
func isBuiltinPiTool(name string) bool {
	_, ok := map[string]bool{
		"read": true, "write": true, "edit": true, "bash": true, "powershell": true,
		"grep": true, "find": true, "ls": true, "glob": true, "todo": true,
		"task": true, "codemode": true, "tool_search": true, "web_fetch": true,
	}[name]
	return ok
}
