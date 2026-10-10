package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// osStat 是一层薄封装：让 fileExists 在测试里可以被替换（也避免直接依赖 os）。
var osStat = os.Stat

// 能力（技能 / MCP / 工具目录）相关的绑定方法。
//
// 与角色分开成一组的理由：这三样东西是**环境级**的（技能目录、mcp.json、
// 装了哪些包），角色只是引用它们。界面上的"能力"页要能独立于角色工作：
// 先看这台机器有什么，再决定角色用哪些。

// SaveMcpServer 新建或更新一条 MCP 服务器。
func (s *PiService) SaveMcpServer(payload string) (Result, error) {
	var server mcpServer
	if err := json.Unmarshal([]byte(payload), &server); err != nil {
		return Result{}, errors.New("服务器配置格式不对: " + err.Error())
	}
	if err := saveMcpServer(server); err != nil {
		return Result{}, err
	}
	return s.listMcpServersResult("save_mcp_server")
}

// DeleteMcpServer 删除一条 MCP 服务器。
func (s *PiService) DeleteMcpServer(name string) (Result, error) {
	if err := deleteMcpServer(strings.TrimSpace(name)); err != nil {
		return Result{}, err
	}
	return s.listMcpServersResult("delete_mcp_server")
}

// ListMcpServers 列出 MCP 服务器 + 配置文件位置 + 装了哪些提供联网能力的包。
//
// 界面据此显示"加了哪些、开没开、是不是装了搜索扩展"——这三点决定了
// 模型到底有没有联网能力，比让用户去猜 mcp.json 里写了什么有用得多。
func (s *PiService) ListMcpServers() (Result, error) {
	return s.listMcpServersResult("list_mcp_servers")
}

func (s *PiService) listMcpServersResult(command string) (Result, error) {
	servers, err := loadMcpServers()
	if err != nil {
		return Result{}, err
	}
	if servers == nil {
		servers = []mcpServer{}
	}
	packages := readPackages()
	payload, err := json.Marshal(map[string]any{
		"servers": servers,
		"config":  mcpConfigPath(),
		"exists":  fileExists(mcpConfigPath()),
		// 联网能力来自扩展而不是 Pi 内置工具（Pi 没有 web_search），
		// 所以这几个字段要一起给出去，界面才能说清"为什么没有联网"
		"packages":      packages,
		"webCapable":    hasWebPackage(packages),
		"adapterReady":  hasPackage(packages, "mcp-adapter"),
		"exposureModes": []string{"codemode", "deferred", "direct", "hidden"},
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: command, Success: true, Data: string(payload)}, nil
}

// ListSkills 见 skills.go；这里只做绑定层。

// OpenSkill 用系统程序打开技能目录（看 SKILL.md、改内容）。
func (s *PiService) OpenSkill(id string) (Result, error) {
	for _, skill := range loadSkills() {
		if strings.EqualFold(skill.ID, strings.TrimSpace(id)) {
			payload, _ := json.Marshal(map[string]any{"path": skill.Dir})
			return Result{Command: "open_skill", Success: true, Data: string(payload)}, nil
		}
	}
	return Result{}, errors.New("没有这个技能: " + id)
}

// hasPackage 判断某个包名（模糊匹配，因为可能是 npm:scope/name@version 形式）。
func hasPackage(packages []string, needle string) bool {
	for _, name := range packages {
		if strings.Contains(strings.ToLower(name), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := osStat(path)
	return err == nil && !info.IsDir()
}
