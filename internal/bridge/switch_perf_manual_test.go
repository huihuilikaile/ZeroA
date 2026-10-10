package bridge

import (
	"os"
	"testing"
	"time"
)

/**
 * 会话相关的**手工**性能测试：默认全部跳过，只在排查"切换/列表变慢了"时手动跑。
 *
 * 为什么留着手工而不是写进常规测试套件：它们要么读开发机真实的会话目录、
 * 要么启动真实的 Pi 进程，跑一次的绝对值也不该拿来当断言（CI 机器之间的差异比优化本身还大）。
 * 但它们能把"慢在哪一段"从猜测变成数字，所以值得留着。
 *
 * 用法：
 *
 *	# ① 会话列表：全量解析 vs 索引缓存
 *	$env:PI_PERF_SESSIONS_DIR = 'E:\2026PlayLearn\pi-gui-c\bin\.pi\sessions'
 *	go test . -run TestManualSessionListPerf -v
 *
 *	# ② 切换会话各阶段耗时（真实 Pi，不调用模型，因此不花钱）
 *	$env:PI_PERF_REAL = '1'
 *	$env:PI_PERF_AGENT_DIR = 'E:\2026PlayLearn\pi-gui-c\bin\.pi'
 *	$env:PI_PERF_CWD = 'E:\2026PlayLearn\pi-gui-c\bin\workspace'
 *	$env:PI_PERF_SESSION = '<会话文件路径>'
 *	$env:PI_PERF_RESOURCES = '1'   # 按应用默认加载扩展/技能；不设则不加载
 *	go test . -run TestManualRealSwitchPerf -v
 */
func TestManualSessionListPerf(t *testing.T) {
	root := os.Getenv("PI_PERF_SESSIONS_DIR")
	if root == "" {
		t.Skip("未设置 PI_PERF_SESSIONS_DIR")
	}

	// ① 全量解析（这条路径现在只被 scanAllSessionsCached 的冷启动用到）
	start := time.Now()
	all := scanAllSessions(root)
	full := time.Since(start)
	t.Logf("scanAllSessions（每次全量解析）  : %d 条 / %v", len(all), full)

	// ② 索引缓存：冷启动 + 全部命中
	invalidateSessionIndex("")
	start = time.Now()
	first := scanAllSessionsCached(root)
	cold := time.Since(start)
	t.Logf("scanAllSessionsCached（冷启动）  : %d 条 / %v", len(first), cold)

	start = time.Now()
	second := scanAllSessionsCached(root)
	hit := time.Since(start)
	t.Logf("scanAllSessionsCached（全部命中）: %d 条 / %v", len(second), hit)

	if len(first) != len(all) || len(second) != len(all) {
		t.Fatalf("缓存结果与全量结果条目数不一致：%d/%d/%d", len(all), len(first), len(second))
	}
	if hit > 0 {
		t.Logf("命中路径相对全量路径加速 %.1fx", float64(full)/float64(hit))
	}
}

func TestManualRealSwitchPerf(t *testing.T) {
	if os.Getenv("PI_PERF_REAL") != "1" {
		t.Skip("未设置 PI_PERF_REAL=1")
	}
	sessionPath := os.Getenv("PI_PERF_SESSION")
	if sessionPath == "" {
		t.Skip("未设置 PI_PERF_SESSION")
	}
	if dir := os.Getenv("PI_PERF_AGENT_DIR"); dir != "" {
		t.Setenv("PI_CODING_AGENT_DIR", dir)
	}

	svc := &PiService{}
	// 默认不加载扩展/技能/MCP；PI_PERF_RESOURCES=1 时按应用真实默认加载（首启与切换都会更慢）。
	svc.SetLoadResources(os.Getenv("PI_PERF_RESOURCES") == "1")

	start := time.Now()
	if err := svc.Start("real", "", os.Getenv("PI_PERF_CWD"), "", "", "", ""); err != nil {
		t.Fatalf("启动真实 Pi 失败: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	t.Logf("进程启动（到 Start 返回）    : %v", time.Since(start))

	start = time.Now()
	if _, err := svc.GetState(); err != nil {
		t.Fatalf("get_state: %v", err)
	}
	t.Logf("首次 get_state（含资源加载） : %v", time.Since(start))

	start = time.Now()
	result, err := svc.SwitchSession(sessionPath)
	if err != nil {
		t.Fatalf("冷启动打开会话: %v", err)
	}
	t.Logf("冷启动打开会话（含 switch_session + get_state + get_messages）: %v（payload %d 字节）",
		time.Since(start), len(result.Data))

	// 再切同一份：一进程一会话之后这是**热切换**——应当几乎零成本（不重载历史、不发 RPC）。
	start = time.Now()
	warm, err := svc.SwitchSession(sessionPath)
	if err != nil {
		t.Fatalf("热切换: %v", err)
	}
	t.Logf("热切换（同一份会话，复用进程）: %v（payload %d 字节）", time.Since(start), len(warm.Data))

	// 换一条别的会话再切回来，测"切回去"这条真实路径
	start = time.Now()
	if _, err := svc.NewSession(); err != nil {
		t.Fatalf("新建会话: %v", err)
	}
	back, err := svc.SwitchSession(sessionPath)
	if err != nil {
		t.Fatalf("切回: %v", err)
	}
	t.Logf("新建后再切回（同样是复用）  : %v", time.Since(start))
	_ = back

	t.Log("提醒：真实应用里这份 payload 还要经 Wails 序列化到前端、再由前端重建时间线并全部渲染 ——" +
		"如果这里的数字是几十到几百毫秒而用户仍觉得慢，瓶颈在渲染，不在 RPC。")
}
