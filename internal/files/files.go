// Package files 负责工作区文件浏览域：一层一层的目录列表、文本预览、图片内联。
//
// 设计照 DSH 的 FilesBody：**一次只列一层**，展开哪层列哪层。
// 不做整树递归——工作区可能是几十万文件的仓库，递归会把界面和磁盘都拖死。
//
// 这里是纯文件 IO，不知道 PiService 的存在；越界判定（workspacePath）留在主包
// files.go，因为它需要"界面正在看的工作区"这个服务侧事实。
package files

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Entry 是文件树里的一行。
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	// Binary 提示界面别去读它（图片/可执行文件之类）
	Binary bool `json:"binary,omitempty"`
}

// MaxPreviewBytes 文本预览的上限。读整份大文件塞进前端没有意义。
const MaxPreviewBytes = 256 << 10

// MaxInlineImageBytes 是侧栏内联显示图片的上限。
// data URL 会让体积膨胀约 1/3（base64），再大就不该塞进界面状态里。
const MaxInlineImageBytes = 8 << 20

// hiddenNames 是不显示的条目：版本控制与依赖目录。
// 不是不能看，而是它们会让树变得没法用（node_modules 一个目录就上万条）。
var hiddenNames = map[string]bool{
	".git": true, "node_modules": true, ".task": true, "dist": true,
	"__pycache__": true, ".venv": true, "venv": true,
}

// List 列出一个目录的下一层，以及它的上一层（到盘符根为止 parent 为空串）。
func List(abs string) (entries []Entry, parent string, err error) {
	children, err := os.ReadDir(abs)
	if err != nil {
		return nil, "", fmt.Errorf("读不到目录: %s", filepath.Base(abs))
	}

	entries = make([]Entry, 0, len(children))
	for _, child := range children {
		name := child.Name()
		if hiddenNames[name] {
			continue
		}
		info, infoErr := child.Info()
		if infoErr != nil {
			continue
		}
		entry := Entry{
			Name:  name,
			Path:  filepath.Join(abs, name),
			IsDir: child.IsDir(),
			Size:  info.Size(),
		}
		if !entry.IsDir {
			entry.Binary = LooksBinary(name)
		}
		entries = append(entries, entry)
	}
	// 目录在前、然后按名字自然序（file2 排在 file10 前面）
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return naturalLess(entries[i].Name, entries[j].Name)
	})

	parent = filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	return entries, parent, nil
}

// ReadTextAt 从 offset 开始读一页文本（最多 maxBytes），供预览分页。
//
// nextOffset 是下一页的起点；truncated 为 false 表示已经读到文件尾。
// offset 语义是字节偏移，但分页不许把一个 UTF-8 字符切开：
// offset>0 时跳过开头的 continuation byte（上一页读到了字符中段，只可能发生在
// 旧会话缓存的偏移上）；块尾部如果切在字符中间，最多回退 3 字节交给下一页。
func ReadTextAt(target string, offset, maxBytes int64) (text string, nextOffset int64, truncated bool, size int64, err error) {
	if maxBytes <= 0 || maxBytes > MaxPreviewBytes {
		maxBytes = MaxPreviewBytes
	}
	if offset < 0 {
		offset = 0
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", 0, false, 0, fmt.Errorf("读不到文件: %s", filepath.Base(target))
	}
	if info.IsDir() {
		return "", 0, false, 0, errors.New("这是一个目录")
	}
	size = info.Size()
	if offset >= size {
		// 空文件，或者文件被外部改短了：都让界面回到第一页
		if size == 0 {
			return "", 0, false, size, nil
		}
		offset = 0
	}
	handle, openErr := os.Open(target)
	if openErr != nil {
		return "", 0, false, 0, openErr
	}
	defer handle.Close()

	want := maxBytes
	if remaining := size - offset; remaining < want {
		want = remaining
	}
	buffer := make([]byte, want)
	read, readErr := handle.ReadAt(buffer, offset)
	if readErr != nil && read == 0 {
		return "", 0, false, 0, readErr
	}
	buffer = buffer[:read]

	// 跳过开头的不完整字符（continuation byte），只对续页有意义。
	start := 0
	if offset > 0 {
		for start < len(buffer) && buffer[start]&0xC0 == 0x80 {
			start++
		}
	}
	buffer = buffer[start:]

	// 尾部切在字符中间就回退，整段交给下一页。UTF-8 字符最长 4 字节，
	// 不完整尾字符至多占 3 字节，所以最多回退 3 步；回退后仍非法说明
	// 非法字节在块中间（不是分页切出来的），原样保留交给下面的判定。
	end := len(buffer)
	if !utf8.Valid(buffer) {
		for i := 1; i <= 3 && i <= end; i++ {
			if utf8.Valid(buffer[:end-i]) {
				end -= i
				break
			}
		}
		buffer = buffer[:end]
	}

	// 第一页就含非法 UTF-8：按非文本文件处理（续页出现说明文件中段才有
	// 非法字节，第一页已经放行了，这里原样返回让界面显示替换字符即可）。
	if offset == 0 && len(buffer) > 0 && !utf8.Valid(buffer) {
		return "", 0, false, 0, errors.New("这不是文本文件，无法预览")
	}
	nextOffset = offset + int64(start) + int64(end)
	return string(buffer), nextOffset, nextOffset < size, size, nil
}

// SearchResult 是文件搜索的一条命中。
type SearchResult struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Rel 是相对搜索根的路径（正斜杠），界面直接显示它
	Rel   string `json:"rel"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// search 的两个硬上限：命中条数与遍历过的条目数。
// 工作区可能是几十万文件的仓库，遍历必须有个止损点。
const (
	maxSearchResults  = 200
	maxSearchVisited  = 200_000
	errSearchStoppedS = "search stopped"
)

var errSearchStopped = errors.New(errSearchStoppedS)

// Search 在工作区内按文件名/相对路径做不区分大小写的包含匹配。
//
// 跳过 hiddenNames（node_modules 之类）——那里既不是用户想找的地方，
// 也是遍历开销的主要来源。truncated 表示撞到了上限，结果只是前一部分。
func Search(root, query string) (results []SearchResult, truncated bool, err error) {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil, false, nil
	}
	results = make([]SearchResult, 0, 32)
	visited := 0
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != root {
			if d.IsDir() && hiddenNames[d.Name()] {
				return filepath.SkipDir
			}
			visited++
			if visited > maxSearchVisited || len(results) >= maxSearchResults {
				return errSearchStopped
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if strings.Contains(strings.ToLower(d.Name()), needle) ||
				strings.Contains(strings.ToLower(relSlash), needle) {
				size := int64(0)
				if info, infoErr := d.Info(); infoErr == nil {
					size = info.Size()
				}
				results = append(results, SearchResult{
					Name: d.Name(), Path: path, Rel: relSlash, IsDir: d.IsDir(), Size: size,
				})
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errSearchStopped) {
		return nil, false, walkErr
	}
	truncated = errors.Is(walkErr, errSearchStopped)
	// 两种止损（命中满 200 / 遍历满 20 万）都算"结果被截断"：
	// 原来还要求 len(results) >= maxSearchResults，于是"遍历超限但命中很少"
	//（大仓库里搜冷门词正是这种）会被标成完整结果，界面把前一部分说成全部。
	// 浅的排前面，同级按自然序：用户找的大多是工作区里靠前的文件
	sort.SliceStable(results, func(i, j int) bool {
		di := strings.Count(results[i].Rel, "/")
		dj := strings.Count(results[j].Rel, "/")
		if di != dj {
			return di < dj
		}
		return naturalLess(results[i].Rel, results[j].Rel)
	})
	return results, truncated, nil
}

// ImageDataURL 读一个图片文件，供侧栏内联预览（返回 MIME 与原始字节，调用方拼 data URL）。
//
// 超过上限直接拒绝而不是截断：截断的图片只会渲染成破图，不如说清原因。
func ImageDataURL(target string) (mime string, data []byte, size int64, err error) {
	mime = ImageMime(target)
	if mime == "" {
		return "", nil, 0, errors.New("这不是可以内联显示的图片")
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", nil, 0, fmt.Errorf("读不到文件: %s", filepath.Base(target))
	}
	if info.IsDir() {
		return "", nil, 0, errors.New("这是一个目录")
	}
	if info.Size() > MaxInlineImageBytes {
		return "", nil, 0, fmt.Errorf("图片太大（%.1fMB，上限 %dMB），用系统程序打开",
			float64(info.Size())/(1<<20), MaxInlineImageBytes>>20)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		return "", nil, 0, err
	}
	return mime, data, info.Size(), nil
}

// LooksBinary 按扩展名判断是否二进制（只用于界面提示，不做内容嗅探）。
func LooksBinary(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip", ".gz",
		".exe", ".dll", ".so", ".dylib", ".woff", ".woff2", ".ttf", ".mp4", ".mp3", ".syso":
		return true
	}
	return false
}

// ImageMime 按扩展名给出图片 MIME；不是图片返回空串。
//
// 只认浏览器能直接画的栅格格式与 SVG：SVG 走 <img> 上下文，
// 脚本不会执行，也不会进入应用 DOM（与 DSH 的静态图片渲染一致）。
func ImageMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	}
	return ""
}

// naturalLess 数字感知的字典序：file2 < file10（纯字典序会反过来）。
func naturalLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	for i, j := 0, 0; i < len(la) && j < len(lb); {
		da, db := digitRun(la, i), digitRun(lb, j)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(la[i:i+da], "0"), strings.TrimLeft(lb[j:j+db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			i, j = i+da, j+db
			continue
		}
		if la[i] != lb[j] {
			return la[i] < lb[j]
		}
		i, j = i+1, j+1
	}
	return len(la) < len(lb)
}

func digitRun(s string, start int) int {
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return end - start
}
