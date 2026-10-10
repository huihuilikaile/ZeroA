package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBrowserStaysInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &PiService{workspaceDir: root}

	if result, err := svc.ListDir(root); err != nil || !result.Success {
		t.Fatalf("工作区根目录应该可列出: %v %+v", err, result)
	}
	if result, err := svc.ReadFileTextAt(inside, 0, 1024); err != nil || !result.Success {
		t.Fatalf("工作区内文件应该可预览: %v %+v", err, result)
	}
	if _, err := svc.ListDir(filepath.Dir(outside)); err == nil {
		t.Fatal("不应列出工作区外目录")
	}
	if _, err := svc.ReadFileTextAt(outside, 0, 1024); err == nil {
		t.Fatal("不应读取工作区外文件")
	}

	// 相邻前缀目录也不属于工作区。
	sibling := root + "-backup"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListDir(sibling); err == nil {
		t.Fatal("同名前缀的相邻目录不应通过工作区校验")
	}
}

// 图片内联预览：只认图片后缀、只读工作区内的文件、超过上限拒绝而不是截断。
func TestReadFileDataURL(t *testing.T) {
	root := t.TempDir()
	// 1x1 PNG
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	imagePath := filepath.Join(root, "dot.png")
	if err := os.WriteFile(imagePath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(textPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &PiService{workspaceDir: root}

	result, err := svc.ReadFileDataURL(imagePath)
	if err != nil {
		t.Fatalf("ReadFileDataURL: %v", err)
	}
	var payload struct {
		Mime    string `json:"mime"`
		DataURL string `json:"dataUrl"`
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

	// 非图片后缀直接拒绝（而不是当作图片读一遍）
	if _, err := svc.ReadFileDataURL(textPath); err == nil {
		t.Error("文本文件不该走图片预览")
	}
	// 工作区之外仍然拒绝
	outside := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(outside, png, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadFileDataURL(outside); err == nil {
		t.Error("工作区外的图片应该拒绝")
	}
	// 超过上限拒绝（不是截断后返回半张图）
	big := filepath.Join(root, "big.png")
	if err := os.WriteFile(big, make([]byte, maxInlineImageBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReadFileDataURL(big); err == nil {
		t.Error("超过上限的图片应该报错")
	}
}
