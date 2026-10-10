package sessionstore

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 本文件负责「读懂会话文件」：扫描会话目录、解析会话头、提取标题。
//
// 为什么不用 RPC：Pi 的 RPC 有 new_session / switch_session，但**没有列会话**的命令
// （见 pi 的 docs/rpc-commands.md）。而会话文件本身就是明确定义的格式
// （pi 的 docs/session-format.md），所以这里直接读会话目录：
//   ~/.pi/agent/sessions/<按工作目录分组>/<时间戳>_<uuid>.jsonl
// 每个文件的第一行就是 {"type":"session","id":...,"cwd":...,"timestamp":...}，
// 于是「这个文件属于哪个工作目录」不需要靠目录名猜，读一行就知道。

// Info 是前端渲染「工作区下的对话记录」需要的最小信息。
type Info struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Cwd       string `json:"cwd,omitempty"`
	Title     string `json:"title"`
	Name      string `json:"name,omitempty"`
	UpdatedAt string `json:"updatedAt"` // RFC3339，前端自己转"刚刚/3天"
	Size      int64  `json:"size"`
	// ModTime 只用于排序：RFC3339 字符串在跨时区/夏令时的历史文件上排序并不可靠。
	ModTime time.Time `json:"-"`
	// CwdKey 是规范化后的工作区归属，供"一次扫出多个工作区"时分组用。
	CwdKey string `json:"-"`
}

// Header 是会话文件第一行里我们关心的字段。
type Header struct {
	ID   string `json:"id"`
	Cwd  string `json:"cwd"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// ScanAll 一次遍历会话目录，把全部会话读出来（带 CwdKey），按修改时间倒序。
//
// 为什么不用目录名分组：分组名是路径转义出来的内部实现，Pi 改一下转义规则，
// 我们就会把会话认成别的目录的。直接读每个文件的首行拿 cwd 才是稳定的。
//
// 为什么先全扫再分组：侧栏现在会同时列出**每个展开的工作区**的会话，
// 每个工作区各扫一遍整个目录就是 N 倍 I/O（每个文件都要读开头找标题）。
// 扫一次、按 CwdKey 分桶，代价与工作区数量无关。
func ScanAll(root string) []Info {
	sessions := []Info{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		header, title := ScanFile(path)
		if header.ID == "" {
			return nil
		}
		sessions = append(sessions, Info{
			ID:        header.ID,
			Path:      path,
			Cwd:       header.Cwd,
			Title:     title,
			Name:      header.Name,
			UpdatedAt: info.ModTime().Format(time.RFC3339),
			Size:      info.Size(),
			ModTime:   info.ModTime(),
			CwdKey:    NormalizeDir(header.Cwd),
		})
		return nil
	})
	// SliceStable：同一毫秒写完的会话（测试里很常见）不能让顺序随机，
	// 稳定排序至少保证"同样的输入给同样的顺序"。
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].ModTime.After(sessions[j].ModTime) })
	return sessions
}

// ScanWorkspace 扫出属于 target 工作区的全部会话，按修改时间倒序。
func ScanWorkspace(root, target string) []Info {
	out := []Info{}
	for _, session := range ScanAllCached(root) {
		if session.CwdKey == target {
			out = append(out, session)
		}
	}
	return out
}

// ScanFile 读一个会话文件，取出头部信息与一个能当标题用的字符串。
//
// 标题优先级：显式会话名 > 第一条用户消息 > 空（前端退化成时间）。
//
// 为什么要读两段（头 + 尾）：
//
//	· 会话头与第一条用户消息在**文件开头**；
//	· 而改名记录 session_info 是用户改名时**追加到文件末尾**的。
//
// 只扫开头就会漏掉改名（这个 bug 被测试抓到过）；只扫全文件又会在超大历史上变慢。
// 所以头扫前 200 行拿标题，尾扫最后 64KB 找 session_info，代价与文件大小无关。
func ScanFile(path string) (Header, string) {
	file, err := os.Open(path)
	if err != nil {
		return Header{}, ""
	}
	defer file.Close()

	var header Header
	title := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for line := 0; line < 200 && scanner.Scan(); line++ {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		if header.ID == "" {
			var first Header
			if json.Unmarshal(raw, &first) == nil && first.Type == "session" {
				header = first
				continue
			}
			// 首行不是会话头就说明不是 Pi 的会话文件，直接放弃
			return Header{}, ""
		}
		if title != "" {
			continue
		}
		// 否则用第一条用户消息当标题：比时间戳有用得多
		var record struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &record) != nil || record.Type != "message" || record.Message.Role != "user" {
			continue
		}
		title = FirstLineOf(ContentText(record.Message.Content), 60)
	}

	// 末尾找改名记录：类型是 **session_info**（见 pi 的 docs/session-format.md）。
	// 早前写成"类型里含 name"永远命不中，改名后列表还显示首条用户消息。
	if name := lastSessionName(file, header); name != "" {
		title = name
		header.Name = name
	}
	return header, title
}

// lastSessionName 只在文件末尾 64KB 里找最后一条 session_info 的 name。
// 取最后一条而不是第一条：改名可以发生多次，以最新的为准。
func lastSessionName(file *os.File, header Header) string {
	if header.ID == "" {
		return ""
	}
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	const tailBytes = 64 * 1024
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	// 从头扫的话第一行可能是被截断的半行，忽略它即可（下面按 JSON 解析，失败就跳过）
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	name := ""
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var record struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &record) == nil && record.Type == "session_info" && record.Name != "" {
			name = record.Name
		}
	}
	return name
}

// Cwd 只读会话头，返回规范化工作区路径。
func Cwd(path string) string { return NormalizeDir(HeaderCwd(path)) }

// HeaderCwd 返回会话头里原样的 cwd（保留用户磁盘上的大小写与分隔符），
// 用于把"推断出来的工作区"显示成用户认识的那个路径。
func HeaderCwd(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	var header Header
	scanner := bufio.NewScanner(file)
	if scanner.Scan() && json.Unmarshal(scanner.Bytes(), &header) == nil && header.Type == "session" {
		return strings.TrimSpace(header.Cwd)
	}
	return ""
}

// ContentText 把消息 content（字符串或分块数组）压成纯文本。
func ContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// FirstLineOf 取第一行并截断，用于列表里的一行标题。
func FirstLineOf(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	runes := []rune(trimmed)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return string(runes)
}

/**
 * ReadHeader 只读会话文件的**第一行**：拿它的 cwd，用来决定冷启动的工作目录。
 *
 * 为什么按会话头而不是按当前工作区起进程：会话是在哪个目录里产生的，就得回到那个目录。
 * 在一个错误的工作目录里恢复对话，模型看到的文件树、相对路径全都不对
 *（用户会看到"它说找不到某个文件"，而那个文件明明就在旁边）。
 */
func ReadHeader(path string) Header {
	file, err := os.Open(path)
	if err != nil {
		return Header{}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var header Header
		if json.Unmarshal(scanner.Bytes(), &header) == nil && header.Type == "session" {
			return header
		}
		break
	}
	return Header{}
}

// NewestFor 在工作区内找最近更新的一条会话。
func NewestFor(key string) string {
	root := SessionsRoot()
	if root == "" || key == "" {
		return ""
	}
	sessions := ScanWorkspace(root, key)
	if len(sessions) == 0 {
		return ""
	}
	return sessions[0].Path
}
