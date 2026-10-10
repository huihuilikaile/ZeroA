package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
)

/**
 * 模型清单缓存：get_available_models / get_available_thinking_levels 最近一次成功应答的原文。
 *
 * 为什么需要它：懒启动之后，"新建会话还没发第一条消息"时没有进程可问，
 * 而模型菜单不该因此空掉——用户正是要在这个时候挑模型（ bug 报告：
 * "新建会话不发送信息不能设置模型"）。有进程时每次成功应答顺手刷新缓存；
 * 没有进程时按缓存回答，菜单照常可用。
 *
 * 缓存可能滞后（用户在别处改了 models.json）：下一次有进程的应答会把它刷新，
 * 所以写侧（AddModel / DeleteModel / UpdateProvider / SetProviderKey）会作废缓存，
 * 宁可让菜单暂时回到"还没有缓存"，也不把已删除的模型递给用户。
 */
type modelsCache struct {
	Models         string `json:"models,omitempty"`
	ThinkingLevels string `json:"thinkingLevels,omitempty"`
}

func modelsCachePath() string {
	dir := agentDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "models-cache.json")
}

// readModelsCache 读缓存；文件不存在或损坏都返回零值（调用方按"没有缓存"处理）。
func readModelsCache() modelsCache {
	var cache modelsCache
	path := modelsCachePath()
	if path == "" {
		return cache
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &cache) != nil {
		return modelsCache{}
	}
	return cache
}

// writeModelsCache 读-改-写缓存。写配置目录用 configMu 串行化（与 models.json 同一把锁，
// 避免与 AddModel 等写者交错）。写失败不致命：下次有进程的成功应答还会再写。
func (s *PiService) writeModelsCache(mutate func(*modelsCache)) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	path := modelsCachePath()
	if path == "" {
		return
	}
	cache := readModelsCache()
	mutate(&cache)
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	// 临时文件 + rename：界面侧可能正在读，半截 JSON 会被当成"没有缓存"静默吞掉，
	// 但既然能原子写就不要依赖那个兜底。
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// invalidateModelsCache 作废缓存：models.json / auth.json 变过，清单就不一定准了。
func invalidateModelsCache() {
	if path := modelsCachePath(); path != "" {
		_ = os.Remove(path)
	}
}
