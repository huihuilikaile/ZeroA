package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Row 是设置页「模型」里的一行。
type Row struct {
	Provider string `json:"provider"`
	// Configured 表示 models.json 里已经有这个 provider（有模型定义）
	Configured bool `json:"configured"`
	// HasKey 表示凭据已配置（auth.json 里有，或 models.json 里写了 apiKey）
	HasKey bool `json:"hasKey"`
	// Builtin 表示它来自 Pi 内置的模型目录（models-store.json）
	Builtin bool   `json:"builtin"`
	API     string `json:"api,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	Models  int    `json:"models"`
	// DisplayName 来自 Pi 内置注册表（TUI 里显示的那个名字）
	DisplayName string `json:"displayName,omitempty"`
	// EnvVar 是 Pi 认可的环境变量名（不放 key 时也能通过环境变量提供）
	EnvVar string `json:"envVar,omitempty"`
	// NeedsKey 表示这个供应商需要密钥（本地跑的不需要）
	NeedsKey bool `json:"needsKey,omitempty"`
	// OAuthReady 表示它支持 OAuth 登录（需要浏览器流程，界面只做提示）
	OAuthReady bool `json:"oauthReady,omitempty"`
}

// List 合并三处信息，列出设置页要显示的提供商。
//
// 为什么不是只读 models.json：用户看到"什么都没有"会以为配置丢了，
// 而 Pi 内置目录里其实早就列着 openai/deepseek/kimi-coding 这些可选提供商——
// 它们只是还没填 key 而已。这正是 DSH 设置页那种"列表 + 状态点"的做法。
func List(modelsPath, storePath, authPath string) []Row {
	rows := map[string]*Row{}
	ensure := func(name string) *Row {
		if row, ok := rows[name]; ok {
			return row
		}
		row := &Row{Provider: name}
		rows[name] = row
		return row
	}

	// 1) models.json：已配置的提供商（可能带 api/baseUrl/apiKey 与模型列表）
	if data, err := os.ReadFile(modelsPath); err == nil {
		var file struct {
			Providers map[string]struct {
				API     string            `json:"api"`
				BaseURL string            `json:"baseUrl"`
				APIKey  string            `json:"apiKey"`
				Models  []json.RawMessage `json:"models"`
			} `json:"providers"`
		}
		if json.Unmarshal(data, &file) == nil {
			for name, entry := range file.Providers {
				row := ensure(name)
				row.Configured = true
				row.API = entry.API
				row.BaseURL = entry.BaseURL
				row.Models = len(entry.Models)
				if strings.TrimSpace(entry.APIKey) != "" {
					row.HasKey = true
				}
			}
		}
	}

	// 2) models-store.json：Pi 内置的模型目录（这些都是"可以直接配置"的提供商）
	if data, err := os.ReadFile(storePath); err == nil {
		var store map[string]struct {
			Models []json.RawMessage `json:"models"`
		}
		if json.Unmarshal(data, &store) == nil {
			for name, entry := range store {
				row := ensure(name)
				row.Builtin = true
				if row.Models == 0 {
					row.Models = len(entry.Models)
				}
			}
		}
	}

	// 3) Pi 内置供应商注册表（TUI 的 /login 用的就是这份）：
	//    只列"已配置的"会让用户以为没得选——RPC 的 get_available_models
	//    只返回已配置的模型，未配置的内置供应商根本不会出现。
	for _, builtin := range Builtin() {
		row := ensure(builtin.ID)
		row.Builtin = true
		if row.API == "" && builtin.OAuth {
			row.OAuthReady = true
		}
		if row.EnvVar == "" {
			row.EnvVar = builtin.EnvVar
		}
		if builtin.Name != "" && row.DisplayName == "" {
			row.DisplayName = builtin.Name
		}
		row.NeedsKey = row.NeedsKey || builtin.NeedsKey
	}

	// 4) auth.json：凭据。只判断"有没有"，不读出内容。
	if data, err := os.ReadFile(authPath); err == nil {
		var auth map[string]json.RawMessage
		if json.Unmarshal(data, &auth) == nil {
			for name, value := range auth {
				// 跳过明显不是提供商条目的键
				if strings.HasPrefix(name, "_") {
					continue
				}
				if len(strings.TrimSpace(string(value))) <= 2 { // {} 或 null
					continue
				}
				ensure(name).HasKey = true
			}
		}
	}

	list := make([]Row, 0, len(rows))
	for _, row := range rows {
		list = append(list, *row)
	}
	// 已配置的排前面，其余按名字；同组内保持稳定顺序便于点击
	sort.Slice(list, func(i, j int) bool {
		if list[i].Configured != list[j].Configured {
			return list[i].Configured
		}
		return list[i].Provider < list[j].Provider
	})
	return list
}

// Update 改一个提供商的 api / baseUrl / apiKey，写回 models.json。
//
// 只写用户填了的字段：留空表示"不动原来的值"，这样界面上的编辑框可以只改 key。
// 与 AddModel 一样：写前备份、临时文件 + rename 落盘。
func Update(modelsPath, provider, api, baseURL, apiKey string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return errors.New("缺少 provider")
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(modelsPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("models.json 不是合法 JSON: %w", err)
		}
		_ = os.WriteFile(modelsPath+".bak", data, 0o644)
	}
	providers, _ := raw["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	entry, _ := providers[provider].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	if trimmed := strings.TrimSpace(api); trimmed != "" {
		entry["api"] = trimmed
	}
	if trimmed := strings.TrimSpace(baseURL); trimmed != "" {
		entry["baseUrl"] = trimmed
	}
	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		entry["apiKey"] = trimmed
	}
	if _, ok := entry["models"]; !ok {
		entry["models"] = []any{} // 至少留一个空列表，Pi 才不会把它当坏配置
	}
	providers[provider] = entry
	raw["providers"] = providers

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := modelsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, modelsPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SetKey 写入某提供商的 API 密钥 —— 这就是 Pi 的"登录"。返回实际使用的凭据类型。
//
// 核对过自带 1.0.2：pi auth 只有 print-api-key / print-bearer-token / check，
// 没有 login / set-key；密钥是写进 <agent-dir>/auth.json 的，形状为
// {"<provider>": {"type": "api_key", "key": "..."}}。类型名是 api_key（带下划线，
// core/auth-storage.js 只认 "api_key" 与 "oauth"）；字段名是 key，不是 apiKey。
func SetKey(authPath, provider, key string) (string, error) {
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if provider == "" {
		return "", errors.New("请选择模型提供商")
	}
	if key == "" {
		return "", errors.New("请填写 API 密钥")
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(authPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return "", fmt.Errorf("auth.json 不是合法 JSON: %w", err)
		}
		_ = os.WriteFile(authPath+".bak", data, 0o644)
	}
	authType := "api_key" // Pi 的类型名带下划线：api_key / oauth
	if entry, ok := raw[provider].(map[string]any); ok {
		if existing, ok := entry["type"].(string); ok && existing != "" {
			authType = existing
		}
	}
	raw[provider] = map[string]any{"type": authType, "key": key}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := authPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, authPath); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return authType, nil
}
