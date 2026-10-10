// Package mcp 负责 MCP 服务器配置域：<agentDir>/mcp.json 的读写、
// mcp-cache.json（pi-mcp-adapter 写的工具缓存）的读取与"重新发现"清除。
//
// 为什么就是这个文件：Pi 的用户级 MCP 配置是 `<agentDir>/mcp.json`（见 pi 的
// docs/mcp.md），而 pi-mcp-adapter 也读同一份（它在 Pi 0.99+ 上"接管内置 MCP"，
// 但服务器来源不变）。所以界面只要维护这一个文件，两种模式都能用。
//
// 写文件时**保留我们不认识的字段**：MCP 配置是用户的东西，其中可能有 oauth、
// toolExposure 之类的键，界面不认不等于可以删掉。
//
// 这里是纯数据与文件 IO，不知道 PiService 的存在；主包 mcp.go / mcp_catalog.go
// 只剩薄封装（解析配置目录、guiLog、Result 形状）。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ConfigFileName 是 MCP 配置文件名。
const ConfigFileName = "mcp.json"

// serverNamePattern 是 Pi 的规定：名字只能有字母、数字、下划线、短横线。
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Server 是界面与磁盘之间的中间形状。
type Server struct {
	Name string `json:"name"`
	// stdio 传输
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	// HTTP 传输
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// 通用
	Type        string  `json:"type,omitempty"`
	Enabled     *bool   `json:"enabled,omitempty"`
	Exposure    string  `json:"exposure,omitempty"`
	Description string  `json:"description,omitempty"`
	Timeout     float64 `json:"timeout,omitempty"`
	// Transport 是界面用的判断：stdio / http / unknown
	Transport string `json:"transport"`
	// Raw 是原始条目（除 name 外的一切），保存时以它为基础合并
	Raw map[string]json.RawMessage `json:"-"`
}

// ConfigPath 返回 mcp.json 的绝对路径（dir 为空时返回空串）。
func ConfigPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ConfigFileName)
}

// LoadServers 读出全部服务器（保持磁盘上的顺序无关，返回按名字排序）。
func LoadServers(path string) ([]Server, error) {
	if path == "" {
		return nil, errors.New("找不到配置目录")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 还没配过
		}
		return nil, err
	}
	var file struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("mcp.json 解析失败: %w", err)
	}
	servers := make([]Server, 0, len(file.McpServers))
	for name, entry := range file.McpServers {
		server := Server{Name: name, Raw: map[string]json.RawMessage{}}
		if err := json.Unmarshal(entry, &server.Raw); err != nil {
			server.Description = "（这条配置读不出来）"
			servers = append(servers, server)
			continue
		}
		// 用一次结构体解析把已知字段填好；未知字段留在 Raw 里
		_ = json.Unmarshal(entry, &server)
		switch {
		case strings.TrimSpace(server.Command) != "":
			server.Transport = "stdio"
		case strings.TrimSpace(server.URL) != "":
			server.Transport = "http"
		default:
			server.Transport = "unknown"
		}
		if server.Exposure == "" {
			server.Exposure = "codemode"
		}
		servers = append(servers, server)
	}
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, nil
}

// SaveServer 新建或更新一条服务器配置，其余内容原样保留。
func SaveServer(path string, server Server) error {
	name := strings.TrimSpace(server.Name)
	if !serverNamePattern.MatchString(name) {
		return errors.New("服务器名只能用字母、数字、下划线、短横线（ZeroA 的规定）")
	}
	if strings.TrimSpace(server.Command) == "" && strings.TrimSpace(server.URL) == "" {
		return errors.New("要么填启动命令（stdio），要么填 URL（HTTP）")
	}
	if server.Exposure != "" && !validExposure(server.Exposure) {
		return fmt.Errorf("未知的 exposure %q（可用：codemode/deferred/direct/hidden）", server.Exposure)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 读出现有文件（保留其他服务器与顶层未知键）
	file := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("mcp.json 解析失败，为避免覆盖你的配置已中止: %w", err)
		}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := file["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("mcpServers 解析失败，为避免覆盖你的配置已中止: %w", err)
		}
	}
	// 以原始条目为基础合并，这样 toolExposure / oauth 这些键不会因为界面不认识而丢失
	merged := map[string]any{}
	if existing, ok := servers[name]; ok {
		_ = json.Unmarshal(existing, &merged)
	}
	applyServer(&merged, server)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	servers[name] = encoded
	encodedServers, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	file["mcpServers"] = encodedServers
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// applyServer 把界面传来的字段写进条目。空值 = 删除该键（用户清空了输入框）。
func applyServer(target *map[string]any, server Server) {
	set := func(key string, value any, keep bool) {
		if keep {
			(*target)[key] = value
		} else {
			delete(*target, key)
		}
	}
	set("command", server.Command, strings.TrimSpace(server.Command) != "")
	set("args", server.Args, len(server.Args) > 0)
	set("env", server.Env, len(server.Env) > 0)
	set("cwd", server.Cwd, strings.TrimSpace(server.Cwd) != "")
	set("url", server.URL, strings.TrimSpace(server.URL) != "")
	set("headers", server.Headers, len(server.Headers) > 0)
	set("type", server.Type, strings.TrimSpace(server.Type) != "")
	set("enabled", server.Enabled, server.Enabled != nil)
	set("exposure", server.Exposure, strings.TrimSpace(server.Exposure) != "")
	set("description", server.Description, strings.TrimSpace(server.Description) != "")
	set("timeout", server.Timeout, server.Timeout > 0)
}

func validExposure(value string) bool {
	switch value {
	case "codemode", "codemode-deferred", "deferred", "direct", "hidden":
		return true
	}
	return false
}

// DeleteServer 删掉一条服务器配置。
func DeleteServer(path, name string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return errors.New("还没有 mcp.json")
	}
	file := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("mcp.json 解析失败: %w", err)
	}
	servers := map[string]json.RawMessage{}
	if rawServers, ok := file["mcpServers"]; ok {
		if err := json.Unmarshal(rawServers, &servers); err != nil {
			return fmt.Errorf("mcpServers 解析失败: %w", err)
		}
	}
	if _, ok := servers[name]; !ok {
		return fmt.Errorf("没有这个服务器: %s", name)
	}
	delete(servers, name)
	encoded, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	file["mcpServers"] = encoded
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// ReadPackages 读出 settings.json 里声明的包（npm:/git:/路径）。
//
// 它决定了"网络搜索能力"到底有没有：web 工具是扩展提供的，不在 Pi 内置工具里。
func ReadPackages(dir string) []string {
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return nil
	}
	var settings struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil
	}
	out := make([]string, 0, len(settings.Packages))
	for _, item := range settings.Packages {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
