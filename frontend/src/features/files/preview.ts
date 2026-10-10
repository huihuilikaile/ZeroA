/**
 * 文件预览的"按扩展名选渲染器"规则。
 *
 * 这份表照着 DSH 的 `@deepseek-ai/dsh-util-code-language` 抄下来：
 * 它维护 `LANGUAGES`（扩展名 → 规范语言 id），编辑器/预览靠 `languageForPath(path)`
 * 决定用哪个语法高亮。这里只搬语言映射本身，高亮引擎换成了更轻的 highlight.js
 * （DSH 用的是同步 shiki 内核 + JS 正则引擎；两者产出的 token 都用 CSS 变量着色，
 * 外观由我们自己的一套 token 色控制，见 tokens.css 的 .hljs-* ）。
 *
 * 表用 Map 而不是对象：`foo.constructor` 这类文件名的扩展名正好是 Object.prototype
 * 的成员，用对象查表会命中原型上的成员（得到非字符串），Map 则会老老实实落空。
 */
const LANGUAGES = new Map<string, string>(
  Object.entries({
    typescript: ['ts', 'tsx', 'mts', 'cts'],
    javascript: ['js', 'jsx', 'mjs', 'cjs'],
    shellscript: ['sh', 'bash', 'zsh'],
    json: ['json', 'jsonc', 'jsonl', 'ndjson', 'ipynb'],
    csv: ['csv'],
    python: ['py', 'pyw', 'pyi'],
    ruby: ['rb', 'rake', 'gemspec'],
    go: ['go'],
    rust: ['rs'],
    java: ['java'],
    c: ['c', 'h'],
    cpp: ['cc', 'cpp', 'cxx', 'hh', 'hpp', 'hxx'],
    csharp: ['cs'],
    kotlin: ['kt', 'kts'],
    swift: ['swift'],
    php: ['php'],
    yaml: ['yaml', 'yml'],
    toml: ['toml'],
    ini: ['ini', 'conf', 'cfg', 'properties'],
    dotenv: ['env'],
    log: ['log'],
    diff: ['diff', 'patch'],
    markdown: ['md', 'markdown'],
    html: ['html', 'htm', 'xhtml'],
    css: ['css'],
    scss: ['scss'],
    less: ['less'],
    sql: ['sql'],
    xml: ['xml', 'xsd', 'xsl', 'xslt', 'plist', 'svg'],
    lua: ['lua'],
    bat: ['bat', 'cmd'],
    powershell: ['ps1', 'psm1', 'psd1'],
    dart: ['dart'],
    scala: ['scala'],
    elixir: ['ex', 'exs'],
    haskell: ['hs'],
    graphql: ['graphql', 'gql'],
    proto: ['proto'],
    nix: ['nix'],
    vue: ['vue'],
    svelte: ['svelte'],
    make: ['makefile', 'mk'],
    cmake: ['cmake'],
  }).flatMap(([language, extensions]) => extensions.map(extension => [extension, language] as const)),
);

/** 路径的扩展名：最后一个点之后的部分，小写。带点的文件（.env）也算有扩展名。 */
function extensionForPath(path: string): string | undefined {
  const slash = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
  const base = path.slice(slash + 1);
  const dot = base.lastIndexOf('.');
  return dot < 0 ? undefined : base.slice(dot + 1).toLowerCase();
}

/** 规范语言 id（DSH 的叫法），未收录或没有扩展名时返回 undefined。 */
export function languageForPath(path: string): string | undefined {
  const extension = extensionForPath(path);
  return extension === undefined ? undefined : LANGUAGES.get(extension);
}

/** 这份表里的全部后缀：文档预览用它来判断"该不该按代码渲染"。 */
export const CODE_HIGHLIGHT_EXTENSIONS: string[] = [...LANGUAGES.keys()];

/** 规范语言 id → highlight.js 的注册名（两者多数同名，这里只列不同名的）。
 *
 * highlight.js 没有 TOML 语法（只有 INI），所以 toml 落到 ini；
 * 其余是"用最接近的已注册语法"，宁可颜色略有偏差也不能整篇不上色。 */
const HLJS_ALIAS: Record<string, string> = {
  shellscript: 'bash',
  bat: 'dos',
  dotenv: 'bash',
  toml: 'ini',
  log: 'plaintext',
  csv: 'plaintext',
  html: 'xml',
  xml: 'xml',
  vue: 'xml',
  svelte: 'xml',
  make: 'makefile',
  proto: 'protobuf',
  ini: 'ini',
  diff: 'diff',
};

/** 交给 highlight.js 的语言名；未注册的语言返回 undefined，调用方按纯文本处理。 */
export function highlightLanguageForPath(path: string): string | undefined {
  const language = languageForPath(path);
  if (!language) return undefined;
  return HLJS_ALIAS[language] ?? language;
}

/**
 * 图片扩展名：这些用"读字节 → data URL → <img>"渲染，而不是当文本读。
 * 与 DSH 的静态图片渲染集合一致（PNG/JPEG/GIF/WebP/BMP/ICO/SVG）。
 */
const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'svg']);

export function isImagePath(path: string): boolean {
  const extension = extensionForPath(path);
  return extension !== undefined && IMAGE_EXTENSIONS.has(extension);
}

/**
 * 已知的二进制容器后缀。
 *
 * 照 DSH 的规则：**没有渲染器的已知二进制后缀不发读取请求**，直接在预览页显示
 * "无法预览 + 用系统程序打开"。这样既快，也不会把几 MB 的字节白读一遍再丢掉。
 */
const BINARY_EXTENSIONS = new Set([
  // 可执行 / 库 / 调试符号
  'exe', 'dll', 'so', 'dylib', 'bin', 'o', 'a', 'lib', 'obj', 'pdb', 'sys', 'msi', 'syso',
  // 压缩 / 归档
  'zip', 'gz', 'tgz', 'bz2', 'xz', '7z', 'rar', 'tar', 'jar', 'war',
  // 文档 / 数据容器
  'pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp', 'sqlite', 'db', 'mdb',
  // 字体
  'ttf', 'otf', 'woff', 'woff2', 'eot',
  // 音视频 / 图像原始
  'mp3', 'wav', 'flac', 'ogg', 'm4a', 'mp4', 'mkv', 'avi', 'mov', 'webm', 'psd', 'ai', 'sketch',
  // 其它本应用不预览的
  'class', 'pyc', 'pyo', 'wasm', 'node', 'lock',
]);

export function isBinaryPath(path: string): boolean {
  const extension = extensionForPath(path);
  return extension !== undefined && BINARY_EXTENSIONS.has(extension);
}

/** 预览类型：决定用哪个渲染器。 */
export type PreviewKind = 'image' | 'code' | 'text';

/**
 * 选渲染器（DSH 的 `documentPreviews` 注册表按扩展名排序取第一个命中的实现）：
 * 图片 → 图片渲染器；语言表命中 → 代码渲染器；已知二进制 → 不支持；
 * 其余按纯文本读，读不动（非 UTF-8）时前端再落到"无法预览"。
 */
export function pickPreviewKind(path: string): PreviewKind | 'unsupported' {
  if (isImagePath(path)) return 'image';
  if (isBinaryPath(path)) return 'unsupported';
  if (languageForPath(path)) return 'code';
  return 'text';
}
