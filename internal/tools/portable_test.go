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
