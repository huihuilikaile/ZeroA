package bridge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	wsfiles "zeroa/internal/files"
)

// 本文件是工作区文件浏览域的薄封装：列表/预览/内联图片的实现已经迁到
// internal/files（纯文件 IO），这里只剩两件事——
//
//	① workspacePath 越界判定（需要"界面正在看的工作区"这个服务侧事实）；
//	② Wails 绑定入口（Result 形状）。

// dirEntry 是文件树里的一行（实现见 internal/files.Entry）。
type dirEntry = wsfiles.Entry

// maxInlineImageBytes 见 internal/files.MaxInlineImageBytes（测试引用主包常量）。
const maxInlineImageBytes = wsfiles.MaxInlineImageBytes

// workspacePath 把文件面板请求限制在当前工作区内。
//
// 边界取"界面正在看的工作区"（currentWorkspace），而不是"Pi 进程启动时用的目录"：
// 切换工作区要先 Stop 再 Start，中间 workspaceDir 是空的；写回旧目录的那个瞬间，
// 面板请求新工作区的目录就会被误判成越界。
func (s *PiService) workspacePath(path string) (string, error) {
	s.mu.Lock()
	root := s.currentWorkspace
	if strings.TrimSpace(root) == "" {
		root = s.workspaceDir
	}
	s.mu.Unlock()
	if strings.TrimSpace(root) == "" {
		root = resolveCwd("")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := strings.TrimSpace(path)
	if target == "" {
		target = root
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	if resolved, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
		target = resolved
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		// 把边界写进错误里：否则用户只看到"越界"，不知道应用认为的工作区是哪个
		return "", fmt.Errorf("只能访问当前工作区内的文件（当前工作区：%s）", root)
	}
	return filepath.Clean(target), nil
}

// ListDir 列出当前工作区内一个目录的下一层。dir 为空时使用工作区根目录。
func (s *PiService) ListDir(dir string) (Result, error) {
	abs, err := s.workspacePath(dir)
	if err != nil {
		return Result{}, err
	}
	entries, parent, err := wsfiles.List(abs)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(map[string]any{"path": abs, "parent": parent, "entries": entries})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_dir", Success: true, Data: string(payload)}, nil
}

// ReadFileTextAt 从 offset 开始读一页文本文件内容（分页预览用，见 internal/files.ReadTextAt）。
func (s *PiService) ReadFileTextAt(path string, offset, maxBytes int64) (Result, error) {
	if strings.TrimSpace(path) == "" {
		return Result{}, errors.New("缺少文件路径")
	}
	target, err := s.workspacePath(path)
	if err != nil {
		return Result{}, err
	}
	text, nextOffset, truncated, size, err := wsfiles.ReadTextAt(target, offset, maxBytes)
	if err != nil {
		return Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"path": target, "text": text, "truncated": truncated, "size": size, "nextOffset": nextOffset,
	})
	return Result{Command: "read_file_text", Success: true, Data: string(payload)}, nil
}

// SearchFiles 在当前工作区内按文件名/相对路径模糊搜索（见 internal/files.Search）。
func (s *PiService) SearchFiles(query string) (Result, error) {
	root, err := s.workspacePath("")
	if err != nil {
		return Result{}, err
	}
	results, truncated, err := wsfiles.Search(root, query)
	if err != nil {
		return Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"root": root, "results": results, "truncated": truncated,
	})
	return Result{Command: "search_files", Success: true, Data: string(payload)}, nil
}

// RevealPath 在系统文件管理器里选中这个文件/目录（仍受工作区边界限制）。
//
// 与 OpenPath 一样不等退出码：explorer /select 打开成功也可能返回非 0。
func (s *PiService) RevealPath(path string) (Result, error) {
	target, err := s.workspacePath(path)
	if err != nil {
		return Result{}, err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", "/select,"+target)
	case "darwin":
		cmd = exec.Command("open", "-R", target)
	default:
		// Linux 桌面没有统一的「选中文件」接口，打开所在目录
		cmd = exec.Command("xdg-open", filepath.Dir(target))
	}
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	return Result{Command: "reveal_path", Success: true}, nil
}

// ReadFileDataURL 把一个图片文件读成 data URL，供侧栏内联预览。
//
// 为什么不直接给 WebView 一个 file:// 路径：Wails 的前端是从内嵌资源提供的，
// file:// 读取会被同源策略拦掉。data URL 是这里唯一可靠的方式，
// 也天然满足"图片不落进 DOM、不执行脚本"的要求。
func (s *PiService) ReadFileDataURL(path string) (Result, error) {
	target, err := s.workspacePath(path)
	if err != nil {
		return Result{}, err
	}
	mime, data, size, err := wsfiles.ImageDataURL(target)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"path":    target,
		"mime":    mime,
		"size":    size,
		"dataUrl": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "read_file_data_url", Success: true, Data: string(payload)}, nil
}
