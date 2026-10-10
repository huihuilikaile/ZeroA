/**
 * 会话路由测试的运行器：把 tests/session-router.test.ts 打包成一个 CJS 文件再跑。
 *
 * 为什么要打包而不是直接跑 TS：
 *   · 测试用的是应用里的 `@/...` 别名与 .ts 扩展名，node 原生跑不了；
 *   · bridge/pi 与 @wailsio/runtime 是**浏览器侧**的东西（Wails 的运行时、
 *     生成的绑定），在 node 里既加载不了也没意义——用两份桩替换掉。
 * 桩不写死返回值以外的行为：测试自己会覆盖它关心的那一个方法
 * （见 session-router.test.ts 里的 piBridge.switchSession）。
 */
import { build } from 'esbuild';
import { mkdir } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
/**
 * 打包产物落在 node_modules/.cache 下：那是**已经被 .gitignore 忽略**的地方，
 * 不需要为了跑一次测试去改仓库的忽略规则（参考实现放在仓库根的 .cache/，
 * 那会多出一个未跟踪目录，还要顺带改 .gitignore）。
 */
const outfile = fileURLToPath(new URL('../node_modules/.cache/session-router.test.cjs', import.meta.url));
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
  entryPoints: ['tests/session-router.test.ts'],
  outfile,
  bundle: true,
  platform: 'node',
  format: 'cjs',
  plugins: [{
    name: 'mock-browser-bridges',
    setup(build) {
      // bridge/pi：桩掉"调用后端"这一层（测试只关心状态怎么流转）
      build.onResolve({ filter: /bridge\/pi$/ }, () => ({ path: 'bridge', namespace: 'mock' }));
      // @wailsio/runtime：Wails 的运行时只在 WebView 里存在，node 里给它一个空 Events
      build.onResolve({ filter: /^@wailsio\/runtime$/ }, () => ({ path: 'wails', namespace: 'mock' }));
      build.onLoad({ filter: /.*/, namespace: 'mock' }, args => args.path === 'wails'
        ? { contents: 'export const Events = { On: () => () => {} };', loader: 'js' }
        : { contents: bridgeStub, loader: 'js' });
    },
  }],
});

const result = spawnSync(process.execPath, ['--test', outfile], { stdio: 'inherit' });
process.exit(result.status ?? 1);
