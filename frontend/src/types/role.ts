/**
 * 角色（Role Profile）在前端的形状，与 Go 的 roles.go 一一对应。
 *
 * 为什么要把它当成"配置"而不是"开关"：Pi 的 RPC 没有 set_system_prompt /
 * set_tools / set_skills，角色的提示词与工具集只能进启动参数。所以切换角色
 * 必然是"重启 Pi + 新开会话"，界面上的动作与文案都要照这个事实来写。
 */
export interface Role {
  id: string;
  name: string;
  description?: string;
  icon?: string;
  /**
   * 角色类型：缺省 / 'role' = 普通角色（输入框可选、能当主会话角色）；
   * 'agent' = 子智能体——只能被其他角色委派（delegation.allowedRoles），
   * 不出现在输入框的角色选择器里，也不能设为当前角色（Go 端会拦）。
   */
  kind?: 'role' | 'agent';
  /** 随程序分发的角色：可编辑，不可删除 */
  builtin?: boolean;
  promptFile?: string;
  /** true = 用 --system-prompt 整体替换基础提示；默认追加 */
  replaceSystemPrompt?: boolean;
  model?: string;
  thinkingLevel?: string;
  /** 作用域模型：cycle_model 只在这几个里循环 */
  scopedModels?: string[];
  /**
   * 工具 / 技能 / 扩展 / 提示模板的白名单。
   *
   * **三种取值语义不同**：`null`/`undefined` = 不限制（用 Pi 的默认全集），
   * `[]` = 一个都不要，非空 = 白名单。
   *
   * ⚠️ 类型里必须写上 `| null`：Go 的 nil 切片在 JSON 里就是 `null`（不是缺字段），
   * 只写 `undefined` 会让 `value.length` 这类代码在运行时炸——
   * 这里真炸过一次：整个界面变成空白页（React 渲染期异常会卸载整棵树）。
   * 读的时候统一用 `== null` 判断，两种都盖住。
   */
  tools?: string[] | null;
  excludeTools?: string[] | null;
  skills?: string[] | null;
  extensions?: string[] | null;
  promptTemplates?: string[] | null;
  /** 是否加载 AGENTS.md 这类目录上下文；undefined = 不干预 */
  contextFiles?: boolean;
  sessionNamePrefix?: string;
  /**
   * 上下文压缩策略（走 RPC，不需要重启）。
   * undefined = 跟随全局设置；true/false = 这个角色强制开关。
   */
  autoCompaction?: boolean;
  /** 手动压缩时附加的指令（compact 的 customInstructions） */
  compactionInstructions?: string;
  /**
   * 想用哪些 MCP 服务器（undefined = 全部）。
   * ⚠️ 目前只记录与展示：Pi 的 MCP 服务器来自全局 mcp.json，没有"只加载这几个"的开关。
   */
  mcpServers?: string[] | null;
  /** 角色分组（ctf / forensics / ir），界面归组用 */
  category?: string;
  /**
   * 角色要的**能力**（如 reverse.static、web.search），不是工具名。
   *
   * 由 Go 的能力解析器映射成本机实际存在的工具：装了 Ghidra 给 Ghidra，
   * 只装了 radare2 就给 radare2；本机没有的能力会明确告诉用户，
   * 而不是让角色一启动就失败（方案第八、十节）。
   */
  capabilities?: string[];
  /** 角色层权限策略（动作 → allow/ask/deny）；最终值还要与全局/用户/会话取交集 */
  permissions?: Record<string, 'allow' | 'ask' | 'deny'>;
  /** 任务循环策略（方案第十九节） */
  loop?: {
    maxRounds?: number;
    maxDurationSeconds?: number;
    stuck?: { enabled: boolean; threshold?: number; window?: number };
    onStuck?: 'replan' | 'stop' | string;
    onLimit?: 'summarize' | 'stop' | string;
  };
  /**
   * 子智能体委派策略（对应 Go 的 roles.Delegation）。
   *
   * 这是**带外策略**：不放进工具参数，模型改不了；真正的上限还要与会话授权取交集。
   * undefined = 这个角色完全不能派子智能体。
   */
  delegation?: Delegation;
  formatVersion?: number;
}

/**
 * 子智能体委派策略，与 Go 的 roles.Delegation 一一对应。
 *
 * 数值字段 0 / 缺省 = 用默认值；mode / lifecycle 等枚举在 Go 端保存时校验，
 * 界面上用下拉把可选值收死，不留自由输入。
 */
export interface Delegation {
  /** false = 这个角色完全不能派子智能体 */
  enabled: boolean;
  /** background（父继续干活，默认）| blocking（等结果——⚠️ 暂未实现，写了只在编译时给警告） */
  mode?: 'background' | 'blocking';
  /** true = 父会话至少调过一次工具才允许派人 */
  requireAudit?: boolean;
  /** turn_end（回合结束后合并投递，默认）| checkpoint（立刻插话） */
  deliverOn?: 'turn_end' | 'checkpoint';
  /** 能派哪些角色的白名单；空 = 不允许派任何角色 */
  allowedRoles?: string[] | null;
  /** 预算：一条会话最多累计派几个（0 = 默认） */
  maxChildren?: number;
  /** 预算：同时最多跑几个（0 = 默认） */
  maxParallel?: number;
  /** 主 Agent 拿到候选 flag 后是否停掉还在跑的子智能体（undefined = 视为 true） */
  stopOnFlag?: boolean;
  /** 单个子智能体的时限（0 = 不限时） */
  maxDurationSeconds?: number;
  /** 可继续子智能体空闲多久后被回收（0 = 默认 10 分钟） */
  idleTimeoutSeconds?: number;
  /** continuable（跑完可继续发消息，默认）| one-shot（跑完即释放） */
  lifecycle?: 'continuable' | 'one-shot';
  /**
   * 子智能体继承父的什么（留空 = 继承全部）。
   * ⚠️ 暂未实现：目前子智能体完全按子角色自己的定义启动，这里只保留类型不写 UI。
   */
  inherit?: {
    skills?: string[];
    mcp?: string[];
    extensions?: 'all' | 'none';
    model?: 'own' | 'inherit';
  };
}

/** 角色 + 提示词正文（编辑用）；broken 非空表示这个角色的文件有问题 */
export interface RoleView extends Role {
  prompt: string;
  broken?: string;
}

export interface RoleListPayload {
  roles: RoleView[];
  /** 工作区规范化键 → 角色 id */
  roleByWorkspace: Record<string, string>;
  /** 会话文件路径 → 角色 id */
  roleBySession: Record<string, string>;
  workspace: string;
  /** 当前工作区选中的角色 */
  selected: string;
  /** 当前 Pi 进程实际使用的角色（可能因切换失败而与 selected 不同） */
  processRole: string;
}

export const EMPTY_ROLE: Role = {
  id: '',
  name: '',
  description: '',
  icon: 'robot',
  tools: undefined,
  skills: undefined,
  contextFiles: true,
};

/** 界面上的角色图标候选（复用 Icon 组件里已有的图形） */
export const ROLE_ICONS = ['robot', 'flag', 'shield', 'db', 'terminal', 'search', 'tool', 'chat'] as const;

export type RoleIcon = (typeof ROLE_ICONS)[number];
