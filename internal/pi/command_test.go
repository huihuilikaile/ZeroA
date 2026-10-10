package pi

import (
	"reflect"
	"strings"
	"testing"
)

// 启动参数是「会话按用户选择的模型/思考级别/工具启动」这条需求的落点，
// 所以这里逐个断言，而不是靠观察 GUI 行为。
func TestCommandArgs(t *testing.T) {
	cases := []struct {
		name     string
		opts     Options
		contains []string
		absent   []string
	}{
		{
			name:     "模型与思考级别拼成 provider/id:level",
			opts:     Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, Model: "deepseek/deepseek-v4-pro", Thinking: "high"},
			contains: []string{"--model", "deepseek/deepseek-v4-pro:high"},
		},
		{
			name:     "模型已自带冒号时不再追加思考级别",
			opts:     Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, Model: "deepseek/deepseek-flash:max", Thinking: "high"},
			contains: []string{"--model", "deepseek/deepseek-flash:max"},
			absent:   []string{"deepseek/deepseek-flash:max:high"},
		},
		{
			name:     "只给模型不给级别",
			opts:     Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, Model: "k3"},
			contains: []string{"--model", "k3"},
		},
		{
			name:   "都不给时不传模型参数（用 Pi 自己的默认）",
			opts:   Options{Mode: "real", CLIPath: `C:\fake\pi.exe`},
			absent: []string{"--model"},
		},
		{
			name:     "工具白名单变成 --tools",
			opts:     Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, Tools: []string{"read", "write", "edit"}},
			contains: []string{"--tools", "read,write,edit"},
			absent:   []string{"--no-tools"},
		},
		{
			name:     "空切片表示全部禁用",
			opts:     Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, Tools: []string{}},
			contains: []string{"--no-tools"},
		},
		{
			name:   "nil 表示不干预：既没有 --tools 也没有 --no-tools",
			opts:   Options{Mode: "real", CLIPath: `C:\fake\pi.exe`},
			absent: []string{"--tools", "--no-tools"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := Command(tc.opts)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			joined := strings.Join(cmd.Args, " ")
			for _, want := range tc.contains {
				if !strings.Contains(joined, want) {
					t.Errorf("参数里应该包含 %q，实际: %s", want, joined)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(joined, unwanted) {
					t.Errorf("参数里不该出现 %q，实际: %s", unwanted, joined)
				}
			}
			// 所有真实模式都必须带 rpc 模式，否则会开成交互式 TUI
			for _, must := range []string{"--mode", "rpc"} {
				if !strings.Contains(joined, must) {
					t.Errorf("缺少必需参数 %q，实际: %s", must, joined)
				}
			}
		})
	}
}

// 会话文件默认要落盘：界面左侧的对话记录靠它，switch_session 也靠它。
// 只有显式要求临时对话时才加 --no-session。
func TestCommandSessionFlags(t *testing.T) {
	def, err := Command(Options{Mode: "real", CLIPath: `C:\fake\pi.exe`})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if strings.Contains(strings.Join(def.Args, " "), "--no-session") {
		t.Error("默认不应该带 --no-session（否则会话不落盘，界面列不出历史）")
	}

	temp, err := Command(Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, NoSession: true})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !strings.Contains(strings.Join(temp.Args, " "), "--no-session") {
		t.Error("NoSession=true 时应该带 --no-session")
	}

	custom, err := Command(Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, SessionDir: `D:\sessions`})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !strings.Contains(strings.Join(custom.Args, " "), "--session-dir D:\\sessions") {
		t.Errorf("应该带 --session-dir，实际: %s", strings.Join(custom.Args, " "))
	}
}

// 工作目录不存在时必须报错，而不是悄悄用进程当前目录启动：
// “启动时按所选目录”如果目录写错，用户看到的现象会是“Pi 在别的地方干活了”。
func TestCommandRejectsBadCwd(t *testing.T) {
	if _, err := Command(Options{Mode: "real", CLIPath: `C:\fake\pi.exe`, Cwd: `C:\definitely\not\here\`}); err == nil {
		t.Fatal("不存在的工作目录应该报错")
	}
}

// 角色系统的落点就是这串参数：参数错了的表现是"角色看起来生效了其实没有"，
// 在界面上几乎看不出来。所以这里对 realArgs 做 golden 断言。
func TestRealArgsRoleCompilation(t *testing.T) {
	boolFalse := false
	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "空配置只带固定开关",
			opts: Options{LoadResources: true},
			want: []string{"--mode", "rpc", "--no-approve"},
		},
		{
			name: "角色提示词默认追加（不改基础提示）",
			opts: Options{
				LoadResources:           true,
				AppendSystemPromptFiles: []string{`E:\data\roles\prompts\ctf.md`},
			},
			want: []string{"--mode", "rpc", "--no-approve",
				"--append-system-prompt", `E:\data\roles\prompts\ctf.md`},
		},
		{
			name: "角色声明替换系统提示时用 --system-prompt",
			opts: Options{LoadResources: true, SystemPrompt: "你是取证分析师"},
			want: []string{"--mode", "rpc", "--no-approve", "--system-prompt", "你是取证分析师"},
		},
		{
			name: "工具白名单 + 排除",
			opts: Options{
				LoadResources: true,
				Tools:         []string{"read", "bash"},
				ExcludeTools:  []string{"write"},
			},
			want: []string{"--mode", "rpc", "--no-approve", "--tools", "read,bash",
				"--exclude-tools", "write"},
		},
		{
			name: "工具全禁（空切片不等于不限制）",
			opts: Options{LoadResources: true, Tools: []string{}},
			want: []string{"--mode", "rpc", "--no-approve", "--no-tools"},
		},
		{
			name: "技能与扩展白名单",
			opts: Options{
				LoadResources: true,
				Skills:        []string{"ctf-web"},
				Extensions:    []string{"web-tools"},
			},
			want: []string{"--mode", "rpc", "--no-approve",
				"--no-skills", "--skill", "ctf-web",
				"--no-extensions", "--extension", "web-tools"},
		},
		{
			name: "技能全禁（空切片）",
			opts: Options{LoadResources: true, Skills: []string{}},
			want: []string{"--mode", "rpc", "--no-approve", "--no-skills"},
		},
		{
			name: "作用域模型与不加载目录上下文",
			opts: Options{
				LoadResources: true,
				Models:        []string{"deepseek/v4-flash", "anthropic/sonnet"},
				ContextFiles:  &boolFalse,
			},
			want: []string{"--mode", "rpc", "--no-approve",
				"--models", "deepseek/v4-flash,anthropic/sonnet", "--no-context-files"},
		},
		{
			// 这条最重要：设置里关掉资源加载时，命令行上已经是四个 --no-*，
			// 再点名技能只会让 Pi 报"找不到技能"直接启动失败。
			name: "资源关闭时忽略角色点名的技能与扩展",
			opts: Options{
				LoadResources: false,
				Skills:        []string{"ctf-web"},
				Extensions:    []string{"web-tools"},
				Tools:         []string{"read"},
			},
			want: []string{"--mode", "rpc", "--no-approve",
				"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes",
				"--tools", "read"},
		},
		{
			// 模型与思考级别合成 Pi 的 "provider/id:level" 形式
			name: "模型带思考级别与工具名单",
			opts: Options{
				LoadResources: true,
				Model:         "deepseek/deepseek-v4-flash",
				Thinking:      "high",
				Tools:         []string{"read", "bash", "edit", "write"},
				SessionDir:    `E:\data\sessions`,
			},
			want: []string{"--mode", "rpc", "--no-approve",
				"--session-dir", `E:\data\sessions`,
				"--model", "deepseek/deepseek-v4-flash:high",
				"--tools", "read,bash,edit,write"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := realArgs(tc.opts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("参数不对\n实际: %s\n期望: %s", strings.Join(got, " "), strings.Join(tc.want, " "))
			}
		})
	}
}

// 给模拟器传的参数要能让它复现"首条 system 消息"：至少得有工具名单与提示词文件。
func TestMockArgsCarryRoleConfig(t *testing.T) {
	opts := Options{
		Tools:                   []string{"read", "bash"},
		AppendSystemPromptFiles: []string{`E:\data\roles\prompts\ctf.md`},
		Skills:                  []string{"ctf-web"},
		Model:                   "deepseek/deepseek-v4-flash",
		Thinking:                "high",
	}
	got := strings.Join(mockArgs(opts), " ")
	for _, want := range []string{
		"--tools read,bash", "--append-system-prompt", "ctf.md",
		"--no-skills --skill ctf-web", "--model deepseek/deepseek-v4-flash", "--thinking high",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("模拟器参数缺少 %q\n实际: %s", want, got)
		}
	}

	// 不限制工具时不能传 --tools / --no-tools：那会把"不干预"变成"有态度"
	if args := mockArgs(Options{Model: "m"}); strings.Contains(strings.Join(args, " "), "--tools") {
		t.Errorf("nil 工具不该出现在模拟器参数里: %v", args)
	}
}
