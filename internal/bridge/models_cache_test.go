package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 新建会话还没发第一条消息时没有进程，但用户恰恰要在这个时候挑模型：
// SetModel 应该改写启动模板（options.Model），第一条消息懒启动时按它起进程。
func TestSetModelWithoutProcessRewritesTemplate(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}
	svc.optionsReady = true
	svc.options.Model = "old/provider-x"

	result, err := svc.SetModel("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("无进程时 SetModel 应该成功改写模板，实际报错 %v", err)
	}
	if !result.Success {
		t.Fatalf("应该返回成功，实际 %+v", result)
	}
	if svc.options.Model != "deepseek/deepseek-chat" {
		t.Errorf("options.Model 应该被改写为启动参数，实际 %q", svc.options.Model)
	}
	if svc.startModel != "deepseek/deepseek-chat" {
		t.Errorf("startModel 应该同步（子智能体沿用它），实际 %q", svc.startModel)
	}
	// 合成应答的形状要对齐前端 routeResult 的读取口径：data.model.id/provider
	var data struct {
		Pending bool `json:"pending"`
		Model   struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
	}
	if err := json.Unmarshal([]byte(result.Data), &data); err != nil {
		t.Fatalf("应答 Data 应该是 JSON，实际 %q", result.Data)
	}
	if !data.Pending || data.Model.ID != "deepseek-chat" || data.Model.Provider != "deepseek" {
		t.Errorf("应答应该带 pending 与 model.id/provider，实际 %+v", data)
	}
}

// 连启动模板都还没准备好（没有 options 可改）时，无进程改写无从谈起。
func TestSetModelWithoutProcessRequiresOptions(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}
	if _, err := svc.SetModel("deepseek", "deepseek-chat"); err == nil {
		t.Fatal("optionsReady=false 时应该报错（没有模板可改）")
	}
}

// SetThinkingLevel 与 SetModel 同一模式：思考档位也是启动参数（--thinking）。
func TestSetThinkingLevelWithoutProcessRewritesTemplate(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}
	svc.optionsReady = true

	result, err := svc.SetThinkingLevel("high")
	if err != nil {
		t.Fatalf("无进程时 SetThinkingLevel 应该成功，实际 %v", err)
	}
	if svc.options.Thinking != "high" {
		t.Errorf("options.Thinking 应该被改写，实际 %q", svc.options.Thinking)
	}
	if !strings.Contains(result.Data, `"thinkingLevel":"high"`) {
		t.Errorf("应答应该带 thinkingLevel，实际 %s", result.Data)
	}
}

// 无进程时模型菜单按磁盘缓存回答；没有缓存就老实说没有（发过一条消息就有了）。
func TestGetAvailableModelsFallsBackToCache(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}

	if _, err := svc.GetAvailableModels(); err == nil {
		t.Fatal("没有缓存时应该报错并说明原因")
	}

	payload := `{"models":[{"id":"deepseek-chat","provider":"deepseek"}]}`
	svc.writeModelsCache(func(cache *modelsCache) { cache.Models = payload })

	result, err := svc.GetAvailableModels()
	if err != nil {
		t.Fatalf("有缓存时应该按缓存回答，实际报错 %v", err)
	}
	if result.Data != payload {
		t.Errorf("应该原样返回缓存的应答原文，实际 %s", result.Data)
	}
}

// 思考档位清单与模型清单共用一份缓存文件，行为一致。
func TestGetAvailableThinkingLevelsFallsBackToCache(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}

	if _, err := svc.GetAvailableThinkingLevels(); err == nil {
		t.Fatal("没有缓存时应该报错")
	}

	payload := `{"levels":["low","medium","high"]}`
	svc.writeModelsCache(func(cache *modelsCache) { cache.ThinkingLevels = payload })

	result, err := svc.GetAvailableThinkingLevels()
	if err != nil {
		t.Fatalf("有缓存时应该按缓存回答，实际报错 %v", err)
	}
	if result.Data != payload {
		t.Errorf("应该原样返回缓存原文，实际 %s", result.Data)
	}
}

// 写侧（AddModel/DeleteModel/UpdateProvider/SetProviderKey）作废缓存：
// 作废后文件不在了，读出来是零值，菜单回到"还没有缓存"而不是递出已删除的模型。
func TestInvalidateModelsCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	svc := &PiService{}

	svc.writeModelsCache(func(cache *modelsCache) { cache.Models = `{"models":[]}` })
	if _, err := os.Stat(filepath.Join(dir, "models-cache.json")); err != nil {
		t.Fatalf("写入后缓存文件应该存在，实际 %v", err)
	}

	invalidateModelsCache()
	if _, err := os.Stat(filepath.Join(dir, "models-cache.json")); !os.IsNotExist(err) {
		t.Error("作废后缓存文件应该被删除")
	}
	if cached := readModelsCache(); cached.Models != "" || cached.ThinkingLevels != "" {
		t.Errorf("作废后应该读到零值，实际 %+v", cached)
	}
}

// 缓存文件损坏（比如界面侧正读到半截）时按"没有缓存"处理，不能把坏 JSON 递出去。
func TestReadModelsCacheCorrupt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "models-cache.json"), []byte("{半截"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cached := readModelsCache(); cached.Models != "" || cached.ThinkingLevels != "" {
		t.Errorf("损坏的缓存应该被当成没有缓存，实际 %+v", cached)
	}
}

// 写缓存是读-改-写：改思考档位不该把已经缓存的模型清单冲掉。
func TestWriteModelsCacheMerges(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	svc := &PiService{}

	svc.writeModelsCache(func(cache *modelsCache) { cache.Models = `{"models":[]}` })
	svc.writeModelsCache(func(cache *modelsCache) { cache.ThinkingLevels = `{"levels":[]}` })

	cached := readModelsCache()
	if cached.Models != `{"models":[]}` {
		t.Errorf("第二次写不该冲掉模型清单，实际 %q", cached.Models)
	}
	if cached.ThinkingLevels != `{"levels":[]}` {
		t.Errorf("思考档位应该被写入，实际 %q", cached.ThinkingLevels)
	}
}
