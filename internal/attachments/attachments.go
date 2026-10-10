// Package attachments 负责附件域：把所选文件的**绝对路径**附在消息后面交给模型，
// 以及把粘贴/拖拽进来的文件内容落成临时文件（浏览器拿不到真实路径，只有内容）。
//
// 这里是纯数据与文件 IO，不知道 PiService 的存在；主包 attachments.go 只剩
// Wails 绑定（解析 paste 目录、包成 Result 形状、最终调 prompt）。
package attachments

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxPastedBytes 上限 20MB：粘贴一张截图通常几百 KB，超过这个量级基本是误操作。
const maxPastedBytes = 20 << 20

// SavePasted 把粘贴/拖进来的**文件内容**落盘，返回绝对路径。
//
// 为什么要落盘而不是直接内联 base64：附件机制统一是"把绝对路径交给模型读"
// （见 Collect）。粘贴的图片也必须走同一条路，否则会出现
// "选文件能看、粘贴的图看不了"这种不一致。
//
// dataURL 接受 `data:image/png;base64,xxxx` 形式（浏览器 FileReader 的输出）。
// name 是原文件名（保留扩展名，模型和工具按类型处理更省事）；为空时按 mime 推。
func SavePasted(dir, name, dataURL string) (path string, size int, mime string, err error) {
	trimmed := strings.TrimSpace(dataURL)
	if trimmed == "" {
		return "", 0, "", errors.New("粘贴内容为空")
	}
	payload := trimmed
	ext := ""
	if strings.HasPrefix(trimmed, "data:") {
		comma := strings.Index(trimmed, ",")
		if comma < 0 {
			return "", 0, "", errors.New("粘贴数据格式不对")
		}
		header := trimmed[len("data:"):comma]
		payload = trimmed[comma+1:]
		mime = header
		if semi := strings.Index(header, ";"); semi > 0 {
			mime = header[:semi]
		}
		switch strings.ToLower(mime) {
		case "image/jpeg", "image/jpg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		case "image/webp":
			ext = ".webp"
		case "image/gif":
			ext = ".gif"
		case "image/svg+xml":
			ext = ".svg"
		case "application/pdf":
			ext = ".pdf"
		case "application/json":
			ext = ".json"
		case "text/plain":
			ext = ".txt"
		case "text/csv":
			ext = ".csv"
		case "text/markdown":
			ext = ".md"
		case "application/zip":
			ext = ".zip"
		}
	}
	// 原名优先：report.pdf 比 paste-20261006.pdf 对模型友好得多
	base := safeName(name)
	if base != "" {
		if own := filepath.Ext(base); own != "" {
			ext = own
		} else if ext != "" {
			base += ext
		}
	} else {
		if ext == "" {
			// 类型都认不出来时不要硬编：落成 .bin，至少还能读
			ext = ".bin"
		}
		base = "paste-" + time.Now().Format("20060102-150405-000") + ext
	}

	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", 0, "", errors.New("粘贴的文件数据无法解码")
	}
	if len(raw) == 0 {
		return "", 0, "", errors.New("粘贴的文件是空的")
	}
	if len(raw) > maxPastedBytes {
		return "", 0, "", fmt.Errorf("粘贴的文件太大（%.1fMB，上限 20MB）", float64(len(raw))/(1<<20))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, "", err
	}
	// 同名文件加序号：拖两次同名文件是常见操作，不该互相覆盖
	path = filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			break
		}
		ownExt := filepath.Ext(base)
		path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", strings.TrimSuffix(base, ownExt), i, ownExt))
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", 0, "", err
	}
	return path, len(raw), mime, nil
}

// safeName 只留文件名本身并去掉危险字符。
//
// 粘贴/拖拽来的名字是外部输入，可能带路径分隔符（../）或 Windows 保留字符——
// 直接拼进目标目录，等于把写入位置交给了调用方。
func safeName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	if slash := strings.LastIndex(trimmed, "/"); slash >= 0 {
		trimmed = trimmed[slash+1:]
	}
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '.' || r == '-' || r == '_' || r == ' ' || r == '(' || r == ')':
			builder.WriteRune(r)
		case r > 127:
			// 中文等非 ASCII 保留：用户在资源管理器里看到的就是这个名字
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	out := strings.TrimSpace(builder.String())
	out = strings.TrimLeft(out, ".")
	if len([]rune(out)) > 80 {
		out = string([]rune(out)[:80])
	}
	return out
}

// Collect 校验并规格化成一组绝对路径。
// 先 stat 是为了当场报错：不存在的路径发过去只会换来一句含糊的读取失败。
func Collect(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if _, err := os.Stat(trimmed); err != nil {
			return nil, fmt.Errorf("附件读不到: %s", filepath.Base(trimmed))
		}
		absolute := trimmed
		if resolved, err := filepath.Abs(trimmed); err == nil {
			absolute = resolved
		}
		out = append(out, absolute)
	}
	return out, nil
}

// WithReferences 把附件的绝对路径写成列表附在消息后面。
//
// 写成列表而不是一句散文：模型对"要读哪些文件"这件事最不容易漏。
// 用绝对路径是关键——Pi 的工作目录是工作区，相对路径会指到别的地方。
func WithReferences(message string, references []string) string {
	if len(references) == 0 {
		return message
	}
	var builder strings.Builder
	builder.WriteString(strings.TrimRight(message, "\n"))
	builder.WriteString("\n\n附件（请用 read 工具查看）：\n")
	for _, path := range references {
		builder.WriteString("- ")
		builder.WriteString(path)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}
