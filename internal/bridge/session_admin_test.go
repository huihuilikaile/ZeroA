package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 会话改名/删除都只能作用于会话目录里的普通 JSONL 文件；删除当前会话时
// 必须先让 Pi 成功切到新会话，防止进程继续握着已经消失的文件。
func TestDeleteSessionGuards(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)
	svc := &PiService{}

	// 会话目录外的文件必须拒绝
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteSession(outside); err == nil {
		t.Fatal("会话目录之外的文件应该拒绝删除")
	}
	if _, err := svc.RenameSession(outside, "新名字"); err == nil {
		t.Fatal("会话目录之外的文件应该拒绝改名")
	}

	// 相邻目录名只是字符串前缀，也绝不能算作 root 内部。
	// 旧实现用 HasPrefix(root) 时会把 sessions-backup 误判成 sessions 的子目录。
	prefixSibling := root + "-backup"
	if err := os.MkdirAll(prefixSibling, 0o755); err != nil {
		t.Fatal(err)
	}
	prefixFile := filepath.Join(prefixSibling, "leak.jsonl")
	if err := os.WriteFile(prefixFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteSession(prefixFile); err == nil {
		t.Fatal("同名前缀的相邻目录不属于会话目录")
	}
	if _, err := os.Stat(prefixFile); err != nil {
		t.Fatalf("被拒绝的相邻目录文件不应被删除: %v", err)
	}

	// 正常删除
	inside := filepath.Join(root, "2026-10-04T00-00-00-000Z_abc.jsonl")
	if err := os.WriteFile(inside, []byte("{\"type\":\"session\",\"id\":\"abc\",\"cwd\":\".\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.DeleteSession(inside); err != nil || !result.Success {
		t.Fatalf("删除失败: %v %+v", err, result)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatal("文件应该已经删掉")
	}

	// 正在使用的会话**允许**删除（用户要求），但必须先成功切到新会话。
	// 这里没有启动 Pi，new_session 会失败；安全语义是旧文件仍然保留，不能先删后报错。
	active := filepath.Join(root, "2026-10-04T00-00-01-000Z_def.jsonl")
	if err := os.WriteFile(active, []byte("{\"type\":\"session\",\"id\":\"def\",\"cwd\":\".\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.sessionFile = active
	if _, err := svc.DeleteSession(active); err == nil {
		t.Fatal("未启动 Pi 时删当前会话应该报错，而不是静默成功")
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("新会话创建失败时原会话必须保留: %v", err)
	}

	// 非 .jsonl 也要拦住
	other := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteSession(other); err == nil {
		t.Fatal("非 .jsonl 应该拒绝删除")
	}
}

// 默认工作区：exe 在 bin/dist/out 这类构建输出目录里时取上一级，
// 否则 Pi 会在一堆编译产物中干活。
func TestResolveCwdFallsBackFromBuildDir(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(project, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := workspaceFallback(bin); got != project {
		t.Errorf("bin 目录应该回退到项目根，得到 %q", got)
	}

	normal := filepath.Join(project, "src")
	if err := os.MkdirAll(normal, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := workspaceFallback(normal); got != "" {
		t.Errorf("普通目录不该回退，得到 %q", got)
	}

	// 显式传入的目录永远优先，不受回退规则影响
	custom := filepath.Join(project, "custom")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveCwd(custom); !strings.EqualFold(got, custom) {
		t.Errorf("显式目录应原样使用，得到 %q", got)
	}
}
