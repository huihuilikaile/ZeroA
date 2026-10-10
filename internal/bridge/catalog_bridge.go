package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"zeroa/internal/delegation"
)

// writeJSON 是清单端点的响应出口（委派域的桥有自己的一份，互不相依）。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// 工具清单的 AI 接口。
//
// 组成方式照搬委派那条链（同一套基础设施，不再造新轮子）：
//
//	Pi 会话里的扩展工具  →  本机 HTTP 控制口（带令牌）  →  Go 侧改写清单 JSON
//
// 于是模型可以：盘点环境（catalog_check）、登记它找到或刚装好的工具（catalog_add）、
// 查看已知清单（catalog_list）。配合"环境管理员"角色，就是一条自我维护的回路。

const catalogExtFile = "tool-catalog.ts"

// registerCatalogRoutes 把清单相关的端点挂到控制口上（与委派共用令牌）。
//
// 它是 ensureBridge 传给 delegation.Manager.EnsureBridge 的 extend 回调：
// 桥（监听/令牌/生命周期）归委派域，清单端点归主包，两边在同一个 mux 上汇合。
func (s *PiService) registerCatalogRoutes(mux *http.ServeMux, guard delegation.Guard) {
	mux.HandleFunc("/catalog/list", guard(s.handleCatalogList))
	mux.HandleFunc("/catalog/check", guard(s.handleCatalogCheck))
	mux.HandleFunc("/catalog/add", guard(s.handleCatalogAdd))
}

func (s *PiService) handleCatalogList(w http.ResponseWriter, _ *http.Request) {
	result, err := s.CatalogList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(result.Data))
}

func (s *PiService) handleCatalogCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Record *bool `json:"record"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	record := true
	if body.Record != nil {
		record = *body.Record
	}
	result, err := s.CatalogCheck(record)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(result.Data))
}

func (s *PiService) handleCatalogAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Path         string   `json:"path"`
		Commands     []string `json:"commands"`
		PythonImport string   `json:"pythonImport"`
		NodeModule   string   `json:"nodeModule"`
		Direction    string   `json:"direction"`
		Note         string   `json:"note"`
		Capabilities []string `json:"capabilities"`
		VersionArgs  []string `json:"versionArgs"`
		AddedBy      string   `json:"addedBy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "参数解析失败: " + err.Error()})
		return
	}
	patch := map[string]any{"id": body.ID}
	if strings.TrimSpace(body.Name) != "" {
		patch["name"] = body.Name
	}
	if strings.TrimSpace(body.Path) != "" {
		patch["path"] = body.Path
	}
	if len(body.Commands) > 0 {
		patch["commands"] = body.Commands
	}
	if strings.TrimSpace(body.PythonImport) != "" {
		patch["pythonImport"] = body.PythonImport
	}
	if strings.TrimSpace(body.NodeModule) != "" {
		patch["nodeModule"] = body.NodeModule
	}
	if strings.TrimSpace(body.Direction) != "" {
		patch["direction"] = body.Direction
	}
	if strings.TrimSpace(body.Note) != "" {
		patch["note"] = body.Note
	}
	if body.Capabilities != nil {
		patch["capabilities"] = body.Capabilities
	}
	if len(body.VersionArgs) > 0 {
		patch["versionArgs"] = body.VersionArgs
	}
	addedBy := strings.TrimSpace(body.AddedBy)
	if addedBy == "" {
		addedBy = "ai"
	}
	patch["addedBy"] = addedBy

	raw, err := json.Marshal(patch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	result, err := s.CatalogAdd(string(raw))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(result.Data))
}

// ensureCatalogExtension 把清单扩展写进用户扩展目录（内容变了就覆盖）。
func ensureCatalogExtension() (string, error) {
	dir := filepath.Join(agentDir(), "extensions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, catalogExtFile)
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == catalogExtensionSource {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(catalogExtensionSource), 0o644); err != nil {
		return "", err
	}
	Logf("catalog_extension_written path=%s", path)
	return path, nil
}
