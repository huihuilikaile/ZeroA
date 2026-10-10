# 项目审计记录（2026-10-05）

> **重要更正（Round 7）**：早期我说"消息分支/fork 需要 Pi 支持、当前缺失"——**那是读漏了**。
> `docs/rpc-commands.md` 全表共 **37 条**命令，其中包含
> `fork` / `clone` / `get_tree` / `get_entries` / `get_fork_messages` / `get_last_assistant_text`
> 与 `bash` / `abort_bash` / `compact` / `set_auto_compaction` / `set_auto_retry` /
> `set_steering_mode` / `set_follow_up_mode` / `cycle_model` / `cycle_thinking_level` / `abort_retry`。
> 教训：不要凭印象断言"上游不支持"，把命令表拉全再下结论。

目标：通读项目代码、排查 bug、对照 DSH / Pi TUI 源码优化功能，并测试。

## 一、当前绿灯状态（本轮实测）

```
gofmt -l          → 无输出（全部已格式化）
go vet ./...      → VET_OK
go test ./...     → ok zeroa / internal/agent / internal/pi
npm run typecheck → 通过
wails3 build      → 通过
```

测试覆盖（Go 侧）：
`command_test` `service_test` `sessions_test` `session_admin_test` `folder_test`
`audit_fixes_test`（含 PI_CODING_AGENT_DIR 覆盖）`model_config_test`（模型配置往返）
`provider_key_test`（凭据写入形状）`builtin_providers_test`（内置供应商提取）
`attachments_test`（附件一律绝对路径）`bindings_test`（绑定与 Service 方法一一对应）

## 二、本轮静态排查结果（无问题）

| 检查项 | 结果 |
|---|---|
| 重复/冲突的 import | 无（含之前用脚本粗改过的 AppShell） |
| `addEventListener` 与 `removeEventListener` 配对 | 全部配对，无泄漏 |
| `setInterval` 与 `clearInterval` 配对 | 全部配对 |
| 明显的死导出 | 未发现（`attachmentIcon` 等仍在使用） |

## 三、本会话已修复的真实 bug（回归样本）

1. **`ref={scrollRef}` 从未挂到滚动容器** → 自动滚动是空操作。字符串替换静默失败的典型后果。
2. **`stick` 自锁失效** → `onScroll` 里用"距底 < 80px"判定跟随，流式下会被自己的程序化滚动带偏，从此不再跟随。
3. **导出 HTML 空会话**：Pi 回英文 `Nothing to export yet`，且协议错误被重复报两次（`reportError` 已加 2 秒同文去重；预检改为只认 user/assistant 条目）。
4. **凭据类型写成 `apiKey`** → Pi 只认 `api_key`（见 `dist/core/auth-storage.js`），写错等于凭据无效。
5. **左栏高度塌陷**：`Sidebar` 依赖 grid 拉伸却不写 `h-full`，包进 flex 后底部工具条跑到顶部。
6. **拖拽无下限**：分隔条 `max` 写死，把文件面板拉宽能把对话区挤成 0（现由窗口宽度算出上限）。
7. **加号弹出的是文件夹选择器**：`CanChooseFiles` 与 `CanChooseDirectories` 同时为 true 时 Windows 会退化成文件夹选择。

## 四、未证实 / 待办（下一轮从这里开始）

| # | 项目 | 现状 | 下一步 |
|---|---|---|---|
| 1 | ~~流式中途向上翻阅是否被尊重~~ | ✅ **本轮已证实（有数字）**：加了 `PI_GUI_DEBUG=1` 探针后实测——流式期间 `gap=1 stick=1`（稳定贴底），上滚 4 格后 `gap=290 stick=0`（停在用户翻到的位置）。上一轮的"未证实"是测量口径错误（UIA 对滚出视口的元素给布局坐标，不随滚动变化） | 完成。探针保留为长期调试手段（`PI_GUI_DEBUG=1` 时在会话标题栏显示 `top/h/c/gap/stick`） || 2 | `SetProviderKey` 真实写入 | ⚠️ **界面路径在本环境无法自动化**：三次尝试（`SendKeys`、`keybd_event`、UIA `ValuePattern.SetValue` + `SelectionItemPattern.Select`）都失败——WebView2 **不接收合成键盘输入**，而 UIA 直接改 DOM 又不触发 React 的 `onChange`，于是表单在 React 侧始终是空的。**这是自动化环境的限制，不是应用 bug**（真人敲键盘没有问题）。Go 层已有单测覆盖形状（api_key / 保留 oauth / 0600 / 不泄漏 key） | 二选一：①**你手动点一次**（设置→模型→添加提供商→选 demo→填 key→保存），10 秒即可，我再看 `auth.json`；②我加一个仅测试用的 RPC 来驱动（要改应用代码，不太愿意） || 3 | ~~二进制文件"用系统程序打开"分支~~ | ✅ **本轮已实测**：文件树里点 `app-autoscroll.png` → 无错误行、且按设计**不开预览标签**（走 `OpenPath` 交给系统）。断言"没开标签 + 没报错"能证明分支走对了 | 完成 || 4 | ~~导出 HTML 的成功路径~~ | ✅ **本轮完成**。① 给 mock 补了 `export_html`（真写一份最小 HTML），这条路径从此能在 mock 下免费回归；② 新增 `export_test.go` 断言三件事（回传 path 与请求一致 / 文件真的落盘且是 HTML / 不传路径时服务层自己挑且文件存在）；③ 实机：mock 模式发一条消息 → 点导出 → 提示 `已导出：…\bin\.pi\sessions\01a1058a….html`，文件确为 235B 的 HTML。另在会话目录发现 10-04 生成的 283KB / 322KB `<!DOCTYPE html>` 文件，是**真实 Pi 导出的完整会话**，佐证真实路径同样可用 || 5 | ~~内置供应商提取的降级提示~~ | ✅ **本轮已完成**：`ListProviders` 返回 `builtinCount`，为 0 时设置页显示一行提示（说明原因与出路）。实测正常路径不显示提示、内置行存在；降级路径由 `builtin_count_test.go` 断言"JSON 里必须真的有这个键"（字段缺失时 Unmarshal 也给 0，弱断言等于没测） | 完成 || 6 | ~~OAuth 登录（8 个供应商）~~ | ✅ **本轮调查完毕，结论：不在应用内做**。源码事实：`pi auth` 只有 `print-api-key` / `print-bearer-token` / `check`（**没有 login**）；OAuth 流程在 TUI 内部（`modes/interactive/components/oauth-selector.js` + `interactive-mode.js` L4773-4992），依赖终端的 `interaction.prompt()`；包里**没有 SDK 导出**（无 `dist/sdk.js`），RPC 里也没有登录命令。要在应用内做就得自己构造 interaction 并直接调 bundle 内部 chunk——依赖未公开的内部结构，Pi 一升级就断。**改为可照做的提示**（已实测显示）：添加卡片里选中 OAuth 供应商时提示"终端运行 pi → `/login` 授权 → 回来重启会话" || 7 | `--no-extensions` 仍未放开 | 破坏性改动，等你确认 | 放开会加载用户全部扩展（启动变慢、可能弹确认框） |

## 五、与 DSH / Pi TUI 的对照结论（已落地部分）

| 来源 | 学到的做法 | 落地位置 |
|---|---|---|
| DSH `ui-settings-general/SettingsRoot.tsx` | 设置面板 **760×500 固定尺寸**（figma 2552:26025） | `SettingsDialog` 800×460 |
| DSH `ui-settings-models/ModelsSection.tsx` | 提供商行 + 凭据状态点 + 编辑/删除 + 虚线"添加" | `ModelsSection.tsx` |
| DSH `ui-dockkit/TabPanel.tsx` | chip 的关闭在 **active/hover/focus** 可见；withheld close 的 chip 要"安静"；相邻 chip 发丝线；激活项自动滚入视野 | `FilePanel.tsx` |
| DSH `ui-sidebar-files/FilesBody.tsx` | 文件树**一次只列一层**、目录展开、文件交给查看器 | `FilePanel.tsx` + `files.go` |
| DSH `ui-sidebar-documentpreview/README.md` + `ui-sidebar-files/README.md` + `dsh-util-code-language` | 文件预览按**扩展名注册渲染器**：图片（PNG/JPEG/GIF/WebP/BMP/ICO/SVG，内联 `<img>`、不超过原始尺寸、12px 内边距）/ 代码（语言标签 + 复制 + 默认行号且行号不进复制内容 + 换行开关）/ 纯文本；**已知二进制后缀没有渲染器时直接显示"无法预览"，连读取请求都不发**；读不动（not-text / too-large）落到同一个"无法预览"空态，出路是"用系统程序打开"；扩展名→语言映射见 `dsh-util-code-language` 的 `LANGUAGES` 表 | `features/files/preview.ts`（照搬语言表 + 图片/二进制后缀集合 + `pickPreviewKind`）、`CodeView.tsx`（highlight.js + 行号 + 复制 + 换行）、`FilePanel.tsx`（三种渲染器 + 无法预览空态）、`files.go` 的 `ReadFileDataURL` |
| DSH `ui-workspace/README.md`（`sidebar.workspaces` 分组浏览器） | **"Each Workspace keeps its own Sessions and row actions"**：分组各自成节、各自列会话与行操作；分组默认展开、折叠状态本地保存；搜索时展开所有分组（"expands every ancestor"）；每节有自己的空态 | `Sidebar.tsx` 改成每个展开的工作区各渲染自己的分组；`stores/sessions.ts` 用 `byWorkspace` 按工作区存列表；折叠状态写 `pi-gui-collapsed`；搜索时强制展开所有分组 |
| DSH `ui-conversation/src/client/input/InputBar.tsx` + `InputBar.module.css` | 输入卡片分两段：上面是附件栏 + 编辑器，下面是 `row` 控制行；`row` 用 `justify-content:space-between`，左侧 `tools` 的**第一项就是加号**（28×28、`border-radius:999px`、`display:grid;place-items:center`、hover 才浮出底色），右侧 `trailing` 用 `margin-left:auto` 放模型与发送；隐藏的原生 `input[type=file]` 挂在同一组里 | `Composer.tsx`（加号从输入框左侧移到底部控制行第一项，输入区独占整行） |
| DSH `ui-primitives/Toast.module.css` + `Toast.tsx` | 胶囊提示：`createPortal` 到 body；`position:fixed; top:40px; left:50%; translateX(-50%)` 顶部居中；`pointer-events:none` 不吃点击；`padding:12px 16px`、`border-radius:lg`、`width:max-content` + `max-width:min(640px,100vw-48px)`；进场上滑 6px/160ms，停留 `HOLD_MS`(3000) 后 `FADE_MS`(1000) 淡出，卸载时机 = hold+fade；`role="alert"`；表面**两套主题都保持深色** | `components/ui/Toast.tsx` + `tokens.css` 的 `.toast`；`stores/ui.ts` 的 `showNotice(text, tone)` 与计时；从标题栏移到窗口顶部居中 |
| Pi `dist/core/auth-storage.js` | 凭据类型 `api_key` / `oauth`，字段 `key`，落盘 0600 | `providers.go` |
| Pi bundle `createProvider({...})` | 内置供应商注册表（41 个）是 `/login` 的来源 | `builtin_providers.go` |
| Pi `docs/configuration.md` | agent 目录、项目 `.pi`、`sessionDir` 的优先级 | `sessions.go` / `internal/pi/locate.go` |

## 六、验证方法注意（踩过三次的坑）

用 UIA 驱动界面时，**`InvokePattern` 对 Radix 组件不生效**（菜单项、下拉项、部分自定义按钮）：
它不会返回错误，只是**什么都不发生**，看起来像"功能没实现"。

已经因此误判三次：
1. 文件面板开关按钮（`工作区文件`）
2. 右栏分隔条（`调整文件面板宽度`）
3. 更多菜单里的「压缩上下文」（本轮：`InvokePattern` 点了没反应，换真实鼠标点击立刻出现提示）

**规则**：UIA 定位元素 + `SetCursorPos` + `mouse_event(0x0002/0x0004)` 真实点击；
`InvokePattern` 只在确认可用时才用。判断"点了没反应"之前，先换真实点击复测一次，
再决定是功能问题还是点击方式问题。

另外两条同类经验：
- 改代码**一律用编辑工具**，脚本字符串替换会静默失败（本项目已中招 5 次）
- 提交前跑**完整**测试，不要只看 `go build`（曾因漏生成绑定导致提交时测试是红的）