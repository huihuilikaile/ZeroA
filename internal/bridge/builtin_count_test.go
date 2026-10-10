package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zeroa/internal/providers"
)

// 界面靠 builtinCount 判断"要不要提示内置目录没读到"。
// 0 必须真的出现在返回里，否则降级时用户只会看到空列表而不知道为什么。
func TestListProvidersReportsBuiltinCount(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"),
		[]byte(`{"providers":{"demo":{"api":"openai-completions","models":[{"id":"demo-1"}]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// 情况一：bundle 缺失（把 PI_AGENT_HOME 指向空目录）→ builtinCount 必须是 0
	providers.ResetCache()
	t.Setenv("PI_AGENT_HOME", t.TempDir())
	result, err := (&PiService{}).ListProviders()
	if err != nil || !result.Success {
		t.Fatalf("ListProviders: %v", err)
	}
	var payload struct {
		Providers []struct {
			Provider string `json:"provider"`
		} `json:"providers"`
		BuiltinCount int `json:"builtinCount"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	// 必须真的存在这个键：字段缺失时 Unmarshal 也会给 0，那样断言等于没测
	if !strings.Contains(result.Data, `"builtinCount"`) {
		t.Fatalf("返回里必须有 builtinCount 字段，实际: %s", result.Data)
	}
	if payload.BuiltinCount != 0 {
		t.Errorf("bundle 缺失时 builtinCount 应为 0（界面据此提示），实际 %d", payload.BuiltinCount)
	}
	if len(payload.Providers) != 1 || payload.Providers[0].Provider != "demo" {
		t.Errorf("没有内置目录时仍应列出已配置的提供商，实际 %+v", payload.Providers)
	}
}
