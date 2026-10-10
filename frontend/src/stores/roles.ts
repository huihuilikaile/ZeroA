import { create } from 'zustand';
import { errorText, piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import { parseData } from '@/rpc/types';
import type { Role, RoleListPayload, RoleView } from '@/types/role';

interface RolesStore {
  roles: RoleView[];
  /** 工作区 → 角色 id */
  roleByWorkspace: Record<string, string>;
  /** 会话文件路径 → 角色 id（侧栏徽标） */
  roleBySession: Record<string, string>;
  /** 后端认为"当前工作区选中"的角色 */
  selected: string;
  /** Pi 进程实际用的角色：与 selected 不一致时说明还没重启，界面要提示 */
  processRole: string;
  /** 正在切换角色（重启进程 + 新会话 + 命名的全程）：输入框角色按钮据此转圈 */
  switching: boolean;
  loading: boolean;

  load(): Promise<void>;
  /** 新建或更新角色（元数据 + 提示词） */
  save(role: Role, prompt: string): Promise<boolean>;
  remove(id: string): Promise<boolean>;
  reset(id: string): Promise<boolean>;
  /** 只记住选择，不重启进程（重启由 useAgent.switchRole 负责） */
  select(id: string): Promise<boolean>;
  rememberSessionRole(sessionPath: string, roleId: string): Promise<void>;
  /** 取某个角色的详情（含提示词正文） */
  fetchPrompt(id: string): Promise<string>;
  roleOf(workspace: string, workspaceKey: (path: string) => string): string;
  setSwitching(switching: boolean): void;
}

/**
 * 角色状态。
 *
 * 分工：这个 store 只负责"角色数据 + 当前选择"，**不负责重启 Pi**。
 * 切换角色 = 用新参数重启进程（Pi 没有 set_system_prompt），
 * 那件事放在 useAgent.switchRole 里，因为它要处理"当前回合还在跑"的确认与状态提示。
 */
export const useRoles = create<RolesStore>((set, get) => ({
  roles: [],
  roleByWorkspace: {},
  roleBySession: {},
  selected: '',
  processRole: '',
  switching: false,
  loading: false,
  setSwitching: switching => set({ switching }),

  load: async () => {
    set({ loading: true });
    try {
      const result = await piBridge.listRoles();
      if (!result.success) {
        reportError(result.error || '读取角色列表失败', '角色');
        return;
      }
      const data = parseData<RoleListPayload>(result);
      if (!data) return;
      set({
        roles: (data.roles ?? []).map(normalizeRole),
        roleByWorkspace: data.roleByWorkspace ?? {},
        roleBySession: data.roleBySession ?? {},
        selected: data.selected ?? '',
        processRole: data.processRole ?? '',
      });
    } catch (error) {
      reportError(error, '角色');
    } finally {
      set({ loading: false });
    }
  },

  save: async (role, prompt) => {
    try {
      const result = await piBridge.saveRole(JSON.stringify({ role, prompt }));
      if (!result.success) {
        reportError(result.error || '保存角色失败', '角色');
        return false;
      }
      await get().load();
      return true;
    } catch (error) {
      reportError(error, '角色');
      return false;
    }
  },

  remove: async id => {
    try {
      const result = await piBridge.deleteRole(id);
      if (!result.success) {
        reportError(result.error || '删除角色失败', '角色');
        return false;
      }
      await get().load();
      return true;
    } catch (error) {
      reportError(error, '角色');
      return false;
    }
  },

  reset: async id => {
    try {
      const result = await piBridge.resetRole(id);
      if (!result.success) {
        reportError(result.error || '恢复默认失败', '角色');
        return false;
      }
      await get().load();
      return true;
    } catch (error) {
      reportError(error, '角色');
      return false;
    }
  },

  select: async id => {
    try {
      const result = await piBridge.setActiveRole(id);
      if (!result.success) {
        reportError(result.error || '切换角色失败', '角色');
        return false;
      }
      // processRole 不动：进程还是旧角色，等重启成功后再由 load() 刷新
      set({ selected: id });
      return true;
    } catch (error) {
      reportError(error, '角色');
      return false;
    }
  },

  rememberSessionRole: async (sessionPath, roleId) => {
    try {
      await piBridge.rememberSessionRole(sessionPath, roleId);
      set({ roleBySession: { ...get().roleBySession, [sessionPath]: roleId } });
    } catch {
      // 徽标是锦上添花：记不上不影响会话本身
    }
  },

  fetchPrompt: async id => {
    try {
      const result = await piBridge.getRole(id);
      if (!result.success) return '';
      const data = parseData<RoleView>(result);
      return data?.prompt ?? '';
    } catch (error) {
      reportError(errorText(error), '角色');
      return '';
    }
  },

  roleOf: (workspace, workspaceKey) => get().roleByWorkspace[workspaceKey(workspace)] ?? '',
}));

/**
 * 把后端的"三态白名单"归一成前端唯一的表示。
 *
 * Go 的 nil 切片在 JSON 里是 `null`，缺字段是 `undefined`——两者语义相同
 * （不限制），但代码里判断两种写法很容易漏掉一种。这里在**进 store 的边界**
 * 统一成 `undefined`，下游只认 `undefined | [] | [...]` 三种。
 * `null` 与 `[]` 的区别（不限制 / 全禁）在这一步必须保住：不能把空数组也归一成 undefined。
 */
function normalizeRole(role: RoleView): RoleView {
  const pick = (value: string[] | null | undefined): string[] | undefined =>
    value == null ? undefined : value;
  return {
    ...role,
    tools: pick(role.tools),
    excludeTools: pick(role.excludeTools),
    skills: pick(role.skills),
    extensions: pick(role.extensions),
    promptTemplates: pick(role.promptTemplates),
    scopedModels: role.scopedModels == null ? undefined : role.scopedModels,
  };
}

/** id → 角色，渲染徽标与名称时用 */
export function roleById(roles: RoleView[], id: string): RoleView | undefined {
  return roles.find(role => role.id === id);
}

/**
 * "选中的角色还没在进程里生效"——也就是需要重启 Pi。
 *
 * 三个地方（徽标、输入框状态行、空态）都要判断这件事，所以放在一处：
 * 判断口径漂了会出现"徽标说已生效、状态行说待重启"这种自相矛盾的界面。
 *
 * 注意 processRole 是**后端在进程起来之后**才有的值：界面上它来自
 * ListRoles 的返回，所以每次 start()/switchRole() 之后都要重新 load() 一次
 * （见 AppShell 启动流程），否则这里会永远判成"待重启"。
 */
export function roleNeedsRestart(selected: string, processRole: string): boolean {
  return Boolean(selected) && processRole !== selected;
}

/**
 * 角色能力摘要：工具/技能的白名单语义（不限制 / 无 / 指定）要在界面上说清楚，
 * 否则"这个角色到底能不能写文件"只能靠猜。
 */
export function summarizeRole(role: Role): string[] {
  const parts: string[] = [];
  // 用 == null（而不是 === undefined）同时盖住 null 与 undefined：
  // 只写 undefined 的版本在线上炸过一次，整个设置页变空白。
  const list = (label: string, values: string[] | null | undefined, all: string) => {
    if (values == null) parts.push(`${label}：${all}`);
    else if (values.length === 0) parts.push(`${label}：无`);
    else parts.push(`${label}：${values.join('/')}`);
  };
  list('工具', role.tools, '默认全部');
  if (role.skills != null) list('技能', role.skills, '默认全部');
  if (role.model) parts.push(`模型：${role.model}`);
  if (role.thinkingLevel) parts.push(`思考：${role.thinkingLevel}`);
  return parts;
}
