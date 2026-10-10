package bridge

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"zeroa/internal/attachments"
	wsfiles "zeroa/internal/files"
	"zeroa/internal/providers"
)

// 本文件是附件域与"自定义模型配置"的薄封装：实现分别迁到 internal/attachments
// （粘贴落盘 / 附件路径列表）与 internal/providers（models.json 的写侧），
// 这里只剩 Wails 绑定（解析目录、configMu 串行化、Result 形状）。

// SavePastedImage 把"粘贴进来的图片"落成一个临时文件，返回它的绝对路径。
// 兼容旧调用：粘贴的图片（内部就是 SavePastedFile 的别名）。
func (s *PiService) SavePastedImage(dataURL string) (Result, error) {
	return s.SavePastedFile("", dataURL)
}

// SavePastedFile 把粘贴/拖进来的**文件内容**落盘，返回绝对路径。
//
// 为什么需要它：web 层拿不到剪贴板或拖拽文件的真实路径（浏览器安全模型），
// 但拿得到内容。所以内容交给 Go 写成文件，之后就和"用加号选的文件"走同一条路
// ——附件机制统一是"绝对路径交给模型读"。
//
// 落盘位置：当前工作区的 .attachments/（早期版本落配置目录的 paste/）。
// 粘贴的内容没有"原始文件"，落盘点就是它的家——跟着工作区走，项目拷到别的
// 机器附件也不断链；目录自带 .gitignore（内容 *），截图不会被误提交进 git。
func (s *PiService) SavePastedFile(name, dataURL string) (Result, error) {
	dir := s.attachmentDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	// 自我屏蔽：这个目录不该进版本库（用户的工作区多数是 git 仓库）
	gitignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignore); err != nil {
		_ = os.WriteFile(gitignore, []byte("*"), 0o644)
	}
	path, size, mime, err := attachments.SavePasted(dir, name, dataURL)
	if err != nil {
		return Result{}, err
	}
	result, err := json.Marshal(map[string]any{"path": path, "bytes": size, "mime": mime})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "save_pasted_file", Success: true, Data: string(result)}, nil
}

// attachmentDir 决定粘贴内容落到哪个目录。
//
// 取"界面正在看的工作区"，与文件面板的边界同一处（见 files.go 的 workspacePath）；
// 实在没有工作区时退回配置目录的 paste/ 兜底（理论上不该发生）。
func (s *PiService) attachmentDir() string {
	s.mu.Lock()
	root := s.currentWorkspace
	if strings.TrimSpace(root) == "" {
		root = s.workspaceDir
	}
	s.mu.Unlock()
	if strings.TrimSpace(root) == "" {
		root = resolveCwd("")
	}
	if strings.TrimSpace(root) == "" {
		return filepath.Join(agentDir(), "paste")
	}
	return filepath.Join(root, ".attachments")
}

// ReadAttachmentImage 读"已附加到输入框"的图片，给附件 chip 出缩略图。
//
// 与 ReadFileDataURL 的差别只在路径边界：附件不限于工作区——粘贴图落在
// 配置目录的 paste/，拖进来的文件可能在磁盘任何位置，它们的共同点是
// "用户亲手附加的"。检查本身沿用侧栏预览那一份（只认浏览器能直接画的
// 图片扩展名 + 8MB 上限），读失败由前端退回通用文件图标。
func (s *PiService) ReadAttachmentImage(path string) (Result, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		return Result{}, errors.New("缺少文件路径")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return Result{}, err
	}
	mime, data, size, err := wsfiles.ImageDataURL(abs)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"path":    abs,
		"mime":    mime,
		"size":    size,
		"dataUrl": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "read_attachment_image", Success: true, Data: string(payload)}, nil
}

// collectAttachments / withFileReferences 是 internal/attachments 的主包薄别名（测试直接引用）。
func collectAttachments(paths []string) ([]string, error) { return attachments.Collect(paths) }

func withFileReferences(message string, references []string) string {
	return attachments.WithReferences(message, references)
}

// PromptWithFiles 与 Prompt 相同，但把附件的绝对路径附在消息后面。
//
// 用户要求：**不再区分类型**（图片/文档/目录一视同仁），选完就把绝对路径给模型。
// 好处是行为统一、没有大小与类型限制、也不用把 base64 塞进 JSONL；
// 代价是模型要多走一步 read —— 这正是 Pi 里读文件的正常方式。
func (s *PiService) PromptWithFiles(message string, files []string) (Result, error) {
	references, err := attachments.Collect(files)
	if err != nil {
		return Result{}, err
	}
	if len(references) == 0 {
		return s.Prompt(message)
	}
	return s.prompt(map[string]any{"message": attachments.WithReferences(message, references)})
}

// AddModel 把一个自定义模型写进 Pi 的配置文件（~/.pi/agent/models.json）。
func (s *PiService) AddModel(provider, modelID, name, api, baseURL, apiKey string, contextWindow int, supportsImages bool) (Result, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	dir := agentDir()
	if dir == "" {
		return Result{}, errors.New("找不到 ZeroA 的配置目录")
	}
	path := filepath.Join(dir, "models.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	updated, err := providers.AddModel(path, provider, modelID, name, api, baseURL, apiKey, contextWindow, supportsImages)
	if err != nil {
		return Result{}, err
	}
	// models.json 变了，模型清单缓存不再可信（见 models_cache.go）
	invalidateModelsCache()
	payload, _ := json.Marshal(map[string]any{"path": path, "provider": provider, "modelId": modelID, "updated": updated})
	return Result{Command: "add_model", Success: true, Data: string(payload)}, nil
}

// ListConfiguredProviders 直接读 Pi 的 models.json，列出已配置的 provider 与模型。
func (s *PiService) ListConfiguredProviders() (Result, error) {
	path := filepath.Join(agentDir(), "models.json")
	out, err := providers.Configured(path)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(map[string]any{"path": path, "providers": out})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_configured_providers", Success: true, Data: string(payload)}, nil
}

// DeleteModel 从 models.json 里删掉一个模型（provider 下最后一个模型被删时连 provider 一起删）。
func (s *PiService) DeleteModel(provider, modelID string) (Result, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	path := filepath.Join(agentDir(), "models.json")
	removed, err := providers.DeleteModel(path, provider, modelID)
	if err != nil {
		return Result{}, err
	}
	invalidateModelsCache()
	payload, _ := json.Marshal(map[string]any{"provider": provider, "modelId": modelID, "providerRemoved": removed})
	return Result{Command: "delete_model", Success: true, Data: string(payload)}, nil
}
