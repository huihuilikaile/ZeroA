package roles

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"sync"

	"zeroa/internal/tools"
)

/**
 * 角色编译缓存：按角色 id 记住 Compile 的产物，输入没变就直接复用。
 *
 * 为什么要有它：Compile 里最贵的一步是能力解析前的全机工具探测（几十个
 * detect 进程，实测单角色约 2 秒+）。切换角色、预览编译、派子智能体都会
 * 触发编译，同一份输入反复算同一道题——界面上的表现就是"切换角色后
 * 徽标挂两秒'待重启'"。
 *
 * 失效键（key）必须盖住 Compile 的**全部**输入，漏一个就是脏缓存：
 *   - 角色定义本身（JSON 序列化）；
 *   - 角色提示词正文（Compile 会读 promptFile）；
 *   - 角色目录清单（文件名+大小+mtime）：委派说明会引用其他角色的名字/简介；
 *   - loadResources（关扩展/技能时是另一份产物）；
 *   - 工具检测版本（tools.DetectVersion：重扫/清缓存都会递增）；
 *   - 能力解析的外部输入（tools.CapabilityInputsFingerprint：MCP 与会话工具）；
 *   - 应用侧策略盐（host.CompileSalt：权限的全局/用户/临时授权）；
 *   - 配置目录（host.AgentDir：测试里同 id 角色会出现在不同目录）。
 *
 * 命中时还要确认产物文件还在（AppendSystemPrompt 指向的 env/delegation 文件
 * 是编译时写在磁盘上的，被外部删掉就不能再用缓存，必须重编译补写）。
 *
 * 缓存只在内存里：进程重启即清空，不存在"上一版的缓存"问题。
 */

type compileCacheEntry struct {
	key      string
	compiled CompiledRole
}

var compileCache = struct {
	sync.Mutex
	entries      map[string]compileCacheEntry
	hits, misses int
}{entries: map[string]compileCacheEntry{}}

// InvalidateCompileCache 清空编译缓存。
//
// 大多数变化已被失效键覆盖，不用调用这里；它留给"目录内容变了但任何字段
// 都没变"的场景——例如导入技能（skills 目录多了内容，角色的 skills 解析
// 结果会变，但角色 JSON 没动）。
func InvalidateCompileCache() {
	compileCache.Lock()
	compileCache.entries = map[string]compileCacheEntry{}
	compileCache.Unlock()
}

// compileCacheKey 计算这次编译输入的指纹。
func compileCacheKey(role Role, loadResources bool) string {
	hash := fnv.New64a()
	hash.Write([]byte(host.AgentDir()))
	if raw, err := json.Marshal(role); err == nil {
		hash.Write(raw)
	}
	if promptPath := PromptFileOf(role); promptPath != "" {
		if data, err := os.ReadFile(promptPath); err == nil {
			hash.Write(data)
		}
	}
	// 角色目录清单：delegationSection 会 Find() 其他角色取名字/简介，
	// 任何角色的增删改都要让缓存作废。文件很小，Stat 一遍代价可忽略。
	if entries, err := os.ReadDir(Dir()); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if info, err := entry.Info(); err == nil {
				fmt.Fprintf(hash, "|%s=%d@%d", entry.Name(), info.Size(), info.ModTime().UnixNano())
			}
		}
	}
	fmt.Fprintf(hash, "|resources=%v|detect=%d|inputs=%s|salt=%s",
		loadResources, tools.DetectVersion(), tools.CapabilityInputsFingerprint(), host.CompileSalt())
	return strconv.FormatUint(hash.Sum64(), 16)
}

// compileCacheLookup 命中且产物文件仍在时返回缓存的编译结果。
func compileCacheLookup(id, key string) (CompiledRole, bool) {
	compileCache.Lock()
	entry, ok := compileCache.entries[id]
	if ok && entry.key == key {
		compileCache.hits++
	} else {
		compileCache.misses++
	}
	compileCache.Unlock()
	if !ok || entry.key != key {
		return CompiledRole{}, false
	}
	// 产物文件被外部删掉时不能用缓存：重编译会把它们补写回来
	for _, path := range entry.compiled.Spawn.AppendSystemPrompt {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			compileCache.Lock()
			compileCache.hits--
			compileCache.misses++
			compileCache.Unlock()
			return CompiledRole{}, false
		}
	}
	return entry.compiled, true
}

// compileCacheStore 在编译成功后记入缓存。
func compileCacheStore(id, key string, compiled CompiledRole) {
	compileCache.Lock()
	compileCache.entries[id] = compileCacheEntry{key: key, compiled: compiled}
	compileCache.Unlock()
}

// compileCacheStats 给测试用的命中/未命中计数。
func compileCacheStats() (hits, misses int) {
	compileCache.Lock()
	defer compileCache.Unlock()
	return compileCache.hits, compileCache.misses
}
