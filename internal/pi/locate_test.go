package pi

import (
	"os"
	"path/filepath"
	"testing"
)

// 「用哪个 Pi」的解析顺序是这次需求的核心，必须钉住：
// 应用自带 > PATH 里的全局安装。否则打包拷到别的机器上，
// 会莫名其妙用回全局那份（版本可能更旧、也可能根本没装）。
func TestResolveCLIPrefersLocalAgent(t *testing.T) {
	// 造一个"应用自带"的目录布局：<home>/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js
	home := t.TempDir()
	moduleDir := filepath.Join(home, "node_modules", "@earendil-works", "pi-coding-agent")
	if err := os.MkdirAll(filepath.Join(moduleDir, "dist", "bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(moduleDir, "dist", "bundle", "cli.js")
	if err := os.WriteFile(cli, []byte("// fake pi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "package.json"),
		[]byte(`{"name":"@earendil-works/pi-coding-agent","version":"1.0.2","bin":{"pi":"dist/bundle/cli.js"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PI_AGENT_HOME", home)
	t.Setenv("PI_CLI_PATH", "")
	t.Setenv("PATH", t.TempDir()) // PATH 里没有 pi，确保测的是"自带"那条路径

	resolved, err := ResolveCLIPath("")
	if err != nil {
		t.Fatalf("ResolveCLIPath: %v", err)
	}
	if resolved.Kind != "local" {
		t.Errorf("应该优先用应用自带的 Pi，实际 kind=%s path=%s", resolved.Kind, resolved.Path)
	}
	if resolved.Path != cli {
		t.Errorf("解析出的入口不对：%s（期望 %s）", resolved.Path, cli)
	}
	if resolved.Version != "1.0.2" {
		t.Errorf("版本应读自 package.json，实际 %q", resolved.Version)
	}
}

// 显式路径与环境变量的优先级都要高于"自带"：用户手动指定就该听用户的。
func TestResolveCLIOverrideOrder(t *testing.T) {
	home := t.TempDir()
	moduleDir := filepath.Join(home, "node_modules", "@earendil-works", "pi-coding-agent")
	if err := os.MkdirAll(filepath.Join(moduleDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "dist", "cli.js"), []byte("// local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "package.json"), []byte(`{"version":"1.0.2","bin":{"pi":"dist/cli.js"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_AGENT_HOME", home)

	custom := filepath.Join(t.TempDir(), "my-pi.js")
	if err := os.WriteFile(custom, []byte("// custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 显式 > 一切
	resolved, err := ResolveCLIPath(custom)
	if err != nil || resolved.Kind != "explicit" || resolved.Path != custom {
		t.Fatalf("显式路径应最优先：%+v err=%v", resolved, err)
	}
	// 环境变量 > 自带
	t.Setenv("PI_CLI_PATH", custom)
	resolved, err = ResolveCLIPath("")
	if err != nil || resolved.Kind != "env" || resolved.Path != custom {
		t.Fatalf("环境变量应优先于自带：%+v err=%v", resolved, err)
	}
}

// 什么都没有时要给一句能照着做的错误信息，而不是一句 "exec: not found"。
func TestResolveCLIMissingGivesAdvice(t *testing.T) {
	t.Setenv("PI_AGENT_HOME", t.TempDir()) // 空目录
	t.Setenv("PI_CLI_PATH", "")
	t.Setenv("PATH", t.TempDir())
	source := DescribeCLI("")
	if source.Kind != "missing" {
		t.Fatalf("找不到时应该是 missing，实际 %+v", source)
	}
	if _, err := ResolveCLIPath(""); err == nil {
		t.Fatal("找不到 Pi 时应该报错")
	}
}
