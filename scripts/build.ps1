# 一条命令完成“从源码到可执行文件”。
#
# 桌面程序走 Wails v3 官方构建（wails3 build → Taskfile）：
#   它会依次做 go mod tidy → 生成图标 → npm install → 生成绑定 → 前端构建 →
#   生成 .syso（图标 + manifest）→ go build -ldflags "-H windowsgui"。
# 所以这里不再手写 go build 桌面程序，只保留 CLI 的构建。
#
# 如果只想重跑前端或只想生成绑定，可以直接用：
#   wails3 task common:generate:bindings
#   wails3 task common:build:frontend
$ErrorActionPreference = 'Stop'   # 任何一步失败立刻停下，不带着半成品继续
$projectRoot = Split-Path -Parent $PSScriptRoot   # 脚本在 scripts/ 里，项目根在上一层
Push-Location $projectRoot
try {
    if (-not (Get-Command wails3 -ErrorAction SilentlyContinue)) {
        throw '未找到 wails3，请先运行：go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.17'
    }
    # 绑定里的数字方法 ID 由 CLI 版本决定，版本不符只会在界面里运行时才报错，
    # 所以这里先卡住（bindings_test.go 只比对方法名，管不了 ID）。
    $expectedWails = 'v3.0.0-beta.17'
    # `wails3 version` 把版本号写到 stderr，而 PowerShell 5.1 在 Stop 模式下
    # 会把原生命令的 stderr 当成终止错误；交给 cmd.exe 合并两个流再取最稳。
    $wailsVersion = (cmd /c 'wails3 version 2>&1' | Out-String).Trim()
    if ($wailsVersion -notmatch [regex]::Escape($expectedWails)) {
        throw "需要 wails3 $expectedWails，当前 $wailsVersion"
    }

    # 桌面程序：wails3 build 负责绑定、前端、图标与可执行文件
    & wails3 build
    if ($LASTEXITCODE -ne 0) { throw 'wails3 build failed' }

    # 应用自带的 Pi 运行时：缺失就拉一份（已存在则不动，避免每次构建都覆盖用户装的版本）
    $localAgent = Join-Path $projectRoot 'bin\pi-agent\node_modules\@earendil-works\pi-coding-agent'
    if (-not (Test-Path $localAgent)) {
        Write-Host '应用同目录没有 Pi 运行时，正在拉取（scripts/fetch-pi-agent.ps1）…'
        & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'fetch-pi-agent.ps1')
        if ($LASTEXITCODE -ne 0) { throw 'fetch-pi-agent.ps1 failed' }
    } else {
        Write-Host 'Pi 运行时已存在（bin/pi-agent），跳过拉取'
    }

    # 命令行学习入口（不需要 GUI 子系统，所以单独构建）
    New-Item -ItemType Directory -Force 'bin' | Out-Null
    & go build -o bin/rpc-demo.exe ./cmd/rpc-demo
    if ($LASTEXITCODE -ne 0) { throw 'CLI build failed' }

    Write-Host 'Ready: bin/zeroa.exe (wails3 build) and bin/rpc-demo.exe (go build)'
} finally {
    Pop-Location   # 成功或失败都回到原来的工作目录
}
