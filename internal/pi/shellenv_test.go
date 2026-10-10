package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonString 把一个字符串编码成 JSON 字面量（写临时 settings.json 用）。
func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// ★ 实测踩到的坑：模型实际跑在 WSL 里，而配置写的是 Git Bash。
//
// 这组测试盯住"PATH 前置"这个修法：只要配置里有可用的 shellPath，
// Pi 子进程的 PATH 第一段就必须是它所在目录——这样 Pi 的回退解析
// （在 PATH 里找 bash）也会选中配置的那个，而不是 System32\bash.exe（WSL）。
func TestPrependPathPutsShellDirFirst(t *testing.T) {
	env := []string{"USERPROFILE=C:\\Users\\x", "PATH=C:\\Windows\\System32;C:\\other", "HOME=C:\\Users\\x"}
	got := prependPath(env, `E:\Git\bin`)

	var pathEntry string
	count := 0
	for _, entry := range got {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			count++
			pathEntry = entry
		}
	}
	if count != 1 {
		t.Fatalf("PATH 应当只有一条，实际 %d 条：%v", count, got)
	}
	want := `PATH=E:\Git\bin` + string(os.PathListSeparator) + `C:\Windows\System32;C:\other`
	if pathEntry != want {
		t.Errorf("PATH 前置结果不对\n got: %s\nwant: %s", pathEntry, want)
	}
	// 其它变量必须原样保留
	if got[0] != "USERPROFILE=C:\\Users\\x" || got[2] != "HOME=C:\\Users\\x" {
		t.Errorf("其它环境变量被改动了：%v", got)
	}
}

// 小写 path= 也要认（Windows 环境变量名不区分大小写）。
func TestPrependPathIsCaseInsensitive(t *testing.T) {
	got := prependPath([]string{"path=C:\\Windows"}, `E:\Git\bin`)
	want := `PATH=E:\Git\bin` + string(os.PathListSeparator) + `C:\Windows`
	if len(got) != 1 || got[0] != want {
		t.Errorf("小写 path 没被识别：%v", got)
	}
}

// 环境块里没有 PATH 时要补一条，而不是悄悄丢掉前置目录。
func TestPrependPathAddsMissingPath(t *testing.T) {
	got := prependPath([]string{"HOME=C:\\Users\\x"}, `E:\Git\bin`)
	if len(got) != 2 || got[1] != `PATH=E:\Git\bin` {
		t.Errorf("缺 PATH 时应当补一条：%v", got)
	}
}

// configuredShellPath：读 settings.json 的 shellPath，坏文件不 panic、返回空。
func TestConfiguredShellPath(t *testing.T) {
	dir := t.TempDir()
	if got := configuredShellPath(dir); got != "" {
		t.Errorf("没有 settings.json 时应返回空，得到 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"shellPath":"E:\\Git\\bin\\bash.exe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := configuredShellPath(dir); got != `E:\Git\bin\bash.exe` {
		t.Errorf("应当读出 shellPath，得到 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{ 坏 JSON`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := configuredShellPath(dir); got != "" {
		t.Errorf("坏 JSON 应当当作没配，得到 %q", got)
	}
}

// shellDirFor：配置的 shell 存在时用它的目录；配置指向不存在的路径时
// **不要**把那个不存在的目录塞进 PATH（会白白污染搜索顺序）。
func TestShellDirForUsesExistingShellOnly(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "bash.exe")
	if err := os.WriteFile(shell, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"shellPath":`+jsonString(shell)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := shellDirFor(dir); got != dir {
		t.Errorf("应当用配置的 shell 目录，得到 %q", got)
	}

	missing := t.TempDir()
	if err := os.WriteFile(filepath.Join(missing, "settings.json"),
		[]byte(`{"shellPath":"C:\\__no_such_shell__\\bash.exe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := shellDirFor(missing); got == `C:\__no_such_shell__` {
		t.Error("配置指向不存在的 shell 时不该把它前置进 PATH")
	}
}

// 端到端：真实 Pi 子进程的 PATH 第一段必须是配置的 shell 目录。
//
// 用 mock 模式也走同一段 env 构造代码，所以这里能覆盖到真正的启动路径。
func TestCommandPrependsConfiguredShellToChildPath(t *testing.T) {
	dir := t.TempDir()
	shellDir := t.TempDir()
	shell := filepath.Join(shellDir, "bash.exe")
	if err := os.WriteFile(shell, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"shellPath":`+jsonString(shell)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", dir)

	cmd, err := Command(Options{Mode: "real", AgentDir: dir, LoadResources: true})
	if err != nil {
		// 真机没有 Pi CLI 时跳过（CI 环境），但本机应当能建出命令
		t.Skipf("无法构造命令（环境缺 Pi CLI）：%v", err)
	}
	var childPath string
	for _, entry := range cmd.Env {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			childPath = entry
			break
		}
	}
	if childPath == "" {
		t.Fatal("子进程环境里没有 PATH")
	}
	if !strings.HasPrefix(childPath, "PATH="+shellDir) {
		t.Errorf("PATH 没有以配置的 shell 目录开头\n got: %s\nwant prefix: PATH=%s", childPath, shellDir)
	}
}
