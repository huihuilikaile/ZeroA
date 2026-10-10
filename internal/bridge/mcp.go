package bridge

import (
	"zeroa/internal/mcp"
)

// 本文件是 MCP 配置域的薄封装：mcp.json 读写的实现已经迁到 internal/mcp
// （纯数据与文件 IO，保留界面不认识的字段），这里只剩主包共用的薄封装函数
// （解析配置目录、guiLog）。Wails 绑定入口在 capabilities_api.go。

// mcpServer 是界面与磁盘之间的中间形状（实现见 internal/mcp.Server）。
type mcpServer = mcp.Server

// mcpConfigPath 返回 mcp.json 的绝对路径。
func mcpConfigPath() string {
	return mcp.ConfigPath(agentDir())
}

// loadMcpServers 读出全部服务器（保持磁盘上的顺序无关，返回按名字排序）。
func loadMcpServers() ([]mcpServer, error) {
	return mcp.LoadServers(mcpConfigPath())
}

// saveMcpServer 新建或更新一条服务器配置，其余内容原样保留。
func saveMcpServer(server mcpServer) error {
	if err := mcp.SaveServer(mcpConfigPath(), server); err != nil {
		return err
	}
	Logf("save_mcp_server name=%s transport=%s exposure=%s",
		server.Name, server.Transport, server.Exposure)
	return nil
}

// deleteMcpServer 删掉一条服务器配置。
func deleteMcpServer(name string) error {
	if err := mcp.DeleteServer(mcpConfigPath(), name); err != nil {
		return err
	}
	Logf("delete_mcp_server name=%s", name)
	return nil
}

// readPackages 读出 settings.json 里声明的包（npm:/git:/路径）。
//
// 它决定了"网络搜索能力"到底有没有：web 工具是扩展提供的，不在 Pi 内置工具里。
func readPackages() []string {
	return mcp.ReadPackages(agentDir())
}
