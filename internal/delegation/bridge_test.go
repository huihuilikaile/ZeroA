package delegation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFileString(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败 %s: %v", path, err)
	}
	return string(body)
}

func fileModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// 扩展文件必须真的写进用户扩展目录，而且内容稳定（避免每次启动都改用户文件）。
func TestDelegateExtensionWritten(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	path, err := EnsureDelegateExtension()
	if err != nil {
		t.Fatalf("EnsureDelegateExtension: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(path), "/extensions/delegate-task.ts") {
		t.Errorf("扩展写错位置了: %s", path)
	}
	body := readFileString(t, path)
	for _, must := range []string{"registerTool", "delegate_task", "delegation_list", DelegateURLEnv, DelegateTokenEnv} {
		if !strings.Contains(body, must) {
			t.Errorf("扩展里缺少 %q", must)
		}
	}
	// 内嵌的 TypeScript 不能含反引号：它嵌在 Go 的原始字符串里，反引号会截断源码。
	// （这个坑真的踩到过：编译报"invalid character U+00B7"。）
	if strings.Contains(DelegateExtensionSource, "`") {
		t.Error("内嵌的 TypeScript 不能包含反引号")
	}

	// 再写一次：内容没变就不该重写（保持文件 mtime 不变）
	first := fileModTime(t, path)
	time.Sleep(20 * time.Millisecond)
	if _, err := EnsureDelegateExtension(); err != nil {
		t.Fatalf("第二次 EnsureDelegateExtension: %v", err)
	}
	if !fileModTime(t, path).Equal(first) {
		t.Error("内容没变时不该重写扩展文件")
	}
}

// ★ 后台委派的结论能不能自动回来，全靠这个选择。
//
// 实测踩到的坑：父会话**已经空闲**（派完活、这一轮结束了）时用 follow_up 投递，
// 消息只会排进 Pi 的队列——界面显示"队列 1"，父 Agent 不被唤醒，
// 子智能体的结论永远躺在队列里，要用户再敲一条消息才被处理。
func TestDeliveryMethodPicksPromptWhenParentIdle(t *testing.T) {
	if got := DeliveryMethod(false); got != "prompt" {
		t.Errorf("父会话空闲时应当用 prompt（直接开始新一轮），得到 %q", got)
	}
	if got := DeliveryMethod(true); got != "follow_up" {
		t.Errorf("父会话在跑时应当用 follow_up（不打断当前回合），得到 %q", got)
	}
}

// 候选 flag 的识别：宁可漏，也不要误伤。
func TestDetectFlagHint(t *testing.T) {
	yes := []string{
		"解出来了：flag{th1s_is_a_flag}",
		"最终答案是 FLAG{ABC_def-123}",
		"我拿到 GKCTF{basic_crypto_1s_fun} 这个",
		"`ctf{lower_case_works}`",
	}
	for _, text := range yes {
		if DetectFlagHint(text) == "" {
			t.Errorf("应当识别出 flag：%q", text)
		}
	}

	no := []string{
		"",
		"这个题还没做出来，flag 还没拿到",
		"代码里有个函数叫 flag_check()",
		"花括号用得很频繁 { } 但没有前缀",
		"flag{}", // 空内容：不像真 flag
	}
	for _, text := range no {
		if got := DetectFlagHint(text); got != "" {
			t.Errorf("不该识别成 flag（误伤会让子智能体白干）：%q → %q", text, got)
		}
	}
}
