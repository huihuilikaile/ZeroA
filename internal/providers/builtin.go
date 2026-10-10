// Package providers 负责「模型提供商」配置域：
//
//   - 内置供应商注册表（从自带 Pi 的 bundle 里抠出来的那份清单，见 builtin.go）；
//   - 设置页「模型」列表（models.json + models-store.json + auth.json 三处合并）；
//   - 写回 models.json（api/baseUrl/apiKey）与 auth.json（API 密钥）。
//
// 这里是纯数据与文件 IO，不知道 PiService 的存在；主包 providers.go 只剩薄封装
// （configPaths 解析路径、configMu 串行化写操作、把结果包成 Wails 的 Result 形状）。
package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Pi 的内置供应商注册表。
//
// 为什么需要它：`get_available_models`（RPC）只列**已配置**的模型，`pi --list-models`
// 也一样——本机只有 6 个供应商。所以"想配 openai 但还没配 key"的时候，
// 界面上根本看不到它。Pi 的 TUI 是靠 /login 列出来的，那份清单在源码里：
//
//	function openaiProvider(){ return createProvider({id:"openai", name:"OpenAI",
//	  baseUrl:"https://api.openai.com/v1",
//	  auth:{ apiKey: envApiKeyAuth("OpenAI API key",["OPENAI_API_KEY"]),
//	         oauth: lazyOAuth({name:"OpenAI (ChatGPT subscription)", …}) },
//	  models: Object.values(OPENAI_MODELS), api: openAIResponsesApi()}) }
//
// 所以这里直接从自带 Pi 的 bundle 里把注册表抠出来：id / 显示名 / baseUrl /
// 支持哪种登录（API key 还是 OAuth）/ 对应的环境变量。
//
// 为什么用"抠"而不是自己维护一份名单：名单有 40 多项，手抄一定会漂。
// 抠出来的东西带降级——bundle 布局变了就返回空，界面退回"只显示已配置的"，
// 不会因为解析失败而崩或显示错误信息。

// Provider 是内置供应商注册表里的一条。
type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	EnvVar   string `json:"envVar,omitempty"`
	NeedsKey bool   `json:"needsKey"`
	OAuth    bool   `json:"oauth"`
}

var (
	builtinOnce  sync.Once
	builtinCache []Provider
)

// createProvider({id:"x",name:"Y",baseUrl:"Z",auth:{...}}) 里我们只关心前三个字段
var providerEntryPattern = regexp.MustCompile(`createProvider\(\{\s*id:\s*"([^"]+)"(?:,\s*name:\s*"([^"]*)")?(?:,\s*baseUrl:\s*"([^"]*)")?([\s\S]{0,600}?)models:`)

// 登录方式与对应的环境变量
var envVarPattern = regexp.MustCompile(`envApiKeyAuth\(\s*"[^"]*"\s*,\s*\[\s*"([A-Z0-9_]+)"`)

// Builtin 返回 Pi 内置的供应商清单（进程内缓存一次）。
func Builtin() []Provider {
	builtinOnce.Do(func() { builtinCache = loadBuiltinProviders() })
	return builtinCache
}

// ResetCache 清掉进程内缓存（测试换 PI_AGENT_HOME 后需要重新扫）。
func ResetCache() {
	builtinOnce = sync.Once{}
	builtinCache = nil
}

// builtinChunkPaths 找出自带 Pi 里定义了注册表的那些 bundle 文件。
func builtinChunkPaths() []string {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	bases := []string{filepath.Dir(self)}
	if home := os.Getenv("PI_AGENT_HOME"); strings.TrimSpace(home) != "" {
		bases = append(bases, home)
	}
	var out []string
	for _, base := range bases {
		for _, agentDir := range []string{"pi-agent", "pi", filepath.Join("runtime", "pi-agent")} {
			chunks := filepath.Join(base, agentDir, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "bundle", "chunks")
			entries, err := os.ReadDir(chunks)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".js") {
					out = append(out, filepath.Join(chunks, entry.Name()))
				}
			}
		}
	}
	return out
}

// loadBuiltinProviders 扫 bundle，抽出注册表条目。
func loadBuiltinProviders() []Provider {
	seen := map[string]Provider{}
	for _, path := range builtinChunkPaths() {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 32<<20 { // 单个 chunk 不该这么大，防呆
			continue
		}
		text := string(data)
		if !strings.Contains(text, "createProvider({") {
			continue
		}
		for _, match := range providerEntryPattern.FindAllStringSubmatch(text, -1) {
			id := strings.TrimSpace(match[1])
			if id == "" {
				continue
			}
			entry := Provider{
				ID:       id,
				Name:     strings.TrimSpace(match[2]),
				BaseURL:  strings.TrimSpace(match[3]),
				NeedsKey: true,
			}
			locale := match[4] // id 与 models 之间那段，含 auth 定义
			if vars := envVarPattern.FindStringSubmatch(locale); vars != nil {
				entry.EnvVar = vars[1]
			}
			if strings.Contains(locale, "lazyOAuth(") {
				entry.OAuth = true
			}
			// 本地/免 key 的供应商（ollama、lmstudio 等）没有 auth 段
			if !strings.Contains(locale, "auth:") {
				entry.NeedsKey = false
			}
			if _, exists := seen[id]; !exists {
				seen[id] = entry
			}
		}
	}
	out := make([]Provider, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
