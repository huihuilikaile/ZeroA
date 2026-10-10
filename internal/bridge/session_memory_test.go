package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 会话记忆要覆盖"多个工作区"这个真实场景：用户有两个文件夹，
// 第一个有记忆、第二个没有，就是这次要修掉的 bug。
//
// 三条不变量：
//  1. 没有任何显式记录时，回退到该工作区"最近一次会话"（source=recent）——
//     这正是第二个工作区过去没有记忆的原因：它从没被显式记过；
//  2. 显式记住的会话优先（source=memory）；
//  3. 用户主动"新建对话"后是 cleared，不能再被回退逻辑撤销。
func TestSessionMemoryPerWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	workspaceA := filepath.Join(t.TempDir(), "alpha")
	workspaceB := filepath.Join(t.TempDir(), "beta")
	for _, dir := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessionA := writeSession(t, root, "a", workspaceA, "2026-10-05T01-00-00-000Z_aaa", "第一条 A 会话")
	sessionB := writeSession(t, root, "b", workspaceB, "2026-10-05T01-00-00-000Z_bbb", "第一条 B 会话")

	svc := &PiService{}

	// 1) 两个工作区都没有显式记录：各自回退到自己的最近会话。
	//    第二个工作区（B）以前在这里拿到空串，界面上就是"没有记忆"。
	for _, want := range []struct {
		cwd  string
		path string
	}{{workspaceA, sessionA}, {workspaceB, sessionB}} {
		payload := rememberedPayload(t, svc, want.cwd)
		if payload.Source != "recent" {
			t.Errorf("%s 应该回退到最近会话，实际 source=%q", want.cwd, payload.Source)
		}
		if !samePath(payload.SessionPath, want.path) {
			t.Errorf("%s 恢复的会话不对: %q，期望 %q", want.cwd, payload.SessionPath, want.path)
		}
	}

	// 2) 显式记住后优先使用记录，并且不影响另一个工作区。
	if _, err := svc.RememberSession(workspaceA, sessionA); err != nil {
		t.Fatalf("RememberSession: %v", err)
	}
	if payload := rememberedPayload(t, svc, workspaceA); payload.Source != "memory" || !samePath(payload.SessionPath, sessionA) {
		t.Errorf("A 应该用显式记忆，实际 %+v", payload)
	}
	if payload := rememberedPayload(t, svc, workspaceB); payload.Source != "recent" || !samePath(payload.SessionPath, sessionB) {
		t.Errorf("B 不该被 A 的记忆影响，实际 %+v", payload)
	}

	// 3) 新建对话 = 显式清空：即使用磁盘上还有历史，也不能自动恢复它。
	if _, err := svc.RememberSession(workspaceB, ""); err != nil {
		t.Fatalf("RememberSession(clear): %v", err)
	}
	if payload := rememberedPayload(t, svc, workspaceB); payload.Source != "cleared" || payload.SessionPath != "" {
		t.Errorf("新建对话后不应恢复历史，实际 %+v", payload)
	}

	// 4) 不允许把别的目录的会话记到当前工作区（防止迟到的回调写错地方）。
	if _, err := svc.RememberSession(workspaceA, sessionB); err == nil {
		t.Error("把 B 的会话记到 A 应该被拒绝")
	}
}

// 回退必须挑"最近一次"，而不是目录里碰巧第一个读到的文件。
func TestSessionMemoryFallbackPicksNewest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	workspace := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	older := writeSession(t, root, "ws", workspace, "2026-10-04T01-00-00-000Z_old", "旧会话")
	newer := writeSession(t, root, "ws", workspace, "2026-10-05T01-00-00-000Z_new", "新会话")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	payload := rememberedPayload(t, (&PiService{}), workspace)
	if !samePath(payload.SessionPath, newer) {
		t.Errorf("应该恢复最近更新的会话 %q，实际 %q", newer, payload.SessionPath)
	}
}

// 记忆文件里的路径一旦失效（会话被删/移走），要安静地回退，而不是报错卡住启动。
func TestSessionMemoryIgnoresStaleRecord(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	workspace := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	session := writeSession(t, root, "ws", workspace, "2026-10-05T01-00-00-000Z_keep", "还在的会话")
	svc := &PiService{}
	if _, err := svc.RememberSession(workspace, session); err != nil {
		t.Fatalf("RememberSession: %v", err)
	}
	if err := os.Remove(session); err != nil {
		t.Fatal(err)
	}
	payload := rememberedPayload(t, svc, workspace)
	if payload.Source == "memory" {
		t.Errorf("失效的记忆不该继续返回: %+v", payload)
	}
	if payload.SessionPath != "" {
		t.Errorf("没有可恢复的会话时应返回空路径，实际 %q", payload.SessionPath)
	}
}

// 上次工作区必须存在后端文件里：WebView 的 localStorage 会整体丢失，
// 一旦只依赖它，重启后就会退回默认目录，恢复出来的是另一个工作区的对话。
func TestActiveWorkspacePersistsInBackend(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())

	first := filepath.Join(t.TempDir(), "alpha")
	second := filepath.Join(t.TempDir(), "beta")
	svc := &PiService{}

	if _, err := svc.SetActiveWorkspace(first); err != nil {
		t.Fatalf("SetActiveWorkspace(first): %v", err)
	}
	if _, err := svc.SetActiveWorkspace(second); err != nil {
		t.Fatalf("SetActiveWorkspace(second): %v", err)
	}
	// 模拟重启：换一个服务实例，只从磁盘读。
	defaults := defaultsPayload(t, &PiService{})
	if !samePath(defaults.LastWorkspace, second) {
		t.Errorf("重启后应回到最后使用的工作区，实际 %q", defaults.LastWorkspace)
	}
	if len(defaults.Workspaces) != 2 {
		t.Fatalf("两个工作区都该保留，实际 %v", defaults.Workspaces)
	}

	// 重复设置同一个工作区不产生重复行（大小写/分隔符差异也要认成同一个）。
	if _, err := svc.SetActiveWorkspace(strings.ToUpper(first)); err != nil {
		t.Fatalf("SetActiveWorkspace(dup): %v", err)
	}
	if defaults := defaultsPayload(t, &PiService{}); len(defaults.Workspaces) != 2 {
		t.Errorf("同一工作区不该重复入列，实际 %v", defaults.Workspaces)
	}

	// 从侧栏移除工作区（现在的语义就是"删除"）：列表里只剩另一个，重启也不复活
	if _, err := svc.DeleteWorkspace(first); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if defaults := defaultsPayload(t, &PiService{}); len(defaults.Workspaces) != 1 {
		t.Errorf("删除后应剩 1 个工作区，实际 %v", defaults.Workspaces)
	}
}

// 工作区列表不能出现重复行：记忆里存的是规范化键（小写 + 正斜杠），
// 而磁盘路径是 `E:\Foo\Bar` 这种写法。两处混用就会把同一个目录显示成两行。
func TestDefaultsWorkspacesHaveNoDuplicateSpellings(t *testing.T) {
	agentDirPath := t.TempDir()
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	workspace := filepath.Join(t.TempDir(), "MixedCase", "WorkSpace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	session := writeSession(t, root, "mixed", workspace, "2026-10-05T01-00-00-000Z_mix", "混合大小写工作区")

	svc := &PiService{}
	if _, err := svc.SetActiveWorkspace(workspace); err != nil {
		t.Fatalf("SetActiveWorkspace: %v", err)
	}
	if _, err := svc.RememberSession(workspace, session); err != nil {
		t.Fatalf("RememberSession: %v", err)
	}

	defaults := defaultsPayload(t, &PiService{})
	// lastWorkspace 必须是磁盘上的原始写法，而不是 e:/... 这种规范化键：
	// 它会被前端直接当成工作区路径用。
	if normalizeDir(defaults.LastWorkspace) != normalizeDir(workspace) {
		t.Errorf("lastWorkspace 指向了别的工作区: %q", defaults.LastWorkspace)
	}
	if strings.Contains(defaults.LastWorkspace, "/") {
		t.Errorf("lastWorkspace 应该是原始路径写法，实际 %q", defaults.LastWorkspace)
	}

	seen := map[string]bool{}
	for _, item := range defaults.Workspaces {
		key := normalizeDir(item)
		if seen[key] {
			t.Fatalf("工作区列表出现重复项: %v", defaults.Workspaces)
		}
		seen[key] = true
	}
	if len(defaults.Workspaces) != 1 {
		t.Errorf("应该只有 1 个工作区，实际 %v", defaults.Workspaces)
	}
}

// 侧栏会同时列出所有展开的工作区，批量接口必须一次返回、顺序对应、互不串组。
func TestListSessionsBatchGroupsByWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	alpha := filepath.Join(t.TempDir(), "alpha")
	beta := filepath.Join(t.TempDir(), "beta")
	empty := filepath.Join(t.TempDir(), "empty")
	for _, dir := range []string{alpha, beta, empty} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSession(t, root, "alpha", alpha, "2026-10-05T01-00-00-000Z_a1", "A 的第一条")
	newest := writeSession(t, root, "alpha", alpha, "2026-10-05T02-00-00-000Z_a2", "A 的第二条")
	writeSession(t, root, "beta", beta, "2026-10-05T03-00-00-000Z_b1", "B 的第一条")
	// 同一毫秒写完的两个文件 mtime 可能相等，排序就成了抽签：
	// 显式把 a2 的时间调新，用例才是在断言"按修改时间倒序"而不是在赌运气。
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, "--alpha--", "2026-10-05T01-00-00-000Z_a1.jsonl"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newest, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := (&PiService{}).ListSessionsBatch([]string{alpha, beta, empty})
	if err != nil {
		t.Fatalf("ListSessionsBatch: %v", err)
	}
	var payload struct {
		Groups []struct {
			Cwd      string `json:"cwd"`
			Sessions []struct {
				Title string `json:"title"`
			} `json:"sessions"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析结果: %v", err)
	}
	if len(payload.Groups) != 3 {
		t.Fatalf("应该按请求顺序返回 3 组，实际 %d", len(payload.Groups))
	}
	if len(payload.Groups[0].Sessions) != 2 || len(payload.Groups[1].Sessions) != 1 || len(payload.Groups[2].Sessions) != 0 {
		t.Fatalf("分组数量不对: %d/%d/%d",
			len(payload.Groups[0].Sessions), len(payload.Groups[1].Sessions), len(payload.Groups[2].Sessions))
	}
	// 组与请求一一对应：第一组只能是 alpha 的会话
	if payload.Groups[0].Sessions[0].Title != "A 的第二条" {
		t.Errorf("组顺序或内容不对: %+v", payload.Groups[0])
	}
	if payload.Groups[1].Sessions[0].Title != "B 的第一条" {
		t.Errorf("第二组应该是 beta 的会话: %+v", payload.Groups[1])
	}
}

// 删除工作区：能删的删干净（会话文件 + 登记 + 记忆），不能删的（默认 / 正在使用）一条都不能少。
func TestDeleteWorkspaceRules(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	alpha := filepath.Join(t.TempDir(), "alpha")
	beta := filepath.Join(t.TempDir(), "beta")
	for _, dir := range []string{alpha, beta} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alphaOne := writeSession(t, root, "alpha", alpha, "2026-10-05T01-00-00-000Z_a1", "A 的第一条")
	alphaTwo := writeSession(t, root, "alpha", alpha, "2026-10-05T02-00-00-000Z_a2", "A 的第二条")
	betaOne := writeSession(t, root, "beta", beta, "2026-10-05T03-00-00-000Z_b1", "B 的第一条")

	// beta 是"当前正在使用的工作区"：删 alpha 应该放行
	svc := &PiService{workspaceDir: beta}
	if _, err := svc.RememberSession(alpha, alphaOne); err != nil {
		t.Fatalf("RememberSession: %v", err)
	}
	result, err := svc.DeleteWorkspace(alpha)
	if err != nil {
		t.Fatalf("DeleteWorkspace(alpha): %v", err)
	}
	var payload struct {
		Sessions int `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Sessions != 2 {
		t.Errorf("应该删掉 2 条会话，实际 %d", payload.Sessions)
	}
	for _, path := range []string{alphaOne, alphaTwo} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("alpha 的会话文件应该被删掉: %s", path)
		}
	}
	if _, err := os.Stat(betaOne); err != nil {
		t.Errorf("不该动别的工作区的会话: %v", err)
	}
	// 记忆与侧栏列表也要清掉，否则下次启动它会带着记忆又冒出来
	if _, err := os.Stat(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "gui-state.json")); err == nil {
		state := loadGUIState()
		if _, ok := state.Sessions[normalizeDir(alpha)]; ok {
			t.Error("删除工作区后不该还留着它的会话记忆")
		}
	}

	// 正在使用的工作区不给删
	if _, err := svc.DeleteWorkspace(beta); err == nil || !strings.Contains(err.Error(), "正在使用") {
		t.Errorf("删除当前工作区应该被拒绝，实际 err=%v", err)
	}
	if _, err := os.Stat(betaOne); err != nil {
		t.Errorf("被拒绝时不该动任何文件: %v", err)
	}

	// 默认工作区不给删
	if _, err := (&PiService{workspaceDir: alpha}).DeleteWorkspace(DefaultWorkspacePath()); err == nil {
		t.Error("默认工作区应该拒绝删除")
	}
}

// 删除过的工作区不能在重启后自己回来。
//
// 这是用户报的真实 bug：删掉的工作区是"默认目录"（exe 的上一级）时，
// 启动流程会把它当默认工作区补进侧栏，于是"删了又回来"。
func TestDeletedWorkspaceStaysGone(t *testing.T) {
	root := t.TempDir()
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", root)

	workspace := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	session := writeSession(t, root, "ws", workspace, "2026-10-05T01-00-00-000Z_ws", "会被删掉的会话")

	svc := &PiService{workspaceDir: filepath.Join(t.TempDir(), "other")}
	if _, err := svc.SetActiveWorkspace(workspace); err != nil {
		t.Fatalf("SetActiveWorkspace: %v", err)
	}
	if _, err := svc.RememberSession(workspace, session); err != nil {
		t.Fatalf("RememberSession: %v", err)
	}
	if _, err := svc.DeleteWorkspace(workspace); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}

	// 模拟重启：新实例只读磁盘
	fresh := &PiService{}
	defaults := defaultsPayload(t, fresh)
	if normalizeDir(defaults.LastWorkspace) == normalizeDir(workspace) {
		t.Errorf("删过的工作区不该再当上次工作区: %q", defaults.LastWorkspace)
	}
	for _, item := range defaults.Workspaces {
		if normalizeDir(item) == normalizeDir(workspace) {
			t.Errorf("删过的工作区不该出现在侧栏列表: %v", defaults.Workspaces)
		}
	}
	if len(defaults.Dismissed) == 0 {
		t.Error("要记住'用户删过哪些工作区'，否则它会从默认目录/记忆里复活")
	}
	// 用户主动切回去 = 重新启用
	if _, err := fresh.SetActiveWorkspace(workspace); err != nil {
		t.Fatalf("SetActiveWorkspace(revive): %v", err)
	}
	if revoked := defaultsPayload(t, &PiService{}); len(revoked.Dismissed) != 0 {
		t.Errorf("主动切回后不该还在'删过'名单里: %v", revoked.Dismissed)
	}
}

type defaultsData struct {
	LastWorkspace string   `json:"lastWorkspace"`
	Workspaces    []string `json:"workspaces"`
	Dismissed     []string `json:"dismissed"`
}

func defaultsPayload(t *testing.T, svc *PiService) defaultsData {
	t.Helper()
	result, err := svc.GetDefaults()
	if err != nil {
		t.Fatalf("GetDefaults: %v", err)
	}
	if !result.Success {
		t.Fatalf("GetDefaults 失败: %+v", result)
	}
	var payload defaultsData
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析默认值: %v", err)
	}
	return payload
}

type rememberedPayloadData struct {
	Cwd         string `json:"cwd"`
	SessionPath string `json:"sessionPath"`
	Source      string `json:"source"`
}

func rememberedPayload(t *testing.T, svc *PiService, cwd string) rememberedPayloadData {
	t.Helper()
	result, err := svc.GetRememberedSession(cwd)
	if err != nil {
		t.Fatalf("GetRememberedSession(%s): %v", cwd, err)
	}
	if !result.Success {
		t.Fatalf("GetRememberedSession(%s) 失败: %+v", cwd, result)
	}
	var payload rememberedPayloadData
	if err := json.Unmarshal([]byte(result.Data), &payload); err != nil {
		t.Fatalf("解析结果: %v", err)
	}
	return payload
}

// writeSession 造一个 Pi 形状的会话文件：首行是 session 头（带 cwd），第二条是用户消息。
// 返回它的绝对路径。
func writeSession(t *testing.T, root, group, cwd, fileName, title string) string {
	t.Helper()
	dir := filepath.Join(root, "--"+group+"--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName+".jsonl")
	body := "{\"type\":\"session\",\"version\":3,\"id\":\"" + fileName + "\",\"cwd\":" + jsonString(cwd) + "}\n" +
		"{\"type\":\"message\",\"id\":\"m1\",\"message\":{\"role\":\"user\",\"content\":" + jsonString(title) + "}}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func samePath(a, b string) bool { return normalizeDir(a) == normalizeDir(b) }
