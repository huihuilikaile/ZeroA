/**
 * 侧栏指示器（上下文圆环 / 完成绿点）的回归测试（node --test，见 scripts/test-activity.mjs）。
 *
 * 这些错误全是**看不见**的那种：界面照常渲染，只是提示在骗人——
 * 后台跑完了不亮绿点（用户永远不知道那边有结果）、当前会话被挂上绿点
 * （让他去看自己刚看完的东西）、点过了绿点还在（以为没点上）。
 * 所以每一条都要有断言守着：
 *
 *   ① 后台 runtime 的 agent_settled → done=true；**前台**那条 → 不置 done；
 *   ② markSeen（点进会话 / 切换成功登记）→ done 变回 false；
 *   ③ 同一个会话文件的两种写法（大小写 / 分隔符）必须认得出是同一条；
 *   ④ Stop / 重新 Start（resetSessionViews）→ 整张表清空，不留上一批进程的痕迹。
 */
import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { beginSessionSwitch, finishSessionSwitch, resetSessionViews, routeEnvelope } from '@/rpc/router';
import { activityKey, useSessionActivity, type SessionActivity } from '@/stores/sessionActivity';
import { useRuntime } from '@/stores/runtime';
import { useTimeline } from '@/stores/timeline';
import type { Result } from '@/rpc/types';

// 合帧用的是 requestAnimationFrame：测试环境里换成"立即拿到一个句柄"
// （与 tests/session-router.test.ts 同一套做法，断言才看得到最新状态）
globalThis.requestAnimationFrame = () => 1;
globalThis.cancelAnimationFrame = () => {};

const result = (id: string, command: string, data: unknown): Result =>
  ({ runtimeId: id, id: '', command, success: true, data: JSON.stringify(data) });

/**
 * 造一份"切到某条会话"的结果，形状与 Go 侧 activateRuntime 一致：
 *   {state, messages, cwd, sessionPath}
 *
 * ⚠️ sessionPath 是关键：活动表就是靠它把 runtimeId 映射到侧栏那一行的，
 * 少了它表里永远没有记录（这正是"事件只带 runtimeId"要解决的那个问题）。
 */
function activate(id: string, sessionPath: string, cold = false) {
  return result(id, 'switch_session', {
    cwd: 'C:/workspace',
    sessionPath,
    state: result(id, 'get_state', { sessionId: id, sessionFile: sessionPath, isStreaming: false }),
    messages: cold ? result(id, 'get_messages', { messages: [] }) : undefined,
  });
}

/** 发一条 Pi 事件（agentId 省略 = 主会话；子智能体要显式给 id） */
function event(runtimeId: string, type: string, data = {}, at = 1000, agentId = '') {
  const record = JSON.stringify({ type, ...data });
  routeEnvelope({
    runtimeId, agentId, sessionId: '', kind: 'event',
    type, raw: record, record, time: '', receivedAt: at,
  });
}

/** 侧栏查这一行时会看到什么（undefined = 什么都不画） */
function mark(sessionPath: string): SessionActivity | undefined {
  return useSessionActivity.getState().byPath[activityKey(sessionPath)];
}

/** 同一条会话的另一种写法：用来验证路径规范化 */
const A = 'C:\\WS\\A.jsonl';
const B = 'C:/ws/b.jsonl';

beforeEach(() => {
  resetSessionViews();
  useRuntime.getState().resetRuntime();
  useTimeline.getState().reset();
  // 从 A 开始：它此刻是"当前会话"，B 等会儿用来当后台那一条
  beginSessionSwitch();
  finishSessionSwitch(activate('a', A, true));
});

test('后台 runtime 跑完亮绿点；前台那条跑完不亮', () => {
  // 切到 B：A 变成后台（它的记录留在表里，不会因为切走就丢）
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));

  event('a', 'agent_start');
  assert.equal(mark(A)?.running, true, '后台在跑：侧栏要画圈');
  assert.equal(mark(A)?.done, false, '还在跑就不该同时亮绿点');

  event('a', 'agent_settled');
  assert.equal(mark(A)?.running, false, '跑完了圈要收掉');
  assert.equal(mark(A)?.done, true, '后台跑完了：要留一个"有新结果"的绿点');

  // B 是当前会话：它结束时不置 done——用户正看着它，绿点没有意义
  event('b', 'agent_start');
  event('b', 'agent_settled');
  assert.equal(mark(B)?.running, false);
  assert.equal(mark(B)?.done, false, '前台那条跑完不该亮绿点');
});

test('点进这条会话（markSeen）→ 绿点消失，且认得出另一种路径写法', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));
  event('a', 'agent_start');
  event('a', 'agent_settled');
  assert.equal(mark(A)?.done, true);

  // 侧栏那一行的 onClick 就是这么做的：点击即刻清，不等切换成功
  useSessionActivity.getState().markSeen('c:\\ws\\a.JSONL');
  assert.equal(mark(A)?.done, false, '看过了就不该再提示"有新结果"');
});

test('切回这条会话（切换成功登记）也会清掉绿点', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));
  event('a', 'agent_start');
  event('a', 'agent_settled');
  assert.equal(mark(A)?.done, true);

  // 切回 A：登记 runtime↔路径的同时把它标成"已看过"，
  // 随后回放的 agent_settled 也不该把它重新点亮（那时它已经是前台）
  beginSessionSwitch();
  finishSessionSwitch(activate('a', A));
  assert.equal(mark(A)?.done, false);
  assert.equal(mark(A)?.running, false, '回放过来的结束事件要把圈收掉');
});

test('同一个会话文件的两种写法认得出是同一条', () => {
  // A 在后台跑完：记录挂在规范化后的键上
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));
  event('a', 'agent_start');
  event('a', 'agent_settled');
  assert.equal(mark('c:/ws/a.jsonl')?.done, true, '正斜杠 + 小写写法');
  assert.equal(mark('C:\\WS\\A.JSONL')?.done, true, '反斜杠 + 大写写法：必须是同一条会话');

  // 换一种写法点开它：也必须打到同一条上，否则绿点点不掉
  useSessionActivity.getState().markSeen('c:\\Ws\\a.jsonl');
  assert.equal(mark('C:/WS/A.jsonl')?.done, false);
});

test('后台又开始跑：绿点换成圆环', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));
  event('a', 'agent_start');
  event('a', 'agent_settled');
  assert.equal(mark(A)?.done, true);

  event('a', 'agent_start');
  assert.equal(mark(A)?.done, false, '又开始干活了：绿点（"有结果没看"）不再成立');
  assert.equal(mark(A)?.running, true);
});

test('子智能体的事件不算这条会话在跑', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));

  // A 在后台，收到的是它派出去的子智能体的事件（带自己的 agentId）：
  // 子智能体有自己的进程与生命周期，把它当成会话的开始/结束会让侧栏的圈乱闪
  event('a', 'agent_start', {}, 1000, 'agent-1');
  assert.equal(mark(A)?.running, false, '子智能体开跑不等于这条会话在跑');

  event('a', 'agent_settled', {}, 2000, 'agent-1');
  assert.equal(mark(A)?.done, false, '子智能体跑完不是这条会话出结果了');
});

test('Stop / 重新 Start（resetSessionViews）把整张表清空', () => {
  event('a', 'agent_start');
  assert.equal(mark(A)?.running, true);

  resetSessionViews();
  assert.deepEqual(useSessionActivity.getState().byPath, {}, '进程全没了：上一批进程的圈/绿点必须一起清');
  // 反查表也清了：重建之后旧 runtimeId 的事件不会再写回侧栏
  event('a', 'agent_start');
  assert.equal(mark(A), undefined);
});

/* ============================================================
   进程退出（noteExit）
   ------------------------------------------------------------
   进程没了就再也不会有 agent_settled——没有一个出口把 running 收掉的话，
   后台会话的圆环会永远转下去（实测：被闲置回收的会话挂了一整夜的圈）。
   ============================================================ */

test('后台会话的进程退出：圆环停下，"跑完没看"的绿点保留', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));

  // A 在后台跑完一轮（绿点亮着），又开始跑新一轮（圆环亮着）时进程死了
  event('a', 'agent_start');
  event('a', 'agent_settled');
  assert.equal(mark(A)?.done, true);
  event('a', 'agent_start');
  assert.equal(mark(A)?.running, true);

  routeEnvelope({
    runtimeId: 'a', agentId: '', sessionId: '', kind: 'system',
    type: 'process_exit', raw: 'exit 1', record: '', time: '', receivedAt: 3000,
  });
  assert.equal(mark(A)?.running, false, '进程没了：圈必须停下，否则永远转下去');

  // 死 runtime 的反查已清：再到来路不明的旧事件也不会写回侧栏
  event('a', 'agent_start');
  assert.equal(mark(A)?.running, false, '死进程的迟到事件不能再点亮圆环');
});

test('进程退出不动"跑完没看"的绿点', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));
  event('a', 'agent_start');
  event('a', 'agent_settled');
  assert.equal(mark(A)?.done, true);

  // 跑完之后进程被闲置回收：绿点是"有结果没看"，回收改变不了这个事实
  routeEnvelope({
    runtimeId: 'a', agentId: '', sessionId: '', kind: 'system',
    type: 'process_exit', raw: 'exit 0', record: '', time: '', receivedAt: 3000,
  });
  assert.equal(mark(A)?.done, true, '进程回收不能把"还没看的结果"抹掉');
  assert.equal(mark(A)?.running, false);
});

test('后台进程退出不影响前台会话', () => {
  beginSessionSwitch();
  finishSessionSwitch(activate('b', B, true));
  routeEnvelope({
    runtimeId: 'a', agentId: '', sessionId: '', kind: 'system',
    type: 'process_exit', raw: 'exit 1', record: '', time: '', receivedAt: 3000,
  });
  assert.equal(useRuntime.getState().runtimeId, 'b', '后台崩溃不能把前台判成掉线');
  assert.equal(useRuntime.getState().connected, true);
  assert.equal(useRuntime.getState().status, 'idle');
});
