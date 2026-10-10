package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 分页读取：第二页必须正好接上第一页，整段拼起来等于原文件。
func TestReadTextAtPaging(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "log.txt")
	original := strings.Repeat("0123456789abcdef\n", 500) // 8500 字节
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	var joined strings.Builder
	offset := int64(0)
	pages := 0
	for {
		text, next, truncated, size, err := ReadTextAt(target, offset, 4096)
		if err != nil {
			t.Fatalf("第 %d 页读取失败: %v", pages+1, err)
		}
		if size != int64(len(original)) {
			t.Errorf("size 应该是 %d，实际 %d", len(original), size)
		}
		if next <= offset {
			t.Fatalf("nextOffset 没有前进: offset=%d next=%d", offset, next)
		}
		joined.WriteString(text)
		offset = next
		pages++
		if !truncated {
			break
		}
		if pages > 10 {
			t.Fatal("页数超出预期，分页可能死循环")
		}
	}
	if joined.String() != original {
		t.Errorf("分页拼接后与原文件不一致（拼出 %d 字节，原 %d 字节）", joined.Len(), len(original))
	}
	if pages < 2 {
		t.Errorf("8500 字节按 4096 一页至少应该分 3 页，实际 %d 页", pages)
	}
}

// 分页边界落在多字节字符中间时，不许把字符切开（界面会出现）。
func TestReadTextAtUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "cjk.txt")
	original := strings.Repeat("你好世界", 100) // 1200 字节，全 3 字节字符
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	var joined strings.Builder
	offset := int64(0)
	for {
		text, next, truncated, _, err := ReadTextAt(target, offset, 100)
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if strings.ContainsRune(text, '�') {
			t.Fatalf("分页把字符切开了（offset=%d）: %q", offset, text)
		}
		joined.WriteString(text)
		offset = next
		if !truncated {
			break
		}
	}
	if joined.String() != original {
		t.Error("CJK 分页拼接后与原文件不一致")
	}
}

// 小文件一页读完：truncated 为 false。
func TestReadTextAtSmallFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "small.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, next, truncated, size, err := ReadTextAt(target, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" || truncated || size != 5 || next != 5 {
		t.Errorf("小文件读取不对: text=%q next=%d truncated=%v size=%d", text, next, truncated, size)
	}
}

// 非 UTF-8 文件第一页就拒绝。
func TestReadTextAtRejectsBinary(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "bin.dat")
	if err := os.WriteFile(target, []byte{0xff, 0xfe, 0x00, 0x01, 0x80}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := ReadTextAt(target, 0, 1024); err == nil {
		t.Error("二进制文件应该报「这不是文本文件」")
	}
}

// 空文件返回空文本而不是错误。
func TestReadTextAtEmptyFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	text, _, truncated, size, err := ReadTextAt(target, 0, 1024)
	if err != nil || text != "" || truncated || size != 0 {
		t.Errorf("空文件读取不对: text=%q truncated=%v size=%d err=%v", text, truncated, size, err)
	}
}

// 搜索：命中文件名与相对路径，跳过隐藏目录，目录与文件都能命中。
func TestSearch(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("src/main.go")
	mk("docs/readme.md")
	mk("node_modules/pkg/index.go") // 隐藏目录里的同名命中不该出现
	if err := os.MkdirAll(filepath.Join(root, "src", "util"), 0o755); err != nil {
		t.Fatal(err)
	}

	results, truncated, err := Search(root, "main")
	if err != nil || truncated {
		t.Fatalf("Search: truncated=%v err=%v", truncated, err)
	}
	if len(results) != 1 || results[0].Rel != "src/main.go" {
		t.Errorf("应该只命中 src/main.go，实际 %+v", results)
	}

	// 按路径片段命中：目录本身的名字也算，浅的排前面
	results, _, err = Search(root, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Rel != "docs" || !results[0].IsDir || results[1].Rel != "docs/readme.md" {
		t.Errorf("按路径搜索应该命中 docs 与 docs/readme.md，实际 %+v", results)
	}

	// 目录也能命中，大小写不敏感
	results, _, err = Search(root, "UTIL")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].IsDir || results[0].Rel != "src/util" {
		t.Errorf("目录命中不对: %+v", results)
	}

	// 空查询不遍历
	if results, _, err = Search(root, "  "); err != nil || results != nil {
		t.Errorf("空查询应该直接返回空: %+v %v", results, err)
	}
}

// 搜索结果的排序：浅的排前面。
func TestSearchOrder(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/b/c/note.txt", "note.txt", "a/note.txt"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	results, _, err := Search(root, "note")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Rel != "note.txt" || results[1].Rel != "a/note.txt" || results[2].Rel != "a/b/c/note.txt" {
		t.Errorf("排序应该是先浅后深，实际 %+v", results)
	}
}
