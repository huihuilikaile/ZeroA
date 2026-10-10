package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/**
 * ★ 工作台自带的扩展工具必须被启用——这是"功能写了却用不上"的那个坑。
 *
 * Pi 的 defaultTools 默认只有 read/bash/edit/write，扩展注册的工具是 inactive 的；
 * 不把 delegate_task / catalog_* 这些名字加进去，模型根本看不到它们。
 */
func TestEnsurePiDefaultToolsEnablesWorkbenchTools(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	// ① 没有 settings.json：创建，用 `+名字` 增量启用（不顶掉默认的 read/bash/edit/write）
	if err := ensurePiDefaultTools([]string{"delegate_task", "catalog_check"}); err != nil {
		t.Fatalf("ensurePiDefaultTools: %v", err)
	}
	settings := readSettingsForTest(t, agentDirPath)
	tools := settings["defaultTools"].([]any)
	if len(tools) != 2 || tools[0] != "+delegate_task" {
		t.Errorf("应当用 + 前缀增量启用: %v", tools)
	}

	// ② 再跑一次不重复添加
	if err := ensurePiDefaultTools([]string{"delegate_task", "catalog_add"}); err != nil {
		t.Fatal(err)
	}
	tools = readSettingsForTest(t, agentDirPath)["defaultTools"].([]any)
	if len(tools) != 3 {
		t.Errorf("重复调用不该重复添加: %v", tools)
	}

	// ③ 用户自己配的 defaultTools（用 +/- 修改继承集合）要保留，只追加缺的
	writeSettingsForTest(t, agentDirPath, map[string]any{
		"defaultTools": []any{"+codemode", "-bash"},
		"theme":        "dark",
	})
	if err := ensurePiDefaultTools([]string{"catalog_list"}); err != nil {
		t.Fatal(err)
	}
	updated := readSettingsForTest(t, agentDirPath)
	tools = updated["defaultTools"].([]any)
	joined := strings.Join(toStrings(tools), ",")
	if !strings.Contains(joined, "+codemode") || !strings.Contains(joined, "-bash") || !strings.Contains(joined, "+catalog_list") {
		t.Errorf("用户已有条目必须保留: %v", tools)
	}
	if updated["theme"] != "dark" {
		t.Errorf("别的键不能被改: %+v", updated)
	}

	// ④ 用户写死了具体工具名（plain，语义是替换默认集合）：我们只追加、不删除，并记日志
	writeSettingsForTest(t, agentDirPath, map[string]any{"defaultTools": []any{"read", "bash"}})
	if err := ensurePiDefaultTools([]string{"delegate_task"}); err != nil {
		t.Fatal(err)
	}
	tools = readSettingsForTest(t, agentDirPath)["defaultTools"].([]any)
	joined = strings.Join(toStrings(tools), ",")
	if !strings.Contains(joined, "read") || !strings.Contains(joined, "+delegate_task") {
		t.Errorf("plain 条目要保留，同时追加我们需要的: %v", tools)
	}

	// ⑤ 已全部存在时不动文件（避免每次启动都写盘）
	before, _ := os.Stat(filepath.Join(agentDirPath, "settings.json"))
	if err := ensurePiDefaultTools([]string{"delegate_task"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(agentDirPath, "settings.json"))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("没有变化时不该重写 settings.json")
	}
}

// 坏 JSON 要报错而不是把 Pi 的 settings.json 写坏。
func TestEnsurePiDefaultToolsRejectsBrokenSettings(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	path := filepath.Join(agentDirPath, "settings.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensurePiDefaultTools([]string{"delegate_task"}); err == nil {
		t.Fatal("坏文件应当报错（绝不能把用户的配置覆盖成一个空文件）")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{ not json" {
		t.Error("报错时不能改动原文件")
	}
}

func readSettingsForTest(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("写出来的不是合法 JSON: %v\n%s", err, string(raw))
	}
	return settings
}

func writeSettingsForTest(t *testing.T, dir string, settings map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func toStrings(list []any) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
