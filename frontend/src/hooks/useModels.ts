import { useEffect, useState } from 'react';
import { piBridge } from '@/bridge/pi';
import { parseData } from '@/rpc/types';
import { useRuntime } from '@/stores/runtime';

/**
 * Pi 配置里的一个模型（字段与真实 Pi 的 Model 对象一致，
 * 见 pi 的 docs/rpc-commands.md "Model object"）。
 */
export interface PiModel {
  id: string;
  name?: string;
  provider: string;
  api?: string;
  baseUrl?: string;
  reasoning?: boolean;
  contextWindow?: number;
  maxTokens?: number;
  cost?: { input?: number; output?: number; cacheRead?: number; cacheWrite?: number };
  /** 该模型支持哪些思考级别：值为 null 表示不支持（真实 Pi 就是这么表达的） */
  thinkingLevelMap?: Record<string, string | null>;
}

interface ModelsData {
  models?: PiModel[];
}

/**
 * 模型清单的模块级缓存。
 *
 * 为什么不能在组件级的 useState 里自生自灭：Composer 按 runtimeId 做 key
 * （草稿隔离，见 Composer.tsx 的 Composer/SessionComposer），进程一起一落
 * 组件就**卸载重挂**，useState 里的清单随之清零——徽标就从显示名
 * （DeepSeek V4.1 Flash）掉回原始 id（deepseek-flash），要等下次拉取才恢复。
 * 而清单描述的是"Pi 配置里有哪些模型"，与组件实例无关：放模块级，
 * 重挂载时同步就能拿到上一份，拉取成功后再整体换新。
 */
let sharedModels: PiModel[] = [];

/**
 * 模型列表 = Pi 自己配置的模型（~/.pi/agent/models.json + models-store.json）。
 *
 * 我们不维护任何模型常量：连上 Pi 之后问一次 get_available_models，
 * Pi 按自己的配置回答什么，界面就显示什么。切换走 set_model（字段是 modelId）。
 * 所以用户在 Pi 里加了模型、改了 provider，界面刷新即见，不用改这里的代码。
 */
export function useModels() {
  const connected = useRuntime(s => s.connected);
  /** 懒启动的落点：runtimeId 从空变成有值 = 进程刚起来。
      只盯 connected 会漏掉这一刻（pending 状态下 connected 已是 true），
      进程起来后必须再拉一次——既刷新界面清单，也让后端把缓存写进磁盘。 */
  const runtimeId = useRuntime(s => s.runtimeId);
  const provider = useRuntime(s => s.provider);
  const model = useRuntime(s => s.model);
  const [models, setModelsState] = useState<PiModel[]>(sharedModels);
  /** 写清单必须走这里：模块级缓存与组件 state 一起换（见 sharedModels 的说明） */
  const setModels = (list: PiModel[]) => {
    sharedModels = list;
    setModelsState(list);
  };
  /** 这次拉取没有进程可问 → 清单来自磁盘缓存（models-cache.go）。
      界面上给个标识，清单滞后时用户知道为什么、什么时候会刷新。 */
  const [fromCache, setFromCache] = useState(false);

  useEffect(() => {
    /**
     * 断线**不清**清单：模型列表来自 Pi 的配置文件，进程死了配置又没变；
     * 清空的话徽标会从显示名（DeepSeek V4.1 Flash）掉回原始 id（deepseek-flash）
     * ——进程崩溃后那几秒的货不对板就是这么来的。能不能**切换**由 connected
     * 单独把关（Composer 的 modelReady），这里只管"有没有名字可显示"。
     */
    if (!connected) return;
    let alive = true;
    (async () => {
      try {
        const data = parseData<ModelsData | PiModel[]>(await piBridge.getAvailableModels());
        if (!alive) return;
        const list = Array.isArray(data) ? data : (data?.models ?? []);
        // 同一个 id 可能挂在多个 provider 下（例如 deepseek 与中转 provider），
        // 所以按 provider/id 去重，而不是按 id。
        const seen = new Set<string>();
        setModels(list.filter(item => {
          const key = `${item.provider}/${item.id}`;
          if (seen.has(key)) return false;
          seen.add(key);
          return true;
        }));
        // 拉取成功了：当时有进程 = 实时应答，没有 = 缓存原文
        setFromCache(!runtimeId);
      } catch { /* 拿不到就保持空列表，界面显示"连接后可切换" */ }
    })();
    return () => { alive = false; };
  }, [connected, runtimeId]);

  /** 按 provider 分组，便于在下拉里分节显示 */
  const groups = models.reduce<Record<string, PiModel[]>>((acc, item) => {
    (acc[item.provider || 'default'] ??= []).push(item);
    return acc;
  }, {});

  /** 当前模型的展示名（优先 name，退回 id） */
  const current = models.find(item => item.id === model && (!provider || item.provider === provider)) ?? models.find(item => item.id === model);
  const currentLabel = current?.name || model || '';

  /** 思考级别：真实 Pi 的 get_available_thinking_levels 只回 levels（没有 current），
      而每个模型支持哪些级别写在 thinkingLevelMap 里（null = 不支持）。
      两边取交集，界面上就不会出现"选了没反应"的档位。 */
  const [levels, setLevels] = useState<string[]>([]);
  useEffect(() => {
    if (!connected) return;
    let alive = true;
    (async () => {
      try {
        const data = parseData<{ levels?: string[] } | string[]>(await piBridge.getAvailableThinkingLevels());
        if (alive) setLevels(Array.isArray(data) ? data : (data?.levels ?? []));
      } catch { /* 同上 */ }
    })();
    return () => { alive = false; };
  }, [connected, runtimeId]);

  const map = current?.thinkingLevelMap;
  const supportedLevels = map
    ? levels.filter(level => map[level] != null)
    : levels;

  return { models, groups, current, currentLabel, levels, supportedLevels, fromCache };
}
