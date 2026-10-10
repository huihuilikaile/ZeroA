package bridge

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zeroa/internal/tools"
)

// ★ AI 登记的工具必须真的写进清单，而且**下次能直接命中**。
//
// 这是这个功能的全部意义：模型在做题时发现/装了新工具，登记之后
// 下次启动不该又变成"本机没找到"。
func TestCatalogAddPersistsAndDetects(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}
	t.Cleanup(func() { tools.SetActiveCatalog(tools.DefaultCatalog()) })

	// 造一个"工具箱目录里的工具"（不在 PATH 里，正是最需要登记的那类）
	toolbox := filepath.Join(t.TempDir(), "toolbox", "bin")
	if err := os.MkdirAll(toolbox, 0o755); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(toolbox, "mynewtool.exe")
	if err := os.WriteFile(toolPath, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{
		"id": "mynewtool", "name": "我的新工具", "path": toolPath,
		"direction": "reverse", "capabilities": []string{"reverse.static"},
		"note": "做题时装的", "addedBy": "ai",
	})
	result, err := service.CatalogAdd(string(payload))
	if err != nil {
		t.Fatalf("CatalogAdd: %v", err)
	}
	var added struct {
		ID     string       `json:"id"`
		Path   string       `json:"path"`
		Status tools.Status `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.Data), &added); err != nil {
		t.Fatal(err)
	}
	if added.ID != "mynewtool" {
		t.Errorf("id 不对: %+v", added)
	}
	if !added.Status.Available || added.Status.Path != toolPath {
		t.Errorf("登记后应当立刻验证为可用: %+v", added.Status)
	}

	// 文件真的写盘了，而且是合法 JSON（能被重新读进来）
	raw, err := os.ReadFile(filepath.Join(agentDirPath, "tool-catalog.json"))
	if err != nil {
		t.Fatalf("清单文件没写出来: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "mynewtool") {
		t.Errorf("文件里没有登记内容:\n%s", text)
	}
	// 注意：JSON 里 Windows 路径的反斜杠是转义的，按原文比会假失败
	var persisted struct {
		Tools []struct {
			ID        string `json:"id"`
			Path      string `json:"path"`
			AddedBy   string `json:"addedBy"`
			Direction string `json:"direction"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(tools.StripJSONComments(text)), &persisted); err != nil {
		t.Fatalf("写出来的不是合法 JSON: %v\n%s", err, text)
	}
	if len(persisted.Tools) != 1 || persisted.Tools[0].Path != toolPath {
		t.Errorf("path 没写对: %+v", persisted.Tools)
	}
	if persisted.Tools[0].AddedBy != "ai" {
		t.Errorf("要记录是谁加的（排查「这条哪来的」）:\n%s", text)
	}
	if !strings.HasPrefix(text, "//") {
		t.Error("文件应当带说明头（合并规则 + 会被程序重写）")
	}

	// 重新加载后：路径来自清单记录，而不是重新扫 PATH
	tools.SetActiveCatalog(tools.LoadCatalog(filepath.Join(agentDirPath, "tool-catalog.json")))
	defer tools.SetActiveCatalog(tools.DefaultCatalog())
	var found tools.Definition
	for _, tool := range tools.ActiveCatalog().Tools {
		if tool.ID == "mynewtool" {
			found = tool
		}
	}
	if found.ID == "" {
		t.Fatal("重新加载后清单里没有这条")
	}
	if found.Path != toolPath || found.AddedBy != "ai" {
		t.Errorf("path/addedBy 没读回来: %+v", found)
	}
	status := tools.DetectOne(found)
	if !status.Available || status.Detail != "清单记录的路径" {
		t.Errorf("应当直接命中清单记录的路径: %+v", status)
	}
}

// 只给 id、不给任何查找线索的登记要被拒绝（否则等于加了一条永远找不到的工具）。
func TestCatalogAddRequiresClue(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	service := &PiService{}
	if _, err := service.CatalogAdd(`{"id":"nolue"}`); err == nil {
		t.Fatal("没有任何查找线索时应当报错")
	}
	if _, err := service.CatalogAdd(`{"id":"../evil","path":"C:\\\\x.exe"}`); err == nil {
		t.Fatal("非法 id 应当被拒绝")
	}
}

// ★ 检测结果写回清单：新发现的路径自动记账（addedBy=detect）。
func TestCatalogCheckRecordsPaths(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}
	t.Cleanup(func() { tools.SetActiveCatalog(tools.DefaultCatalog()) })

	toolbox := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(toolbox, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(toolbox, "recordme.exe")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 只在"额外搜索目录"里能找到它：PATH 里没有
	userCatalog := filepath.Join(agentDirPath, "tool-catalog.json")
	if err := os.WriteFile(userCatalog, []byte(`{
	  "version": 1,
	  "searchPaths": ["`+strings.ReplaceAll(toolbox, `\`, `\\`)+`"],
	  "tools": [ { "id": "recordme", "name": "待记账", "direction": "base",
	    "detect": { "commands": ["recordme"] }, "capabilities": ["script.run"] } ]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	service.loadToolCatalog()

	result, err := service.CatalogCheck(true)
	if err != nil {
		t.Fatalf("CatalogCheck: %v", err)
	}
	var payload struct {
		Recorded []string `json:"recorded"`
		Failures []string `json:"failures"`
		Summary  struct {
			Available int `json:"available"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Failures) != 0 {
		t.Fatalf("写回失败: %v", payload.Failures)
	}
	if !containsString(payload.Recorded, "recordme") {
		t.Errorf("应当把 recordme 的路径写回清单，实际 %v", payload.Recorded)
	}
	raw, _ := os.ReadFile(userCatalog)
	var persisted struct {
		Tools []struct {
			ID      string `json:"id"`
			Path    string `json:"path"`
			AddedBy string `json:"addedBy"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(tools.StripJSONComments(string(raw))), &persisted); err != nil {
		t.Fatalf("写回来的不是合法 JSON: %v", err)
	}
	var recorded *struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		AddedBy string `json:"addedBy"`
	}
	for index := range persisted.Tools {
		if persisted.Tools[index].ID == "recordme" {
			recorded = &persisted.Tools[index]
		}
	}
	if recorded == nil || recorded.Path != stub {
		t.Errorf("文件里没有记录路径: %+v", persisted.Tools)
	}
	if recorded != nil && recorded.AddedBy != "detect" {
		t.Errorf("写回应标记来源为 detect: %+v", recorded)
	}
}

// 桥接端点：模型调的就是这三个（令牌不对必须被拒）。
func TestCatalogBridgeEndpoints(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}
	t.Cleanup(func() { tools.SetActiveCatalog(tools.DefaultCatalog()) })
	service.loadToolCatalog()

	// 起真桥（与委派共用一个控制口）：清单端点挂在它的 mux 上、共用同一套令牌。
	if err := service.ensureBridge(); err != nil {
		t.Fatalf("ensureBridge: %v", err)
	}
	manager := service.delegationManager()
	address, token := manager.BridgeAddress(), manager.BridgeToken()
	if address == "" {
		t.Fatal("桥没起来")
	}
	t.Cleanup(func() { manager.StopBridge() })

	call := func(path, body, tok string) (int, string) {
		request, _ := http.NewRequest(http.MethodPost, address+path, strings.NewReader(body))
		request.Header.Set("x-delegate-token", tok)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(raw)
	}

	// 令牌错 → 403（控制口是本机的，但也不能裸奔）
	if got, _ := call("/catalog/list", "{}", "wrong"); got != http.StatusForbidden {
		t.Errorf("令牌不对应当 403，实际 %d", got)
	}

	// 列表
	code, text := call("/catalog/list", "{}", token)
	if code != http.StatusOK {
		t.Fatalf("catalog/list 应当 200，实际 %d（%s）", code, text)
	}
	if !strings.Contains(text, "\"tools\"") {
		t.Errorf("列表返回结构不对: %s", text)
	}

	// 登记
	stub := filepath.Join(t.TempDir(), "bridge-tool.exe")
	if err := os.WriteFile(stub, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"id": "bridge-tool", "path": stub, "direction": "base"})
	code, text = call("/catalog/add", string(body), token)
	if code != http.StatusOK {
		t.Fatalf("catalog/add 应当 200，实际 %d（%s）", code, text)
	}
	if !strings.Contains(text, "bridge-tool") {
		t.Errorf("add 返回不对: %s", text)
	}

	// 检测（不写回）
	code, text = call("/catalog/check", `{"record":false}`, token)
	if code != http.StatusOK {
		t.Fatalf("catalog/check 应当 200，实际 %d", code)
	}
	if !strings.Contains(text, "\"summary\"") {
		t.Errorf("check 返回不对: %s", text)
	}
}

// 扩展源码要落盘（模型靠它才能调用这几个工具），并且内容不能含反引号（Go 原始字符串）。
func TestCatalogExtensionWritten(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)

	path, err := ensureCatalogExtension()
	if err != nil {
		t.Fatalf("ensureCatalogExtension: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, must := range []string{"catalog_list", "catalog_check", "catalog_add", "catalog_record", "registerTool"} {
		if !strings.Contains(text, must) {
			t.Errorf("扩展里缺少 %s", must)
		}
	}
	// 内容一致时不重写（避免每次启动都动文件）
	info, _ := os.Stat(path)
	if _, err := ensureCatalogExtension(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(info.ModTime()) {
		t.Error("内容没变时不该重写扩展文件")
	}
}

// "环境管理员"角色不再内置（方向性角色由用户按需导入）。
// 这里只保留它依赖的扩展命令本身的行为测试。

