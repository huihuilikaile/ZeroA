package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 让"工作台自带的扩展工具"真的被启用。
//
// 为什么必须做这件事（实测踩到的坑）：
// Pi 的 `defaultTools` 默认只有 read/bash/edit/write，**扩展注册的工具默认是 inactive**，
// 要靠 `defaultTools` 或 `--tools` 显式启用（见 Pi 的 docs/settings.md：
// "Tools enabled at startup"、"built-in extensions register inactive, and other extension
// tools registered inactive"）。我们生成的 delegate-task.ts / tool-catalog.ts 里
// registerTool 一直是"注册了但没启用"，所以模型从来没见过 delegate_task、
// catalog_check 这些工具——功能写了却用不上。
//
// 写法用 `+name`：按官方语义，"只有 +name/-name 的列表是在继承的默认集合上做增删"，
// 于是 read/bash/edit/write 这些默认工具不会被顶掉。
var workbenchToolNames = []string{
	"delegate_task",
	"delegation_list",
	"delegation_stop",
	// 可继续子智能体的两个工具（仿 DSH 的 send_message / interrupt_agent）：
	// 不加进这张表，模型同样"看不到"，功能写了也用不上——这个坑上面刚踩过。
	"send_to_agent",
	"agent_interrupt",
	"catalog_list",
	"catalog_check",
	"catalog_add",
	"catalog_record",
}

// ensurePiDefaultTools 把工作台的扩展工具合并进 Pi 的 settings.json。
//
// 合并原则：**只加不减、其他人的键一个不动**。settings.json 是 Pi 的文件，
// 用户与 Pi 自己都会写它（模型、主题、packages…），我们不能整份覆盖。
func ensurePiDefaultTools(names []string) error {
	dir := agentDir()
	if strings.TrimSpace(dir) == "" {
		return errors.New("找不到配置目录")
	}
	path := filepath.Join(dir, "settings.json")

	settings := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	existing := []string{}
	if list, ok := settings["defaultTools"].([]any); ok {
		for _, item := range list {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				existing = append(existing, strings.TrimSpace(text))
			}
		}
	}

	/**
	 * 三种情况分开处理，避免把用户的选择顶掉：
	 *   · 没配过 → 直接写 `+名字`（在默认集合上增量启用，最安全）；
	 *   · 只配了 +/- 条目 → 追加我们缺的 `+名字`；
	 *   · 配了具体工具名（plain，语义是"替换默认集合"）→ 也追加，但记日志提醒
	 *     （用户既然写死了清单，我们不该替他改，但没人会希望自己的工具被悄悄漏掉）。
	 */
	hasPlain := false
	for _, item := range existing {
		if !strings.HasPrefix(item, "+") && !strings.HasPrefix(item, "-") {
			hasPlain = true
			break
		}
	}

	added := make([]string, 0, len(names))
	for _, name := range names {
		wanted := "+" + name
		found := false
		for _, item := range existing {
			if item == wanted || item == name {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, wanted)
			added = append(added, name)
		}
	}
	if len(added) == 0 {
		return nil
	}
	settings["defaultTools"] = existing

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	Logf("pi_default_tools_updated path=%s added=%s plainNames=%v", path, strings.Join(added, ","), hasPlain)
	return nil
}

// piConfiguredDefaults 读 Pi 配置（settings.json）里的默认模型与思考级别。
//
// 用途只有一个：进程还没起的时候，界面也要能显示"将要使用哪个模型"
// （模型徽标、设置页）。这些值是 Pi 自己写进 settings.json 的
// （defaultModel/defaultProvider/defaultThinkingLevel），读文件比为它启动
// 一个进程便宜得多；文件读不到/没配过时返回空串，界面按"未知"处理。
func piConfiguredDefaults() (provider, modelID, thinkingLevel string) {
	dir := agentDir()
	if strings.TrimSpace(dir) == "" {
		return "", "", ""
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return "", "", ""
	}
	var settings struct {
		DefaultProvider      string `json:"defaultProvider"`
		DefaultModel         string `json:"defaultModel"`
		DefaultThinkingLevel string `json:"defaultThinkingLevel"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return "", "", ""
	}
	return strings.TrimSpace(settings.DefaultProvider),
		strings.TrimSpace(settings.DefaultModel),
		strings.TrimSpace(settings.DefaultThinkingLevel)
}
