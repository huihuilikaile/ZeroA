package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 会话名记录的真正类型是 session_info（见 pi 的 docs/session-format.md）。
// 早前判的是"类型里含 name"，永远命不中，于是改名后列表仍显示第一条用户消息——
// 这条测试专门钉住这个字段名。
func TestListSessionsPrefersSessionInfoName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	cwd := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	body := strings.Join([]string{
		`{"type":"session","version":3,"id":"abc","timestamp":"2026-10-04T00:00:00.000Z","cwd":` + jsonString(cwd) + `}`,
		`{"type":"message","id":"m1","message":{"role":"user","content":"这是第一条用户消息，不该成为标题","timestamp":1}}`,
		`{"type":"session_info","id":"s1","parentId":"m1","timestamp":"2026-10-04T00:01:00.000Z","name":"我的功能分支"}`,
	}, "\n") + "\n"
	path := filepath.Join(root, "--ws--", "2026-10-04T00-00-00-000Z_abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := (&PiService{}).ListSessions(cwd)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	var data struct {
		Sessions []struct {
			Title string `json:"title"`
			Name  string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("解析结果: %v", err)
	}
	if len(data.Sessions) != 1 {
		t.Fatalf("应该有 1 条会话，实际 %d", len(data.Sessions))
	}
	if data.Sessions[0].Title != "我的功能分支" {
		t.Errorf("标题应该取 session_info.name，实际 %q", data.Sessions[0].Title)
	}
}

// set_model 要求 provider 与模型同时精确匹配：provider 为空时 Pi 只会回
// "Model not found"，所以要在 Go 侧就用一句能看懂的话挡住。
func TestSetModelRequiresProvider(t *testing.T) {
	svc := &PiService{}
	if _, err := svc.SetModel("", "deepseek-flash"); err == nil {
		t.Fatal("provider 为空时应该报错（Pi 不会自动推断 provider）")
	}
	if _, err := svc.SetModel("   ", "deepseek-flash"); err == nil {
		t.Fatal("provider 只有空白也应该报错")
	}
	if _, err := svc.SetModel("deepseek", "  "); err == nil {
		t.Fatal("模型为空应该报错")
	}
}

// 会话目录要认 Pi 设置里的 sessionDir：配到别处时我们还在老地方找，
// 界面上就会表现为"一个会话都没有"。
func TestSessionsRootHonoursSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读这个
	t.Setenv("HOME", home)        // 其它平台
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	// 配置目录现在是"应用同目录优先"，所以这里显式钉住 agent 目录：
	// 本测试测的是"agent 目录里的 settings.json 生效"，不是解析顺序。
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))

	custom := filepath.Join(home, "custom-sessions")
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"sessionDir":`+jsonString(custom)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := sessionsRoot(); got != custom {
		t.Errorf("应该读 settings.json 的 sessionDir，得到 %q", got)
	}

	// 环境变量优先级更高
	override := filepath.Join(home, "env-sessions")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", override)
	if got := sessionsRoot(); got != override {
		t.Errorf("环境变量应该优先，得到 %q", got)
	}
}

// 配置目录本身也可以被 PI_CODING_AGENT_DIR 挪走（见 1.0.2 的
// docs/configuration.md 与 docs/environment-variables.md）。
// 我们读写 settings.json / models.json / sessions 都要跟着走，
// 否则用户把配置放进项目里做便携版时，界面会显示"没有会话、没有模型"。
func TestAgentDirOverride(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", custom)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")

	if got := agentDir(); got != custom {
		t.Errorf("agentDir 应该认 PI_CODING_AGENT_DIR，得到 %q", got)
	}
	if got := sessionsRoot(); got != filepath.Join(custom, "sessions") {
		t.Errorf("会话根目录应该落在新的 agent 目录下，得到 %q", got)
	}

	// 新目录里的 settings.json 同样要生效
	sessionDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(custom, "settings.json"), []byte(`{"sessionDir":`+jsonString(sessionDir)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sessionsRoot(); got != sessionDir {
		t.Errorf("新 agent 目录里的 sessionDir 应该被采纳，得到 %q", got)
	}

	// 读模型配置也要落在新目录里：写一个 provider，列表就应该看得见
	if err := os.WriteFile(filepath.Join(custom, "models.json"),
		[]byte(`{"providers":{"demo":{"api":"openai-completions","models":[{"id":"demo-1"}]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&PiService{}).ListConfiguredProviders()
	if err != nil || !result.Success {
		t.Fatalf("ListConfiguredProviders: %v %+v", err, result)
	}
	if !strings.Contains(result.Data, "demo-1") {
		t.Errorf("应该从新的 agent 目录读到模型，实际: %s", result.Data)
	}
}
