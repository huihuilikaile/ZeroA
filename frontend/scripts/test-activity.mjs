/**
 * 侧栏指示器（上下文圆环 / 完成绿点）测试的运行器。
 *
 * 与 scripts/test-sessions.mjs 同一套路（那份是会话路由的测试）：
 *   · 测试用的是 `@/...` 别名与 .ts 扩展名，node 原生跑不了 → 先用 esbuild 打包；
 *   · bridge/pi 与 @wailsio/runtime 是**浏览器侧**的东西（Wails 运行时、生成的绑定），
 *     node 里既加载不了也没意义 → 用桩替换掉。
 *
 * 两份运行器分开写而不是合并成一个"通用 runner"：入口与产物名不同，
 * 合并只会让"跑哪一套"变成一个参数，反而容易看错输出属于哪套测试。
 */
import { build } from 'esbuild';
import { mkdir } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
/** 产物同样落在 node_modules/.cache（已被 .gitignore 忽略），不往仓库里丢中间文件 */
const outfile = fileURLToPath(new URL('../node_modules/.cache/session-activity.test.cjs', import.meta.url));
await mkdir(new URL('../node_modules/.cache/', import.meta.url), { recursive: true });

/** piBridge 的桩：所有调用都成功返回，data 为空（调用方都会自己兜住空 data） */
const bridgeStub = `
const ok = (command, data = {}) => Promise.resolve({ id: '', command, success: true, data: JSON.stringify(data) });
export const piBridge = {
  switchSession: () => ok('switch_session'),
  newSession: () => ok('new_session'),
  switchWorkspace: () => ok('switch_workspace'),
  deleteSession: () => ok('delete_session', { wasActive: false }),
  renameSession: () => ok('rename_session'),
  listSessions: () => ok('list_sessions', { sessions: [] }),
  listSessionsBatch: () => ok('list_sessions_batch', { groups: [] }),
  rememberSession: () => ok('remember_session'),
  getRememberedSession: () => ok('get_remembered_session', { sessionPath: '', source: 'none' }),
  getState: () => ok('get_state'),
  getMessages: () => ok('get_messages', { messages: [] }),
  /** 打开一条还没有进程的历史会话时走它（直接读 .jsonl，形状与 get_messages 相同） */
  loadSessionHistory: () => ok('load_session_history', { messages: [] }),
  getSessionStats: () => ok('get_session_stats'),
  prompt: () => ok('prompt'),
};
export const errorText = (error) => String(error);
`;

await build({
  absWorkingDir: root,
  entryPoints: ['tests/session-activity.test.ts'],
  outfile,
  bundle: true,
  platform: 'node',
  format: 'cjs',
  plugins: [{
    name: 'mock-browser-bridges',
    setup(build) {
      build.onResolve({ filter: /bridge\/pi$/ }, () => ({ path: 'bridge', namespace: 'mock' }));
      build.onResolve({ filter: /^@wailsio\/runtime$/ }, () => ({ path: 'wails', namespace: 'mock' }));
      build.onLoad({ filter: /.*/, namespace: 'mock' }, args => args.path === 'wails'
        ? { contents: 'export const Events = { On: () => () => {} };', loader: 'js' }
        : { contents: bridgeStub, loader: 'js' });
    },
  }],
});

const result = spawnSync(process.execPath, ['--test', outfile], { stdio: 'inherit' });
process.exit(result.status ?? 1);
