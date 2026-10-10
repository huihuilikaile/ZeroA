package bridge

import (
	"encoding/json"
	"strings"
	"time"

	"zeroa/internal/mcp"
	"zeroa/internal/tools"
)

// 本文件是 MCP 目录域的薄封装：服务器信息合并（mcp.json + mcp-cache.json）、
// 工具清单、"重新发现"的实现已经迁到 internal/mcp，这里只剩——
//
//	① 解析配置目录里的两个文件路径；
//	② mcpCapabilityStatuses：把服务器信息映射成 tools.Status（依赖 internal/tools，
//	   是角色能力判定的接线处）；
//	③ Wails 绑定入口 RediscoverMcp（Result 形状、guiLog）。

// mcpServerInfo 是界面要显示的一个 MCP 服务器（实现见 internal/mcp.ServerInfo）。
type mcpServerInfo = mcp.ServerInfo

// mcpCachePath 返回 mcp-cache.json 的绝对路径。
func mcpCachePath() string {
	return mcp.CachePath(agentDir())
}

// loadMcpServerInfos 合并"配置里的服务器"与"缓存里的工具"，得到界面所需的完整信息。
func loadMcpServerInfos() []mcpServerInfo {
	return mcp.LoadInfos(mcpConfigPath(), mcpCachePath())
}

// mcpCapabilityStatuses 把每个 MCP 服务器映射成一条"能力 → 状态"，
// 供 tools.Resolve 判定角色声明的 mcp.<服务器> 能不能满足。
func mcpCapabilityStatuses() map[string][]tools.Status {
	out := map[string][]tools.Status{}
	for _, server := range loadMcpServerInfos() {
		status := tools.Status{
			Definition: tools.Definition{
				ID:           "mcp:" + server.Name,
				Name:         server.Name,
				Type:         tools.KindMCP,
				Direction:    "mcp",
				Capabilities: []string{server.Capability},
				AgentTool:    true,
				Provider:     server.Name,
				Note:         mcp.Note(server),
			},
			Available: server.Enabled && server.ToolCount > 0,
			Path:      server.Target,
			Detail:    mcp.Note(server),
			CheckedAt: time.Now(),
		}
		out[server.Capability] = append(out[server.Capability], status)
	}
	return out
}

// mcpServerTools 返回某台服务器的工具清单（界面点开看详情用）。
func mcpServerTools(name string) []map[string]string {
	return mcp.ServerTools(mcpCachePath(), name)
}

// RediscoverMcp 让某台服务器"下次重新发现工具"。
func (s *PiService) RediscoverMcp(name string) (Result, error) {
	trimmed := strings.TrimSpace(name)
	path := mcpCachePath()
	if err := mcp.ForgetCachedServer(path, trimmed); err != nil {
		return Result{}, err
	}
	Logf("mcp_rediscover server=%s cache=%s", trimmed, path)
	payload, _ := json.Marshal(map[string]any{
		"server": trimmed,
		"cache":  path,
		"note":   "已清除该服务器的工具缓存：下次会话（或适配器下次调用它）时会重新发现。",
	})
	return Result{Command: "rediscover_mcp", Success: true, Data: string(payload)}, nil
}
