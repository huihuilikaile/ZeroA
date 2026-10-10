package bridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"zeroa/internal/roles"
)

// 角色导入导出的**接口层**：参数解析与 Result 包装在这里，
// 真正的读写逻辑在 internal/roles（Export / Import），可以独立测试。

// ExportRoles 把角色导出成一个 JSON 文件。
//
// payload: {"roleIds": ["ctf", ...], "path": "可选的导出路径"}
//   - roleIds 为空 = 导出全部角色；
//   - path 为空 = 写到 <配置目录>/exports/roles-<时间戳>.json；
//   - path 是目录 = 在目录里放默认文件名；是文件 = 用它（自动补 .json 后缀）。
func (s *PiService) ExportRoles(payload string) (Result, error) {
	var request struct {
		RoleIDs []string `json:"roleIds"`
		Path    string   `json:"path"`
	}
	if strings.TrimSpace(payload) != "" {
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			return Result{}, fmt.Errorf("导出参数解析失败: %w", err)
		}
	}
	target, ids, err := roles.Export(request.RoleIDs, request.Path)
	if err != nil {
		return Result{}, err
	}
	payloadOut, _ := json.Marshal(map[string]any{
		"path":  target,
		"count": len(ids),
		"ids":   ids,
	})
	return Result{Command: "export_roles", Success: true, Data: string(payloadOut)}, nil
}

// ImportRoles 从文件或目录导入角色，返回逐条结果。
//
// 支持三种输入（都判一遍，用户不用记格式）：
//  1. 导出的自包含 JSON 包（format = zeroa-roles）；
//  2. 单个角色定义 JSON（有 id 字段）；
//  3. 目录：读里面的 *.json（可选 prompts/<id>.md）——社区包与 .pi/roles 都是这个形状。
func (s *PiService) ImportRoles(source string) (Result, error) {
	created, overwritten, failed, err := roles.Import(source)
	if err != nil {
		return Result{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"created":     created,
		"overwritten": overwritten,
		"failed":      failed,
		"source":      strings.TrimSpace(source),
	})
	return Result{Command: "import_roles", Success: true, Data: string(payload)}, nil
}
