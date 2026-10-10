import { create } from 'zustand';
import { piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import { parseData } from '@/rpc/types';
import { useUI } from '@/stores/ui';
import type {
  McpListPayload,
  McpServer,
  Skill,
  SkillListPayload,
  ToolCatalogPayload,
  ToolInfo,
} from '@/types/capabilities';

interface CapabilitiesStore {
  skills: Skill[];
  skillsRoot: string;
  skillsExist: boolean;
  mcpServers: McpServer[];
  mcpConfig: string;
  /** 装了哪些 Pi 包（联网能力就来自它们，Pi 内置工具里没有 web_search） */
  packages: string[];
  webCapable: boolean;
  adapterReady: boolean;
  piTools: ToolInfo[];
  toolHints: { direction: string; label: string; tools: string[] }[];
  loading: boolean;

  load(): Promise<void>;
  loadSkills(): Promise<void>;
  loadMcp(): Promise<void>;
  saveMcp(server: McpServer): Promise<boolean>;
  removeMcp(name: string): Promise<boolean>;
  importSkills(dir: string): Promise<void>;
  openPath(path: string): Promise<void>;
}

/**
 * 能力状态（技能 / MCP / 工具目录）。
 *
 * 单独一个 store 而不是塞进 useRoles：角色的保存会频繁刷新角色列表，
 * 而技能扫描与 mcp.json 读取要慢得多（要遍历目录树）。分开之后
 * 编辑角色时不会反复去扫磁盘。
 */
export const useCapabilities = create<CapabilitiesStore>((set, get) => ({
  skills: [],
  skillsRoot: '',
  skillsExist: false,
  mcpServers: [],
  mcpConfig: '',
  packages: [],
  webCapable: false,
  adapterReady: false,
  piTools: [],
  toolHints: [],
  loading: false,

  load: async () => {
    set({ loading: true });
    try {
      await Promise.all([get().loadSkills(), get().loadMcp()]);
    } finally {
      set({ loading: false });
    }
  },

  loadSkills: async () => {
    try {
      const result = await piBridge.listSkills();
      if (!result.success) {
        reportError(result.error || '读取技能失败', '技能');
        return;
      }
      const data = parseData<SkillListPayload>(result);
      if (!data) return;
      set({ skills: data.skills ?? [], skillsRoot: data.root ?? '', skillsExist: Boolean(data.exists) });
    } catch (error) {
      reportError(error, '技能');
    }
  },

  loadMcp: async () => {
    try {
      // 一次调用同时拿到 MCP 服务器、已装包与工具目录：
      // 界面上它们属于同一屏（"这台机器有什么能力"），分三次请求只会更慢。
      const [mcpResult, toolResult] = await Promise.all([piBridge.listMcpServers(), piBridge.listToolCatalog()]);
      if (mcpResult.success) {
        const data = parseData<McpListPayload>(mcpResult);
        if (data) {
          set({
            mcpServers: data.servers ?? [],
            mcpConfig: data.config ?? '',
            packages: data.packages ?? [],
            webCapable: Boolean(data.webCapable),
            adapterReady: Boolean(data.adapterReady),
          });
        }
      } else {
        reportError(mcpResult.error || '读取 MCP 配置失败', 'MCP');
      }
      if (toolResult.success) {
        const data = parseData<ToolCatalogPayload>(toolResult);
        if (data) set({ piTools: data.piTools ?? [], toolHints: data.toolHints ?? [] });
      }
    } catch (error) {
      reportError(error, 'MCP');
    }
  },

  saveMcp: async server => {
    try {
      const result = await piBridge.saveMcpServer(JSON.stringify(server));
      if (!result.success) {
        reportError(result.error || '保存 MCP 服务器失败', 'MCP');
        return false;
      }
      await get().loadMcp();
      return true;
    } catch (error) {
      reportError(error, 'MCP');
      return false;
    }
  },

  removeMcp: async name => {
    try {
      const result = await piBridge.deleteMcpServer(name);
      if (!result.success) {
        reportError(result.error || '删除 MCP 服务器失败', 'MCP');
        return false;
      }
      await get().loadMcp();
      return true;
    } catch (error) {
      reportError(error, 'MCP');
      return false;
    }
  },

  importSkills: async dir => {
    try {
      const result = await piBridge.importSkills(dir);
      if (!result.success) {
        reportError(result.error || '导入技能失败', '技能');
        return;
      }
      const data = parseData<{ imported?: string[]; skipped?: string[]; total?: number }>(result);
      const imported = data?.imported?.length ?? 0;
      const skipped = data?.skipped?.length ?? 0;
      useUI.getState().showNotice(
        imported > 0 ? `已导入 ${imported} 个技能（跳过 ${skipped} 个已存在）` : `没有新技能（${skipped} 个已存在）`,
        imported > 0 ? 'success' : undefined,
      );
      await get().loadSkills();
    } catch (error) {
      reportError(error, '技能');
    }
  },

  openPath: async path => {
    try {
      await piBridge.openPath(path);
    } catch (error) {
      reportError(error, '打开目录');
    }
  },
}));

// 放在文件末尾导入，避免与 stores/ui 形成循环依赖（它也会引用本文件的能力状态）。

/** 按方向分组技能：界面上一个方向一组卡片 */
export function groupSkills(skills: Skill[]): { category: string; label: string; items: Skill[] }[] {
  const labels: Record<string, string> = {
    crypto: '密码学', web: 'Web', pwn: 'Pwn', reverse: '逆向',
    forensics: '取证', misc: '杂项', cve: '漏洞', ir: '应急响应',
  };
  const groups = new Map<string, Skill[]>();
  for (const skill of skills) {
    const key = skill.category || '其他';
    groups.set(key, [...(groups.get(key) ?? []), skill]);
  }
  return [...groups.entries()]
    .map(([category, items]) => ({ category, label: labels[category] ?? category, items }))
    .sort((a, b) => a.category.localeCompare(b.category));
}
