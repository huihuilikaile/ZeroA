/**
 * 能力层的类型：技能 / MCP 服务器 / 工具目录。
 *
 * 这三样是**环境级**的东西（技能目录、mcp.json、装了哪些包），角色只是引用它们。
 * 所以类型也独立于 Role：界面上"能力"与"角色"是两个视角，
 * 一个回答"这台机器有什么"，一个回答"这个角色用哪些"。
 */

export interface Skill {
  id: string;
  name: string;
  description: string;
  /** 技能的绝对路径（Pi 的 --skill 收的就是它） */
  dir: string;
  /** 方向：skills 下的一级目录名（crypto/web/pwn…） */
  category: string;
  license?: string;
  allowedTools?: string[];
  files: number;
  bytes: number;
  hasReferences: boolean;
  userInvocable: boolean;
  /** 非空表示这个技能的 SKILL.md 有问题 */
  broken?: string;
}

export interface SkillListPayload {
  root: string;
  exists: boolean;
  total: number;
  skills: Skill[];
}

export interface McpServer {
  name: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  url?: string;
  headers?: Record<string, string>;
  type?: string;
  enabled?: boolean;
  /** codemode（默认，脚本里调用）/ deferred（tool_search 加载）/ direct（直接声明）/ hidden */
  exposure?: string;
  description?: string;
  timeout?: number;
  /** stdio / http / unknown */
  transport?: string;
}

export interface McpListPayload {
  servers: McpServer[];
  config: string;
  exists: boolean;
  /** settings.json 里声明的包 */
  packages: string[];
  /** 是否装了提供联网能力的扩展（Pi 内置工具里没有 web_search） */
  webCapable: boolean;
  /** 是否装了 MCP 适配器 */
  adapterReady: boolean;
  exposureModes: string[];
}

export interface ToolInfo {
  id: string;
  name: string;
  note: string;
  category: string;
}

export interface ToolCatalogPayload {
  piTools: ToolInfo[];
  toolHints: { direction: string; label: string; tools: string[] }[];
  packages: string[];
  webCapable: boolean;
  configPath: string;
  skillsRoot: string;
}

/** 字节数转成人看的单位（技能卡片上显示体积） */
export function humanBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
