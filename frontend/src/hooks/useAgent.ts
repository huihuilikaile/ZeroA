import { useCallback } from 'react';
import { errorText, piBridge } from '@/bridge/pi';
import { reportError } from '@/lib/reportError';
import { parseData } from '@/rpc/types';
import { collapseTurn, completeSessionRequest, resetSessionViews, routeMessages, routeResult, adoptPendingRuntime } from '@/rpc/router';
import { useRoles } from '@/stores/roles';
import { useRuntime } from '@/stores/runtime';
import { useSessions } from '@/stores/sessions';
import { timelineFor, useTimeline } from '@/stores/timeline';
import { beginTurn } from '@/stores/changedFiles';
import { useTraces } from '@/stores/traces';
import { useUI } from '@/stores/ui';

/**
 * "进程已经死了"类错误：abort/steer/follow_up 打到已退出的进程上时会收到
 * 「Pi 在响应前退出」或「请先启动 Pi」。这几乎都发生在"进程刚死、process_exit
 * 事件还没到"的毫秒级窗口里——回合其实随进程一起结束了，不是用户操作错误。
 */
function isDeadProcessError(error: unknown): boolean {
  return /响应前退出|请先启动 ZeroA/.test(errorText(error));
}

/**
 * Agent 动作集合：所有「前端 → Go」的调用都从这里发出，返回后交给 router 解释。
 * 覆盖 Pi RPC 的对话侧全部能力：prompt / steer / follow_up / abort / clear_queue /
 * get_state / get_messages / get_session_stats / set_model / set_thinking_level /
 * get_available_* / respondUI。
 */
/**
 * 首条消息发出时，用它给会话命名。
 *
 * 为什么需要这一步：Pi 的会话标题默认取**首条用户消息**，但会话文件里没有内容
 * 之前根本没有标题——新建会话后列表里就显示成空的/无会话（用户报的问题）。
 * 所以：
 *   · 新建后由界面先显示占位名「新建会话」（见 Sidebar 的 draft 行）；
 *   · 发出第一条消息时，把「角色前缀 + 消息首行」写进会话名（set_session_name），
 *     列表立刻就有可读的名字，不必等回合结束后的文件扫描。
 *
 * 已有会话（重新打开、切换过来）标着 currentNamed=true，这里什么都不做——
 * 不能因为用户又发了一条就把历史会话改名。
 */
async function nameSessionFromFirstMessage(text: string): Promise<void> {
  const sessions = useSessions.getState();
  if (sessions.currentNamed) return;
  const title = sessionTitleFrom(text);
  if (!title) return;
  // 先标记：命名是 fire-and-forget，不标记的话连发两条消息会命名两次
  sessions.setCurrentNamed(true);
  try {
    const result = await piBridge.nameCurrentSession(title);
    if (!result.success) return;
    // 名字变了要立刻刷新列表：否则侧栏还显示占位名，用户以为没生效
    void sessions.refresh();
  } catch {
    /* 命名只是锦上添花：失败不影响消息发送 */
  }
}

/** 会话标题：角色前缀 + 消息首行（截断到 40 字符，换行只取第一行） */
function sessionTitleFrom(text: string): string {
  const firstLine = text.split('\n').map(line => line.trim()).find(Boolean) ?? '';
  if (!firstLine) return '';
  const clipped = firstLine.length > 40 ? firstLine.slice(0, 40) + '…' : firstLine;
  const roles = useRoles.getState();
  const role = roles.roles.find(item => item.id === roles.selected);
  const prefix = role?.sessionNamePrefix?.trim();
  return prefix ? `${prefix} ${clipped}` : clipped;
}
export function useAgent() {
  /** 启动：先 Stop 清残留，再 Start（带上设置里的目录/模型/思考级别/工具），
      最后用 get_state 证明"这套参数能用"。
      silent=true 时不弹"已连接 Pi"提示：切换工作区会重启进程，那不是用户关心的信息，
      界面上只留转圈动画（见 ui.switchingWorkspace）。

      ⚠️ Start **不创建任何 Pi 进程**（会话=日志，进程留到第一条消息，见 Go 的 Start）：
      所以"启动成功"不再等于"已经连上某个进程"，而是"启动参数模板已经准备好"。
      Go 的 GetState 在没有进程时会如实作答（合成状态，pending=true），
      启动流程因此不会把它当成失败，也不会弹错误条。 */
  const start = useCallback(async (silent = false) => {
    const runtime = useRuntime.getState();
    runtime.setBusy(true);
    useUI.getState().dismissErrorNotices();
    useTimeline.getState().reset();
    useTraces.getState().clear();
    try {
      // 标记"是我停的"：进程退出事件随后会到，router 据此不往对话里插一条
      runtime.setStopping(true);
      await piBridge.stop();
      /**
       * Stop 把所有会话进程都收掉了：那些"后台会话的视图"此刻全部作废
       * （它们记着 connected=true、会话路径还指着已经不在的进程）。
       * 这里一并清空，下一次切换才不会取回一份死掉的状态。
       */
      resetSessionViews();
      // 会话按设置启动：模型与目录不是启动后再补，而是直接进 Pi 的命令行参数。
      // 角色也一样——它就是"这一轮启动带哪套提示词与工具"，见 stores/roles.ts。
      await piBridge.start(
        runtime.mode,
        runtime.cliPath,
        runtime.cwd,
        runtime.startModel,
        runtime.startThinking,
        runtime.startTools,
        useRoles.getState().selected,
      );
      /**
       * 启动参数就绪。
       *
       * 这是"现在能不能干活"的正确判据（而不是 connected）：进程可以还没有，
       * 但参数已经有了——新建会话、打开历史会话、发第一条消息全都能往下走。
       * 侧栏那个"在某个工作区里新建会话"的按钮读的就是它（见 Sidebar 的 newSessionIn）：
       * 用 connected 判，会让"进程刚崩过"表现成"点了没反应"。
       */
      useRuntime.getState().setParamsReady(true);
      const result = await piBridge.getState();
      routeResult(result);
      useRuntime.getState().setConnected(result.success);
      const state = parseData<{ pending?: boolean; sessionFile?: string }>(result);
      /**
       * 启动之后"当前会话"是什么，取决于这次启动**有没有会话被挂上**：
       *
       *   · 没有（刚进应用、或刚改完设置）→ 待创建：清掉上一条会话的指针，
       *     让侧栏与输入区说清"发出第一条消息时会自动启动 Pi"。
       *     紧接着 AppShell 会 open(记住的那条会话)，那时 activePath 会被重新写上。
       *   · 有 → 保持原样，别把用户正在看的那条抹掉。
       *
       * 老实现这里是 `pendingNew: false`——因为 Start 会立刻为"当前空会话"起进程。
       * 那个进程正是"进入应用慢"的一半原因（实测 +3071ms），现在没有了。
       */
      if (!state?.sessionFile) {
        useSessions.setState({ pendingNew: true, currentNamed: false, activePath: '' });
      }
      /**
       * 启动后立刻问一次用量。
       *
       * 上下文环读的是 Pi 的 usage，而 usage 只在模型回话时才推事件——
       * 恢复一个旧会话时没有这一步，环上永远是 0（用户报的"重新打开显示 0"）。
       * get_session_stats 是按会话内容算的，所以刚起来就能给出正确数字。
       *
       * ⚠️ 只在**真的有进程**时问：没有进程时这条命令必然失败（"请先启动 Pi"），
       * 而 refreshStats 会把失败报成错误条——那会在启动时凭空弹一条报错，
       * 用户什么都没做错。
       */
      if (!state?.pending) void refreshStats();
      if (result.success && !silent) useUI.getState().showNotice('已连接 ZeroA', 'success');
    } catch (error) {
      useRuntime.getState().setConnected(false);
      reportError(error);
    } finally {
      useRuntime.getState().setBusy(false);
      useRuntime.getState().setStopping(false);
    }
  }, []);

  /**
   * 应用设置变更（设置模态窗点「保存」时调用）。
   *
   * 判断规则：目录/模型/思考级别/工具是 Pi 的**启动参数**，改它们必须重启进程；
   * 而模式（mock/real）与 Pi 入口同理。重启会清空当前会话，所以这里明确提示。
   * 只想在当前会话里换模型，用检查器/输入区的模型快切（走 set_model，不重启）。
   */
  const applySettings = useCallback(async () => {
    const runtime = useRuntime.getState();
    if (!runtime.connected) {
      useUI.getState().showNotice('设置已保存，下次启动生效');
      return;
    }
    useUI.getState().showNotice('设置已保存，正在按新配置重启会话…');
    await start();
  }, [start]);

  const stop = useCallback(async () => {
    const runtime = useRuntime.getState();
    runtime.setBusy(true);
    try {
      runtime.setStopping(true);
      await piBridge.stop();
      // 进程一停，所有会话视图都作废（同上）
      resetSessionViews();
      useUI.getState().closeDialog();
      useRuntime.getState().setConnected(false);
      useRuntime.getState().setSending(false);
    } catch (error) {
      reportError(error);
    } finally {
      useRuntime.getState().setBusy(false);
      useRuntime.getState().setStopping(false);
    }
  }, []);

  /** 查询状态：失败只提示错误，不改连接状态（一次 RPC 失败不等于进程死了） */
  const refreshState = useCallback(async () => {
    try {
      routeResult(await piBridge.getState());
    } catch (error) {
      reportError(error);
    }
  }, []);

  /** 会话花费/用量统计（get_session_stats） */
  const refreshStats = useCallback(async () => {
    // 没有进程（待创建/待恢复的会话）统计一定问不出来：进程都没起，
    // 拿"请先启动 ZeroA"弹红卡只会吓人。用量要在发出第一条消息后才存在。
    if (!useRuntime.getState().runtimeId) {
      useUI.getState().showNotice('这条会话还没有用量统计（发出第一条消息后才有）');
      return;
    }
    try {
      routeResult(await piBridge.getSessionStats());
    } catch (error) {
      reportError(error);
    }
  }, []);

  /**
   * 重建时间线（切换会话 / 重新连接后恢复界面）。
   *
   * 两条来源，形状相同（都是 `{messages:[...]}`，所以解析只有一份）：
   *   · 有进程 → `get_messages`（进程内存里的权威内容）；
   *   · 没有进程（待创建的会话）→ `load_session_history`（直接读 .jsonl）。
   *     这时问 get_messages 只会得到一句"请先启动 Pi"——用户点的是"重新载入消息"，
   *     历史明明在磁盘上，没有理由报错。
   */
  const loadHistory = useCallback(async () => {
    try {
      const runtime = useRuntime.getState();
      if (runtime.runtimeId) {
        // 有进程：进程内存里的那份就是权威内容
        routeMessages(await piBridge.getMessages());
        useUI.getState().showNotice('已重新载入会话消息', 'success');
        return;
      }
      const path = runtime.sessionFile || useSessions.getState().activePath;
      if (!path) {
        // 新建的会话既没有进程也没有文件：没有历史可载入，别拿"请先启动 Pi"去吓用户
        useUI.getState().showNotice('这条会话还没有内容');
        return;
      }
      routeMessages(await piBridge.loadSessionHistory(path));
      useUI.getState().showNotice('已重新载入会话消息', 'success');
    } catch (error) {
      reportError(error);
    }
  }, []);

  /**
   * 从某条用户消息开新分支（Pi 的 fork）。
   *
   * 语义：新分支从这条消息之后重来，原分支保留。会话是一棵只追加的树，
   * 所以分支完要重新拉一次消息，让界面显示新分支的内容。
   * entryId 来自 get_fork_messages（界面上按文本配对到具体消息）。
   */
  const forkFrom = useCallback(async (entryId: string) => {
    try {
      const result = await piBridge.fork(entryId);
      if (!result.success) {
        reportError(result.error || '分支失败', '分支');
        return;
      }
      // 扩展可能拦下这次分支：如实说明，别让用户以为已经切过去了
      if (result.data?.includes('"cancelled":true')) {
        useUI.getState().showNotice('这次分支被扩展取消了');
        return;
      }
      routeMessages(await piBridge.getMessages());
      useUI.getState().showNotice('已从这条消息开新分支');
    } catch (error) {
      reportError(error, '分支');
    }
  }, []);

  /**
   * 发送对话。
   *
   * **不再需要"先启动 Pi"**：新建的会话在后端是"待创建"状态（会话=日志，进程懒创建，
   * 见 Go 的 NewSession / ensureActiveRuntime），第一条消息到达后端时才现起进程。
   * 所以这里既不拦"没连接"，也不在发送前补一次 start()——那正是"新建会话要等
   * 1–3.5 秒"的来源。进程起来之后，它的 runtimeId 随 prompt 的返回值回来，
   * 由 adoptPendingRuntime 认领（认领时**不动时间线**：用户刚发的那条消息在上面）。
   *
   * files 是图片附件的路径（附件机制统一是"把绝对路径交给模型"，见 Composer）。
   *
   * switching 期间直接返回：那一刻"这条消息发给谁"是不确定的（旧视图已经存下、
   * 新视图还没立起来），发出去就可能落到刚切走的那条会话上。
   */
  const prompt = useCallback(async (text: string, files: string[] = []) => {
    const runtime = useRuntime.getState();
    if (runtime.switching || runtime.sending || runtime.status === 'running') return;

    useTimeline.getState().addUser(text, files);
    /**
     * 「本轮改动」从这条消息开始重新算。
     *
     * 边界按"用户发消息"而不是 agent_start：委派链里结果回传会再唤醒一轮，
     * 按回合清就会把子智能体刚写的文件抹掉（见 stores/changedFiles 的文件头注释）。
     */
    beginTurn();
    runtime.setSending(true);
    useUI.getState().dismissErrorNotices();
    /**
     * 这条消息属于哪个 runtime。
     *
     * 懒启动时它一开始是空的（还没有进程），认领之后才有值；而"中途用户切走了"
     * 的情况下必须继续用**发出去时**那一个——收尾时不能去收别人那条会话的 sending
     * （见 completeSessionRequest 的说明）。
     */
    let target = runtime.runtimeId;
    try {
      const result = await (files.length ? piBridge.promptWithFiles(text, files) : piBridge.prompt(text));
      /**
       * 懒启动的落点：新进程的 id 就在这条返回值里（Go 侧 ensureActiveRuntime）。
       * 先认领（把当前会话指向它、回放等它出生期间攒下的事件），再按普通响应处理。
       */
      adoptPendingRuntime(result.runtimeId ?? '');
      // 认领成功 = 这条会话不再是"待创建"：空态与状态行的文案要跟着变
      if (result.runtimeId) useSessions.setState({ pendingNew: false });
      target = useRuntime.getState().runtimeId || target;
      routeResult(result);
      /**
       * 首条消息给会话命名（用户要求：新建后显示"新建会话"，发出消息后按消息改名）。
       *
       * ⚠️ 必须等 prompt 受理之后再发：懒启动时进程正是被这条 prompt 带起来的，
       * 提前调 set_session_name 只会得到一句"请先启动 Pi"（命名是 fire-and-forget，
       * 用户看不到错误，只会看到会话一直没有名字）。
       */
      void nameSessionFromFirstMessage(text);
      return true;
    } catch (error) {
      reportError(error);
      return false;
    } finally {
      /**
       * 只收掉**这条会话**的发送中标记。
       *
       * 为什么不能用 setSending(false)：这次请求在飞的时候用户可能已经切到别的
       * 会话了，那里也许正跑着另一轮——一起收掉的话，那边输入框会从"等待接收"
       * 跳回"可以发送"，用户以为上一句已经答完。
       */
      completeSessionRequest(target);
    }
  }, []);

  /** 插入（steering）：运行中把消息插到当前回合之后，不打断正在跑的工具调用 */
  const steer = useCallback(async (text: string) => {
    const runtime = useRuntime.getState();
    if (!runtime.connected || runtime.switching) return;
    useUI.getState().dismissErrorNotices();
    useTimeline.getState().addUser(text);
    try {
      routeResult(await piBridge.steer(text));
      useUI.getState().showNotice('已插入队列（当前回合结束后生效）', 'success');
    } catch (error) {
      // 进程已死：刚才那条插入不会生效，但下一条消息会触发懒重启并接回原会话
      if (isDeadProcessError(error)) {
        useUI.getState().showNotice('ZeroA 进程已退出，请重新发送');
      } else {
        reportError(error);
      }
    }
  }, []);

  /**
   * 跟进（follow-up）：等 agent 干完当前所有活之后再生效。
   *
   * 与 steer 不同，这里**不立刻**往时间线里写用户消息：排队中的消息显示在
   * 输入框上方的排队面板里（queue_update 提供原文），用户还能编辑/删除；
   * 等 Pi 真正把它投递出去时，message_end（role=user）会把它回放进对话
   * （addInjectedUser 按全文去重，不会重复）。立刻写时间线的坏处是：
   * 删掉一条排队消息后，对话里还留着一条"假已发送"的记录。
   */
  const followUp = useCallback(async (text: string) => {
    const runtime = useRuntime.getState();
    if (!runtime.connected || runtime.switching) return;
    useUI.getState().dismissErrorNotices();
    try {
      routeResult(await piBridge.followUp(text));
      useUI.getState().showNotice('已排队：显示在输入框上方，可插入/编辑/删除', 'success');
    } catch (error) {
      if (isDeadProcessError(error)) {
        useUI.getState().showNotice('ZeroA 进程已退出，请重新发送');
      } else {
        reportError(error);
      }
    }
  }, []);

  /**
   * 重排队列：clear_queue 之后按给定内容重新排队。
   *
   * Pi 的 RPC 只有"全部清空"（clear_queue），没有单条移除/改序——
   * 排队面板的删除、编辑、"立刻插入"都是先清空再按新内容重排。
   * 竞态窗口很小（清空到重排之间，agent 正好走到检查点把队列吃空），
   * 就算撞上，结果也只是"那条消息提前生效"，不会丢消息。
   *
   * 返回是否成功：调用方据此决定要不要继续做后续动作（例如 steerQueued
   * 在重排失败时不能把那条消息 steer 出去，否则它就进了队列两次）。
   */
  const replaceQueue = useCallback(async (steering: string[], followUps: string[]): Promise<boolean> => {
    const runtime = useRuntime.getState();
    if (!runtime.connected || runtime.switching) return false;
    try {
      await piBridge.clearQueue();
      for (const item of steering) {
        const result = await piBridge.steer(item);
        if (!result.success) throw new Error(result.error || 'steer 失败');
      }
      for (const item of followUps) {
        const result = await piBridge.followUp(item);
        if (!result.success) throw new Error(result.error || 'follow_up 失败');
      }
      // queue_update 事件会把同一份内容再写一遍（权威来源），这里先落一份
      // 是为了"事件还没到"的窗口期面板里也是对的（mock 模式尤其需要）。
      useRuntime.setState({
        queueSteering: steering,
        queueFollowUp: followUps,
        pendingCount: steering.length + followUps.length,
      });
      return true;
    } catch (error) {
      reportError(error, '队列');
      return false;
    }
  }, []);

  /** 移除一条排队消息（面板上的「删除」） */
  const removeQueued = useCallback(async (kind: 'steering' | 'followUp', index: number) => {
    const { queueSteering, queueFollowUp } = useRuntime.getState();
    const steering = kind === 'steering' ? queueSteering.filter((_, i) => i !== index) : queueSteering;
    const followUps = kind === 'followUp' ? queueFollowUp.filter((_, i) => i !== index) : queueFollowUp;
    if (steering.length + followUps.length === queueSteering.length + queueFollowUp.length) return;
    if (await replaceQueue(steering, followUps)) useUI.getState().showNotice('已移除排队消息');
  }, [replaceQueue]);

  /**
   * 把一条排队消息改成"现在就插进去"（面板上的「插入」）。
   *
   * 先从队列里拿掉，再走 steer——不清掉就 steer 的话，同一条消息会
   * 既在队列里、又作为 steering 发出去，生效两次。
   */
  const steerQueued = useCallback(async (kind: 'steering' | 'followUp', index: number) => {
    const { queueSteering, queueFollowUp } = useRuntime.getState();
    const source = kind === 'steering' ? queueSteering : queueFollowUp;
    const text = source[index];
    if (text === undefined) return;
    const steering = kind === 'steering' ? queueSteering.filter((_, i) => i !== index) : queueSteering;
    const followUps = kind === 'followUp' ? queueFollowUp.filter((_, i) => i !== index) : queueFollowUp;
    if (await replaceQueue(steering, followUps)) await steer(text);
  }, [replaceQueue, steer]);

  /**
   * 切换角色。
   *
   * 为什么必须重启进程：Pi 的 RPC **没有** set_system_prompt / set_tools / set_skills
   * （只有 set_model / set_thinking_level 这类运行时可改的东西），角色的提示词与
   * 工具集只能进启动参数。所以这里的流程是：
   *
   *   记住选择（SetActiveRole）→ 重启 Pi → 新会话 → 校验真的生效了
   *
   * 重启会丢掉当前会话，所以调用方要先问用户（见 RoleBadge 的确认框）。
   */
  const switchRole = useCallback(async (roleId: string) => {
    const role = useRoles.getState().roles.find(item => item.id === roleId);
    const changed = await useRoles.getState().select(roleId);
    if (!changed) return;
    // 切换全程（重启 → 新会话 → 命名）立标志：角色按钮用它转圈，
    // 代替以前那句"待重启生效"——编译缓存让这个过程接近瞬时，文字提示只会闪屏。
    useRoles.getState().setSwitching(true);
    try {
      await start(true);
      // 再读一次角色状态：此时 processRole 才应该是新角色
      await useRoles.getState().load();
      // 新会话记上角色 + 用角色的前缀命名（会话列表里一眼能看出属于哪个方向）。
      //
      // 命名走 NameCurrentSession（直接 set_session_name）：不能走 RenameSession，
      // 那个为了改历史会话要先 switch_session 切过去再切回来，对刚开的新会话是白折腾。
      const state = await piBridge.getState();
      if (state.success) {
        const data = parseData<{
          sessionFile?: string;
          thinkingLevel?: string;
          model?: { id?: string; provider?: string };
        }>(state);
        if (data?.sessionFile) {
          void useRoles.getState().rememberSessionRole(data.sessionFile, roleId);
        }
        if (role?.sessionNamePrefix) {
          const stamp = new Date().toLocaleString('zh-CN', {
            month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
          });
          void piBridge.nameCurrentSession(`${role.sessionNamePrefix} ${stamp}`).catch(() => { /* 名字只是锦上添花 */ });
        }
        /**
         * 切角色 = 新会话：名字先给个占位（角色前缀 + 时间），
         * 等用户发出第一条消息再按消息内容改名（用户要求）。
         */
        useSessions.getState().setCurrentNamed(false);
        /**
         * 核对角色是否**真的**生效。
         *
         * 为什么值得单独做一步：角色的模型与思考级别是启动参数，理论上一定会生效；
         * 但"角色只生效一半"是最难发现的一类问题（提示词生效了、模型没生效），
         * 用户会以为是模型不行。这里拿 get_state 的实测值和角色声明比一次，
         * 不一致就明确说出来，不让它悄悄过去。
         */
        if (role) {
          const problems: string[] = [];
          if (role.model && data?.model?.id && !role.model.endsWith(data.model.id)) {
            problems.push(`模型声明 ${role.model}，实际 ${data.model.id}`);
          }
          if (role.thinkingLevel && data?.thinkingLevel && role.thinkingLevel !== data.thinkingLevel) {
            problems.push(`思考级别声明 ${role.thinkingLevel}，实际 ${data.thinkingLevel}`);
          }
          if (problems.length > 0) {
            reportError(`角色「${role.name}」只生效了一部分：${problems.join('；')}`, '角色校验');
          }
        }
      }
      // 成功的反馈就是结果本身：空态中央的角色文本（带出现动画）+ 按钮上的新名字，
      // 不再额外弹胶囊通知。
    } finally {
      useRoles.getState().setSwitching(false);
    }
  }, [start]);

  /**
   * 停止：只停当前回合，不杀进程，之后仍可继续发送。
   *
   * switching 期间直接返回：abort 必须打到一条**明确的**会话上，
   * 而切换中的"当前会话"正处于旧已存下、新还没立起来的状态——
   * 这时候发出去，可能停掉的是用户并没有在看的那条会话。
   */
  const abort = useCallback(async () => {
    if (useRuntime.getState().switching) return;
    // 先立标记再发命令：Pi 会在中止时回一条 "This operation was aborted"，
    // 有了标记就知道那是"我们停的"，不会当成失败弹报错。
    const runtime = useRuntime.getState();
    runtime.setAborting(true);
    try {
      const result = await piBridge.abort();
      routeResult(result);
      /**
       * 中止**没成功**时也要把这一轮收尾（用户实测报的 bug）。
       *
       * 现场：点「停止」→ 报 `Pi 在响应前退出`（进程已经不在了）→ 输入框**永远**
       * 停在"运行中：回车插入一条 steering 消息…"、停止按钮也不消失，只能重启应用。
       * 原因：这一轮的收尾是跟着"请求成功"走的，而请求失败时只弹了一条错误。
       *
       * 为什么失败也要收：用户点的是"停"，把界面留在"运行中"且停止按钮失灵是
       * 最糟的结果。若进程其实还活着、只是这次 abort 没生效，后续增量事件会立刻
       * 把状态改回 running，不会造成误导；反过来（进程已死）则正好避免了死锁状态。
       */
      if (!result || result.success === false) {
        collapseTurn();
      }
    } catch (error) {
      /**
       * 进程已经死了的「停止」不是操作错误：回合随进程一起结束了，
       * 状态由 process_exit 收尾（或马上会）。这种残留态报错大多发生在
       * "进程刚死、事件还没到"的毫秒级窗口里，弹红色错误条只会吓人，
       * 给一句中性提示即可。
       */
      if (isDeadProcessError(error)) {
        useUI.getState().showNotice('ZeroA 进程已退出，本轮已结束');
      } else {
        reportError(error);
      }
      collapseTurn();
    } finally {
      // 无论成败都要把"正在中止"的标记摘掉，否则按钮会一直转
      useRuntime.getState().setAborting(false);
    }
  }, []);

  const clearQueue = useCallback(async () => {
    try {
      routeResult(await piBridge.clearQueue());
      useRuntime.setState({ pendingCount: 0, queueSteering: [], queueFollowUp: [] });
      useUI.getState().showNotice('已清空队列', 'success');
    } catch (error) {
      reportError(error);
    }
  }, []);

  /** 切换模型（set_model） */
  const setModel = useCallback(async (provider: string, model: string) => {
    try {
      routeResult(await piBridge.setModel(provider, model));
      await refreshState();
      useUI.getState().showNotice(`模型已切换：${model}`, 'success');
    } catch (error) {
      reportError(error);
    }
  }, [refreshState]);

  /** 思考级别（set_thinking_level） */
  const setThinkingLevel = useCallback(async (level: string) => {
    try {
      routeResult(await piBridge.setThinkingLevel(level));
      await refreshState();
      useUI.getState().showNotice(`思考级别：${level}`, 'success');
    } catch (error) {
      reportError(error);
    }
  }, [refreshState]);

  /** 回传扩展 UI 的选择；agentId 非空时应答发回那个子智能体的进程 */
  const respondExtension = useCallback(async (requestId: string, payload: Record<string, unknown>, agentId?: string) => {
    const response = JSON.stringify({ type: 'extension_ui_response', id: requestId, ...payload });
    try {
      if (agentId) {
        routeResult(await piBridge.respondAgentUI(agentId, response));
        timelineFor(agentId).getState().resolveExtension(requestId, payload.confirmed === false ? 'denied' : 'allowed');
      } else {
        const result = await piBridge.respondUI(response);
        routeResult(result);
        useTimeline.getState().resolveExtension(requestId, payload.confirmed === false ? 'denied' : 'allowed');
        /**
         * 应答成功后把状态从 waiting-ui 拉回运行态。
         *
         * waiting-ui 是 extension_ui_request 进来时设的（router），但回合并没停——
         * 扩展拿到应答后会继续跑工具。而这之后到 agent_settled 之前，
         * 没有任何事件会再把状态改回 running，于是输入框一直停在
         * 「扩展在等你确认」、发送键也不显示停止（用户实测报的 bug）。
         *
         * 两个前提：
         *   · 只在应答成功时恢复——失败了扩展还在等，waiting-ui 是对的；
         *   · 主时间线上没有其它还 pending 的扩展请求——同时挂着多个对话框时，
         *     得等最后一个答完才能解除等待态。
         */
        if (result.success && useRuntime.getState().status === 'waiting-ui') {
          const stillPending = useTimeline.getState().items.some(
            item => item.kind === 'extension' && item.status === 'pending' && !item.agentId,
          );
          if (!stillPending) {
            const rt = useRuntime.getState();
            rt.setStatus(rt.runStartedAt ? 'running' : 'idle');
          }
        }
      }
    } catch (error) {
      reportError(error);
    } finally {
      useUI.getState().closeDialog();
    }
  }, []);

  return {
    start, stop, applySettings,
    refreshState, refreshStats, loadHistory,
    prompt, steer, followUp, abort, clearQueue, forkFrom,
    replaceQueue, removeQueued, steerQueued,
    setModel, setThinkingLevel, respondExtension,
    switchRole,
  };
}
