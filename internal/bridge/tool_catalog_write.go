package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeroa/internal/tools"
)

// 清单的**写入**侧：让用户和 AI 都能往里加东西。
//
// 为什么需要写：清单最初只是"我们维护一张表"，但真实使用里有两件事只有运行期才知道——
//  1. 工具装在 PATH 之外的目录（工具箱、便携包、pip 的 Scripts），检测到之后应该**记住路径**；
//  2. 做题过程中会现装新工具或新库（`pip install pycryptodome`、下载 ffuf），装完要登记，
//     否则下次启动又变成"本机没找到"。
//
// 所以：检测结果写回清单（addedBy=detect），AI 也可以用工具登记（addedBy=ai）。
// 写入目标是**用户级清单** <数据目录>/tool-catalog.json（项目级清单是人手维护的，不自动改）。

// catalogPatch 是一次写入请求（字段为 nil 表示不动）。
type catalogPatch struct {
	ID           string    `json:"id"`
	Name         *string   `json:"name,omitempty"`
	Type         *string   `json:"type,omitempty"`
	Direction    *string   `json:"direction,omitempty"`
	Note         *string   `json:"note,omitempty"`
	Path         *string   `json:"path,omitempty"`
	AddedBy      *string   `json:"addedBy,omitempty"`
	Commands     *[]string `json:"commands,omitempty"`
	PythonImport *string   `json:"pythonImport,omitempty"`
	NodeModule   *string   `json:"nodeModule,omitempty"`
	Paths        *[]string `json:"paths,omitempty"`
	VersionArgs  *[]string `json:"versionArgs,omitempty"`
	Capabilities *[]string `json:"capabilities,omitempty"`
	Disabled     *bool     `json:"disabled,omitempty"`
}

// upsertCatalogEntry 把一条改动写进用户级清单（不存在则创建），然后重新加载。
//
// 写盘策略：读现有文件（容忍 // 注释）→ 在内存里改 → 重新输出。
// **会丢掉用户自己写在文件里的注释**，所以文件头固定写明"这是可被程序重写的文件，
// 想写注释请用项目级清单"——不这么写的话，用户加一行注释又被清掉会很难解释。
func (s *PiService) upsertCatalogEntry(patch catalogPatch) (string, error) {
	id, err := tools.NormalizeCatalogID(patch.ID)
	if err != nil {
		return "", err
	}
	target := tools.DefaultCatalogPath(agentDir())
	if strings.TrimSpace(target) == "" {
		return "", errors.New("找不到配置目录")
	}

	file := readCatalogFileForWrite(target)
	file.Version = 1
	if file.Version == 0 {
		file.Version = 1
	}

	index := -1
	for i := range file.Tools {
		if strings.EqualFold(strings.TrimSpace(file.Tools[i].ID), id) {
			index = i
			break
		}
	}
	if index < 0 {
		if patch.Disabled != nil && *patch.Disabled {
			return "", fmt.Errorf("清单里没有 %s，无需禁用", id)
		}
		file.Tools = append(file.Tools, tools.FileTool{ID: id})
		index = len(file.Tools) - 1
	}
	entry := &file.Tools[index]
	applyPatchToFileTool(entry, patch)
	if entry.Name == nil || strings.TrimSpace(*entry.Name) == "" {
		name := id
		entry.Name = &name
	}
	if entry.Direction != nil {
		if err := ensureDirection(&file, *entry.Direction); err != nil {
			return "", err
		}
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(target, []byte(catalogFileHeader()+string(data)+"\n"), 0o644); err != nil {
		return "", err
	}
	Logf("tool_catalog_written path=%s id=%s addedBy=%v", target, id, patch.AddedBy)
	s.loadToolCatalog()
	return id, nil
}

// catalogFileHeader 是写盘时固定的头注释（解释合并规则与"可能被重写"）。
func catalogFileHeader() string {
	return "// ZeroA 工具清单。改完保存后点「工具 → 重新加载」即时生效。\n" +
		"// 合并规则：同 id 覆盖字段 / 新 id 追加 / \"disabled\": true 删除 / \"mode\": \"replace\" 整份替换。\n" +
		"// ⚠️ 这个文件会被程序写回检测结果（path 字段）与 AI 登记的条目，\n" +
		"//    想长期维护自己的注释与分组，请放到 <工作区>/.pi/tool-catalog.json（项目级清单不会被自动改写）。\n\n"
}

func readCatalogFileForWrite(path string) tools.CatalogFile {
	var file tools.CatalogFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return file
	}
	cleaned := tools.StripJSONComments(string(raw))
	if err := json.Unmarshal([]byte(cleaned), &file); err != nil {
		Logf("tool_catalog_write_parse_failed path=%s err=%v（按空清单重写）", path, err)
		return tools.CatalogFile{}
	}
	return file
}

func applyPatchToFileTool(entry *tools.FileTool, patch catalogPatch) {
	if patch.Name != nil {
		entry.Name = patch.Name
	}
	if patch.Type != nil {
		entry.Type = patch.Type
	}
	if patch.Direction != nil {
		entry.Direction = patch.Direction
	}
	if patch.Note != nil {
		entry.Note = patch.Note
	}
	if patch.Path != nil {
		entry.Path = patch.Path
	}
	if patch.AddedBy != nil {
		entry.AddedBy = patch.AddedBy
	}
	if patch.Commands != nil || patch.PythonImport != nil || patch.NodeModule != nil || patch.Paths != nil {
		if entry.Detect == nil {
			entry.Detect = &tools.FileDetect{}
		}
		if patch.Commands != nil {
			entry.Detect.Commands = patch.Commands
		}
		if patch.PythonImport != nil {
			entry.Detect.PythonImport = patch.PythonImport
		}
		if patch.NodeModule != nil {
			entry.Detect.NodeModule = patch.NodeModule
		}
		if patch.Paths != nil {
			entry.Detect.Paths = patch.Paths
		}
	}
	if patch.VersionArgs != nil {
		entry.VersionArgs = patch.VersionArgs
	}
	if patch.Capabilities != nil {
		entry.Capabilities = patch.Capabilities
	}
	if patch.Disabled != nil {
		entry.Disabled = *patch.Disabled
	}
}

func ensureDirection(file *tools.CatalogFile, direction string) error {
	trimmed := strings.TrimSpace(direction)
	if trimmed == "" {
		return errors.New("方向不能为空")
	}
	for _, item := range file.Directions {
		if item.ID == trimmed {
			return nil
		}
	}
	file.Directions = append(file.Directions, tools.CatalogDirection{ID: trimmed, Label: trimmed})
	return nil
}

/* ------------------------------ 给界面/AI 用的方法 ------------------------------ */

// CatalogCheck 跑一次检测并把"已确认存在的路径"写回清单（addedBy=detect）。
//
// 这是"让 AI/用户看到真相"的入口：返回每条工具的可用性、路径、版本与判定依据。
func (s *PiService) CatalogCheck(record bool) (Result, error) {
	s.ensureToolCatalog()
	tools.ResetCache()
	statuses := tools.DetectAll(true)

	recorded := []string{}
	var failures []string
	if record {
		for _, status := range statuses {
			if !status.Available || strings.TrimSpace(status.Path) == "" {
				continue
			}
			if strings.EqualFold(status.Path, status.Definition.Path) {
				continue // 已经记着同一个路径，不用写盘
			}
			addedBy := "detect"
			id, err := s.upsertCatalogEntry(catalogPatch{ID: status.ID, Path: &status.Path, AddedBy: &addedBy})
			if err != nil {
				failures = append(failures, status.ID+": "+err.Error())
				continue
			}
			recorded = append(recorded, id)
		}
	}

	payload, err := json.Marshal(map[string]any{
		"checkedAt": time.Now().Format(time.RFC3339),
		"tools":     statuses,
		"recorded":  recorded,
		"failures":  failures,
		"summary":   summarizeStatuses(statuses),
		"catalog":   tools.ActiveCatalog().Paths,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "catalog_check", Success: true, Data: string(payload)}, nil
}

// CatalogAdd 登记/更新一条工具（界面上"加一条"，AI 也用它）。
//
// payload 是 catalogPatch 的 JSON；至少要有 id，并且给出一条能找到它的线索
// （path / commands / pythonImport / nodeModule）。
func (s *PiService) CatalogAdd(payload string) (Result, error) {
	var patch catalogPatch
	if err := json.Unmarshal([]byte(payload), &patch); err != nil {
		return Result{}, fmt.Errorf("参数解析失败: %w", err)
	}
	if patch.AddedBy == nil {
		addedBy := "user"
		patch.AddedBy = &addedBy
	}
	if patch.Path == nil && patch.Commands == nil && patch.PythonImport == nil && patch.NodeModule == nil && patch.Paths == nil {
		// 允许"只有 id"的登记吗？不行：那等于加了一条永远找不到的工具。
		return Result{}, errors.New("至少要给一条查找线索：path / commands / pythonImport / nodeModule / paths")
	}
	id, err := s.upsertCatalogEntry(patch)
	if err != nil {
		return Result{}, err
	}
	// 登记完立刻验一次：让调用方知道"加进去了，而且（不）找得到"
	status := tools.DetectOne(findDefinition(id))
	payloadOut, _ := json.Marshal(map[string]any{
		"id":      id,
		"path":    tools.DefaultCatalogPath(agentDir()),
		"status":  status,
		"message": "已写入清单；status.available 说明这次是否真的找到了",
	})
	return Result{Command: "catalog_add", Success: true, Data: string(payloadOut)}, nil
}

// CatalogList 返回当前清单（给 AI 看"已经知道什么"，避免重复登记）。
func (s *PiService) CatalogList() (Result, error) {
	s.ensureToolCatalog()
	catalog := tools.ActiveCatalog()
	payload, err := json.Marshal(map[string]any{
		"tools":        catalog.Tools,
		"directions":   catalog.Directions,
		"searchPaths":  catalog.SearchPaths,
		"capabilities": tools.KnownCapabilities(),
		"files":        catalog.Paths,
		"issues":       catalog.Issues,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Command: "catalog_list", Success: true, Data: string(payload)}, nil
}

func findDefinition(id string) tools.Definition {
	for _, tool := range tools.ActiveCatalog().Tools {
		if strings.EqualFold(tool.ID, id) {
			return tool
		}
	}
	return tools.Definition{ID: id}
}

type catalogSummary struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Missing   int `json:"missing"`
}

func summarizeStatuses(statuses []tools.Status) catalogSummary {
	summary := catalogSummary{Total: len(statuses)}
	for _, status := range statuses {
		if status.Available {
			summary.Available++
		} else {
			summary.Missing++
		}
	}
	return summary
}
