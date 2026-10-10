/**
 * 会话路由的回归测试（node --test，见 scripts/test-sessions.mjs）。
 *
 * 覆盖的是"一进程一会话"之后最容易出错的那几件事——它们全是**看不见**的错误：
 * 界面照常渲染，只是内容属于错误的那条会话。所以每一条都必须有断言守着：
 *
 *   ① 切走时旧帧被冲干净；后台会话的文本与结束事件留在它自己的视图里，
 *      切回去能看到累积文本与耗时；
 *   ② 切换失败要把待处理输出回放到当前视图，且**不清空**现有内容；
 *   ③ 后台会话的扩展确认框与请求完成不能影响前台；
 *   ④ 迟到的 get_state 响应不能覆盖别的会话的状态；
 *   ⑤ 快速连点只发一次切换；点同一条会话一次都不发。
 */
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import {
  adoptPendingRuntime, beginSessionSwitch, enterPendingSession, finishSessionSwitch,
  flushDeltas, resetSessionViews, routeEnvelope, routeResult, completeSessionRequest,
} from '@/rpc/router';
import { useRuntime } from '@/stores/runtime';
import { useTimeline } from '@/stores/timeline';
import { useUI } from '@/stores/ui';
import { useSessions } from '@/stores/sessions';
import { piBridge } from '@/bridge/pi';
import type { Result } from '@/rpc/types';

// 合帧用的是 requestAnimationFrame：测试环境里换成"立即拿到一个句柄"，
// 这样断言看到的永远是最新状态（真正的落地靠 flushDeltas）。
globalThis.requestAnimationFrame = () => 1;
globalThis.cancelAnimationFrame = () => {};

const result = (id: string, command: string, data: unknown): Result =>
  ({ runtimeId: id, id: '', command, success: true, data: JSON.stringify(data) });

/**
 * 造一份"切到某条会话"的结果，形状与 Go 侧 activateRuntime 的返回一致：
 *   {state, messages, cwd, sessionPath}
 * cold=true 时带 messages（冷启动，前端要重建时间线）；
 * cold=false 时 messages 为 undefined（热切换，界面**不许动时间线**）。
 */
function activate(id: string, cold = false) {
  return result(id, 'switch_session', {
    cwd: 'C:/workspace',
    state: result(id, 'get_state', { sessionId: id, sessionFile: `${id}.jsonl`, isStreaming: false }),
    messages: cold ? result(id, 'get_messages', { messages: [] }) : undefined,
  });
}

function event(id: string, type: string, data = {}, at = 1000) {
  const record = JSON.stringify({ type, ...data });
  routeEnvelope({ runtimeId: id, agentId: '', sessionId: '', kind: 'event', type, raw: record, record, time: '', receivedAt: at });
}

/** 当前时间线里所有助手文本拼起来（断言"这条会话里到底有什么"用） */
function text() {
  return useTimeline.getState().items
    .flatMap(item => item.kind === 'assistant' ? item.blocks.flatMap(b => b.kind === 'text' ? [b.text] : []) : [])
    .join('');
}

beforeEach(() => {
  resetSessionViews();
  useRuntime.getState().resetRuntime();
  useTimeline.getState().reset();
  useUI.getState().closeDialog();
  beginSessionSwitch(); finishSessionSwitch(activate('a', true));
});

test('switch flushes the old frame; background output and completion stay in their session', () => {
  event('a', 'agent_start');
  event('a', 'message_start', { message: { role: 'assistant' } });
  event('a', 'message_update', { assistantMessageEvent: { type: 'text_delta', delta: 'before ' } });
  // 切走：这一步必须把缓冲里的 'before ' 落到 A 自己的时间线上
  beginSessionSwitch();
  // 切换期间到达的增量属于 A：攒着，不能落到正在切过去的 B 上
  event('a', 'message_update', { assistantMessageEvent: { type: 'text_delta', delta: 'after' } });
  finishSessionSwitch(activate('b', true));
  // A 在后台继续跑完：这些事件同样只能进 A 的视图
  event('a', 'message_end', { message: { role: 'assistant', content: [{ type: 'text', text: 'before after' }] } });
  event('a', 'agent_settled', {}, 4000);
  assert.equal(text(), '', '前台（B）不该看到 A 的任何文字');
  assert.equal(useRuntime.getState().status, 'idle');
  // 切回 A：热切换（没有 messages），内容从视图里取回，后台事件按原顺序回放
  beginSessionSwitch(); finishSessionSwitch(activate('a'));
  assert.equal(text(), 'before after');
  assert.equal(useRuntime.getState().status, 'idle');
  assert.equal(useRuntime.getState().lastDuration, 3, '耗时要用事件到达时刻算，不是回放时刻');
  assert.equal(useTimeline.getState().items.filter(i => i.kind === 'settled').length, 1);
});

test('failed switch replays pending output without clearing current content', () => {
  event('a', 'message_start', { message: { role: 'assistant' } });
  beginSessionSwitch();
  // 切换失败（finishSessionSwitch 不带 result）：这些输出本来就属于 A
  event('a', 'message_update', { assistantMessageEvent: { type: 'text_delta', delta: 'retained' } });
  finishSessionSwitch();
  assert.equal(text(), 'retained', '切换失败不能吞掉这期间的内容');
  assert.equal(useRuntime.getState().runtimeId, 'a', '切换失败不能换会话');
});

test('background extension dialog and request completion cannot affect foreground', () => {
  useRuntime.setState({ sending: true });
  beginSessionSwitch(); finishSessionSwitch(activate('b', true));
  // A 的 prompt 完成：只收 A 的 sending，前台的等待状态不受影响
  completeSessionRequest('a');
  // A 的扩展要用户确认：绝不能弹到前台来
  event('a', 'extension_ui_request', { id: 'permission-a', method: 'confirm', title: 'Approve' });
  assert.equal(useUI.getState().dialog, null);
  beginSessionSwitch(); finishSessionSwitch(activate('a'));
  assert.equal(useUI.getState().dialog?.requestId, 'permission-a', '切回去才看得到它自己的确认框');
  assert.equal(useRuntime.getState().sending, false);
  assert.equal(useRuntime.getState().status, 'waiting-ui');
});

test('late state responses cannot overwrite another session', () => {
  beginSessionSwitch(); finishSessionSwitch(activate('b', true));
  // A 的 get_state 迟到：它属于 A，不能写进正在看的 B
  routeResult(result('a', 'get_state', { sessionId: 'a', isStreaming: true }));
  assert.equal(useRuntime.getState().sessionId, 'b');
  assert.equal(useRuntime.getState().status, 'idle');
});

test('rapid clicks issue one switch and same-session clicks issue none', async () => {
  let resolve!: (r: Result) => void;
  let calls = 0;
  piBridge.switchSession = () => { calls++; return new Promise(r => { resolve = r; }); };
  const first = useSessions.getState().open('b.jsonl');
  // 第一次还在飞：第二次点击不该再发一次切换
  await useSessions.getState().open('c.jsonl');
  assert.equal(calls, 1);
  resolve(activate('b', true));
  await first;
  assert.equal(useRuntime.getState().sessionId, 'b');
  // 点同一条会话：一次都不发
  await useSessions.getState().open('b.jsonl');
  assert.equal(calls, 1);
  flushDeltas();
});

/* ============================================================
   懒启动（会话=日志，进程按需创建）
   ------------------------------------------------------------
   新建会话不再立刻起进程（Go 侧 NewSession），所以多出来两个新状态：
   「待创建」（没有 runtimeId、没有进程）与「认领」（第一条消息把进程带起来）。
   这两步错了会很难发现：界面照常渲染，只是消息发到了别的会话，
   或者用户刚发的那句话在进程起来的一瞬间被历史重建抹掉。
   ============================================================ */

test('懒启动：新建会话立刻可用，第一条消息才认领进程', () => {
  // ① 新建：后端只换了"当前会话"的指针，没有进程（pending 结果）
  enterPendingSession('C:/workspace');
  useSessions.setState({ activePath: '', currentNamed: false, pendingNew: true });
  assert.equal(useRuntime.getState().runtimeId, '', '待创建的会话还没有进程 id');
  assert.equal(useSessions.getState().pendingNew, true, '"还没有进程"由 pendingNew 表达');
  assert.equal(useTimeline.getState().items.length, 0, '新会话的时间线必须是空的');
  /**
   * connected **有意保持原值**：侧栏"在某个工作区里新建会话"的按钮把它当总开关，
   * 置 false 会让那个按钮静默失效（见 enterPendingSession 的说明）。
   * 这里手工把它置 false，模拟"从没连上过"的起点：认领必须把它翻回来。
   */
  useRuntime.setState({ connected: false });

  // ② 用户发出第一条消息：界面上先画出来（与 useAgent.prompt 的顺序一致）
  useTimeline.getState().addUser('你好');
  // ③ 进程在后台出生：它此刻还不是"当前会话"，事件先攒着
  event('r1', 'agent_start');
  event('r1', 'message_update', { assistantMessageEvent: { type: 'text_delta', delta: '在的' } });
  assert.equal(text(), '', '还没认领的进程不能往前台写');

  // ④ 认领（真实路径是 prompt 的返回值带来的 runtimeId）
  adoptPendingRuntime('r1');
  assert.equal(useRuntime.getState().runtimeId, 'r1');
  assert.equal(useRuntime.getState().connected, true, '认领 = 这条会话真的能说话了');
  assert.equal(useRuntime.getState().status, 'running', '攒下的 agent_start 要回放出来');
  assert.equal(text(), '在的', '攒下的文本要按原顺序回放');
  const mine = useTimeline.getState().items.filter(item => item.kind === 'user');
  assert.equal(mine.length, 1, '认领不能重建时间线：用户刚发的那条消息必须还在');
});

test('懒启动：已经有当前会话时，迟到的认领不能抢前台', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', true));
  adoptPendingRuntime('r9');
  assert.equal(useRuntime.getState().runtimeId, 'b', '这不是"待创建被带起来"，不能抢当前会话');
});

test('新建会话的 pending 结果把自己切成未命名新会话', async () => {
  const pendingResult: Result = {
    id: '', command: 'new_session', success: true, runtimeId: '',
    data: JSON.stringify({
      state: null, messages: null, cwd: 'C:/workspace',
      sessionPath: '', runtimeId: '', pending: true,
    }),
  };
  piBridge.newSession = () => Promise.resolve(pendingResult);
  await useSessions.getState().create();
  assert.equal(useRuntime.getState().runtimeId, '', '新建不再起进程：当前会话没有 runtimeId');
  assert.equal(useSessions.getState().pendingNew, true);
  assert.equal(useSessions.getState().activePath, '', '侧栏据此显示「新建会话」占位行');
  assert.equal(useSessions.getState().currentNamed, false, '首条消息还要给它命名');
  assert.equal(useTimeline.getState().items.length, 0);
});

/* ============================================================
   打开历史会话：冷启动那条路改成"读文件历史 + 进待创建状态"
   ------------------------------------------------------------
   这是"进入应用慢"的另一半（实测两次冷启动，另一次在 Start）。
   错法的表现很隐蔽：界面照样渲染，但内容可能是空的、或者被当成一次
   普通切换（于是上一条会话的视图留在屏幕上，用户以为没点动）。
   ============================================================ */

test('打开还没进程的历史会话：读文件历史 + 进待创建（不起进程）', async () => {
  const path = 'C:/workspace/hist.jsonl';
  let switchCalls = 0;
  let historyPath = '';
  piBridge.switchSession = () => {
    switchCalls++;
    return Promise.resolve({
      id: '', command: 'switch_session', success: true, runtimeId: '',
      data: JSON.stringify({
        state: null, messages: null, cwd: 'C:/workspace',
        sessionPath: path, runtimeId: '', pending: true,
      }),
    });
  };
  piBridge.loadSessionHistory = (target: string) => {
    historyPath = target;
    return Promise.resolve({
      id: '', command: 'load_session_history', success: true, runtimeId: '',
      data: JSON.stringify({
        messages: [
          { role: 'user', content: '上一次问的问题' },
          { role: 'assistant', content: [{ type: 'text', text: '上一次的回答' }] },
        ],
      }),
    });
  };

  const ok = await useSessions.getState().open(path);
  assert.equal(ok, true, '打开成功');
  assert.equal(switchCalls, 1, '要问一次后端"这条会话是不是已经开着"');
  assert.equal(historyPath, path, '历史是直接从会话文件读的（不经过进程）');
  assert.equal(useRuntime.getState().runtimeId, '', '不起进程：当前会话没有 runtimeId');
  assert.equal(useSessions.getState().activePath, path, '当前会话就是这条历史会话');
  assert.equal(useSessions.getState().pendingNew, true, '"还没有进程"由 pendingNew 表达');
  assert.equal(useSessions.getState().currentNamed, true, '历史会话本来就有名字，不能被首条消息改名');
  assert.equal(useRuntime.getState().sessionFile, path, 'get_state 的合成状态要指向这条会话');
  assert.equal(text(), '上一次的回答', '文件里的历史要画出来');
  const users = useTimeline.getState().items.filter(item => item.kind === 'user');
  assert.equal(users.length, 1, '用户消息也只画一次');
});

test('打开会话失败时不动当前会话（文件读不出来的那条）', async () => {
  piBridge.switchSession = () => Promise.resolve({
    id: '', command: 'switch_session', success: false, runtimeId: '',
    data: '', error: '会话的工作目录不存在：C:/gone',
  });
  const ok = await useSessions.getState().open('C:/workspace/gone.jsonl');
  assert.equal(ok, false, '失败要如实返回 false（启动恢复不能把失败当成功）');
  assert.equal(useRuntime.getState().runtimeId, 'a', '当前会话一动不动');
  assert.equal(text(), '', '时间线也不动');
});

/* ============================================================
   进程退出（崩溃 / 回收 / 强杀）
   ------------------------------------------------------------
   两条铁律，都是实测踩出来的：
     ① 死 runtime 的积压倒这就地清掉——它永远不会再成为"当前会话"
        （Go 侧已把它摘出登记表，下次打开按会话文件重建），攒着就是
        随"开过又死掉的会话数"无限涨的内存泄漏；
     ② 前台那条死了要**就地收尾**（输入框、停止按钮、流式光标），
        并把死 id 从"当前会话"上摘下来——否则懒启动的新进程认领不进来，
        表现为"发了消息、agent 在跑、界面一个字不动"。
   ============================================================ */

test('后台会话进程退出：前台不受影响，积压不再为它保留', () => {
  beginSessionSwitch(); finishSessionSwitch(activate('b', true));
  // A 在后台攒了一些输出与一条 prompt 受理凭证
  event('a', 'message_update', { assistantMessageEvent: { type: 'text_delta', delta: '后台输出' } });
  routeResult(result('a', 'prompt', {}));
  // A 的进程死了（崩溃/被回收）
  routeEnvelope({
    runtimeId: 'a', agentId: '', sessionId: '', kind: 'system',
    type: 'process_exit', raw: 'exit 1', record: '', time: '', receivedAt: 1,
  });
  // 前台 B 一切照常
  assert.equal(useRuntime.getState().runtimeId, 'b');
  assert.equal(useRuntime.getState().connected, true);
  assert.equal(useRuntime.getState().status, 'idle');
  assert.equal(text(), '', '死会话的输出一个字都不能漏到前台');
});

test('切换途中当前进程退出：就地收尾，之后新进程仍能认领（崩溃恢复链路）', () => {
  useRuntime.setState({ sending: true });
  beginSessionSwitch();
  // 切换还没完成，A 的进程死了
  routeEnvelope({
    runtimeId: 'a', agentId: '', sessionId: '', kind: 'system',
    type: 'process_exit', raw: 'exit 1', record: '', time: '', receivedAt: 1,
  });
  assert.equal(useRuntime.getState().runtimeId, '', '死进程的 id 不能占着"当前会话"');
  assert.equal(useRuntime.getState().connected, false);
  assert.equal(useRuntime.getState().sending, false, '输入框不能卡在"等待接收"');
  // 切换收尾（失败路径）：不能把死 id 放回来
  finishSessionSwitch();
  assert.equal(useRuntime.getState().runtimeId, '');
  // 用户再发消息：懒启动的新进程必须能认领进来（不摘 id 时这一步被拒，界面永远不动）
  adoptPendingRuntime('r-new');
  assert.equal(useRuntime.getState().runtimeId, 'r-new');
  assert.equal(useRuntime.getState().connected, true);
});

test('前台进程退出（非切换中）：时间线里留一条进程已退出，状态落 offline', () => {
  useRuntime.setState({ sending: true, connected: true });
  routeEnvelope({
    runtimeId: 'a', agentId: '', sessionId: '', kind: 'system',
    type: 'process_exit', raw: 'exit 1', record: '', time: '', receivedAt: 1,
  });
  assert.equal(useRuntime.getState().runtimeId, '');
  assert.equal(useRuntime.getState().status, 'offline');
  assert.equal(useRuntime.getState().sending, false);
  const exitLines = useTimeline.getState().items.filter(
    item => item.kind === 'system' && item.title === 'ZeroA 进程已退出');
  assert.equal(exitLines.length, 1, '要留一条看得见的退出记录（可追溯）');
});
