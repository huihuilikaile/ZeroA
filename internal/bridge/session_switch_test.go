package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zeroa/internal/pi"
)

/**
 * 会话切换的回归测试（**一进程一会话 + 懒启动**之后的契约）。
 *
 * 以前的这一批用例测的是"排队"：回合运行中点另一个会话时，后端只记住目标、
 * 等 agent_settled 之后再切。那是单进程时代的补丁，现在架构换了，契约也换了：
 *
 *   1. **打开**一条会话不起进程：只记住"用户在看它"（pendingSessionFile），
 *      历史由 LoadSessionHistory 直接读 .jsonl，进程留到第一条消息；
 *   2. 第一条消息起进程时必须**先 switch_session 再发 prompt**（否则消息落进空会话）；
 *   3. 切换**不碰**正在干活的那个进程（既不发 switch_session，也不发 abort）；
 *   4. 已经开着的会话**复用原进程**，热切换**不重载历史**；
 *   5. 打开失败不改变当前会话；
 *   6. 并发点同一条会话只会开出一个进程；
 *   7. 改名/删除后台会话不影响前台。
 *
 * 这些正是"切换会话会被中断"那个问题的结构性保证：不是"我们小心不去打断"，
 * 而是"根本没有代码路径会去动它"。
 */

// sessionFixture 造一份最小合法的会话文件（只有会话头，够 readSessionHeader 用）。
func sessionFixture(t *testing.T, root, cwd, name string) string {
	t.Helper()
	path := filepath.Join(root, name+".jsonl")
	header, _ := json.Marshal(map[string]string{"type": "session", "id": name, "cwd": cwd})
	if err := os.WriteFile(path, append(header, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// pendingPayload 解析"待创建"那次切换的返回。
func pendingPayload(t *testing.T, result Result) (pending bool, sessionPath, runtimeID string) {
	t.Helper()
	var payload struct {
		Pending     bool   `json:"pending"`
		SessionPath string `json:"sessionPath"`
		RuntimeID   string `json:"runtimeId"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析切换结果失败: %v（%s）", err, result.Data)
	}
	return payload.Pending, payload.SessionPath, payload.RuntimeID
}

// 打开一条会话**不起进程**：只记住"用户在看它"，历史走文件。
//
// 这砍掉的是启动时间线里那两次冷启动之一（实测各 1–3.5 秒）：打开会话时进程
// 要干的活只是"把消息读出来"，而消息本来就在磁盘上（见 session_history.go）。
func TestOpenSessionIsLazy(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 前置条件：Start 之后一个进程都没有（旧契约是"Start 会起一个当前会话进程"）
	if s.client != nil || len(s.runtimes) != 0 || s.activeRuntime != nil {
		t.Fatal("Start 不该创建任何进程（会话=日志，进程留到第一条消息）")
	}

	a := sessionFixture(t, root, cwd, "lazy-a")
	result, err := s.SwitchSession(a)
	if err != nil {
		t.Fatalf("打开会话 a: %v", err)
	}
	pending, sessionPath, runtimeID := pendingPayload(t, result)
	if !pending || runtimeID != "" {
		t.Fatalf("打开会话应当是「待创建」状态（pending=true、runtimeId 为空），实际 %s", result.Data)
	}
	if normalizeDir(sessionPath) != normalizeDir(a) {
		t.Errorf("待创建状态必须记住用户在看哪条会话，实际 sessionPath=%q", sessionPath)
	}
	if len(s.runtimes) != 0 {
		t.Fatalf("打开会话不该起进程，实际有 %d 个", len(s.runtimes))
	}
	// "在看哪条会话"要能从状态里读出来（状态栏与侧栏高亮都靠它）
	if normalizeDir(s.pendingSessionFile) != normalizeDir(a) {
		t.Errorf("pendingSessionFile 没记住目标：%q", s.pendingSessionFile)
	}
	if normalizeDir(s.sessionFile) != normalizeDir(a) {
		t.Errorf("sessionFile 应当如实反映界面在看的那条会话：%q", s.sessionFile)
	}
	state, err := s.GetState()
	if err != nil {
		t.Fatalf("无进程时 GetState 不该报错: %v", err)
	}
	if !strings.Contains(state.Data, `"pending":true`) || !strings.Contains(state.Data, `"isStreaming":false`) {
		t.Errorf("无进程时的状态要如实说明 pending/未在跑，实际 %s", state.Data)
	}
}

// 第一条消息**必须先 switch_session 再发 prompt**。
//
// 不这么做的坏法极其隐蔽：进程会把这句话写进刚出生的空会话里，
// 界面上完全正常（用户看到自己说的话与模型的回答），但他打开的那条历史
// 从此断成了两条——A 的 .jsonl 里永远不会有这一句。
func TestFirstPromptSwitchesToPendingSessionBeforeSending(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := sessionFixture(t, root, cwd, "switch-before-prompt")
	if _, err := s.SwitchSession(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt("这句话属于打开的那条会话"); err != nil {
		t.Fatalf("发送第一条消息: %v", err)
	}
	runtime := s.activeRuntime
	if runtime == nil {
		t.Fatal("第一条消息应当把进程带起来")
	}
	if normalizeDir(runtime.sessionFile) != normalizeDir(a) {
		t.Fatalf("进程没有切到用户打开的那条会话：%s（期望 %s）", runtime.sessionFile, a)
	}
	// pending 的使命结束：现在由 activeRuntime 表达"当前在看哪条会话"
	if s.pendingSessionFile != "" {
		t.Errorf("进程接手之后 pendingSessionFile 应当清空，实际 %q", s.pendingSessionFile)
	}
	messages, err := runtimeRequest(runtime, "get_messages", nil)
	if err != nil {
		t.Fatalf("读取消息: %v", err)
	}
	if !strings.Contains(messages.Data, "这句话属于打开的那条会话") {
		t.Fatalf("第一条消息没有进到那条会话的进程里：%s", messages.Data)
	}
}

// TestSwitchPreservesRunningSessionAndReusesProcess 是这次改造的**核心验收**。
//
// 流程比旧契约多一步"先把 A 带起来"（第一条消息），因为现在"有进程"本身就是
// 由第一条消息决定的；其余断言（切换不杀进程、热切换复用进程且不重载历史、
// 后台那一轮自己跑完且不被 abort）一字未改。
func TestSwitchPreservesRunningSessionAndReusesProcess(t *testing.T) {
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "8")
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	a := sessionFixture(t, root, cwd, "a")
	b := sessionFixture(t, root, cwd, "b")

	settled := make(chan string, 20)
	s := &PiService{OnEvent: func(trace pi.Trace) {
		if strings.Contains(trace.Raw, `"type":"agent_settled"`) {
			settled <- trace.RuntimeID
		}
	}}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 打开 A（此时还没有进程）→ 发一条消息把它的进程带起来
	if _, err := s.SwitchSession(a); err != nil {
		t.Fatalf("打开会话 a: %v", err)
	}
	if _, err := s.Prompt("继续完成这项工作"); err != nil {
		t.Fatalf("发送任务: %v", err)
	}
	original := s.activeRuntime
	originalClient := s.client
	if original == nil || originalClient == nil {
		t.Fatal("第一条消息应当为 A 起一个进程")
	}

	// 生成中切到另一条**还没聊过**的会话 B：不起进程，更不碰 A 的那个
	other, err := s.SwitchSession(b)
	if err != nil {
		t.Fatalf("打开会话 b: %v", err)
	}
	pending, otherPath, otherRuntimeID := pendingPayload(t, other)
	if !pending || otherRuntimeID != "" || normalizeDir(otherPath) != normalizeDir(b) {
		t.Fatalf("打开没开过的会话应当是待创建状态，实际 %s", other.Data)
	}
	if len(s.runtimes) != 1 {
		t.Fatalf("打开一条还没聊过的会话不该起进程，实际 %d 个", len(s.runtimes))
	}
	select {
	case <-originalClient.Done():
		t.Fatal("切换把正在跑的会话进程杀掉了")
	default:
	}

	// 切回 A：必须复用原进程，并且**不重载历史**
	back, err := s.SwitchSession(a)
	if err != nil {
		t.Fatalf("切回会话 a: %v", err)
	}
	if back.RuntimeID != original.id || s.client != originalClient {
		t.Fatal("热切换重开了进程（应当复用）")
	}
	var data struct {
		Messages *Result `json:"messages"`
	}
	_ = json.Unmarshal([]byte(back.Data), &data)
	if data.Messages != nil {
		t.Fatal("热切换把整份历史又搬了一遍")
	}

	// 后台那一轮必须自己跑完，而且不能被 abort
	deadline := time.After(15 * time.Second)
	for {
		select {
		case id := <-settled:
			if id != original.id {
				continue
			}
			messages, err := s.GetMessages()
			if err != nil {
				t.Fatalf("读取消息: %v", err)
			}
			if strings.Contains(messages.Data, `"stopReason":"aborted"`) {
				t.Fatalf("后台生成被打断了：%s", messages.Data)
			}
			return
		case <-deadline:
			t.Fatal("后台生成一直没有结束")
		}
	}
}

// 新会话/切工作区都必须保留已有进程（旧实现是"停掉再起"，等于杀掉后台任务）。
//
// ⚠️ 契约在"会话=日志、进程懒创建"之后又往前走了一步：不只是**新建**会话不起进程，
// **切工作区**也不起了（同样只换"当前会话"的指针）。所以这条用例测的是：
//   · 新建/切工作区都不动任何**已经活着**的进程（后台那条继续跑）；
//   · 第一条消息为当前会话起来的进程，必须与原来那条会话的进程不是同一个；
//   · 切回原来那条会话（它开着）时，仍然用回它自己那个进程。
func TestWorkspaceAndNewSessionKeepPreviousProcesses(t *testing.T) {
	root, cwd, other := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 先在当前工作区里开一条会话并聊一句：这就有了"原来那条会话的进程"
	first_path := sessionFixture(t, root, cwd, "keep-a")
	if _, err := s.SwitchSession(first_path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt("先在这条会话里说一句"); err != nil {
		t.Fatal(err)
	}
	first := s.client
	if first == nil {
		t.Fatal("前置条件：第一条消息应当为这条会话起一个进程")
	}

	result, err := s.NewSession()
	if err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	var pending struct {
		Pending   bool   `json:"pending"`
		RuntimeID string `json:"runtimeId"`
	}
	if json.Unmarshal([]byte(result.Data), &pending) != nil || !pending.Pending || pending.RuntimeID != "" {
		t.Fatalf("新建会话应当是「待创建」状态（pending=true、runtimeId 为空），实际 %s", result.Data)
	}
	if s.client != nil || s.activeRuntime != nil {
		t.Fatal("新建会话不该起进程，也不该继续指着上一条会话的进程")
	}
	select {
	case <-first.Done():
		t.Fatal("新建会话把原来那条会话的进程杀掉了")
	default:
	}

	// 第一条消息才起进程：它必须与原来那条会话的进程**不是同一个**
	if _, err := s.Prompt("开始吧"); err != nil {
		t.Fatalf("发送第一条消息: %v", err)
	}
	second := s.client
	if second == nil || second == first {
		t.Fatal("第一条消息没有为这条新会话起一个独立进程")
	}

	// 切工作区：同样是"待创建"，不起进程
	if _, err := s.SwitchWorkspace(other); err != nil {
		t.Fatalf("切换工作区: %v", err)
	}
	if s.client != nil || len(s.runtimes) != 2 {
		t.Fatalf("切工作区不该起进程（旧契约会冷启动一个），实际 client=%v runtimes=%d",
			s.client != nil, len(s.runtimes))
	}
	if _, err := s.SwitchWorkspace(cwd); err != nil {
		t.Fatalf("切回工作区: %v", err)
	}
	// 切回工作区只是"回到这个目录"，不会替用户认领某条会话——
	// 界面会再走一次"打开会话"，那时才把进程接回来（下面就是那一步）。
	if _, err := s.SwitchSession(first_path); err != nil {
		t.Fatalf("切回那条会话: %v", err)
	}
	if s.client != first {
		t.Fatal("切回一条已经开着的会话没有用回它自己的进程")
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	// 关闭时必须把**两个**后台进程都收掉（first 会话的 + 第一条消息起的那个）
	for _, client := range []*pi.Client{first, second} {
		select {
		case <-client.Done():
		default:
			t.Fatal("关闭时漏掉了后台会话进程")
		}
	}
}

// 懒启动最容易错的一步：新建会话之后 active 指针没清干净，
// 结果"第一条消息"被发进了**上一条会话**的进程里（界面上看不出来，内容却串了）。
func TestLazyNewSessionRoutesFirstPromptToNewProcess(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 前置条件：当前会话真的有一个进程（Start 不再起，靠第一条消息）
	if _, err := s.Prompt("上一条会话先聊一句"); err != nil {
		t.Fatal(err)
	}
	previous := s.activeRuntime
	if previous == nil {
		t.Fatal("前置条件：第一条消息之后应当有一个当前会话进程")
	}
	if _, err := s.NewSession(); err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	const firstMessage = "这句话属于新建的那条会话"
	if _, err := s.Prompt(firstMessage); err != nil {
		t.Fatalf("发送第一条消息: %v", err)
	}
	if s.activeRuntime == nil || s.activeRuntime == previous {
		t.Fatal("第一条消息没有把新建的会话带起来（active 还指着上一条会话）")
	}
	// 上一条会话的进程里绝不能出现这句话：那说明消息发错了地方
	messages, err := runtimeRequest(previous, "get_messages", nil)
	if err != nil {
		t.Fatalf("读取上一条会话的消息: %v", err)
	}
	if strings.Contains(messages.Data, firstMessage) {
		t.Fatalf("新会话的第一条消息被发进了上一条会话的进程：%s", messages.Data)
	}
}

// 并发点同一条会话只能开出一个进程；打开失败不能改变当前会话。
//
// ⚠️ 契约变了：并发**打开**同一条会话现在不起进程（那是懒启动），
// 所以"只开出一个进程"要测在**第一条消息**上——那才是真正建进程的地方，
// 也是并发点得最快的地方（用户连点两次发送）。原意图（绝不能并存两个进程）
// 一字未改，只是把它钉在真正建进程的那一步上。
func TestConcurrentSwitchAndFailedTarget(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	path := sessionFixture(t, root, cwd, "same")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SwitchSession(path); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(s.runtimes) != 0 {
		t.Fatalf("并发打开也不该起进程：%d 个", len(s.runtimes))
	}
	// 8 次并发打开之后，"当前在看哪条会话"必须是同一条（后到的不能把它弄丢）
	if normalizeDir(s.pendingSessionFile) != normalizeDir(path) {
		t.Fatalf("并发打开之后 pendingSessionFile 不对：%q", s.pendingSessionFile)
	}

	// 并发"发送第一条消息"：只能开出一个进程
	var prompts sync.WaitGroup
	for range 8 {
		prompts.Add(1)
		go func() {
			defer prompts.Done()
			// 并发发送时可能有人的消息被前一个回合挡住，这里只关心进程数量
			_, _ = s.Prompt("并发发送")
		}()
	}
	prompts.Wait()
	if len(s.runtimes) != 1 {
		t.Fatalf("并发发送开出了重复进程：%d 个（期望 1）", len(s.runtimes))
	}
	previous := s.client
	if previous == nil {
		t.Fatal("并发发送之后应当有一个进程")
	}

	// 打开失败不能改变当前会话，也不能留下半成品进程。
	//
	// 此刻"当前会话"由进程表达（并发发送已经把它带起来了），所以要盯的是
	// activeRuntime / sessionFile 一动不动——旧契约里没有进程时它盯的是 client。
	previousRuntime := s.activeRuntime
	previousFile := s.sessionFile
	bad := sessionFixture(t, root, filepath.Join(cwd, "missing"), "bad")
	if _, err := s.SwitchSession(bad); err == nil {
		t.Fatal("工作目录不存在的会话应当打不开")
	}
	if s.client != previous || s.activeRuntime != previousRuntime || len(s.runtimes) != 1 {
		t.Fatal("打开失败改变了当前会话（或者留下了一个半成品进程）")
	}
	if normalizeDir(s.sessionFile) != normalizeDir(previousFile) {
		t.Fatalf("打开失败把'用户在看的会话'改了：%q", s.sessionFile)
	}
}

// 改名/删除后台会话不该影响前台，删除后台会话要把它那个进程收掉。
func TestRenameAndDeleteBackgroundSession(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	a := sessionFixture(t, root, cwd, "a")
	b := sessionFixture(t, root, cwd, "b")
	// A 与 B 都要真的开过进程：一条当后台、一条当前台。
	// （懒启动之后"打开"本身不起进程，所以两条都得先发一条消息。）
	if _, err := s.SwitchSession(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt("A 里说一句"); err != nil {
		t.Fatal(err)
	}
	background := s.client
	if background == nil {
		t.Fatal("前置条件：A 应当有一个进程")
	}
	if _, err := s.SwitchSession(b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt("B 里说一句"); err != nil {
		t.Fatal(err)
	}
	foreground := s.client
	if foreground == nil || foreground == background {
		t.Fatal("前置条件：B 应当是另一个进程")
	}

	if _, err := s.RenameSession(a, "renamed"); err != nil {
		t.Fatalf("重命名后台会话: %v", err)
	}
	if s.client != foreground {
		t.Fatal("重命名后台会话改变了前台")
	}
	if _, err := s.DeleteSession(a); err != nil {
		t.Fatalf("删除后台会话: %v", err)
	}
	if s.client != foreground {
		t.Fatal("删除后台会话改变了前台")
	}
	select {
	case <-background.Done():
	default:
		t.Fatal("删除会话之后它的进程还活着（会继续往一个已经不存在的文件里写）")
	}
}

// 空闲回收：只回收"不在跑、也不是当前会话"的进程。
//
// ⚠️ 前置条件变了：以前"启动时那个进程"是被回收的对象，现在 Start 不起进程
// （见 service.go 的 Start），所以要先造出两个进程——A 聊一句（它成为后台），
// 再切到 B 聊一句（B 是当前会话）。要回收的仍然是"没人用、又不在跑"的那个。
func TestIdleRuntimeReaping(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	// mock 默认 18ms/字符，一轮要跑好几秒；这里只关心"哪个进程可以被回收"，
	// 所以把输出调快，好让后面两个回合都真的结束（忙的进程本来就不该被回收）。
	t.Setenv("PI_MOCK_TEXT_DELAY_MS", "1")
	s := &PiService{}
	if err := s.Start("mock", "", cwd, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })

	// 第一条会话（聊一句，让它有一个进程），随后它会被切到后台
	idlePath := sessionFixture(t, root, cwd, "idle-a")
	if _, err := s.SwitchSession(idlePath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt("A 里说一句"); err != nil {
		t.Fatal(err)
	}
	staleRuntime := s.activeRuntime
	stale := s.client
	if stale == nil || staleRuntime == nil {
		t.Fatal("前置条件：A 应当有一个进程")
	}

	// 当前会话换成另一条（同样是先聊一句才有进程）
	currentPath := sessionFixture(t, root, cwd, "idle-b")
	if _, err := s.SwitchSession(currentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prompt("B 里说一句"); err != nil {
		t.Fatal(err)
	}
	current := s.client
	if current == stale || current == nil {
		t.Fatal("前置条件：当前会话应当是另一个进程")
	}

	/**
	 * 等 A 那个回合真的跑完。
	 *
	 * 回收规则里"正在跑回合的进程不回收"是硬要求（回收 = 把用户的后台任务掐掉），
	 * 所以这里必须先让它闲下来——不然测的是"忙的不回收"，而不是"闲的要回收"。
	 */
	waitRuntimeIdle(t, s, staleRuntime.id)

	// 把"现在"推到 TTL 之后：那个不再被使用的进程该被回收，当前会话不动
	reaped := s.reapIdleRuntimes(time.Now().Add(idleRuntimeTTL + time.Minute))
	if reaped != 1 {
		t.Fatalf("应当回收 1 个空闲进程，实际 %d", reaped)
	}
	select {
	case <-stale.Done():
	default:
		t.Fatal("空闲下来的旧进程没有被回收（会一直占着内存）")
	}
	select {
	case <-current.Done():
		t.Fatal("回收动了当前会话的进程")
	default:
	}
	if s.client != current || s.activeRuntime == nil {
		t.Fatal("回收之后当前会话应当仍然是刚打开的那条")
	}
}

// waitRuntimeIdle 等到某个会话进程的回合结束（agent_settled 把它标回不忙）。
func waitRuntimeIdle(t *testing.T, s *PiService, runtimeID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		runtime := s.runtimes[runtimeID]
		busy := runtime != nil && runtime.busy
		s.mu.Unlock()
		if runtime == nil {
			t.Fatalf("进程 %s 不见了", runtimeID)
		}
		if !busy {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("进程 %s 的回合一直没有结束", runtimeID)
}
