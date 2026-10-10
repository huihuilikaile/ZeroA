/**
 * 前端唯一的「调用后端」出口。
 *
 * 只有这个文件知道 Wails 生成的绑定长什么样（路径由 Go module 名决定）。
 * 组件一律通过 piBridge.xxx(...) 调用，于是：
 *   · 后端方法改名时只改这里；
 *   · 「前端能调用哪些能力」一眼可见；
 *   · 生成的绑定是 CancellablePromise，这里统一收敛成 Promise。
 */
import * as Binding from '../../bindings/zeroa/internal/bridge/piservice';
import type { Result } from '@/rpc/types';

export const piBridge = {
  /* 生命周期：启动参数就是设置里的选择（目录、模型、思考级别、工具白名单、角色） */
  start: (mode: string, cliPath: string, cwd: string, model: string, thinking: string, tools: string, roleId: string) =>
    Binding.Start(mode, cliPath, cwd, model, thinking, tools, roleId) as Promise<void>,
  stop: () => Binding.Stop() as Promise<void>,

  /* 角色：角色不是运行时可变的开关，而是"重启 Pi 时带哪一套参数"的声明 */
  /* 角色导入导出（role_io.go）：导出成自包含 JSON，导入支持文件或目录 */
  exportRoles: (payload: string) => Binding.ExportRoles(payload) as Promise<Result>,
  importRoles: (source: string) => Binding.ImportRoles(source) as Promise<Result>,
  listRoles: () => Binding.ListRoles() as Promise<Result>,
  getRole: (id: string) => Binding.GetRole(id) as Promise<Result>,
  /** 保存角色（元数据 + 提示词正文），payload 是 JSON 字符串 */
  saveRole: (payload: string) => Binding.SaveRole(payload) as Promise<Result>,
  deleteRole: (id: string) => Binding.DeleteRole(id) as Promise<Result>,
  /** 记住"当前工作区用哪个角色"。它不启动进程：切换由前端走重启流程 */
  setActiveRole: (id: string) => Binding.SetActiveRole(id) as Promise<Result>,
  /** 把内置角色恢复成随程序分发的默认内容 */
  resetRole: (id: string) => Binding.ResetRole(id) as Promise<Result>,
  /** 记录会话属于哪个角色（侧栏徽标用） */
  rememberSessionRole: (path: string, roleId: string) =>
    Binding.RememberSessionRole(path, roleId) as Promise<Result>,
  /** 给当前会话改名（角色前缀用它；不用像 RenameSession 那样来回切会话） */
  nameCurrentSession: (name: string) => Binding.NameCurrentSession(name) as Promise<Result>,

  /* 能力：技能 / MCP / 工具目录（环境级的东西，角色只是引用它们） */
  listSkills: () => Binding.ListSkills() as Promise<Result>,
  importSkills: (dir: string) => Binding.ImportSkills(dir) as Promise<Result>,
  openSkill: (id: string) => Binding.OpenSkill(id) as Promise<Result>,
  listMcpServers: () => Binding.ListMcpServers() as Promise<Result>,
  saveMcpServer: (payload: string) => Binding.SaveMcpServer(payload) as Promise<Result>,
  deleteMcpServer: (name: string) => Binding.DeleteMcpServer(name) as Promise<Result>,
  listToolCatalog: () => Binding.ListToolCatalog() as Promise<Result>,

  /* 工具注册表（P7）：检测这台机器上有什么（不下载、不安装） */
  /* 工具清单（tool-catalog.json）：导出当前清单 / 改完热加载，都不用重启 */
  exportToolCatalog: (path: string) => Binding.ExportToolCatalog(path) as Promise<Result>,
  reloadToolCatalog: () => Binding.ReloadToolCatalog() as Promise<Result>,
  /* MCP：工具清单来自 mcp-cache.json；重新发现 = 清掉那台服务器的缓存 */
  listMcpTools: (name: string) => Binding.ListMcpTools(name) as Promise<Result>,
  rediscoverMcp: (name: string) => Binding.RediscoverMcp(name) as Promise<Result>,
  listTools: () => Binding.ListTools() as Promise<Result>,
  detectTools: (force: boolean) => Binding.DetectTools(force) as Promise<Result>,

  /* 子智能体（方案 docs/05）：派活是模型发起的，但停止必须人手能按 */
  listAgents: () => Binding.ListAgents() as Promise<Result>,
  killAgent: (agentId: string) => Binding.KillAgent(agentId) as Promise<Result>,
  /**
   * 给一个（可继续的）子智能体发消息 —— 仿 DSH 的 send_message。
   *
   * `delivery` 在这里是字符串而不是 store 里那个联合类型：这一层是"后端能力的出口"，
   * 不该反过来依赖业务 store（不然 store 与 bridge 会绕成一个环）。
   *   - "queue"（默认）：排队等它当前这一轮跑完再送；
   *   - "steer"：插进它当前这一轮的最近检查点。
   * 返回的只是**受理凭证**（受理 ≠ 完成）：回复要等它自己那一轮跑完，走它的事件流回来。
   */
  sendToAgent: (agentId: string, message: string, delivery: string) =>
    Binding.SendToAgent(agentId, message, delivery) as Promise<Result>,
  /** 只中断它**当前这一轮**：进程与会话都留着，之后还能继续发消息（销毁它用 killAgent） */
  interruptAgent: (agentId: string) => Binding.InterruptAgent(agentId) as Promise<Result>,
  /** 预览角色的编译结果（Effective Configuration） */
  resolveRole: (id: string) => Binding.ResolveRole(id) as Promise<Result>,

  /* 权限引擎（P10）：四层策略 + 临时授权 */
  getPermissions: () => Binding.GetPermissions() as Promise<Result>,
  setPermissionPolicy: (layer: string, payload: string) => Binding.SetPermissionPolicy(layer, payload) as Promise<Result>,
  authorizePermission: (action: string, minutes: number, reason: string) =>
    Binding.AuthorizePermission(action, minutes, reason) as Promise<Result>,
  revokePermission: (action: string) => Binding.RevokePermission(action) as Promise<Result>,

  /* 任务循环（P11）：轮次 / 时长 / 卡住检测 */
  getLoopState: () => Binding.GetLoopState() as Promise<Result>,
  stopLoop: () => Binding.StopLoop() as Promise<Result>,

  /* 状态 */
  getState: () => Binding.GetState() as Promise<Result>,
  getMessages: () => Binding.GetMessages() as Promise<Result>,
  /**
   * 直接读一条会话文件的消息（**不需要 Pi 进程**）。
   *
   * 会话历史就在 .jsonl 里，而 `get_messages` 不过是把这些消息从内存里取出来——
   * 两者内容一致。所以"打开一条历史会话"不该为了一份磁盘上已有的数组去等
   * 1–3.5 秒的冷启动（那正是进入应用慢的全部原因）。
   *
   * 返回的形状与 getMessages **完全一致**（`{messages:[...]}`），
   * 所以调用方直接交给 routeMessages 即可，前端不需要第二套解析。
   */
  loadSessionHistory: (sessionPath: string) => Binding.LoadSessionHistory(sessionPath) as Promise<Result>,
  getSessionStats: () => Binding.GetSessionStats() as Promise<Result>,
  /** 默认值由后端算：工作目录 = exe 同目录、会话根目录、exe 路径、运行模式 */
  getDefaults: () => Binding.GetDefaults() as Promise<Result>,
  /** 记录"当前工作区"（重启后回到这里），并把它并入侧栏工作区列表 */
  setActiveWorkspace: (cwd: string) => Binding.SetActiveWorkspace(cwd) as Promise<Result>,
  /** 删除一个工作区：移除登记 + 删掉它名下的会话文件（磁盘目录不动） */
  deleteWorkspace: (cwd: string) => Binding.DeleteWorkspace(cwd) as Promise<Result>,
  /** 上报"当前是否有回合在跑"：Go 关窗前要问它 */
  setRunningWork: (running: boolean) => Binding.SetRunningWork(running) as Promise<void>,
  /** 强制关闭：先收掉 Pi 进程，再退出应用 */
  forceClose: () => Binding.ForceClose() as Promise<void>,
  /** 切换工作区前先确认目录还在（可能被删/改名） */
  statFolder: (path: string) => Binding.StatFolder(path) as Promise<Result>,
  /** 新建文件夹并返回路径（工作区 = Pi 的工作目录） */
  createFolder: (parentDir: string, name: string) => Binding.CreateFolder(parentDir, name) as Promise<Result>,

  /* 会话：列表是读会话目录（Pi 没有列会话的 RPC），新建/切换/改名走 RPC */
  listSessions: (cwd: string) => Binding.ListSessions(cwd) as Promise<Result>,
  /** 一次取回多个工作区的会话（侧栏同时展开多个分组时用，后端只扫一遍会话目录） */
  listSessionsBatch: (cwds: string[]) => Binding.ListSessionsBatch(cwds) as Promise<Result>,
  /**
   * 新建一条会话：**立刻返回，不等进程**（会话=日志，进程懒创建）。
   *
   * 为什么不是 Pi 的 `new_session` 命令：那个命令是让当前进程换一份新的会话文件，
   * 于是原来那条会话正在跑的一轮就没了。一进程一会话之后，新会话 = 换一个进程说话，
   * 原来的会话继续在它自己的进程里跑（切回去还能看到它在动）。
   *
   * 而进程**现在不起了**：冷启动实测 1–3.5 秒，而"新建"之后用户往往先想一句话怎么写。
   * data 里因此是待创建的形状：
   *   `{state: null, messages: null, cwd, sessionPath: "", runtimeId: "", pending: true}`
   * 前端据此把当前会话切成"未命名新会话"（清空时间线、侧栏留占位行），
   * 进程由发送第一条消息时的 prompt 按需创建（见下面 prompt 的说明）。
   */
  newSession: () => Binding.NewSession() as Promise<Result>,
  /**
   * 打开一条已有会话。
   *
   * 返回的 data 里是 `{state, messages, cwd, sessionPath}`：
   *   · 这条会话**已经开着**（热切换）→ `messages` 为 null，前端不要动时间线
   *     （它手上那份就是这条会话的最新内容）；
   *   · 首次打开（冷启动）→ `messages` 有值，用它重建时间线。
   * 两种情况的判断只看 messages 有没有值，别按别的方式猜。
   *
   * 曾经有过 loadSession / loadSessionForce（排队 + 立即切换）与 pendingSession：
   * 一进程一会话之后它们全部退役——切换不再需要碰正在干活的进程，
   * 也就没有"等待"与"打断"这两件事了。
   */
  switchSession: (sessionPath: string) => Binding.SwitchSession(sessionPath) as Promise<Result>,
  /**
   * 切工作区：**不打断**其它工作区里正在跑的会话。
   *
   * 返回形状与 switchSession 相同（换成了新工作区里的一条会话）。
   */
  switchWorkspace: (cwd: string) => Binding.SwitchWorkspace(cwd) as Promise<Result>,
  /** 后端持久化的按工作区会话记忆（不依赖 WebView localStorage） */
  rememberSession: (cwd: string, sessionPath: string) => Binding.RememberSession(cwd, sessionPath) as Promise<Result>,
  getRememberedSession: (cwd: string) => Binding.GetRememberedSession(cwd) as Promise<Result>,
  /**
   * 给某条会话改名。
   *
   * 一进程一会话之后是"在目标会话自己的进程里改"（它开着就直接发命令，
   * 没开着就起一个临时进程改完关掉），所以不再需要像旧实现那样把当前会话
   * 切过去再切回来——"改名会不会打断我手上的活"这个问题从此不存在。
   */
  renameSession: (sessionPath: string, name: string) => Binding.RenameSession(sessionPath, name) as Promise<Result>,
  /**
   * 删除一条会话。
   *
   * 删掉的正好是当前会话时，Go 会先把那个进程收掉、再开一条新会话顶上，
   * 并把那条 new_session 的结果放在 data 的 `activation` 字段里
   * （前端据此切换视图，见 stores/sessions.ts 的 remove）。
   */
  deleteSession: (sessionPath: string) => Binding.DeleteSession(sessionPath) as Promise<Result>,

  /* 对话 */
  /**
   * 发送一条用户消息。
   *
   * 它是**懒启动的触发点**：当前会话还没有进程时（刚新建、刚 Stop 过），
   * Go 侧会先按启动参数模板建一个（首次约 1–3.5 秒）再发出去。
   * 返回值里带着这个新进程的 `runtimeId`（前台据此认领它，
   * 见 rpc/router.ts 的 adoptPendingRuntime），所以前端不需要"发送前先启动 Pi"。
   */
  prompt: (message: string) => Binding.Prompt(message) as Promise<Result>,
  /** 可以从哪几条用户消息分支（带 entryId）：界面用它给消息配「分支」按钮 */
  getForkMessages: () => Binding.GetForkMessages() as Promise<Result>,
  /** 从指定用户消息开一条新分支 */
  fork: (entryId: string) => Binding.Fork(entryId) as Promise<Result>,
  /** 把粘贴进来的图片（data URL）落成临时文件，返回绝对路径 */
  savePastedImage: (dataUrl: string) => Binding.SavePastedImage(dataUrl) as Promise<Result>,
  /**
   * 把粘贴/拖进来的文件内容落盘，返回绝对路径。
   *
   * web 层拿不到剪贴板/拖拽文件的真实路径（浏览器安全模型），只能拿内容；
   * 落盘之后就和"用加号选的文件"走同一条附件路径。
   */
  savePastedFile: (name: string, dataUrl: string) => Binding.SavePastedFile(name, dataUrl) as Promise<Result>,
  /** 启动 Pi 时是否加载扩展/技能/提示模板/主题（关掉则斜杠命令为空） */
  setLoadResources: (enabled: boolean) => Binding.SetLoadResources(enabled) as Promise<Result>,
  /** 最后一条助手回答的文本（「复制上一条回答」用） */
  getLastAssistantText: () => Binding.GetLastAssistantText() as Promise<Result>,
  /** 直接执行一条 shell 命令（输出会进会话上下文） */
  bash: (command: string) => Binding.Bash(command) as Promise<Result>,
  /** 中止正在跑的 bash 命令 */
  abortBash: () => Binding.AbortBash() as Promise<Result>,
  /** 运行中插入的消息（steer）怎么投递：all | one-at-a-time */
  setSteeringMode: (mode: string) => Binding.SetSteeringMode(mode) as Promise<Result>,
  /** 排到后面的消息（follow_up）怎么投递：all | one-at-a-time */
  setFollowUpMode: (mode: string) => Binding.SetFollowUpMode(mode) as Promise<Result>,
  /** 失败时是否自动重试（过载 / 限流 / 5xx） */
  setAutoRetry: (enabled: boolean) => Binding.SetAutoRetry(enabled) as Promise<Result>,
  /** 中止正在进行的重试 */
  abortRetry: () => Binding.AbortRetry() as Promise<Result>,
  /** 会话的树结构（分支后能看见另一条分支还在） */
  getTree: () => Binding.GetTree() as Promise<Result>,
  /** 把当前分支复制成一个新会话 */
  cloneSession: () => Binding.CloneSession() as Promise<Result>,
  /** 手动压缩上下文（long会话 token 快满时的主动操作），customInstructions 可留空 */
  compact: (customInstructions: string) => Binding.Compact(customInstructions) as Promise<Result>,
  /** 上下文快满时是否自动压缩 */
  setAutoCompaction: (enabled: boolean) => Binding.SetAutoCompaction(enabled) as Promise<Result>,
  /** 带图片附件的 prompt（Pi 的 images 字段，见 attachments.go） */
  promptWithFiles: (message: string, files: string[]) => Binding.PromptWithFiles(message, files) as Promise<Result>,
  steer: (message: string) => Binding.Steer(message) as Promise<Result>,
  followUp: (message: string) => Binding.FollowUp(message) as Promise<Result>,
  abort: () => Binding.Abort() as Promise<Result>,
  clearQueue: () => Binding.ClearQueue() as Promise<Result>,

  /* 模型与思考：列表一律走 Pi RPC（get_available_models），不读配置文件 */
  setModel: (provider: string, model: string) => Binding.SetModel(provider, model) as Promise<Result>,
  getAvailableModels: () => Binding.GetAvailableModels() as Promise<Result>,
  setThinkingLevel: (level: string) => Binding.SetThinkingLevel(level) as Promise<Result>,
  getAvailableThinkingLevels: () => Binding.GetAvailableThinkingLevels() as Promise<Result>,

  /** 模型配置（读/写 Pi 的 models.json；Pi 没有 add_model 这类 RPC） */
  listConfiguredProviders: () => Binding.ListConfiguredProviders() as Promise<Result>,
  /** 设置页「模型」用：内置目录 + 已配置 + 凭据三处合并后的提供商列表 */
  listProviders: () => Binding.ListProviders() as Promise<Result>,
  /** 改 provider 级别的 api / baseUrl / apiKey（留空表示不改） */
  updateProvider: (provider: string, api: string, baseUrl: string, apiKey: string) =>
    Binding.UpdateProvider(provider, api, baseUrl, apiKey) as Promise<Result>,
  /** 写入提供商的 API 密钥（Pi 的登录：写 auth.json 的 key 字段） */
  setProviderKey: (provider: string, key: string) => Binding.SetProviderKey(provider, key) as Promise<Result>,
  /** 可用命令（扩展命令 / 提示模板 / 技能）：输入区的斜杠面板用 */
  getCommands: () => Binding.GetCommands() as Promise<Result>,
  /** 工作区文件树：一次列一层（照 DSH 的 FilesBody） */
  listDir: (dir: string) => Binding.ListDir(dir) as Promise<Result>,
  /** 文本预览分页：从 offset 读一页（≤256KB），返回 nextOffset/truncated/size */
  readFileTextAt: (path: string, offset: number, maxBytes: number) =>
    Binding.ReadFileTextAt(path, offset, maxBytes) as Promise<Result>,
  /** 工作区内按文件名/相对路径模糊搜索 */
  searchFiles: (query: string) => Binding.SearchFiles(query) as Promise<Result>,
  /** 在系统文件管理器里选中这个文件/目录（限工作区内） */
  revealPath: (path: string) => Binding.RevealPath(path) as Promise<Result>,
  /** 图片预览：读字节转成 data URL，供侧栏内联显示（限工作区内） */
  readFileDataUrl: (path: string) => Binding.ReadFileDataURL(path) as Promise<Result>,
  /** 附件缩略图：不限工作区（粘贴图在配置目录、拖入文件在任意路径），仍只认图片 + 8MB 上限 */
  readAttachmentImage: (path: string) => Binding.ReadAttachmentImage(path) as Promise<Result>,
  /** 用系统默认方式打开文件或文件夹 */
  openPath: (path: string) => Binding.OpenPath(path) as Promise<Result>,
  /** 把当前会话导出成 HTML（Pi 的 export_html） */
  exportHTML: (outputPath: string) => Binding.ExportHTML(outputPath) as Promise<Result>,
  addModel: (
    provider: string,
    modelId: string,
    name: string,
    api: string,
    baseUrl: string,
    apiKey: string,
    contextWindow: number,
    supportsImages: boolean,
  ) => Binding.AddModel(provider, modelId, name, api, baseUrl, apiKey, contextWindow, supportsImages) as Promise<Result>,
  deleteModel: (provider: string, modelId: string) => Binding.DeleteModel(provider, modelId) as Promise<Result>,

  /* 扩展 UI：response 是完整的 extension_ui_response JSON 文本 */
  respondUI: (response: string) => Binding.RespondUI(response) as Promise<Result>,
  /** 应答子智能体的扩展 UI 请求（应答发回它自己的进程） */
  respondAgentUI: (agentId: string, response: string) => Binding.RespondAgentUI(agentId, response) as Promise<Result>,
};

/** 把后端错误转成适合直接显示的一句话 */
export function errorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
