package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 模型配置的读写必须用临时 HOME 测：这条路径直接操作 ~/.pi/agent/models.json，
// 测试里绝不允许碰到用户真实配置。四个不变量：
//  1. 新增会写进 providers.<name>.models，同 id 视为更新而不是追加；
//  2. 写之前留一份 .bak；
//  3. 删除模型后，provider 下没有模型时连 provider 一起去掉；
//  4. 列出来的 key 状态只有"有没有"，不泄露 key 本身。
func TestModelConfigRoundTrip(t *testing.T) {
	home := t.TempDir()
	// 配置目录现在是"应用同目录优先"：本测试要的是一个干净的 agent 目录，所以显式指定
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "models.json")
	initial := `{
  "providers": {
    "ollama": {
      "api": "openai-completions",
      "apiKey": "secret-key-should-not-leak",
      "baseUrl": "http://127.0.0.1:11434/v1",
      "models": [{"id": "gemma4:e4b", "contextWindow": 131072}]
    }
  }
}
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &PiService{}

	// 1) 新增模型到已有 provider
	if result, err := svc.AddModel("ollama", "qwen3:8b", "Qwen3 8B", "", "", "", 32768, true); err != nil || !result.Success {
		t.Fatalf("AddModel: %v %+v", err, result)
	}
	// 2) 同 id 再写一次 = 更新，不应产生第二条
	if _, err := svc.AddModel("ollama", "qwen3:8b", "Qwen3 8B v2", "", "", "", 65536, true); err != nil {
		t.Fatalf("AddModel(update): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]struct {
			API    string `json:"api"`
			APIKey string `json:"apiKey"`
			Models []struct {
				ID            string   `json:"id"`
				Name          string   `json:"name"`
				ContextWindow int      `json:"contextWindow"`
				Input         []string `json:"input"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("写出来的不是合法 JSON: %v", err)
	}
	ollama := parsed.Providers["ollama"]
	if len(ollama.Models) != 2 {
		t.Fatalf("同 id 应该更新而不是追加，实际 %d 条", len(ollama.Models))
	}
	if ollama.Models[0].ContextWindow != 131072 {
		t.Errorf("原有模型不该被改瘦: %+v", ollama.Models[0])
	}
	var added bool
	for _, model := range ollama.Models {
		if model.ID == "qwen3:8b" {
			added = true
			if model.Name != "Qwen3 8B v2" || model.ContextWindow != 65536 {
				t.Errorf("更新后的字段不对: %+v", model)
			}
			if len(model.Input) != 2 {
				t.Errorf("声明支持图片时应写入 input: text,image，实际 %v", model.Input)
			}
		}
	}
	if !added {
		t.Fatal("新模型没有写进配置")
	}
	if ollama.API != "openai-completions" {
		t.Errorf("已有 provider 的 api 不该被清掉: %q", ollama.API)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("写之前应该留一份 .bak 备份")
	}

	// 3) 列表：只看得到"有没有 key"，看不到 key 本身
	result, err := svc.ListConfiguredProviders()
	if err != nil || !result.Success {
		t.Fatalf("ListConfiguredProviders: %v %+v", err, result)
	}
	if json.Valid([]byte(result.Data)) == false {
		t.Fatalf("返回的 data 不是合法 JSON: %s", result.Data)
	}
	if containsSecret(result.Data) {
		t.Fatal("列表里不该出现 apiKey 的值")
	}
	var listed struct {
		Providers []struct {
			Provider string `json:"provider"`
			HasKey   bool   `json:"hasKey"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(result.Data), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Providers) != 1 || !listed.Providers[0].HasKey {
		t.Fatalf("应该列出 ollama 且标记已配置 key: %+v", listed.Providers)
	}

	// 4) 删到只剩最后一个时，provider 一起消失
	if _, err := svc.DeleteModel("ollama", "qwen3:8b"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if _, err := svc.DeleteModel("ollama", "gemma4:e4b"); err != nil {
		t.Fatalf("DeleteModel(last): %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	providers, _ := after["providers"].(map[string]any)
	if len(providers) != 0 {
		t.Fatalf("最后一个模型被删后 provider 也该删掉，实际剩下 %v", providers)
	}

	// 5) 非法输入要挡住
	if _, err := svc.AddModel("", "x", "", "", "", "", 0, false); err == nil {
		t.Error("provider 为空应该报错")
	}
	if _, err := svc.AddModel("a/b", "x", "", "", "", "", 0, false); err == nil {
		t.Error("provider 带斜杠应该报错")
	}
	if _, err := svc.DeleteModel("ollama", "not-there"); err == nil {
		t.Error("删不存在的模型应该报错")
	}
}

// containsSecret 检查输出里是否夹带了 key 的值（脱敏测试用）。
func containsSecret(text string) bool {
	return len(text) > 0 && jsonContains(text, "secret-key-should-not-leak")
}

func jsonContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
