package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"zeroa/internal/providers"
)

// 本文件是「模型提供商」域的薄封装：实现已经迁到 internal/providers
// （纯数据与文件 IO），这里只剩三件事——
//
//	① configPaths 解析配置目录里的几个文件路径；
//	② Wails 绑定入口（configMu 串行化写操作、包成 Result 形状）；
//	③ 与提供商无关、但同样属于"设置/导出"的 OpenPath / ExportHTML。
//
// 模型这块的设计（照 DeepSeek Harness 的设置页）：
//   - 列表只展示 Pi **已知的提供商**（内置目录 models-store.json + 已配置的 models.json
//     + 凭据 auth.json 三处合并），每行一个，带"配没配 key"的状态点；
//   - 「编辑」改的是 api / baseUrl / apiKey，写回 models.json；
//   - 自定义模型的高级写法不进界面，直接点「打开配置文件」改 JSON 即可。

// configPaths 汇总本次要用到的几个配置文件路径。
func configPaths() (agentDirPath, modelsPath, storePath, authPath, settingsPath string) {
	dir := agentDir()
	return dir,
		filepath.Join(dir, "models.json"),
		filepath.Join(dir, "models-store.json"),
		filepath.Join(dir, "auth.json"),
		filepath.Join(dir, "settings.json")
}

// ListProviders 合并三处信息，列出设置页要显示的提供商。
func (s *PiService) ListProviders() (Result, error) {
	agentDirPath, modelsPath, storePath, authPath, settingsPath := configPaths()
	if agentDirPath == "" {
		return Result{}, errors.New("找不到 ZeroA 的配置目录")
	}

	payload, err := json.Marshal(map[string]any{
		"providers": providers.List(modelsPath, storePath, authPath),
		// 内置目录提取到几条。0 = 没读到（自带 Pi 的 bundle 布局可能变了），界面据此提示用户
		"builtinCount": len(providers.Builtin()),
		"modelsPath":   modelsPath,
		"settingsPath": settingsPath,
		"agentDir":     agentDirPath,
		"agentDirKind": agentDirKind(),
		"authPath":     authPath,
		"storePath":    storePath,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "list_providers", Success: true, Data: string(payload)}, nil
}

// UpdateProvider 改一个提供商的 api / baseUrl / apiKey，写回 models.json。
func (s *PiService) UpdateProvider(provider, api, baseURL, apiKey string) (Result, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return Result{}, errors.New("缺少 provider")
	}
	_, modelsPath, _, _, _ := configPaths()
	if err := providers.Update(modelsPath, provider, api, baseURL, apiKey); err != nil {
		return Result{}, err
	}
	invalidateModelsCache()
	payload, _ := json.Marshal(map[string]any{"provider": provider, "path": modelsPath})
	return Result{Command: "update_provider", Success: true, Data: string(payload)}, nil
}

// SetProviderKey 写入某提供商的 API 密钥 —— 这就是 Pi 的“登录”。
func (s *PiService) SetProviderKey(provider, key string) (Result, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	_, _, _, authPath, _ := configPaths()
	if authPath == "" {
		return Result{}, errors.New("找不到凭据文件路径")
	}
	authType, err := providers.SetKey(authPath, provider, key)
	if err != nil {
		return Result{}, err
	}
	// 密钥影响清单里的鉴权状态（models.json 没变也作废，成本只是一次重测）
	invalidateModelsCache()
	payload, _ := json.Marshal(map[string]any{"provider": provider, "type": authType, "path": authPath})
	return Result{Command: "set_provider_key", Success: true, Data: string(payload)}, nil
}

// OpenPath 用系统默认方式打开一个文件或文件夹（设置页的"打开配置文件"、
// 会话标题栏的"打开文件夹"都用它）。
func (s *PiService) OpenPath(path string) (Result, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return Result{}, errors.New("缺少路径")
	}
	if _, err := os.Stat(trimmed); err != nil {
		return Result{}, fmt.Errorf("路径不存在: %s", trimmed)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// explorer 对目录会直接打开窗口；对文件会用默认程序打开
		cmd = exec.Command("explorer", trimmed)
	case "darwin":
		cmd = exec.Command("open", trimmed)
	default:
		cmd = exec.Command("xdg-open", trimmed)
	}
	// explorer 打开成功也会返回非 0，所以这里不等它、也不看退出码
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("打开失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	payload, _ := json.Marshal(map[string]any{"path": trimmed})
	return Result{Command: "open_path", Success: true, Data: string(payload)}, nil
}

// ExportHTML 把当前会话导出成 HTML 文件（Pi 的 export_html 命令）。
// outputPath 为空时由 Go 侧给一个默认位置：会话目录下的 <sessionId>.html。
func (s *PiService) ExportHTML(outputPath string) (Result, error) {
	target := strings.TrimSpace(outputPath)
	if target == "" {
		s.mu.Lock()
		sessionID := s.sessionID
		s.mu.Unlock()
		name := sessionID
		if name == "" {
			name = "session"
		}
		target = filepath.Join(sessionsRoot(), name+".html")
	}
	if dir := filepath.Dir(target); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, fmt.Errorf("导出目录创建失败: %w", err)
		}
	}
	return s.request("export_html", map[string]any{"outputPath": target})
}
