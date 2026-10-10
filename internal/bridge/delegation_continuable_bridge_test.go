package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"zeroa/internal/agents"
	"zeroa/internal/delegation"
)

/**
 * 桥上的两个新端点：`/message` 与 `/interrupt`。
 *
 * 它们就是父模型调用 `send_to_agent` / `agent_interrupt` 时真正打到的地方
 * （扩展只是把 HTTP 包一层）。所以这一层测通了，才算"模型能继续跟子智能体说话"
 * 这条链路真的通了——Supervisor 的单测覆盖不到桥与服务的接线。
 */
func TestDelegateBridgeMessageAndInterrupt(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	// mock 每轮很快：本用例测的是**桥与服务的接线**，不是中断时序
	// （"运行中中断只停一轮"由 internal/agents 的 TestInterruptKeepsContinuableAlive 覆盖）。
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")

	service := &PiService{}
	seedRoles(t, service)
	saveRoleForTest(t, service, Role{
		ID: "ctf", Name: "CTF 综合",
		// 默认形态就是 continuable；这里显式写出来，让用例表达"我在测可继续形态"
		Delegation: &Delegation{Enabled: true, AllowedRoles: []string{"ctf-recon"}, Lifecycle: "continuable"},
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
	post := func(path string, payload any) map[string]any {
		t.Helper()
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

	// ① 派活
	delegated := post("/delegate", map[string]string{"role": "ctf-recon", "task": "第一轮：查资料"})
	if delegated["ok"] != true {
		t.Fatalf("派活应当成功: %+v", delegated)
	}
	agentID, _ := delegated["agentId"].(string)
	if agentID == "" {
		t.Fatalf("派活回执里应当带 agentId: %+v", delegated)
	}
	if delegated["mode"] != "continuable" {
		t.Fatalf("形态应当是 continuable，回执：%+v", delegated)
	}

	sup := service.supervisorFor()
	// ② 等它这一轮得出结论（可继续形态：结论到达 ≠ 实例结束）
	if _, ok := sup.WaitTurn(t.Context(), agentID, 20*time.Second); !ok {
		t.Fatal("WaitTurn 找不到子智能体")
	}
	first, _ := sup.Get(agentID)
	if first == nil || first.Turns < 1 {
		t.Fatalf("第一轮应当已经结束: %+v", first)
	}

	// ③ 空闲时给它发消息：应当开新一轮，而不是"已结束不能发"
	sent := post("/message", map[string]string{"agentId": agentID, "message": "追加一句：再看一下 XX"})
	if sent["ok"] != true {
		t.Fatalf("给空闲的可继续子智能体发消息应当成功: %+v", sent)
	}
	if id, _ := sent["messageId"].(string); id == "" {
		t.Error("受理凭证（messageId）应当返回给模型——照 DSH，回复不是本次调用的返回值，凭据才是")
	}
	// 等第二轮
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if snap, ok := sup.Get(agentID); ok && snap.Turns >= 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	second, _ := sup.Get(agentID)
	if second == nil || second.Turns < 2 {
		t.Fatalf("追问应当让它再跑一轮（Turns 应 ≥2），实际 %+v", second)
	}
	if second.Status.Finished() {
		t.Fatalf("可继续子智能体不该在追问后结束: %s", second.Status)
	}

	// ④ 中断：对空闲实例是**空操作但受理**（照 DSH 的语义），关键是它不会被销毁
	interrupted := post("/interrupt", map[string]string{"agentId": agentID})
	if interrupted["ok"] != true {
		t.Fatalf("中断应当被受理: %+v", interrupted)
	}
	if msg, _ := interrupted["message"].(string); !strings.Contains(msg, "还在") {
		t.Errorf("中断的回复应当说明它没有被销毁，实际：%q", msg)
	}
	if snap, ok := sup.Get(agentID); !ok || snap.Status.Finished() {
		t.Fatalf("中断不该销毁可继续子智能体，实际 %+v", snap)
	}
	// ⑤ 一次性子智能体不能收消息：显式派一个 one-shot
	sup2 := service.supervisorFor()
	oneShot, err := sup2.Spawn(agents.SpawnRequest{
		ParentID: delegation.MainAgentID, RoleID: "ctf-recon", Task: "一次性任务", Mode: agents.ModeOneShot,
	})
	if err != nil {
		t.Fatalf("派一次性子智能体: %v", err)
	}
	rejected := post("/message", map[string]string{"agentId": oneShot.ID, "message": "喂"})
	if rejected["ok"] == true {
		t.Fatalf("一次性子智能体不该接受消息: %+v", rejected)
	}
	if msg, _ := rejected["message"].(string); !strings.Contains(msg, "一次性") {
		t.Errorf("拒绝原因应当说清是形态问题，实际：%q", msg)
	}
}
