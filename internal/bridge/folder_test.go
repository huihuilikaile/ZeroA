package bridge

import (
	"os"
	"path/filepath"
	"testing"
)

// 「新建文件夹」不是装饰：它决定下一次启动时 Pi 的工作目录，
// 所以三条行为要钉住：相对名建在父目录下、绝对路径直接用、同名文件要报错。
func TestCreateFolder(t *testing.T) {
	parent := t.TempDir()
	svc := &PiService{}

	// 1) 相对名
	result, err := svc.CreateFolder(parent, "notes")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if !result.Success {
		t.Fatalf("创建失败: %+v", result)
	}
	created := filepath.Join(parent, "notes")
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("目录没有真的建出来: %v", err)
	}

	// 2) 已存在时当成"切过去"，不报错（用户可能只是想切工作区）
	if _, err := svc.CreateFolder(parent, "notes"); err != nil {
		t.Fatalf("重复创建应视为幂等: %v", err)
	}

	// 3) 绝对路径
	absolute := filepath.Join(t.TempDir(), "workspace-x")
	absResult, err := svc.CreateFolder(parent, absolute)
	if err != nil {
		t.Fatalf("绝对路径创建失败: %v", err)
	}
	if !absResult.Success {
		t.Fatalf("绝对路径创建失败: %+v", absResult)
	}
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		t.Fatalf("绝对路径目录没有建出来: %v", err)
	}

	// 4) 同名文件（不是目录）必须报错，否则会被当成"切换成功"
	filePath := filepath.Join(parent, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateFolder(parent, "afile"); err == nil {
		t.Fatal("同名文件存在时应该报错")
	}

	// 5) 空名与带分隔符的相对名要拦住
	if _, err := svc.CreateFolder(parent, "   "); err == nil {
		t.Fatal("空名应该报错")
	}
	if _, err := svc.CreateFolder(parent, `a/b`); err == nil {
		t.Fatal("相对名带分隔符应该报错")
	}
}
