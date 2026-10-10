# ZeroA 项目说明

Pi Agent 的桌面界面。Wails v3（Go + WebView2）+ React 19 + Tailwind v4，
通过 **Pi 的 RPC 模式**（长驻子进程、stdin/stdout 上的 JSONL）驱动本机 Pi。

---

## 一、进程与数据流

```
┌─ 应用（zeroa.exe）────────────────────────────────────────────┐
│  Go 侧                                                             │
│   service.go      PiService：13+ 个方法暴露给前端（Wails 绑定）     │
│   internal/pi     协议层：启动子进程、JSONL 读写、request/response   │
│                   + Notify（只写不等响应，用于 extension_ui_response）│
│   internal/mockpi 假 Pi：PI_GUI_MODE=mock 时替代真机（可复现、免费） │
│   sessions.go     会话目录扫描 / 统计 / 默认工作目录 / agent 目录    │
│   providers.go    模型与凭据（models.json / auth.json）             │
│   files.go        工作区文件树（一次一层）                          │
│   builtin_providers.go  从自带 Pi 的 bundle 抠内置供应商注册表       │
│   attachments.go  附件（一律绝对路径）与模型配置写入                 │
│                                                                    │
│  WebView2（前端）                                                  │
│   stores/  zustand：runtime / timeline / sessions / ui / traces     │
│   rpc/router.ts   把事件与响应翻译成界面状态（唯一入口）            │
│   features/ chat（对话、输入区）· shell（标题栏、侧栏、设置）·       │
│            files（文件树与标签页）· console（原始日志、分支树、命令）│
└────────────────────────────────────────────────────────────────────┘
                    ▲ pi:event / pi:trace（Wails 事件）
                    ▼ PiService 方法（绑定调用）
┌─ Pi 子进程（node cli.js --mode rpc）───────────────────────────────┐
│  会话文件 ~/.pi/agent/sessions/<cwd-slug>/<ISO>_<uuid>.jsonl        │
│  或便携模式 <exe目录>/.pi/…（存在则优先）                          │
└────────────────────────────────────────────────────────────────────┘
```

**"用哪个 Pi"的解析顺序**（`internal/pi/locate.go`）：
显式路径 → `PI_CLI_PATH` → **应用同目录 `pi-agent/`** → PATH 里的 `pi`。
默认自带 1.0.2，用户不需要全局安装。

**"配置在哪"的解析顺序**（`sessions.go` 的 `agentDir`）：
`PI_CODING_AGENT_DIR` → `<exe目录>/.pi`（便携）→ `~/.pi/agent`。

---

## 二、RPC 命令 ↔ 界面入口

Pi 的 RPC 共 37 条；**已接入 28 条**。

| 命令 | 界面入口 |
|---|---|
| `prompt` / `promptWithFiles` | 输入区发送（附件走绝对路径） |
| `steer` | 运行中回车 |
| `follow_up` | 运行中「排到后面」 |
| `abort` | 运行中「停止」 |
| `clear_queue` | 「队列 N · 清空」/ 设置 → 会话 |
| `get_state` | 状态显示、用量、会话文件 |
| `get_messages` | 载入/重载时间线 |
| `get_session_stats` | 用量统计（上下文 % / token / 费用） |
| `get_available_models` | 模型下拉（输入区 + 设置） |
| `set_model` | 输入区模型菜单 / 设置 |
| `get_available_thinking_levels` | 推理等级菜单 |
| `set_thinking_level` | 推理等级菜单 / 设置 |
| `new_session` | 工作区行的 `+` |
| `switch_session` | 左栏会话点击 |
| `set_session_name` | 会话重命名 |
| `export_html` | 会话标题栏「导出 HTML」 |
| `get_commands` | 输入区斜杠命令面板 |
| `fork` | 用户消息旁「分支」/ 分支树里的「从这里分支」 |
| `get_fork_messages` | 上面那个按钮的 entryId 来源 |
| `clone` | Go 层已接（`CloneSession`），界面入口待加 |
| `compact` | 更多菜单「压缩上下文」/ 设置 → 会话「立即压缩」 |
| `set_auto_compaction` | 设置 → 会话「快满时自动压缩」 |
| `set_auto_retry` | 设置 → 会话「失败时自动重试」 |
| `abort_retry` | 输入区「停止重试」（仅在重试态出现） |
| `set_steering_mode` | 设置 → 会话「运行中插入的投递」 |
| `set_follow_up_mode` | 设置 → 会话「排到后面的投递」 |
| `get_tree` | 更多菜单「分支树」 |
| `bash` / `abort_bash` | 更多菜单「运行命令」 |

**未接入**：`get_entries`（游标增量拉条目）、`get_last_assistant_text`、
`cycle_model` / `cycle_thinking_level`（快速切换）、`get_fork_messages` 之外的导航 API。

**故意不做的**：OAuth 登录（`pi auth` 没有 login 子命令；流程在 TUI 内部，依赖
`interaction.prompt()` 与未公开的 bundle 结构）。界面给的是可照做的指引：
终端运行 `pi` → `/login` → 回来重启会话。

---

## 三、验证方法

### 免费回归（不需要真机、不花钱）

```
go test ./...                      # Go 侧：协议、会话、配置、附件、分支、压缩…
npm --prefix frontend run typecheck
wails3 build
```

mock 的能力开关（写在 `docs/audit.md` 也有）：

| 环境变量 | 作用 |
|---|---|
| `PI_GUI_MODE=mock` | 用假 Pi 起界面，全部交互可跑 |
| `PI_MOCK_TEXT_DELAY_MS=3` | 流式延迟（测自动滚动、逐字渲染） |
| `PI_MOCK_RETRY_ONCE=1` | 演一次「429 → 自动重试」，测重试态与「停止重试」 |
| `PI_GUI_DEBUG=1` | 会话标题栏显示滚动指标 `top/h/c/gap/stick` |

### 实机验证的三条硬规则（踩过坑总结）

1. **改代码一律用编辑工具**。脚本字符串替换会静默失败（本项目已中招 6 次），
   改完必须 grep 关键片段确认存在。
2. **UIA 的 `InvokePattern` 对 Radix 组件不生效**（菜单项、自定义按钮）——不报错，
   只是什么都不发生。用 `SetCursorPos` + `mouse_event` 真实点击。
3. **WebView2 不接收合成键盘输入**（`SendKeys` / `keybd_event` 都无效），
   且 UIA 的 `ValuePattern.SetValue` 对 `<textarea>` 有效、对 `<input>` 不触发 React 的
   `onChange`。所以"填表 + 点按钮"这类路径无法自动化，只能手点。

### 提交前

```
gofmt -l . && go vet ./... && go test ./... && npm --prefix frontend run typecheck
```

新增/改动 `PiService` 方法后**必须**重新生成绑定，否则 `TestGeneratedBindingsMatchService` 会红：

```
wails3 generate bindings -ts -i
```

---

## 四、目录

```
main.go / service.go / sessions.go / providers.go / files.go
attachments.go / builtin_providers.go / *_test.go
internal/pi/        协议层（client.go / command.go / locate.go）
internal/mockpi/    假 Pi（唯一的 Serve 入口）
internal/agent/     早期命令行实验
cmd/rpc-demo/       命令行入口（-check / -where 等自检开关）
frontend/src/       React 界面（stores / rpc / features / components）
scripts/            build.ps1 · pack-release.ps1 · fetch-pi-agent.ps1 · portable-config.ps1
docs/               本文件与 audit.md
design/             界面预览与验收截图
```

## 五、打包分发

```powershell
powershell -File scripts/pack-release.ps1              # 构建 + 打包
powershell -File scripts/pack-release.ps1 -SkipBuild   # 用现有产物
```

产物：`dist/pi-gui-c-win-x64-<sha>.zip`（约 38MB，含自带 Pi 运行时 118MB 解压后）。
**打包脚本会拒绝把 `auth.json` / `models.json` 打进包**（那是使用者的密钥）。
使用者需要 Node.js 18+ 在 PATH（自带 Pi 是 Node 脚本）。
