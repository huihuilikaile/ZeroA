# 把 Pi Agent 拉到「应用同目录」，让打包后的应用自带运行时，不依赖全局安装。
#
# 目标目录：<repo>/bin/pi-agent/ —— 也就是 pi-rpc-lab.exe 旁边，
# 因为 internal/pi/locate.go 是按 exe 目录去找自带 Pi 的。
#
# 用法：
#   powershell -File scripts/fetch-pi-agent.ps1                 # 安装/更新到默认版本
#   powershell -File scripts/fetch-pi-agent.ps1 -Version 1.0.2  # 指定版本
param(
    [string]$Version = '',
    [string]$TargetDir = ''
)

# param 必须是第一个语句（注释除外），所以这行放在它后面。
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
if (-not $TargetDir) { $TargetDir = Join-Path $root 'bin\pi-agent' }

New-Item -ItemType Directory -Force $TargetDir | Out-Null
$pkgJson = Join-Path $TargetDir 'package.json'
if (-not (Test-Path $pkgJson)) {
    '{ "name": "pi-agent-runtime", "private": true, "description": "应用自带的 Pi Agent 运行时（由 scripts/fetch-pi-agent.ps1 生成）" }' |
        Set-Content -Path $pkgJson -Encoding UTF8
}

$spec = '@earendil-works/pi-coding-agent'
if ($Version) { $spec = "$spec@$Version" }

Write-Host "安装 $spec 到 $TargetDir ..."
& npm.cmd install --prefix $TargetDir --no-audit --no-fund $spec
if ($LASTEXITCODE -ne 0) { throw "npm install 失败（exit $LASTEXITCODE）" }

# 校验：入口真的在，且能读出版本
$moduleDir = Join-Path $TargetDir 'node_modules\@earendil-works\pi-coding-agent'
$manifest = Join-Path $moduleDir 'package.json'
if (-not (Test-Path $manifest)) { throw "安装后找不到 $manifest" }
$pkg = Get-Content $manifest -Raw | ConvertFrom-Json
$cli = Join-Path $moduleDir $pkg.bin.pi
if (-not (Test-Path $cli)) { throw "入口不存在: $cli" }

$size = (Get-ChildItem $TargetDir -Recurse -File | Measure-Object -Property Length -Sum).Sum
Write-Host ("完成：Pi {0} → {1}（{2:N0} MB）" -f $pkg.version, $cli, ($size / 1MB))
Write-Host '应用启动时会优先使用这份自带 Pi（见 internal/pi/locate.go 的解析顺序）。'
