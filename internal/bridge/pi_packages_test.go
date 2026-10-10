package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

// ★ `pi list` 是"装了哪些包"的权威来源，输出要能稳定解析。
//
// 它的形状是"缩进即层级、分段即作用域"：两层缩进是包来源，四层是解析后的路径。
// 解析错了会让界面显示一堆路径碎片——所以这里用真实输出做断言。
func TestParsePiListOutput(t *testing.T) {
	output := `User packages:
  npm:pi-mcp-adapter
    E:\2026PlayLearn\pi-gui-c\bin\.pi\npm\node_modules\pi-mcp-adapter
  npm:pi-web-access
    E:\2026PlayLearn\pi-gui-c\bin\.pi\npm\node_modules\pi-web-access
Project packages:
  ./tools/my-pack
    E:\proj\.pi\packages\my-pack
`
	items := parsePiListOutput(output)
	if len(items) != 3 {
		t.Fatalf("应当解析出 3 个包，实际 %d：%+v", len(items), items)
	}
	if items[0].Source != "npm:pi-mcp-adapter" || items[0].Scope != "user" {
		t.Errorf("第一条解析不对: %+v", items[0])
	}
	if items[0].Path == "" || filepath.Base(items[0].Path) != "pi-mcp-adapter" {
		t.Errorf("路径没贴到对应条目上: %+v", items[0])
	}
	if items[2].Scope != "project" {
		t.Errorf("项目级作用域没识别: %+v", items[2])
	}
	for _, item := range items {
		if !item.Explicit {
			t.Errorf("pi list 里列出的都是显式声明的包: %+v", item)
		}
	}
}

// ★ 官方发现规则：没有 `pi` manifest、只靠约定目录的包也要认出来。
//
// 只认 manifest 会漏报——用户装了包，界面却显示"没有扩展包"，
// 而 Pi 其实已经在加载它了（docs/packages.md：conventional directories）。
func TestPackageDetectedByConventionalDirs(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "my-pi-pack")
	if err := os.MkdirAll(filepath.Join(pkgDir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{
	  "name": "my-pi-pack", "version": "1.2.3", "description": "只靠约定目录的包"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg, ok := readPiPackage(pkgDir, "my-pi-pack", map[string]bool{"my-pi-pack": true})
	if !ok {
		t.Fatal("有 skills/ 约定目录的包应当被认成 Pi 包")
	}
	if pkg.Version != "1.2.3" || !pkg.Explicit {
		t.Errorf("字段没读全: %+v", pkg)
	}
	if len(pkg.Resources) != 1 || pkg.Resources[0] != "skills" {
		t.Errorf("应当记录约定目录 skills: %+v", pkg.Resources)
	}
}

// 显式 manifest 与 gallery 标记（keywords: pi-package）也要认。
func TestPackageDetectedByManifestAndKeyword(t *testing.T) {
	dir := t.TempDir()
	manifestDir := filepath.Join(dir, "with-manifest")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "package.json"), []byte(`{
	  "name": "with-manifest", "pi": { "extensions": ["./src/index.ts"] }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPiPackage(manifestDir, "with-manifest", nil); !ok {
		t.Error("有 pi manifest 的包应当被认出来")
	}

	keywordDir := filepath.Join(dir, "gallery-item")
	if err := os.MkdirAll(keywordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keywordDir, "package.json"), []byte(`{
	  "name": "gallery-item", "keywords": ["pi-package", "extension"]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPiPackage(keywordDir, "gallery-item", nil); !ok {
		t.Error("带 pi-package 关键词的包应当被认出来")
	}

	// 对照：普通 npm 包（依赖，不是 Pi 扩展）不该被列进来
	plainDir := filepath.Join(dir, "plain-lib")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plainDir, "package.json"), []byte(`{"name":"plain-lib"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPiPackage(plainDir, "plain-lib", nil); ok {
		t.Error("普通依赖不该被当成 Pi 包（否则界面会被几十个传递依赖刷屏）")
	}
}
