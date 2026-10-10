package bridge

import (
	"encoding/json"
	"strings"

	"zeroa/internal/tools"
)

// 工具目录接口：给界面提供三层信息，语义不同、必须分开显示。
//
//  1. **模型可调用的工具**（Pi 工具）：真正能被 --tools 收窄的东西，
//     角色的工具白名单操作的就是它们。清单**自动加载**——从运行中的会话
//     （system 消息的 sections.tools）解析，因为内置 + 扩展 + MCP 三者合起来
//     才是真清单，代码里枚举不完（见 session_tools.go）。
//  2. **本机环境工具**：命令行程序 / Python 包 / 桌面程序。它们不是 LLM 工具，
//     而是"这台机器上有什么"的环境清单，由 tool-catalog.json 驱动、
//     探测结果实时生成（见 internal/tools 与 tool_catalog.go）。
//  3. **MCP 服务器**：来自 mcp.json 的配置与 pi-mcp-adapter 写的 mcp-cache.json。
//
// 这三层过去混在一张写死的表里；现在只剩第 2 层需要我们出数据。

// toolInfo 是界面上的一个"模型可调用工具"（自动发现，不带任何写死的说明）。
type toolInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Note     string `json:"note"`
	Category string `json:"category"`
	Provider string `json:"provider,omitempty"`
}

// ListToolCatalog 返回三层信息（界面底部那两块 + MCP 列表都从这里取）。
func (s *PiService) ListToolCatalog() (Result, error) {
	catalog := tools.ActiveCatalog()

	// 方向的线索：直接来自目录（不再写死），并附带该方向有哪些工具、哪些可用
	type directionInfo struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		// Tools 是"这个方向常用的命令行线索"（界面「插入提示词」插的就是它）。
		// 名字沿用旧接口，前端不用改。
		Tools []string `json:"tools"`
		// CatalogTools 是这个方向在清单里的工具 id（可探测、可点开）
		CatalogTools []string `json:"catalogTools,omitempty"`
	}
	directions := make([]directionInfo, 0, len(catalog.Directions))
	for _, direction := range catalog.Directions {
		info := directionInfo{ID: direction.ID, Label: direction.Label, Tools: direction.Hints}
		if len(info.Tools) == 0 {
			// 没写线索的方向：退回"这个方向有哪些工具"，总比空着强
			for _, tool := range catalog.Tools {
				if tool.Direction == direction.ID {
					info.Tools = append(info.Tools, tool.ID)
				}
			}
		}
		for _, tool := range catalog.Tools {
			if tool.Direction == direction.ID {
				info.CatalogTools = append(info.CatalogTools, tool.ID)
			}
		}
		directions = append(directions, info)
	}

	// 模型可调用的工具：自动加载
	piToolList, piToolSource := s.sessionTools()
	packages := readPackages()
	packageReport := s.installedPiPackages(toolNamesOf(piToolList))
	installed := packageReport.Items
	piTools := make([]toolInfo, 0, len(piToolList))
	for _, tool := range piToolList {
		piTools = append(piTools, toolInfo{
			ID:       tool.ID,
			Name:     tool.Name,
			Note:     tool.Note,
			Category: toolCategory(tool.Kind),
			Provider: tool.Provider,
		})
	}
	// 归属（能认出来时）：把"哪个包提供这个工具"补上，界面能说清来路
	for index := range piTools {
		for _, pkg := range installed {
			for _, name := range pkg.Tools {
				if name == piTools[index].ID {
					piTools[index].Provider = pkg.Name
				}
			}
		}
	}
	piToolsNote := ""
	if piToolSource == "" {
		piToolsNote = "还没有会话记录：发一条消息启动 Pi 后，扩展会把真实启用的工具集上报到这里（内置 + 扩展 + MCP，含未启用的）。"
	}

	payload, err := json.Marshal(map[string]any{
		"piTools":        piTools,
		"piToolsSource":  piToolSource,
		"piToolsNote":    piToolsNote,
		"toolHints":      directions,
		"directions":     directions,
		"packages":       packages,
		"piPackages":     installed,
		"packagesSource": packageReport.Source,
		"packagesNote":   packageReport.Note,
		"webCapable":     hasWebPackage(packages),
		"configPath":     mcpConfigPath(),
		"skillsRoot":     skillsRoot(),
		// 清单本身的来源与问题：界面上要说清"我改了为什么没生效"
		"catalogPaths":    catalog.Paths,
		"catalogIssues":   catalog.Issues,
		"catalogUserPath": tools.DefaultCatalogPath(agentDir()),
		"mcpServers":      loadMcpServerInfos(),
		"capabilities":    tools.KnownCapabilities(),
		"capabilityNames": tools.CapabilityLabels(),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_tool_catalog", Success: true, Data: string(payload)}, nil
}

func toolCategory(kind string) string {
	switch kind {
	case "builtin":
		return "内置"
	case "mcp":
		return "MCP"
	default:
		return "扩展"
	}
}

func toolNamesOf(list []piToolInfo) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.ID)
	}
	return out
}

// ListMcpTools 返回某台 MCP 服务器的工具清单（界面点开看详情用）。
func (s *PiService) ListMcpTools(name string) (Result, error) {
	tools := mcpServerTools(strings.TrimSpace(name))
	payload, err := json.Marshal(map[string]any{"server": name, "tools": tools, "count": len(tools)})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_mcp_tools", Success: true, Data: string(payload)}, nil
}

// hasWebPackage 判断是否装了提供联网工具的扩展。
//
// 为什么在 Go 侧判断而不是前端硬编码：包名会变（@ollama/pi-web-search、
// pi-web-access…），这里只认"名字里带 web/search/fetch"这一类信号，
// 并明确告诉界面"网络能力来自扩展"。
func hasWebPackage(packages []string) bool {
	for _, name := range packages {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "web") || strings.Contains(lower, "search") || strings.Contains(lower, "fetch") {
			return true
		}
	}
	return false
}

/**
 * toolSuggestions 供角色校验使用：工具名不在"当前会话的工具清单"里时给出提示。
 *
 * 不阻止保存——扩展与 MCP 提供的工具只能在运行期知道，而角色可以在别的机器上
 * 使用（那里装的东西不一样）。所以这里只做"可选提示"。
 */
func (s *PiService) toolSuggestions() []string {
	list, _ := s.sessionTools()
	names := make([]string, 0, len(list))
	for _, item := range list {
		names = append(names, item.ID)
	}
	return names
}
