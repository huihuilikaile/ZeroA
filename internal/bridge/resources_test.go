package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zeroa/internal/pi"
)

// writeFakeCLI 造一个假的 cli.js 路径：Command 只解析参数，不会真的启动它。
func writeFakeCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli.js")
	if err := os.WriteFile(path, []byte("// fake pi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 是否加载扩展/技能/模板必须"默认放开、可显式关闭"：
//
//	· 默认放开 → 斜杠命令面板才有内容（get_commands 只返回这类东西）
//	· 显式关闭 → 设置里关掉后不再加载（启动快、排障用）
//
// Go 的零值是 false，所以这里专门测"没设置过"与"显式设 false"要区分开。
func TestLoadResourcesDefaultAndOverride(t *testing.T) {
	// 1) 没设置过：默认 true
	t.Setenv("PI_GUI_NO_RESOURCES", "")
	svc := &PiService{}
	cmd, err := pi.Command(pi.Options{Mode: "real", CLIPath: writeFakeCLI(t), LoadResources: svc.loadResources || !svc.loadResourcesSet})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "--no-extensions") {
		t.Errorf("默认不应禁用扩展：%s", joined)
	}

	// 2) 显式关闭：四个 --no-* 都要出现
	disabled := &PiService{}
	disabled.loadResources = false
	disabled.loadResourcesSet = true
	cmd, err = pi.Command(pi.Options{Mode: "real", CLIPath: writeFakeCLI(t), LoadResources: disabled.loadResources || !disabled.loadResourcesSet})
	if err != nil {
		t.Fatalf("Command(关闭): %v", err)
	}
	joined = strings.Join(cmd.Args, " ")
	for _, flag := range []string{"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes"} {
		if !strings.Contains(joined, flag) {
			t.Errorf("关闭时应该带 %s：%s", flag, joined)
		}
	}
	// --no-approve 一直保留（项目信任的安全边界）
	if !strings.Contains(joined, "--no-approve") {
		t.Errorf("--no-approve 应当始终保留：%s", joined)
	}

	// 3) SetLoadResources 能记住"已被显式设置"
	if _, err := svc.SetLoadResources(false); err != nil {
		t.Fatalf("SetLoadResources: %v", err)
	}
	if !svc.loadResourcesSet || svc.loadResources {
		t.Errorf("SetLoadResources(false) 之后应当记录为已设置且为 false：%+v", svc)
	}
	if _, err := svc.SetLoadResources(true); err != nil {
		t.Fatalf("SetLoadResources(true): %v", err)
	}
	if !svc.loadResources || !svc.loadResourcesSet {
		t.Errorf("SetLoadResources(true) 之后应当为 true：%+v", svc)
	}
}

// ExtraEnv 必须真的出现在子进程的环境里。
//
// 它承载的是委派通道的地址与令牌：**静默丢失**的表现是"工具在、一调就报通道不可用"，
// 而真正的原因（环境变量没注入）在界面上完全看不出来。所以这条要有测试盯着。
func TestCommandInjectsExtraEnv(t *testing.T) {
	cmd, err := pi.Command(pi.Options{
		Mode:     "real",
		CLIPath:  writeFakeCLI(t),
		AgentDir: t.TempDir(),
		ExtraEnv: map[string]string{
			"PI_GUI_DELEGATE_URL":   "http://127.0.0.1:12345",
			"PI_GUI_DELEGATE_TOKEN": "secret-token",
		},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	joined := strings.Join(cmd.Env, "\n")
	for _, want := range []string{
		"PI_GUI_DELEGATE_URL=http://127.0.0.1:12345",
		"PI_GUI_DELEGATE_TOKEN=secret-token",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("环境变量没注入：%s", want)
		}
	}
	// 真实 Pi 的配置目录也要在（两者共存，不能互相覆盖）
	if !strings.Contains(joined, "PI_CODING_AGENT_DIR=") {
		t.Error("注入 ExtraEnv 时把 PI_CODING_AGENT_DIR 弄丢了")
	}
}
