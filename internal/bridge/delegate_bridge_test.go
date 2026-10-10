package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"zeroa/internal/delegation"
)

// 桥的令牌是**唯一**的防线：端口只绑本机，但本机上任何进程都能连过来。
// 所以"没带令牌 / 带错令牌一律拒绝"必须有测试盯着。
func TestDelegateBridgeRejectsBadToken(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	if err := service.ensureBridge(); err != nil {
		t.Fatalf("ensureBridge: %v", err)
	}
	manager := service.delegationManager()
	address := manager.BridgeAddress()
	token := manager.BridgeToken()
	if address == "" || token == "" {
		t.Fatal("桥没起来")
	}
	t.Cleanup(func() { manager.StopBridge() })

	for _, path := range []string{"/delegate", "/agents", "/kill", "/health"} {
		// 不带令牌
		response, err := http.Post(address+path, "application/json", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("%s 不带令牌应当 403，实际 %d", path, response.StatusCode)
		}
		_ = response.Body.Close()

		// 带错令牌
		request, _ := http.NewRequest(http.MethodPost, address+path, bytes.NewReader([]byte(`{}`)))
		request.Header.Set("x-delegate-token", "wrong-token")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("%s 带错令牌应当 403，实际 %d", path, response.StatusCode)
		}
		_ = response.Body.Close()
	}

	// 令牌正确：健康检查通过
	request, _ := http.NewRequest(http.MethodPost, address+"/health", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("x-delegate-token", token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("令牌正确时健康检查应当 200，实际 %d", response.StatusCode)
	}

	// 令牌不进日志（只给前几位用于排障）
	if strings.Contains(manager.TokenPreview(), token) {
		t.Error("日志预览不该泄露完整令牌")
	}
}

// 通过桥真的派一个子智能体：这条路径就是模型调用 delegate_task 时走的。
func TestDelegateBridgeSpawnsAgent(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}},
	}, "")
	saveRoleForTest(t, service, Role{ID: "ctf-recon", Name: "资料检索", Kind: "agent", Tools: []string{"read"}}, "")

	if err := service.Start("mock", "", t.TempDir(), "", "", "", "ctf"); err != nil {
		t.Fatalf("启动父会话: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	waitForState(t, service)
	service.noteParentToolCall("")

	manager := service.delegationManager()
	address := manager.BridgeAddress()
	token := manager.BridgeToken()
	if address == "" {
		t.Fatal("Start 之后桥应当在")
	}
	// 扩展拿到的两个环境变量必须与桥一致（否则扩展连不上，且症状很隐蔽）
	env := service.bridgeEnvFor()
	if env[delegation.DelegateURLEnv] != address || env[delegation.DelegateTokenEnv] != token {
		t.Fatalf("注入的环境变量与桥不一致: %+v", env)
	}

	post := func(path string, payload any) map[string]any {
		body, _ := json.Marshal(payload)
		request, _ := http.NewRequest(http.MethodPost, address+path, bytes.NewReader(body))
		request.Header.Set("x-delegate-token", token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer response.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
			t.Fatalf("解析 %s 响应: %v", path, err)
		}
		return out
	}

	// 1) 派活
	result := post("/delegate", map[string]string{"role": "ctf-recon", "task": "查资料"})
	if result["ok"] != true {
		t.Fatalf("派活应当成功: %+v", result)
	}
	agentID, _ := result["agentId"].(string)
	if agentID == "" {
		t.Fatalf("应当返回 agentId: %+v", result)
	}
	if message, _ := result["message"].(string); !strings.Contains(message, "继续") {
		t.Errorf("回给模型的话要说清「你可以继续」：%q", message)
	}

	// 2) 查列表能看到它
	listed := post("/agents", map[string]any{})
	agents, _ := listed["agents"].([]any)
	if len(agents) == 0 {
		t.Fatalf("列表里应当有刚派的子智能体: %+v", listed)
	}

	// 3) 闸门被挡时返回 ok=false 而不是 HTTP 错误——模型需要看到原因，不是 500
	blocked := post("/delegate", map[string]string{"role": "ctf-route", "task": "越权"})
	if blocked["ok"] != false {
		t.Errorf("白名单外的角色应当返回 ok=false: %+v", blocked)
	}
	if message, _ := blocked["message"].(string); message == "" {
		t.Error("被挡时要说清原因")
	}

	// 4) 停掉它
	killed := post("/kill", map[string]string{"agentId": agentID})
	if killed["ok"] != true {
		t.Errorf("kill 应当成功: %+v", killed)
	}
}
