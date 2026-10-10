// cdp-smoke.mjs — ZeroA 桌面端冒烟测试：正常停止 / 运行中插入 / 进程崩溃恢复。
//
// 用法：node cdp-smoke.mjs <zeroaPid>
// 前提：zeroa 以 PI_GUI_MODE=mock + PI_GUI_DEBUG_PORT=9222 启动。
// 强烈建议同时设 PI_MOCK_TEXT_DELAY_MS=5：mock 默认 200ms/字，一轮完整回合约 2 分钟，
// 不设的话场景 4 要等满整个回合（脚本已按最长 3 分钟轮询，但没必要每次都真等）。
// 场景 3 会杀掉带 --mock-pi 参数的子进程，模拟"Pi 在响应前退出"的真实成因。
import { writeFileSync, readFileSync } from 'node:fs';
import { execSync } from 'node:child_process';

const zeroaPid = process.argv[2] ? Number(process.argv[2]) : null;
const CDP = 'http://127.0.0.1:9222';
const GUI_LOG = 'bin/.pi/gui-log.txt';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const results = [];
function check(name, ok, detail = '') {
  results.push({ name, ok });
  console.log((ok ? 'PASS' : 'FAIL') + '  ' + name + (detail ? '  —  ' + detail : ''));
}

// ---------- CDP 连接 ----------
const targets = await (await fetch(CDP + '/json')).json();
const page = targets.find(t => t.type === 'page');
if (!page) { console.error('没有找到 page 调试目标'); process.exit(1); }
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0;
const pending = new Map();
ws.onmessage = e => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) {
    const p = pending.get(m.id);
    pending.delete(m.id);
    m.error ? p.rej(new Error(m.error.message)) : p.res(m.result);
  }
};
const send = (method, params = {}) => new Promise((res, rej) => {
  const id = ++seq;
  pending.set(id, { res, rej });
  ws.send(JSON.stringify({ id, method, params }));
});
async function evaljs(expression) {
  const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) {
    throw new Error('页面内执行失败: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  }
  return r.result.value;
}
async function shot(name) {
  const r = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(name, Buffer.from(r.data, 'base64'));
}

await send('Runtime.enable');
await send('Page.enable');

// ---------- 页面助手 ----------
await evaljs(`window.__t = {
  ta() { return document.querySelector('textarea[aria-label="输入消息"]'); },
  setText(t) {
    const ta = this.ta();
    const s = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;
    s.call(ta, t);
    ta.dispatchEvent(new Event('input', { bubbles: true }));
  },
  btn(label) { return [...document.querySelectorAll('button')].find(b => b.textContent.trim() === label); },
  click(label) { const b = this.btn(label); if (!b || b.disabled) return false; b.click(); return true; },
  state() {
    const body = document.body.innerText;
    return {
      hasSend: !!this.btn('发送'), hasStop: !!this.btn('停止'),
      hasInsert: !!this.btn('插入'), hasQueue: !!this.btn('排队'),
      hasErrCard: body.includes('出错了'),
      hasOldBug: body.includes('Pi 在响应前退出'),
      exitedEntry: body.includes('ZeroA 进程已退出'),
      abortedEntry: body.includes('已中止'),
      tail: body.slice(-260)
    };
  }
}; 'ok'`);

let ready = false;
for (let i = 0; i < 40 && !ready; i++) { ready = await evaljs('!!window.__t.ta()'); if (!ready) await sleep(500); }
check('界面就绪（输入框可见）', ready);
if (!ready) process.exit(1);

// ---------- 场景 1：正常停止 ----------
await evaljs(`window.__t.setText('写一首关于秋天的长诗，至少二十行，慢慢写')`);
check('点击发送', await evaljs(`window.__t.click('发送')`));
await sleep(2500);
let st = await evaljs('window.__t.state()');
check('回合进行中（主按钮变为停止）', st.hasStop, JSON.stringify(st));
await shot('shot-1-running.png');
check('运行中点击停止', await evaljs(`window.__t.click('停止')`));
await sleep(2000);
st = await evaljs('window.__t.state()');
check('停止后回到空闲（发送按钮）', st.hasSend && !st.hasStop, JSON.stringify(st));
check('停止后无报错卡片/旧报错文案', !st.hasErrCard && !st.hasOldBug);
check('时间线出现中性「已中止」', st.abortedEntry);
await shot('shot-1-after-stop.png');

// ---------- 场景 2：运行中插入（steer / follow_up） ----------
await evaljs(`window.__t.setText('再写一首关于冬天的长诗，至少二十行')`);
await evaljs(`window.__t.click('发送')`);
await sleep(2500);
st = await evaljs('window.__t.state()');
check('第二回合进行中', st.hasStop, JSON.stringify(st));
await evaljs(`window.__t.setText('补充：结尾加一句关于月亮的')`);
st = await evaljs('window.__t.state()');
const steerLabel = st.hasInsert ? '插入' : (st.hasQueue ? '排队' : null);
check('运行中带稿时主按钮是插入/排队', !!steerLabel, JSON.stringify(st));
if (steerLabel) check('点击「' + steerLabel + '」', await evaljs(`window.__t.click('${steerLabel}')`));
await sleep(1500);
st = await evaljs('window.__t.state()');
check('插入后无报错卡片/旧报错文案', !st.hasErrCard && !st.hasOldBug);
await evaljs(`window.__t.click('停止')`); // 清场，停掉这一回合
await sleep(1500);

// ---------- 场景 3：进程崩溃（本次修复的核心回归） ----------
const logMark = readFileSync(GUI_LOG, 'utf8').length;
await evaljs(`window.__t.setText('写一首关于春天的长诗，至少二十行')`);
await evaljs(`window.__t.click('发送')`);
await sleep(2500);
st = await evaljs('window.__t.state()');
check('第三回合进行中', st.hasStop, JSON.stringify(st));

if (!zeroaPid) {
  check('崩溃场景', false, '未传 zeroaPid');
} else {
  writeFileSync('kill-mockpi.ps1',
    `$p = ${zeroaPid}\n` +
    `Get-CimInstance Win32_Process |\n` +
    `  Where-Object { $_.ParentProcessId -eq $p -and $_.CommandLine -match '--mock-pi' } |\n` +
    `  ForEach-Object { Stop-Process -Id $_.ProcessId -Force; "killed " + $_.ProcessId }\n`);
  const out = execSync('powershell -NoProfile -ExecutionPolicy Bypass -File kill-mockpi.ps1').toString().trim();
  check('已杀 mock Pi 子进程', out.includes('killed'), out || '(无匹配子进程)');
}
await sleep(2500);
st = await evaljs('window.__t.state()');
check('崩溃后界面自动收尾（回到空闲）', st.hasSend && !st.hasStop, JSON.stringify(st));
check('崩溃后出现「ZeroA 进程已退出」提示', st.exitedEntry);
check('崩溃后无「出错了：Pi 在响应前退出」', !st.hasOldBug);
await shot('shot-3-after-crash.png');

// ---------- 场景 4：崩溃后再发送 → 懒重启绑回同一会话 ----------
await evaljs(`window.__t.setText('用一句话回答：1+1 等于几')`);
await evaljs(`window.__t.click('发送')`);
// 懒重启 + 完整回合：mock 默认 200ms/字，一轮最长约 2 分钟，轮询等它跑完
//（等不了这么久就在启动时设 PI_MOCK_TEXT_DELAY_MS=5，见文件头注释）。
let settled4 = false;
for (let i = 0; i < 180 && !settled4; i++) {
  await sleep(1000);
  const s4 = await evaljs('window.__t.state()');
  settled4 = s4.hasSend && !s4.hasStop;
}
const fresh = readFileSync(GUI_LOG, 'utf8').slice(logMark);
const lazy = fresh.match(/lazy_started[^\n]*/);
const boundBack = !!lazy && /session=\S+\.jsonl/.test(lazy[0]);
// mock 不落盘会话文件：如果崩溃前那条会话本身就没有 .jsonl（session= 为空），
// "绑回原会话文件"无从谈起——这时正确行为就是开一条新的（有文件却不绑回来才是 bug）。
const nothingToBind = !!lazy && /session=\s*$/.test(lazy[0]);
check('gui-log 记录 session_runtime_exit_pending',
  fresh.includes('session_runtime_exit_pending') || nothingToBind,
  nothingToBind ? '崩溃的是无落盘文件的 mock 会话，无文件可交还（预期）' : '');
check('懒重启绑回崩溃前的会话文件', boundBack || nothingToBind,
  lazy ? lazy[0] : '(无 lazy_started 行)');
st = await evaljs('window.__t.state()');
check('重启后回合跑完且无报错', settled4 && !st.hasErrCard && !st.hasOldBug && st.hasSend, JSON.stringify(st));
await shot('shot-4-final.png');

const fails = results.filter(r => !r.ok).length;
console.log(`\n=== ${results.length - fails}/${results.length} 通过 ===`);
process.exit(fails ? 1 : 0);
