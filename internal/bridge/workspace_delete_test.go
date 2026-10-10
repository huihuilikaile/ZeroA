package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★ 空输入必须原样返回空，不能变成"进程当前目录"。
//
// 起因（用户实测报的 bug）：在便携安装里删"和安装目录同名的工作区"时，
// 明明一条会话都没有，却报"工作区正在使用：它里面还有会话正被 Pi 打开着"。
// 根因是 `filepath.Clean("")` 返回 "."，导致空值一路走到 filepath.Abs，
// 变成了 app 的当前工作目录；而 app 的工作目录恰好就是安装目录。
func TestNormalizeDirEmptyInputStaysEmpty(t *testing.T) {
	for _, input := range []string{"", "   ", "\t", "."} {
		if got := normalizeDir(input); got != "" {
			t.Errorf("normalizeDir(%q) 应当返回空串，得到 %q（会变成进程 cwd，引发假匹配）", input, got)
		}
	}

	// 正常路径仍然照常规范化：小写 + 正斜杠
	dir := t.TempDir()
	got := normalizeDir(dir)
	if got == "" || strings.Contains(got, "\\") {
		t.Errorf("正常路径应当被规范化成小写正斜杠，得到 %q", got)
	}
	if got != strings.ToLower(filepath.ToSlash(dir)) {
		t.Errorf("规范化结果不对：%q", got)
	}
}

// sessionCwd 对"读不到的会话文件"必须返回空串。
//
// 这是上面那个 bug 的直接触发点：返回 cwd 时，守卫会把"要删的工作区"
// 和"app 的工作目录"比成相等。
func TestSessionCwdMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := sessionCwd(filepath.Join(dir, "nope.jsonl")); got != "" {
		t.Errorf("文件不存在时应当返回空串，得到 %q", got)
	}
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sessionCwd(empty); got != "" {
		t.Errorf("空文件应当返回空串，得到 %q", got)
	}
	junk := filepath.Join(dir, "junk.jsonl")
	if err := os.WriteFile(junk, []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sessionCwd(junk); got != "" {
		t.Errorf("非会话文件应当返回空串，得到 %q", got)
	}

	// 正常会话头照常读出 cwd
	header := `{"type":"session","id":"x","timestamp":"2026-10-06T11:32:51Z","cwd":"C:\\some\\workspace"}`
	ok := filepath.Join(dir, "ok.jsonl")
	if err := os.WriteFile(ok, []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sessionCwd(ok); got != "c:/some/workspace" {
		t.Errorf("正常会话头的 cwd 没读出来: %q", got)
	}
}

// ★ 端到端复现用户场景：没有会话的工作区必须能删掉。
//
// 构造方式刻意贴近真实故障：当前会话文件指向一个**不存在的文件**
// （Pi 报了会话路径但还没落盘），而要删的工作区是**进程当前目录**。
// 修复前这两者会被判成相等 → 误报"还有会话被打开着"。
func TestDeleteWorkspaceWithoutSessionsSucceeds(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// 有"当前会话"，但文件并不存在（这正是触发条件）
	service.sessionFile = filepath.Join(agentDirPath, "sessions", "not-written-yet.jsonl")

	if _, err := service.DeleteWorkspace(cwd); err != nil {
		t.Fatalf("没有会话的工作区应当能删除，却被拒绝: %v", err)
	}
}

// 对照：真的有一个**能读到的**会话落在这个工作区里时，仍然要拒绝。
func TestDeleteWorkspaceRefusedWhenSessionOpen(t *testing.T) {
	agentDirPath := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDirPath)
	service := &PiService{}

	workspace := t.TempDir()
	sessionPath := filepath.Join(agentDirPath, "sessions", "live.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(workspace)
	header := `{"type":"session","id":"live","timestamp":"2026-10-06T11:32:51Z","cwd":` + string(encoded) + `}`
	if err := os.WriteFile(sessionPath, []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service.sessionFile = sessionPath

	_, err := service.DeleteWorkspace(workspace)
	if err == nil {
		t.Fatal("会话确实落在这个工作区里时应当拒绝删除")
	}
	if !strings.Contains(err.Error(), "正在使用") {
		t.Errorf("拒绝原因应当说清是「正在使用」，实际: %v", err)
	}
}
