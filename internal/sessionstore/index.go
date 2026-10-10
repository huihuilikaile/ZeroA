package sessionstore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

/**
 * 会话列表的索引缓存。
 *
 * 为什么需要它：`ScanAll` 每次都要遍历整个会话目录，并且对**每个**文件
 * 读开头（拿 cwd 与首条用户消息当标题）+ 读末尾 64KB（找改名记录）。
 * 而它被调用的频率比想象中高得多：
 *
 *   · 侧栏刷新（每次新建/删除/改名/回合结束）；
 *   · 切换工作区（`ListSessionsBatch` 一次扫全部）；
 *   · 每次切换会话时，"这条会话在不在列表里"的判断。
 *
 * 用 mtime + size 当版本号：只有真正被写过的会话文件才重新解析。
 * 这一层是纯粹的缓存——判断依据永远来自文件本身，缓存命中与否不改变结果。
 */
type indexEntry struct {
	item  Info
	mtime time.Time
	size  int64
}

type index struct {
	mu      sync.Mutex
	root    string
	entries map[string]*indexEntry
	// scans 只用于排查与自测：命中/未命中次数说明缓存到底有没有起作用。
	hits, misses int
}

var cache = &index{entries: map[string]*indexEntry{}}

/**
 * ScanAllCached 与 ScanAll 结果完全一致，但只重新解析变化的文件。
 *
 * 目录换了（会话根目录可被 settings.json 的 sessionDir 挪走）就整表作废：
 * 留着别的根目录下的条目只会让"这个工作区的会话"多出幽灵。
 */
func ScanAllCached(root string) []Info {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.root != root {
		cache.root = root
		cache.entries = map[string]*indexEntry{}
	}

	seen := make(map[string]bool, len(cache.entries))
	sessions := make([]Info, 0, len(cache.entries))

	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		seen[path] = true

		// 命中条件必须同时看 mtime 与 size：mtime 的分辨率在某些文件系统上只有秒级，
		// 同一秒内的追加写入只能靠 size 变化发现。
		if cached, ok := cache.entries[path]; ok &&
			cached.size == info.Size() && cached.mtime.Equal(info.ModTime()) {
			cache.hits++
			sessions = append(sessions, cached.item)
			return nil
		}

		cache.misses++
		header, title := ScanFile(path)
		if header.ID == "" {
			// 不是 Pi 的会话文件：不缓存（下次还得重新判断，但这类文件通常不在会话目录里）
			delete(cache.entries, path)
			return nil
		}
		item := Info{
			ID:        header.ID,
			Path:      path,
			Cwd:       header.Cwd,
			Title:     title,
			Name:      header.Name,
			UpdatedAt: info.ModTime().Format(time.RFC3339),
			Size:      info.Size(),
			ModTime:   info.ModTime(),
			CwdKey:    NormalizeDir(header.Cwd),
		}
		cache.entries[path] = &indexEntry{item: item, mtime: info.ModTime(), size: info.Size()}
		sessions = append(sessions, item)
		return nil
	})

	// 清掉已经不在磁盘上的条目，避免删除会话后它还在列表里复活。
	for path := range cache.entries {
		if !seen[path] {
			delete(cache.entries, path)
		}
	}

	// 与 ScanAll 保持同样的排序与稳定性（SliceStable + 时间倒序）
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].ModTime.After(sessions[j].ModTime) })
	return sessions
}

// InvalidateIndex 让某条会话（或整个索引）失效。
//
// 改名/删除走的是"写文件"，mtime+size 本会发现；但删除是**消失**，
// 而某些删除操作之后同名文件可能立刻以同样的 size 重新出现（罕见但可能），
// 所以显式提供入口，由改名/删除路径调用，语义更明确。
func InvalidateIndex(path string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if path == "" {
		cache.entries = map[string]*indexEntry{}
		return
	}
	delete(cache.entries, filepath.Clean(path))
}

// IndexStats 返回 (命中, 未命中)，给测试与排查用。
func IndexStats() (int, int) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.hits, cache.misses
}
