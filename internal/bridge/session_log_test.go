package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

/**
 * gui-log 的两条硬要求（都是实测踩出来的）：
 *
 *   ① 超限必须**轮转**而不是清空——这份日志是出问题时唯一的现场记录，
 *      "攒满就全丢"意味着恰恰在跑了很久、最需要追溯的时刻它是空的；
 *   ② 多 goroutine 并发写不能交织——trace 回调、委派监督器、回收器都在
 *      各自的 goroutine 里写，没锁的话两行会拼成一行，排查时连一条
 *      完整记录都找不到。
 */

func TestLogfRotatesInsteadOfTruncating(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	big := strings.Repeat("x", 200*1024)
	Logf("first %s", big)
	Logf("second %s", big) // 累计约 400KB，超过 256KB 上限
	Logf("third")          // 这一次调用看到超限，触发轮转

	rotated, err := os.ReadFile(filepath.Join(dir, "gui-log.1.txt"))
	if err != nil {
		t.Fatalf("轮转后上一代日志必须完整保留: %v", err)
	}
	if !strings.Contains(string(rotated), "first") || !strings.Contains(string(rotated), "second") {
		t.Fatal("上一代日志的内容丢了（旧实现是整体清空，现场全灭）")
	}

	current, err := os.ReadFile(filepath.Join(dir, "gui-log.txt"))
	if err != nil {
		t.Fatalf("轮转后当前日志必须继续可写: %v", err)
	}
	if !strings.Contains(string(current), "third") {
		t.Fatal("轮转后新日志要继续追加")
	}
	if strings.Contains(string(current), "first") {
		t.Fatal("新文件不该带着上一代的内容")
	}
}

func TestLogfConcurrentWritesStayWholeLines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				Logf("concurrent-%d-%d", g, i)
			}
		}(g)
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "gui-log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 320 {
		t.Fatalf("16×20 行一行都不能丢/不能交织，实际 %d 行", len(lines))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		// 行格式是「时间戳 + 空格 + 内容」：交织的话这步就会对不上
		const prefix = "2006-01-02 15:04:05.000 " // 与 Logf 的时间格式同长
		if len(line) <= len(prefix) {
			t.Fatalf("行被交织截断: %q", line)
		}
		payload := line[len(prefix):]
		if !strings.HasPrefix(payload, "concurrent-") {
			t.Fatalf("行首不是完整日志内容，发生了交织: %q", line)
		}
		seen[payload] = true
	}
	if len(seen) != 320 {
		t.Fatalf("有行丢失或重复: 期望 320 条唯一内容，实际 %d", len(seen))
	}
}
