package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// 本文件是 models.json 的"写"侧：添加/删除自定义模型、列出原始配置。
//
// 为什么不是 RPC：Pi 1.0.0 的 RPC 只有 set_model / get_available_models，
// 没有任何"添加模型"的命令（docs/rpc-commands.md 全表核对过）。
// 模型目录是配置驱动的，所以新增模型只能改 Pi 读的那份 JSON，
// 改完必须重启 Pi 才会生效——界面上的流程就是"保存 → 重启会话"。
//
// 写入前会备份一份 models.json.bak：这是用户的真实配置，写坏了至少要能手工救回来。

// AddModel 把一个自定义模型写进 models.json。同 id 视为"更新"，返回 updated。
func AddModel(modelsPath, provider, modelID, name, api, baseURL, apiKey string, contextWindow int, supportsImages bool) (updated bool, err error) {
	provider = strings.TrimSpace(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" || modelID == "" {
		return false, errors.New("provider 与模型 id 都必须填写")
	}
	if strings.ContainsAny(provider, `/\`) {
		return false, errors.New("provider 不能包含斜杠")
	}

	// 读现有配置：不存在就当成空配置，不报错（首次配置的用户就是这种情况）
	raw := map[string]any{}
	if data, readErr := os.ReadFile(modelsPath); readErr == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return false, fmt.Errorf("models.json 不是合法 JSON，先手工修一下: %w", err)
		}
		_ = os.WriteFile(modelsPath+".bak", data, 0o644) // 备份
	}

	providers, _ := raw["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	entry, _ := providers[provider].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	if api != "" {
		entry["api"] = strings.TrimSpace(api)
	}
	if baseURL != "" {
		entry["baseUrl"] = strings.TrimSpace(baseURL)
	}
	if apiKey != "" {
		entry["apiKey"] = strings.TrimSpace(apiKey)
	}

	models, _ := entry["models"].([]any)
	// 同 id 视为"更新"：覆盖而不是追加，避免配置文件里出现两个同名模型
	for _, item := range models {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := model["id"].(string); id == modelID {
			applyModelFields(model, name, contextWindow, supportsImages)
			updated = true
			break
		}
	}
	if !updated {
		model := map[string]any{"id": modelID}
		applyModelFields(model, name, contextWindow, supportsImages)
		models = append(models, model)
	}
	entry["models"] = models
	providers[provider] = entry
	raw["providers"] = providers

	if err := writeJSONAtomic(modelsPath, raw); err != nil {
		return false, err
	}
	return updated, nil
}

// applyModelFields 只写用户填了的字段，其余保持原样（避免把配置改瘦）。
func applyModelFields(model map[string]any, name string, contextWindow int, supportsImages bool) {
	if strings.TrimSpace(name) != "" {
		model["name"] = strings.TrimSpace(name)
	}
	if contextWindow > 0 {
		model["contextWindow"] = contextWindow
	}
	if supportsImages {
		model["input"] = []string{"text", "image"}
	}
}

// DeleteModel 从 models.json 里删掉一个模型
// （provider 下最后一个模型被删时连 provider 一起删，返回 providerRemoved）。
func DeleteModel(modelsPath, provider, modelID string) (providerRemoved bool, err error) {
	provider = strings.TrimSpace(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" || modelID == "" {
		return false, errors.New("provider 与模型 id 都不能为空")
	}
	data, err := os.ReadFile(modelsPath)
	if err != nil {
		return false, fmt.Errorf("读不到 models.json: %w", err)
	}
	raw := map[string]any{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("models.json 不是合法 JSON: %w", err)
	}
	providers, _ := raw["providers"].(map[string]any)
	entry, _ := providers[provider].(map[string]any)
	if entry == nil {
		return false, fmt.Errorf("配置里没有 provider: %s", provider)
	}
	models, _ := entry["models"].([]any)
	kept := make([]any, 0, len(models))
	found := false
	for _, item := range models {
		model, ok := item.(map[string]any)
		if ok {
			if id, _ := model["id"].(string); id == modelID {
				found = true
				continue
			}
		}
		kept = append(kept, item)
	}
	if !found {
		return false, fmt.Errorf("provider %s 下没有模型 %s", provider, modelID)
	}
	if len(kept) == 0 {
		delete(providers, provider) // 最后一个模型被删掉，provider 也没意义了
	} else {
		entry["models"] = kept
		providers[provider] = entry
	}
	raw["providers"] = providers

	_ = os.WriteFile(modelsPath+".bak", data, 0o644)
	if err := writeJSONAtomic(modelsPath, raw); err != nil {
		return false, err
	}
	return len(kept) == 0, nil
}

// ModelSummary 是「已配置模型」列表里一个模型的条目。
type ModelSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

// ConfiguredSummary 是界面渲染「模型」列表需要的信息。
type ConfiguredSummary struct {
	Provider  string         `json:"provider"`
	API       string         `json:"api,omitempty"`
	BaseURL   string         `json:"baseUrl,omitempty"`
	HasKey    bool           `json:"hasKey"`
	ModelList []ModelSummary `json:"models"`
}

// Configured 直接读 models.json，列出已配置的 provider 与模型。
//
// 与 get_available_models 的分工：
//
//	· get_available_models 是"运行时能用哪些模型"（要 Pi 在跑，含内置目录与鉴权状态）；
//	· 这里是"配置里写了什么"（不启动 Pi 也能看，用于设置页的编辑界面）。
//
// 设置页要能编辑配置，所以必须能读到"原始配置"，不能只看运行时的合并结果。
func Configured(modelsPath string) ([]ConfiguredSummary, error) {
	raw := map[string]any{}
	if data, err := os.ReadFile(modelsPath); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("models.json 不是合法 JSON: %w", err)
		}
	}
	providers, _ := raw["providers"].(map[string]any)
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]ConfiguredSummary, 0, len(names))
	for _, name := range names {
		entry, _ := providers[name].(map[string]any)
		summary := ConfiguredSummary{Provider: name}
		if api, ok := entry["api"].(string); ok {
			summary.API = api
		}
		if baseURL, ok := entry["baseUrl"].(string); ok {
			summary.BaseURL = baseURL
		}
		// 只回报"有没有 key"，绝不把 key 本身送进前端：界面上只需要一个状态点
		if key, ok := entry["apiKey"].(string); ok && strings.TrimSpace(key) != "" {
			summary.HasKey = true
		}
		models, _ := entry["models"].([]any)
		for _, item := range models {
			model, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := model["id"].(string)
			entryName, _ := model["name"].(string)
			window := 0
			if value, ok := model["contextWindow"].(float64); ok {
				window = int(value)
			}
			summary.ModelList = append(summary.ModelList, ModelSummary{ID: id, Name: entryName, ContextWindow: window})
		}
		out = append(out, summary)
	}
	return out, nil
}

// writeJSONAtomic 先写临时文件再改名：中途失败不会留下半截 JSON 把配置弄坏。
func writeJSONAtomic(path string, raw map[string]any) error {
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
