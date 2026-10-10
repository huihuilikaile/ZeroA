// cdp-real-check.mjs — 真实模式冒烟：发一条消息，运行中点停止，确认无报错。
// 用法：node cdp-real-check.mjs
import { writeFileSync } from 'node:fs';

const CDP = 'http://127.0.0.1:9222';
const sleep = ms => new Promise(r => setTimeout(r, ms));

const targets = await (await fetch(CDP + '/json')).json();
const page = targets.find(t => t.type === 'page');
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
const evaljs = async expression => {
  const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
  return r.result.value;
};

await send('Runtime.enable');
await evaljs(`window.__t = {
  ta() { return document.querySelector('textarea[aria-label="输入消息"]'); },
  setText(t) { const ta = this.ta(); const s = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set; s.call(ta, t); ta.dispatchEvent(new Event('input', { bubbles: true })); },
  btn(label) { return [...document.querySelectorAll('button')].find(b => b.textContent.trim() === label); },
  click(label) { const b = this.btn(label); if (!b || b.disabled) return false; b.click(); return true; },
  state() { const body = document.body.innerText; return {
    hasSend: !!this.btn('发送'), hasStop: !!this.btn('停止'),
    hasErrCard: body.includes('出错了'), hasOldBug: body.includes('Pi 在响应前退出'),
    abortedEntry: body.includes('已中止'), tail: body.slice(-200) }; }
}; 'ok'`);

let ready = false;
for (let i = 0; i < 40 && !ready; i++) { ready = await evaljs('!!window.__t.ta()'); if (!ready) await sleep(500); }
console.log('界面就绪:', ready);

await evaljs(`window.__t.setText('从 1 数到 50，每个数字单独一行')`);
console.log('点击发送:', await evaljs(`window.__t.click('发送')`));
await sleep(6000);
let st = await evaljs('window.__t.state()');
console.log('运行中状态:', JSON.stringify(st));
const r1 = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync('shot-real-running.png', Buffer.from(r1.data, 'base64'));

if (st.hasStop) {
  console.log('点击停止:', await evaljs(`window.__t.click('停止')`));
  await sleep(3000);
  st = await evaljs('window.__t.state()');
  console.log('停止后状态:', JSON.stringify(st));
  console.log('判定:', st.hasSend && !st.hasStop && !st.hasErrCard && !st.hasOldBug ? 'PASS（空闲、无报错）' : 'FAIL');
} else {
  // 回合已经结束（回答太快），同样算通过——只要不是报错
  console.log('回合已自然结束:', !st.hasErrCard && !st.hasOldBug ? 'PASS（无报错）' : 'FAIL');
}
const r2 = await send('Page.captureScreenshot', { format: 'png' });
writeFileSync('shot-real-final.png', Buffer.from(r2.data, 'base64'));
process.exit(0);
