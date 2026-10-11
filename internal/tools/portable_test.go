package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★ 分发要求"用户不装 Python 和工具"——那些东西跟着 exe 走，清单里就必须写相对路径。
//
// 这里验证便携路径解析：${TOOLS} / ${EXE} / 裸相对路径，都解析到 exe 目录下。
func TestExpandPortablePaths(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exeDir := filepath.Dir(exe)

	cases := []struct {
		in   string
		want string
	}{
		{"${TOOLS}/python/python.exe", filepath.Join(exeDir, "tools", "python", "python.exe")},
		{"tools/bin/ffuf.exe", filepath.Join(exeDir, "tools", "bin", "ffuf.exe")},
		{"${EXE}/pi-agent/cli.js", filepath.Join(exeDir, "pi-agent", "cli.js")},
	}
	for _, item := range cases {
		if got := expandPortable(item.in); got != item.want {
			t.Errorf("expandPortable(%q) = %q，期望 %q", item.in, got, item.want)
		}
	}

	// 绝对路径与 %VAR% 写法保持原样语义
	absolute := filepath.Join(t.TempDir(), "some.exe")
	if got := expandPortable(absolute); got != absolute {
		t.Errorf("绝对路径不该被改写: %q", got)
	}
	if got := expandPortable("%USERPROFILE%\\x.exe"); !strings.HasPrefix(got, os.Getenv("USERPROFILE")) {
		t.Errorf("环境变量写法要保留: %q", got)
	}
	// 空值原样返回空，避免被解析成 exe 目录（历史上踩过 filepath.Clean(\"\") 的坑）
	if got := expandPortable("   "); got != "" {
		t.Errorf("空白输入应当返回空串，得到 %q", got)
	}
}

// ★ 用户实测的 bug：%ProgramFiles%\Git\bin\bash.exe 被判成"相对路径"，
// 拼成 <exe目录>\%ProgramFiles%\...，内置清单里的 bash 永远检测不到。
//
// 根因是 %VAR% 的展开放在了"相对→exe目录"拼接之后。这里锁住正确顺序：
// %VAR% 先展开成绝对路径，exe 目录前缀绝不能出现。
func TestExpandPortablePercentEnvBeforeRelativeJoin(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exeDir := filepath.Dir(exe)

	// 用一个带括号、带空格的值，模拟 Program Files 这类真实路径
	probeDir := filepath.Join(t.TempDir(), "Program Files (x86)")
	t.Setenv("ZETA_PROG_FILES", probeDir)

	got := expandPortable("%ZETA_PROG_FILES%\\Git\\bin\\bash.exe")
	want := filepath.Join(probeDir, "Git", "bin", "bash.exe")
	if got != want {
		t.Errorf("expandPortable(%%ZETA_PROG_FILES%%\\Git\\bin\\bash.exe) = %q，期望 %q", got, want)
	}
	if strings.HasPrefix(got, exeDir) {
		t.Errorf("%%VAR%% 路径被错误地拼上了 exe 目录前缀: %q", got)
	}

	// 三种写法要殊途同归：%VAR% / $VAR / ${VAR}
	for _, in := range []string{"$ZETA_PROG_FILES\\Git\\bin\\bash.exe", "${ZETA_PROG_FILES}\\Git\\bin\\bash.exe"} {
		if got := expandPortable(in); got != want {
			t.Errorf("expandPortable(%q) = %q，期望 %q", in, got, want)
		}
	}

	// 未设置的变量原样保留（而不是被替换成空串、悄悄改写路径）
	if got := expandPortable("%ZETA_NO_SUCH_VAR_9F3K%\\x.exe"); !strings.Contains(got, "%ZETA_NO_SUCH_VAR_9F3K%") {
		t.Errorf("未设置的变量应当原样保留: %q", got)
	}

	// 混在文本里的孤立 % 不能吃掉后半段
	mixed := expandPortable(filepath.Join(t.TempDir(), "100% sure", "x.exe"))
	if !strings.Contains(mixed, "100% sure") {
		t.Errorf("孤立的 %% 不该被当成变量起始: %q", mixed)
	}
}

// 清单里写相对路径时，检测要能命中自带工具目录里的东西。
func TestDetectionResolvesPortableToolPath(t *testing.T) {
	exe, _ := os.Executable()
	toolsDir := filepath.Join(filepath.Dir(exe), "tools", "bin")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 用一个只有测试会创建的临时名字，避免污染真实目录
	name := "zetaportableprobe"
	stub := filepath.Join(toolsDir, name+".exe")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Skip("测试目录不可写，跳过")
	}
	defer os.Remove(stub)

	def := Definition{
		ID: "portable-probe", Name: "便携探针", Type: KindCommand, Direction: "base",
		Capabilities: []string{"script.run"},
		Detect:       Detect{Commands: []string{name}},
		Path:         "tools/bin/" + name + ".exe",
	}
	catalog := DefaultCatalog()
	catalog.Tools = append(catalog.Tools, def)
	catalog.SearchPaths = append(catalog.SearchPaths, "tools/bin")
	SetActiveCatalog(catalog)
	defer SetActiveCatalog(DefaultCatalog())

	status := DetectOne(def)
	if !status.Available {
		t.Fatalf("自带目录里的工具应当被判为可用: %+v", status)
	}
	if !strings.Contains(strings.ToLower(status.Path), "tools") {
		t.Errorf("命中的应当是便携路径: %q", status.Path)
	}
}
