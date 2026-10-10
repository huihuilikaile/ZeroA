param(
    [string]$Version = '',
    [switch]$SkipBuild
)

# 打包一份可以发给用户的 Windows 压缩包。
#
# 包里有什么、为什么：
#   pi-rpc-lab.exe  应用本体
#   pi-agent/       自带的 Pi 运行时（应用优先用它，不要求用户全局装 pi）
#   README.txt      给用户看的说明（含 Node 依赖与配置目录位置）
#
# 包里**故意不放**什么：
#   bin/.pi/        这是你自己的配置目录，里面有 auth.json（API 密钥）和会话历史。
#                   分发出去等于把你的密钥送人，所以明确排除；用户第一次运行时
#                   Pi 会用 ~/.pi/agent（我们自带的 .pi 不存在时自动回退）。
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$binDir = Join-Path $root 'bin'
$exePath = Join-Path $binDir 'pi-rpc-lab.exe'
$agentDir = Join-Path $binDir 'pi-agent'
$distDir = Join-Path $root 'dist'

if (-not $SkipBuild) {
    Write-Host '先构建最新产物（scripts/build.ps1）...'
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'build.ps1')
    if ($LASTEXITCODE -ne 0) { throw 'build.ps1 失败' }
}

if (-not (Test-Path $exePath)) { throw "找不到 $exePath，先跑 scripts/build.ps1" }

if (-not $Version) {
    $sha = (& git -C $root rev-parse --short HEAD 2>$null)
    if (-not $sha) { $sha = 'dev' }
    $Version = "win-x64-$sha"
}

$stageName = "pi-gui-c-$Version"
$stage = Join-Path $distDir $stageName
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Force $stage | Out-Null

Write-Host "暂存目录: $stage"
Copy-Item $exePath (Join-Path $stage 'pi-rpc-lab.exe')

if (-not (Test-Path $agentDir)) {
    Write-Warning 'bin\pi-agent 不存在：包里将没有自带 Pi，用户需要自己装 pi。建议先跑 scripts/fetch-pi-agent.ps1'
} else {
    Write-Host '复制自带 Pi 运行时（约 118MB，稍等）...'
    # 排除缓存与日志，这些对运行没用
    robocopy $agentDir (Join-Path $stage 'pi-agent') /E /NFL /NDL /NJH /NJS /R:1 /W:1 /XD .cache logs /XF *.log | Out-Null
    if ($LASTEXITCODE -ge 8) { throw "robocopy 失败（exit $LASTEXITCODE）" }
}

# 检查：绝不能把凭据打进包里
$leak = Get-ChildItem $stage -Recurse -File -Include auth.json,models-store.json -ErrorAction SilentlyContinue
if ($leak) {
    $leak | ForEach-Object { Write-Warning ("包里出现了不该有的文件: " + $_.FullName) }
    throw '发现配置文件泄漏，已中止打包'
}

$readme = @"
BTF (pi-gui-c) —— Pi Agent 桌面界面
====================================

怎么用
------
1. 双击 pi-rpc-lab.exe
2. 在右下角输入框里给 Pi 派活（Enter 发送，Shift+Enter 换行）

前置条件
--------
* Windows 10/11 x64
* Node.js 18+ 需要在 PATH 里（自带的 Pi 运行时是 Node 脚本，靠 node 启动）

配置与数据放在哪
----------------
默认使用你的用户目录：C:\Users\<你>\.pi\agent\
  settings.json / models.json / auth.json（API 密钥）/ sessions\（会话历史）

如果想做成"配置跟着程序走"的便携版：
  在本目录下新建 .pi 文件夹，程序会自动优先使用它
  （可参考仓库里 scripts/portable-config.ps1 的说明）

第一次要配模型密钥
------------------
设置 → 模型 → 添加模型提供商 → 选择提供商 + 填 API 密钥 → 保存 → 重启会话。
密钥写在 auth.json 里，格式与 Pi 命令行完全一致（type=api_key）。

主要功能
--------
* 对话：思考过程 / 工具调用 / 流式回答，Markdown 渲染（表格、代码块、复制）
* 附件：任意类型都能传，图片与文档一视同仁——把绝对路径交给模型读
* 工作区：左栏切换工作目录，会话按目录分组
* 文件：右上角分栏按钮打开工作区文件树，点文件开标签页查看
* 导出：会话标题栏「导出 HTML」把当前对话存成自包含网页
* 面板宽度都能拖，双击分隔条复位

版本：$Version
"@
Set-Content -Path (Join-Path $stage 'README.txt') -Value $readme -Encoding UTF8

# 压缩
$zipPath = Join-Path $distDir "$stageName.zip"
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Write-Host '压缩中（118MB 的运行时，需要一会儿）...'
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zipPath -CompressionLevel Optimal

$zip = Get-Item $zipPath
$hash = (Get-FileHash $zipPath -Algorithm SHA256).Hash
$files = (Get-ChildItem $stage -Recurse -File | Measure-Object -Property Length -Sum)

Write-Host ''
Write-Host ('完成：{0}' -f $zip.FullName)
Write-Host ('  压缩包 {0:N1} MB / 解压后 {1:N0} 个文件 {2:N0} MB' -f ($zip.Length / 1MB), $files.Count, ($files.Sum / 1MB))
Write-Host ('  SHA256 {0}' -f $hash)
Write-Host ''
Write-Host '包里内容：'
Get-ChildItem $stage | ForEach-Object { Write-Host ('  ' + $_.Name + $(if ($_.PSIsContainer) { '\' } else { '' })) }
