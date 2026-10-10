package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 凭据写入必须照 Pi 的 auth-storage.js：类型字符串是 "api_key"（带下划线）或 "oauth"，
// 字段名是 key，文件权限 0600，且返回值里不能夹带密钥。
// 这里用临时配置目录测，绝不碰用户真实的 auth.json。
func TestSetProviderKeyWritesPiShapedCredentials(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	authPath := filepath.Join(agentDirPath, "auth.json")
	// 已有一个 oauth 条目：换 key 时必须保留它的 type，否则会把 OAuth 凭据弄坏
	seed := `{"github-copilot":{"type":"oauth","key":"old-token"},"_comment":"keep me"}`
	if err := os.WriteFile(authPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	service := &PiService{}
	const secret = "sk-test-should-not-leak"

	// 1) 新提供商：type 必须是 api_key
	result, err := service.SetProviderKey("openai", secret)
	if err != nil || !result.Success {
		t.Fatalf("SetProviderKey: %v %+v", err, result)
	}
	if strings.Contains(result.Data, secret) {
		t.Fatal("返回值里不该出现密钥本身")
	}

	// 2) 已有 oauth 条目：只换 key，type 不变
	if _, err := service.SetProviderKey("github-copilot", "new-token"); err != nil {
		t.Fatalf("SetProviderKey(oauth): %v", err)
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("写出来的不是合法 JSON: %v", err)
	}
	entry := func(name string) map[string]any {
		value, _ := parsed[name].(map[string]any)
		return value
	}
	if got := entry("openai")["type"]; got != "api_key" {
		t.Errorf("新条目的 type 必须是 api_key（Pi 只认这个），实际 %v", got)
	}
	if got := entry("openai")["key"]; got != secret {
		t.Errorf("key 字段没写对: %v", got)
	}
	if got := entry("github-copilot")["type"]; got != "oauth" {
		t.Errorf("已有 oauth 条目的 type 不该被改掉，实际 %v", got)
	}
	if got := entry("github-copilot")["key"]; got != "new-token" {
		t.Errorf("oauth 条目的 key 应该被替换，实际 %v", got)
	}
	if _, ok := parsed["_comment"]; !ok {
		t.Error("不该丢掉 auth.json 里原有的其它字段")
	}

	// 3) 权限必须是 0600（这是凭据文件）。
	//    Windows 上 Unix 权限位没有意义（Go 的 mode 只映射只读属性，
	//    实际 ACL 由父目录继承），所以这条只在类 Unix 平台断言——
	//    在 Windows 上假装它能测出来才是更糟的做法。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(authPath)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("auth.json 权限应为 0600，实际 %o", mode)
		}
	}

	// 4) 写之前要留一份备份
	if _, err := os.Stat(authPath + ".bak"); err != nil {
		t.Error("写之前应该备份 auth.json.bak")
	}

	// 5) 空输入要挡住
	if _, err := service.SetProviderKey("", "x"); err == nil {
		t.Error("缺 provider 应该报错")
	}
	if _, err := service.SetProviderKey("openai", "  "); err == nil {
		t.Error("缺密钥应该报错")
	}
}

// 提供商列表要覆盖"Pi 已知但还没配置"的那些：只列 models.json 会让用户以为没得选。
func TestListProvidersIncludesBuiltinAndCredentialOnlyProviders(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	// models-store.json 是 Pi 的内置目录缓存
	if err := os.WriteFile(filepath.Join(agentDirPath, "models-store.json"),
		[]byte(`{"openai":{"models":[{"id":"gpt-5"},{"id":"gpt-5-mini"}]},"anthropic":{"models":[{"id":"claude-x"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// models.json 是用户配置过的
	if err := os.WriteFile(filepath.Join(agentDirPath, "models.json"),
		[]byte(`{"providers":{"ollama":{"api":"openai-completions","models":[{"id":"gemma"}]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// auth.json 只有凭据
	if err := os.WriteFile(filepath.Join(agentDirPath, "auth.json"),
		[]byte(`{"deepseek":{"type":"api_key","key":"k"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := (&PiService{}).ListProviders()
	if err != nil || !result.Success {
		t.Fatalf("ListProviders: %v %+v", err, result)
	}
	var payload struct {
		Providers []struct {
			Provider   string `json:"provider"`
			Configured bool   `json:"configured"`
			HasKey     bool   `json:"hasKey"`
			Builtin    bool   `json:"builtin"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	byName := map[string]struct {
		Configured bool
		HasKey     bool
		Builtin    bool
	}{}
	for _, row := range payload.Providers {
		byName[row.Provider] = struct {
			Configured bool
			HasKey     bool
			Builtin    bool
		}{row.Configured, row.HasKey, row.Builtin}
	}
	if len(byName) != 4 {
		t.Fatalf("三处合并后应该有 4 个提供商（openai/anthropic/ollama/deepseek），实际 %d: %v", len(byName), byName)
	}
	if row := byName["openai"]; !row.Builtin || row.Configured {
		t.Errorf("openai 应标记为内置且未配置: %+v", row)
	}
	if row := byName["anthropic"]; !row.Builtin {
		t.Errorf("anthropic 应来自内置目录: %+v", row)
	}
	if row := byName["ollama"]; !row.Configured || row.Builtin {
		t.Errorf("ollama 应为已配置的自定义提供商: %+v", row)
	}
	if row := byName["deepseek"]; !row.HasKey {
		t.Errorf("deepseek 应标记为已配置凭据: %+v", row)
	}
	// 已配置的排在前面（列表里用户最先要看到自己配过的）
	if payload.Providers[0].Provider != "ollama" {
		t.Errorf("已配置的提供商应排在最前，实际第一行是 %s", payload.Providers[0].Provider)
	}
}
