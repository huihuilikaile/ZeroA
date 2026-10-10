package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"zeroa/internal/tools"
)

// 工具清单（tool catalog）的装配与热加载。
//
// 三层来源（优先级低 → 高）：
//
//	内置默认（internal/tools/tool-catalog.default.json，编译进程序）
//	<数据目录>/tool-catalog.json            用户级
//	<工作区>/.pi/tool-catalog.json          项目级（团队共享，可进 git）
//
// 加载时机：启动时一次；切换工作区时一次（项目级清单跟着工作区走）；
// 界面点「重新加载」时一次。加载完顺手把 MCP 的能力来源注册给解析器，
// 这样角色声明的 mcp.ida 也能被判定。

// toolCatalogPaths 返回要加载的文件（低优先级在前）。
func (s *PiService) toolCatalogPaths() []string {
	var paths []string
	if dir := agentDir(); dir != "" {
		paths = append(paths, filepath.Join(dir, "tool-catalog.json"))
	}
	s.mu.Lock()
	workspace := s.workspaceDir
	s.mu.Unlock()
	if dir := strings.TrimSpace(workspace); dir != "" {
		paths = append(paths, filepath.Join(dir, ".pi", "tool-catalog.json"))
	}
	return paths
}

var catalogLoadedOnce bool

// ensureToolCatalog 保证目录已加载过（前端调用顺序不保证时也不会用错清单）。
func (s *PiService) ensureToolCatalog() {
	if catalogLoadedOnce {
		return
	}
	s.loadToolCatalog()
	catalogLoadedOnce = true
}

// loadToolCatalog 重新加载目录并装上（返回加载到的目录）。
func (s *PiService) loadToolCatalog() tools.Catalog {
	paths := s.toolCatalogPaths()
	catalog := tools.LoadCatalog(paths...)
	tools.SetActiveCatalog(catalog)
	// MCP 的能力来源（mcp.<服务器>）也交给解析器：它们的可用性来自配置与缓存，
	// 不来自 PATH 探测——这是"外部能力来源"注入的用途。
	tools.SetExternalCapabilityProvider(mcpCapabilityStatuses)
	// 模型可调用的工具来自会话（内置 + 扩展 + MCP），环境清单与界面都用它
	tools.SetAgentToolProvider(func() []tools.AgentTool {
		list, _ := s.sessionTools()
		owners := map[string]string{}
		for _, pkg := range s.installedPiPackages(toolNamesOf(list)).Items {
			for _, name := range pkg.Tools {
				owners[name] = pkg.Name
			}
		}
		out := make([]tools.AgentTool, 0, len(list))
		for _, item := range list {
			out = append(out, tools.AgentTool{Name: item.ID, Kind: item.Kind, Provider: owners[item.ID]})
		}
		return out
	})
	Logf("tool_catalog_loaded tools=%d directions=%d capabilities=%d files=%d issues=%d",
		len(catalog.Tools), len(catalog.Directions), len(catalog.Capabilities), len(catalog.Paths), len(catalog.Issues))
	for _, issue := range catalog.Issues {
		Logf("tool_catalog_issue path=%s message=%s", issue.Path, issue.Message)
	}
	return catalog
}

// ReloadToolCatalog 让界面上的「重新加载」生效：改完 JSON 不用重启应用。
func (s *PiService) ReloadToolCatalog() (Result, error) {
	catalog := s.loadToolCatalog()
	payload, err := json.Marshal(map[string]any{
		"tools":      len(catalog.Tools),
		"directions": len(catalog.Directions),
		"sources":    catalog.Paths,
		"issues":     catalog.Issues,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "reload_tool_catalog", Success: true, Data: string(payload)}, nil
}

// ExportToolCatalog 导出当前清单，方便用户照着改。
//
// 默认写到 <数据目录>/tool-catalog.json；**已经存在就另存一份带时间戳的**，
// 绝不悄悄覆盖用户自己的清单（那是他的配置，不是我们的产物）。
func (s *PiService) ExportToolCatalog(path string) (Result, error) {
	text, err := tools.ExportCatalog()
	if err != nil {
		return Result{}, err
	}
	target := s.resolveCatalogExportPath(path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
		return Result{}, err
	}
	Logf("tool_catalog_exported path=%s", target)
	payload, _ := json.Marshal(map[string]any{"path": target, "bytes": len(text)})
	return Result{Command: "export_tool_catalog", Success: true, Data: string(payload)}, nil
}

func (s *PiService) resolveCatalogExportPath(requested string) string {
	trimmed := strings.TrimSpace(requested)
	dir := agentDir()
	if dir == "" {
		dir = exeDir()
	}
	if trimmed == "" {
		target := filepath.Join(dir, "tool-catalog.json")
		if !fileExists(target) {
			return target
		}
		return filepath.Join(dir, "tool-catalog.export.json")
	}
	if strings.HasSuffix(trimmed, string(os.PathSeparator)) || strings.HasSuffix(trimmed, "/") {
		return filepath.Join(trimmed, "tool-catalog.json")
	}
	if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
		return filepath.Join(trimmed, "tool-catalog.json")
	}
	if !strings.EqualFold(filepath.Ext(trimmed), ".json") {
		trimmed += ".json"
	}
	return trimmed
}

// validateToolCatalogPath 供界面"我填的路径对不对"用（避免用户以为改了却没生效）。
func validateToolCatalogPath(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return errors.New("路径为空")
	}
	if _, err := os.Stat(trimmed); err != nil {
		return err
	}
	return nil
}
