package bridge

import (
	"os"
	"strings"
	"testing"
	"time"

	"zeroa/internal/mockpi"
)

// 测试二进制要能当"假 Pi"用：mock 模式启动的是 os.Executable() 加 --mock-pi，
// 在测试里那就是测试二进制本身，所以这里拦一下参数，交给 mockpi.Serve。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--mock-pi" {
		mockpi.Serve(os.Stdin, os.Stdout)
		return
	}
	os.Exit(m.Run())
}

// waitIdle 等到 mock 把这一轮跑完（agent_settled 之后 get_state 会回到 isStreaming=false）。
func waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if _, err := svc.GetState(); err == nil {
			// mock 在 agent_settled 之后不再发事件；这里只要求进程仍在响应
			return
		}
	}
	t.Fatal("mock 没有在 10 秒内回到可响应状态")
}

var svc *PiService

/**
 * warmUpSession 让当前会话**真的有一个 Pi 进程**。
 *
 * Start 不再创建进程（会话=日志，进程留到第一条消息，见 service.go 的 Start
 * 与 session_history.go 开头"为什么读历史不需要进程"的说明）。于是"需要一个能应答的
 * 进程"的用例必须先显式触发一次懒启动——这与界面上发出第一条消息走的是**同一个入口**
 * （ensureActiveRuntime），区别只是不发那条消息。
 *
 * 为什么不让测试直接调 startRuntime：那会绕开懒启动真正要保证的两件事
 * （先 switch_session 到"用户在看的那条会话"、下发角色的自动压缩），
 * 测出来的东西会与线上不一致。
 */
func warmUpSession(t *testing.T, service *PiService) {
	t.Helper()
	if err := service.ensureActiveRuntime("test_warmup"); err != nil {
		t.Fatalf("懒启动进程失败: %v", err)
	}
}

// TestServiceEndToEnd 走一遍界面真正会调用的那些方法（全部打在 mock 上，不花钱）。
func TestServiceEndToEnd(t *testing.T) {
	// 模拟器默认 18ms/字符，一轮要几秒；测协议时把它调快，
	// 否则「扩展请求到达」会正好卡在轮询窗口边缘（踩过一次）。
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "2")
	svc = &PiService{}
	if err := svc.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	// 1) 连接自检：Start 只准备了启动参数模板，此刻**还没有进程**——
	//    GetState 必须能答上来（返回合成状态），否则前端启动流程会把它当成失败并弹报错条。
	state, err := svc.GetState()
	if err != nil {
		t.Fatalf("GetState（无进程时也必须能答）: %v", err)
	}
	if !state.Success || state.Command != "get_state" {
		t.Fatalf("get_state 返回异常: %+v", state)
	}
	if !strings.Contains(state.Data, `"pending":true`) {
		t.Errorf("无进程时的 get_state 应当标明 pending，实际 %s", state.Data)
	}
	// 1b) 懒启动：界面上这一步就是"用户发出第一条消息"，这里显式触发一次
	warmUpSession(t, svc)

	// 2) 模型：字段名是 modelId，mock 按真实 Pi 只认这个键（只认它自己列表里的 id）
	models, err := svc.GetAvailableModels()
	if err != nil || !models.Success {
		t.Fatalf("GetAvailableModels: %v %+v", err, models)
	}
	set, err := svc.SetModel("mock", "mock-fast")
	if err != nil {
		t.Fatalf("SetModel: %v（注意真实 Pi 认的字段是 modelId）", err)
	}
	if !set.Success {
		t.Fatalf("SetModel 被拒绝: %+v", set)
	}

	// 3) 思考级别：真实 Pi 只回 levels，没有 current
	if levels, err := svc.GetAvailableThinkingLevels(); err != nil || !levels.Success {
		t.Fatalf("GetAvailableThinkingLevels: %v %+v", err, levels)
	}
	if level, err := svc.SetThinkingLevel("high"); err != nil || !level.Success {
		t.Fatalf("SetThinkingLevel: %v %+v", err, level)
	}

	// 4) 对话：prompt 的 response 只代表"已接受"，随后事件流才会跑完。
	//    文案里带「确认」时，mock 会像真实扩展那样发一条 extension_ui_request 并等待回应。
	prompt, err := svc.Prompt("请先确认一下：把 README 补一段安装说明")
	if err != nil || !prompt.Success {
		t.Fatalf("Prompt: %v %+v", err, prompt)
	}

	// 5) 插入（steering）与清空队列
	steer, err := svc.Steer("顺便加一句 --no-tools 的说明")
	if err != nil || !steer.Success {
		t.Fatalf("Steer: %v %+v", err, steer)
	}
	if cleared, err := svc.ClearQueue(); err != nil || !cleared.Success {
		t.Fatalf("ClearQueue: %v %+v", err, cleared)
	}

	// 6) 扩展 UI 回应：前端把收到的整条 JSON 原样传进来即可（关联键由 Go 侧映射）
	var uiErr error
	for i := 0; i < 100; i++ {
		if _, uiErr = svc.RespondUI(`{"type":"extension_ui_response","id":"mock-ui-1","confirmed":true}`); uiErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if uiErr != nil {
		t.Fatalf("RespondUI: %v", uiErr)
	}
	if _, err := svc.RespondUI(`{"type":"nope"}`); err == nil {
		t.Fatal("RespondUI 应该拒绝非 extension_ui_response 的载荷")
	}

	// 7) 停止：只停当前回合，进程要还活着
	if abort, err := svc.Abort(); err != nil || !abort.Success {
		t.Fatalf("Abort: %v %+v", err, abort)
	}
	waitIdle(t)

	if after, err := svc.GetState(); err != nil || !after.Success {
		t.Fatalf("abort 之后进程不可用: %v %+v", err, after)
	}

	// 8) 会话消息与统计
	if messages, err := svc.GetMessages(); err != nil || !messages.Success {
		t.Fatalf("GetMessages: %v %+v", err, messages)
	}
	if stats, err := svc.GetSessionStats(); err != nil || !stats.Success {
		t.Fatalf("GetSessionStats: %v %+v", err, stats)
	}
}

// TestServiceRejectsBadInput 覆盖界面可能传错的两类输入
func TestServiceRejectsBadInput(t *testing.T) {
	if _, err := (&PiService{}).GetState(); err == nil {
		t.Fatal("未启动时 GetState 应该报错")
	}

	s := &PiService{}
	if err := s.Start("mock", "", t.TempDir(), "", "", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	if _, err := s.Prompt("   "); err == nil {
		t.Fatal("空消息应该被拒绝")
	}
	if _, err := s.Prompt(string(make([]byte, 33000))); err == nil {
		t.Fatal("超过 32KB 的消息应该被拒绝")
	}
	if _, err := s.SetModel("deepseek", "  "); err == nil {
		t.Fatal("空模型名应该被拒绝")
	}
}
