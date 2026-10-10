package bridge

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 粘贴/拖进来的文件要落盘成一个"能和普通附件一样用"的路径。
//
// 这条链路是给 web 层兜底的：浏览器 / WebView 拿不到剪贴板或拖拽文件的真实路径，
// 只给内容。内容落盘 → 绝对路径 → 交给模型 read，和其它附件完全同一条路。
func TestSavePastedFileKeepsNameAndExtension(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	workspace := t.TempDir()
	svc := &PiService{workspaceDir: workspace}

	payload := base64.StdEncoding.EncodeToString([]byte("hello attachment"))
	result, err := svc.SavePastedFile("report.pdf", "data:application/pdf;base64,"+payload)
	if err != nil {
		t.Fatalf("SavePastedFile: %v", err)
	}
	var out struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
		Mime  string `json:"mime"`
	}
	if err := json.Unmarshal([]byte(result.Data), &out); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out.Path) != "report.pdf" {
		t.Errorf("应该保留原文件名（模型按名字判断类型更省事），实际 %s", filepath.Base(out.Path))
	}
	if out.Bytes != len("hello attachment") {
		t.Errorf("写入字节数不对: %d", out.Bytes)
	}
	body, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatalf("落盘的文件读不到: %v", err)
	}
	if string(body) != "hello attachment" {
		t.Errorf("内容不一致: %q", string(body))
	}
	// 落盘点：当前工作区的 .attachments/（粘贴内容跟着项目走）
	wantDir := filepath.Join(workspace, ".attachments")
	if filepath.Dir(out.Path) != wantDir {
		t.Errorf("应当落在工作区的 .attachments/ 下: 期望 %s，实际 %s", wantDir, filepath.Dir(out.Path))
	}
	// 目录自带 .gitignore：截图不该被误提交进用户工作区的 git 仓库
	if ignore, err := os.ReadFile(filepath.Join(wantDir, ".gitignore")); err != nil || strings.TrimSpace(string(ignore)) != "*" {
		t.Errorf(".attachments 应当自带内容为 * 的 .gitignore: %v", err)
	}

	// 同名再粘一次：加序号，不覆盖（连拖两次同名文件是常见操作）
	second, err := svc.SavePastedFile("report.pdf", "data:application/pdf;base64,"+payload)
	if err != nil {
		t.Fatalf("SavePastedFile(第二次): %v", err)
	}
	var out2 struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(second.Data), &out2)
	if out2.Path == out.Path {
		t.Error("同名文件第二次落盘不该覆盖第一个")
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Errorf("第一个文件应当还在: %v", err)
	}
}

// 文件名是外部输入：带路径的必须被剥成纯文件名，不能写到附件目录外面去。
func TestSavePastedFileSanitizesName(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	workspace := t.TempDir()
	svc := &PiService{workspaceDir: workspace}

	payload := base64.StdEncoding.EncodeToString([]byte("x"))
	for _, evil := range []string{"../../evil.txt", `..\..\evil.txt`, "C:/Windows/System32/evil.txt", `..\..\..\evil.exe`} {
		result, err := svc.SavePastedFile(evil, "data:text/plain;base64,"+payload)
		if err != nil {
			t.Fatalf("SavePastedFile(%q): %v", evil, err)
		}
		var out struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(result.Data), &out); err != nil {
			t.Fatal(err)
		}
		pasteDir := filepath.Join(workspace, ".attachments")
		rel, err := filepath.Rel(pasteDir, out.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("越界写入了：%q → %s", evil, out.Path)
		}
	}
}

// 没有文件名时按 mime 推扩展名；类型也认不出来就落成 .bin（不要瞎编成图片）。
func TestSavePastedFileWithoutName(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{workspaceDir: t.TempDir()}
	payload := base64.StdEncoding.EncodeToString([]byte("png-ish"))

	result, err := svc.SavePastedFile("", "data:image/png;base64,"+payload)
	if err != nil {
		t.Fatalf("SavePastedFile: %v", err)
	}
	var out struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(result.Data), &out)
	if filepath.Ext(out.Path) != ".png" {
		t.Errorf("按 mime 应该推出 .png，实际 %s", filepath.Base(out.Path))
	}

	odd, err := svc.SavePastedFile("", "data:application/x-something;base64,"+payload)
	if err != nil {
		t.Fatalf("SavePastedFile(未知类型): %v", err)
	}
	var oddOut struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal([]byte(odd.Data), &oddOut)
	if filepath.Ext(oddOut.Path) != ".bin" {
		t.Errorf("认不出的类型应当落成 .bin，实际 %s", filepath.Base(oddOut.Path))
	}
}

// 兼容性：SavePastedImage 仍然可用（粘贴截图那条老路径）。
func TestSavePastedImageStillWorks(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{workspaceDir: t.TempDir()}
	payload := base64.StdEncoding.EncodeToString([]byte("fake-png"))
	result, err := svc.SavePastedImage("data:image/png;base64," + payload)
	if err != nil {
		t.Fatalf("SavePastedImage: %v", err)
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(result.Data), &out); err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(out.Path) != ".png" {
		t.Errorf("粘贴图片应该落成 .png: %s", out.Path)
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Errorf("文件没落盘: %v", err)
	}
}

// 空内容与超大文件都要当场拒绝，而不是写一个空壳让人以为成功了。
func TestSavePastedFileRejectsBadInput(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{workspaceDir: t.TempDir()}

	if _, err := svc.SavePastedFile("a.txt", "   "); err == nil {
		t.Error("空内容应当报错")
	}
	if _, err := svc.SavePastedFile("a.txt", "data:text/plain;base64,"); err == nil {
		t.Error("空的 base64 应当报错")
	}
	if _, err := svc.SavePastedFile("a.txt", "data:text/plain;base64,!!!not-base64!!!"); err == nil {
		t.Error("非法 base64 应当报错")
	}
	huge := base64.StdEncoding.EncodeToString(make([]byte, 21<<20))
	if _, err := svc.SavePastedFile("huge.bin", "data:application/octet-stream;base64,"+huge); err == nil {
		t.Error("超过 20MB 应当报错")
	}
}

// 附件一律走"绝对路径"，不再区分类型（用户要求）。
// 三条边界：所有类型都被接受（含目录与无扩展名文件）、相对路径被规格化成绝对路径、
// 不存在的附件当场报错而不是静默丢掉。
func TestCollectAttachmentsAcceptsAnyType(t *testing.T) {
	dir := t.TempDir()

	files := []string{}
	for _, name := range []string{"shot.png", "report.pdf", "notes.md", "LICENSE"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, path)
	}
	subDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files = append(files, subDir)

	got, err := collectAttachments(files)
	if err != nil {
		t.Fatalf("collectAttachments: %v", err)
	}
	if len(got) != len(files) {
		t.Fatalf("所有类型（含目录）都该被接受，期望 %d 个，实际 %d 个: %v", len(files), len(got), got)
	}
	for _, path := range got {
		if !filepath.IsAbs(path) {
			t.Errorf("应该给绝对路径，实际 %q", path)
		}
	}

	// 空串与空白被忽略，不产生空条目
	got, err = collectAttachments([]string{"", "   ", files[0]})
	if err != nil || len(got) != 1 {
		t.Fatalf("空条目应该被忽略：%v %v", got, err)
	}

	// 不存在的附件：明确报错（含文件名，便于用户定位）
	if _, err := collectAttachments([]string{filepath.Join(dir, "nope.pdf")}); err == nil {
		t.Fatal("不存在的附件应该报错")
	} else if !strings.Contains(err.Error(), "nope.pdf") {
		t.Errorf("错误信息里应带上文件名，实际: %v", err)
	}
}

// 相对路径必须被规格化成绝对路径：Pi 的工作目录是工作区，
// 相对路径会指到别的地方，模型读不到。
func TestCollectAttachmentsMakesPathsAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 切到该目录后用相对路径传入
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	got, err := collectAttachments([]string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	absolute, _ := filepath.Abs("a.txt")
	if got[0] != absolute {
		t.Errorf("相对路径应被规格化：期望 %s，实际 %s", absolute, got[0])
	}
}

// 附件的路径列表要写在消息正文里，且原消息在最前。
func TestWithFileReferencesAppendsPaths(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "notes.md")

	got := withFileReferences("帮我看看这个", []string{absolute})
	if !strings.HasPrefix(got, "帮我看看这个") {
		t.Errorf("原消息要在最前面，实际:\n%s", got)
	}
	if !strings.Contains(got, "请用 read 工具查看") {
		t.Errorf("应该提示模型用 read 工具，实际:\n%s", got)
	}
	if !strings.Contains(got, absolute) {
		t.Errorf("应该带上绝对路径 %s，实际:\n%s", absolute, got)
	}

	// 没有附件时原样返回，不平白多出空行
	if plain := withFileReferences("只有文本", nil); plain != "只有文本" {
		t.Errorf("没有附件时不该改消息，实际 %q", plain)
	}
}

// 附件缩略图读取：附件不在工作区是常态（粘贴图落在配置目录的 paste/，
// 拖入图可能在磁盘任何位置），所以这个口子不做工作区边界——
// 但仍然只放行图片扩展名、走 8MB 上限，读不到要报错而不是静默。
func TestReadAttachmentImageOutsideWorkspace(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{} // 不设置任何工作区：附件读取不依赖它

	// 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	// 模拟"从桌面拖进来"：既不在工作区，也不在配置目录
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imagePath, png, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := svc.ReadAttachmentImage(imagePath)
	if err != nil {
		t.Fatalf("ReadAttachmentImage: %v", err)
	}
	if !result.Success {
		t.Fatalf("工作区外的图片附件应当能读: %+v", result)
	}
	var payload struct {
		Mime    string `json:"mime"`
		DataURL string `json:"dataUrl"`
		Size    int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析结果: %v", err)
	}
	if payload.Mime != "image/png" {
		t.Errorf("MIME 应该是 image/png，实际 %q", payload.Mime)
	}
	if !strings.HasPrefix(payload.DataURL, "data:image/png;base64,") {
		t.Errorf("dataUrl 前缀不对: %.40s", payload.DataURL)
	}
	if payload.Size != int64(len(png)) {
		t.Errorf("size 不对: %d", payload.Size)
	}

	// 非图片扩展名要被拒（缩略图只给图片出）
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadAttachmentImage(textPath); err == nil {
		t.Error("非图片文件应当被拒")
	}
	// 空路径与读不到的文件都要明确报错（前端据此退回文件图标）
	if _, err := svc.ReadAttachmentImage("   "); err == nil {
		t.Error("空路径应当报错")
	}
	if _, err := svc.ReadAttachmentImage(filepath.Join(dir, "gone.png")); err == nil {
		t.Error("不存在的文件应当报错")
	}
}
