package mcp

// MCP 服务器与它们的工具：**不写死任何服务器**，全部来自已有的两份 JSON。
//
//	mcp.json        服务器清单（应用自己的配置，角色页可增删）
//	mcp-cache.json  pi-mcp-adapter 写的工具缓存：
//	                servers.<名字>.tools[] 每项含 name/description/inputSchema
//	                servers.<名字>.cachedAt / discoveryFailed
//
// 于是"这台机器有哪些 MCP 工具"这件事不需要我们枚举：
// 服务器列表从配置来，工具列表从缓存来，能力名由服务器名推导（mcp.<名字>）。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ServerInfo 是界面要显示的一个 MCP 服务器。
type ServerInfo struct {
	Name        string   `json:"name"`
	Transport   string   `json:"transport"`
	Target      string   `json:"target,omitempty"` // 命令或 URL，界面上显示"连的是什么"
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description,omitempty"`
	ToolCount   int      `json:"toolCount"`
	Resources   int      `json:"resources"`
	Prompts     int      `json:"prompts"`
	CachedAt    string   `json:"cachedAt,omitempty"`
	Failed      bool     `json:"failed,omitempty"`
	Capability  string   `json:"capability"` // mcp.<名字>
	ToolNames   []string `json:"toolNames,omitempty"`
}

// CacheFile 对应 pi-mcp-adapter 写的 mcp-cache.json。
type CacheFile struct {
	Version int `json:"version"`
	Servers map[string]struct {
		ConfigHash string `json:"configHash"`
		/**
		 * CachedAt 用 RawMessage 而不是 string：**这个文件是别人写的**，
		 * 而它换过格式——老版本适配器写 ISO 字符串，新版本写 epoch 毫秒数字。
		 * 按 string 读的后果是整份缓存解析失败，一次报错把所有服务器一起带下水：
		 * 用户实测点「看工具 / 重新发现」后弹
		 * `json: cannot unmarshal number into Go struct field mcpCacheFile.servers.ida.cachedAt of type string`
		 * — windows-mcp 明明已经连上（20 个工具），面板却整体报错。
		 * 所以这里两种都收，由 FormatCachedAt 统一成可显示的文本。
		 */
		CachedAt        json.RawMessage   `json:"cachedAt"`
		DiscoveryFailed bool              `json:"discoveryFailed"`
		Tools           []json.RawMessage `json:"tools"`
		Resources       []json.RawMessage `json:"resources"`
		Prompts         []json.RawMessage `json:"prompts"`
	} `json:"servers"`
}

/**
 * FormatCachedAt 把"发现时间"统一成可显示文本：字符串原样用，数字按时间戳解释。
 *
 * 数字分支要能认出秒与毫秒：适配器写的是 Date.now()（毫秒，13 位），
 * 但早期/别处也可能写秒（10 位）。1e12 是分界线（毫秒时间戳早就超过它）。
 */
func FormatCachedAt(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return ""
	}
	if text[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
		return ""
	}
	var number float64
	if json.Unmarshal(raw, &number) != nil || number <= 0 {
		return ""
	}
	seconds := int64(number)
	if number > 1e12 {
		seconds = int64(number / 1000)
	}
	return time.Unix(seconds, 0).Format(time.RFC3339)
}

// CachePath 返回 mcp-cache.json 的绝对路径（dir 为空时返回空串）。
func CachePath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mcp-cache.json")
}

// LoadInfos 合并"配置里的服务器"与"缓存里的工具"，得到界面所需的完整信息。
func LoadInfos(configPath, cachePath string) []ServerInfo {
	servers, err := LoadServers(configPath)
	if err != nil {
		return nil
	}
	cache := LoadCache(cachePath)

	out := make([]ServerInfo, 0, len(servers))
	for _, server := range servers {
		info := ServerInfo{
			Name:        server.Name,
			Transport:   server.Transport,
			Enabled:     server.Enabled == nil || *server.Enabled,
			Description: server.Description,
			Capability:  "mcp." + server.Name,
		}
		switch strings.ToLower(server.Transport) {
		case "stdio":
			info.Target = strings.TrimSpace(server.Command)
		default:
			info.Target = strings.TrimSpace(server.URL)
		}
		if entry, ok := cache.Servers[server.Name]; ok {
			info.ToolCount = len(entry.Tools)
			info.Resources = len(entry.Resources)
			info.Prompts = len(entry.Prompts)
			info.CachedAt = FormatCachedAt(entry.CachedAt)
			info.Failed = entry.DiscoveryFailed
			for _, raw := range entry.Tools {
				var tool struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(raw, &tool) == nil && tool.Name != "" {
					info.ToolNames = append(info.ToolNames, tool.Name)
				}
			}
			sort.Strings(info.ToolNames)
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LoadCache 读取 mcp-cache.json；读不到或解析失败都返回空缓存。
func LoadCache(path string) CacheFile {
	var cache CacheFile
	if path == "" {
		return cache
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	_ = json.Unmarshal(raw, &cache)
	return cache
}

// Note 给一台服务器配一句界面提示（状态说明）。
func Note(server ServerInfo) string {
	switch {
	case !server.Enabled:
		return "已在配置里停用"
	case server.Failed:
		return "上次发现失败：服务器没起或地址不对（启动它后点「重新发现」）"
	case server.ToolCount == 0:
		return "还没发现工具：先让服务器跑起来（IDEA/GUI/扩展），新开一次会话或点「重新发现」"
	default:
		return "已发现 " + strconv.Itoa(server.ToolCount) + " 个工具"
	}
}

// ServerTools 返回某台服务器的工具清单（界面点开看详情用）。
func ServerTools(cachePath, name string) []map[string]string {
	cache := LoadCache(cachePath)
	entry, ok := cache.Servers[name]
	if !ok {
		return nil
	}
	out := make([]map[string]string, 0, len(entry.Tools))
	for _, raw := range entry.Tools {
		var tool struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if json.Unmarshal(raw, &tool) != nil || tool.Name == "" {
			continue
		}
		out = append(out, map[string]string{"name": tool.Name, "description": tool.Description})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["name"] < out[j]["name"] })
	return out
}

// ForgetCachedServer 让某台服务器"下次重新发现工具"。
//
// 缓存归 pi-mcp-adapter 所有，我们**不伪造数据**：清掉这台服务器的缓存条目，
// 适配器下次用到它时会重新拉一次工具清单。所以回复里要说清"什么时候生效"。
func ForgetCachedServer(cachePath, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("请指定 MCP 服务器名")
	}
	if cachePath == "" {
		return errors.New("还没有工具缓存（先启动一次会话，让适配器连上服务器）")
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("还没有工具缓存（先启动一次会话，让适配器连上服务器）")
		}
		return err
	}
	var cache CacheFile
	if err := json.Unmarshal(raw, &cache); err != nil {
		return err
	}
	if cache.Servers == nil {
		return errors.New("缓存里没有服务器记录")
	}
	if _, ok := cache.Servers[name]; !ok {
		return errors.New("缓存里没有「" + name + "」（先启动一次会话让它被发现）")
	}
	delete(cache.Servers, name)
	updated, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(updated, '\n'), 0o644)
}
