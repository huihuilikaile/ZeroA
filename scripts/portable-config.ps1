param(
    [switch]$Migrate,
    [string]$TargetDir = ''
)

# 便携配置：把 Pi 的配置目录放到「应用同目录」的 .pi 里。
#
#   - 只报告：powershell -File scripts/portable-config.ps1
#   - 迁移（复制，不移动）：powershell -File scripts/portable-config.ps1 -Migrate
#
# 为什么是复制而不是移动：这是用户真实的模型配置与凭据（auth.json / models.json）。
# 原目录保持不动，出问题随时能退回去。
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
if (-not $TargetDir) { $TargetDir = Join-Path $root 'bin\.pi' }
$source = Join-Path $env:USERPROFILE '.pi\agent'

Write-Host "源（Pi 默认配置目录）: $source"
Write-Host "目标（应用同目录）    : $TargetDir"
Write-Host ''

if (-not (Test-Path $source)) { throw "找不到源目录 $source" }

$files = Get-ChildItem $source -Force
Write-Host ("源目录内容（{0} 项）：" -f $files.Count)
$files | ForEach-Object {
    $kind = if ($_.PSIsContainer) { 'dir ' } else { 'file' }
    Write-Host ("  [{0}] {1}" -f $kind, $_.Name)
}

if (-not $Migrate) {
    Write-Host ''
    Write-Host '仅报告。要复制到应用同目录，加 -Migrate。'
    exit 0
}

New-Item -ItemType Directory -Force $TargetDir | Out-Null
# /E 连子目录一起复制（sessions 是按工作目录分的多层结构），/XO 不覆盖更新的文件
& robocopy $source $TargetDir /E /XO /NFL /NDL /NJH /NJS /R:1 /W:1 | Out-Null
# robocopy 的退出码 0-7 都算成功（1=有文件被复制，3=有文件被复制+有额外文件）
if ($LASTEXITCODE -ge 8) { throw "robocopy 失败（exit $LASTEXITCODE）" }

# 明确不复制 npm 依赖目录：那是扩展的安装产物，体积大且可以在目标机器重装
$heavy = Join-Path $TargetDir 'npm'
if (Test-Path $heavy) { Write-Host "提示：$heavy 也一起复制了（用户级 Pi 扩展）；不需要可以删掉" }

$copied = (Get-ChildItem $TargetDir -Recurse -File | Measure-Object -Property Length -Sum)
Write-Host ''
Write-Host ("完成：{0} 个文件，{1:N1} MB → {2}" -f $copied.Count, ($copied.Sum / 1MB), $TargetDir)
Write-Host '应用启动时会优先使用这个目录（见 sessions.go 的 agentDir()）。'
Write-Host '原目录未改动，仍然可以用 PI_CODING_AGENT_DIR 指回它。'
