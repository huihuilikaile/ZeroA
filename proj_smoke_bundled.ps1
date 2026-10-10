$ErrorActionPreference = 'Stop'
function Invoke-Cdp {
  param([string]$WsUrl, [string]$Expression, [int]$Id, [int]$WaitSec = 40)
  $socket = New-Object System.Net.WebSockets.ClientWebSocket
  $null = $socket.ConnectAsync([Uri]$WsUrl, [Threading.CancellationToken]::None).Wait(8000)
  $payload = @{ id = $Id; method = 'Runtime.evaluate'; params = @{ expression = $Expression; returnByValue = $true; awaitPromise = $true } } | ConvertTo-Json -Depth 6 -Compress
  $bytes = [Text.Encoding]::UTF8.GetBytes($payload)
  $null = $socket.SendAsync([ArraySegment[byte]]::new($bytes), [System.Net.WebSockets.WebSocketMessageType]::Text, $true, [Threading.CancellationToken]::None).Wait(8000)
  $buffer = New-Object byte[] 262144
  $deadline = (Get-Date).AddSeconds($WaitSec + 25)
  while ((Get-Date) -lt $deadline) {
    $acc = New-Object System.Text.StringBuilder
    do { $task = $socket.ReceiveAsync([ArraySegment[byte]]::new($buffer), [Threading.CancellationToken]::None); if (-not $task.Wait($WaitSec * 1000)) { break }; $null = $acc.Append([Text.Encoding]::UTF8.GetString($buffer, 0, $task.Result.Count)) } while (-not $task.Result.EndOfMessage)
    $text = $acc.ToString(); if ($text -match "`"id`":\s*$Id") { $socket.Dispose(); return $text }
  }
  $socket.Dispose(); return ''
}

$smoke = "$env:TEMP\zeroa-bundled"
Remove-Item $smoke -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $smoke | Out-Null
Write-Output '=== 拷贝分发包到临时目录（等价于解压）==='
robocopy 'dist\ZeroA-v0.1.0-win64-blank' "$smoke\ZeroA" /E /NFL /NDL /NJH /NJS /NP | Out-Null
Write-Output "  大小: $([math]::Round((Get-ChildItem "$smoke\ZeroA" -Recurse -File | Measure-Object Length -Sum).Sum/1MB,0)) MB"

# 只在测试环境放一份密钥（跑完连目录一起删；分发包里没有它）
Copy-Item 'bin\.pi\auth.json' "$smoke\ZeroA\.pi\auth.json" -Force
Write-Output '  已临时放入 auth.json（仅测试用）'

Get-Process -Name 'zeroa' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Get-Process -Name 'msedgewebview2' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 3
Start-Process -FilePath "$smoke\ZeroA\zeroa.exe" -WorkingDirectory "$smoke\ZeroA" -Environment @{ PI_GUI_DEBUG_PORT = '9333' }
for ($i = 1; $i -le 12; $i++) { Start-Sleep -Seconds 3; try { $t = (Invoke-RestMethod -Uri 'http://127.0.0.1:9333/json' -TimeoutSec 4 | Where-Object { $_.type -eq 'page' })[0]; break } catch {} }
"启动: $(if ($t) { '成功' } else { '失败' })"
Start-Sleep -Seconds 10
$w = $t.webSocketDebuggerUrl

Write-Output '=== ① 工具页：自带 python / ffuf 是否被认成"可用"且路径在包内 ==='
$null = Invoke-Cdp -WsUrl $w -Expression "(()=>{const b=[...document.querySelectorAll('button')].find(x=>(x.textContent||'').trim()==='工具');b?.click();return !!b})()" -Id 16101 -WaitSec 30
Start-Sleep -Seconds 8
$r1 = Invoke-Cdp -WsUrl $w -Expression "JSON.stringify({counts:(document.body.innerText.match(/可用 \d+ · 不可用 \d+ · 共 \d+/)||[''])[0],python:/tools\\\\python\\\\python\.exe/.test(document.body.innerText),ffuf:/tools\\\\bin\\\\ffuf/.test(document.body.innerText),node:/tools\\\\node\\\\node\.exe/.test(document.body.innerText)})" -Id 16102 -WaitSec 30
"  " + ($r1 | ConvertFrom-Json).result.result.value

Write-Output '=== ② 发一条消息，验证 Pi 由自带 node 启动 ==='
$null = Invoke-Cdp -WsUrl $w -Expression "(()=>{const b=[...document.querySelectorAll('button')].find(x=>(x.getAttribute('aria-label')||'').includes('新建会话'));b?.click();return !!b})()" -Id 16103 -WaitSec 30
Start-Sleep -Seconds 4
$js = @'
(()=>{const el=document.querySelector('textarea');if(!el)return 'no-textarea';const setter=Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype,'value').set;setter.call(el,'只回复两个字：收到');el.dispatchEvent(new Event('input',{bubbles:true}));return 'typed'})()
'@
$null = Invoke-Cdp -WsUrl $w -Expression $js -Id 16104 -WaitSec 30
Start-Sleep -Seconds 1
$null = Invoke-Cdp -WsUrl $w -Expression "(()=>{const b=[...document.querySelectorAll('button')].find(x=>(x.textContent||'').trim()==='发送');b?.click();return !!b})()" -Id 16105 -WaitSec 30
for ($i = 0; $i -lt 12; $i++) {
  Start-Sleep -Seconds 5
  $done = Invoke-Cdp -WsUrl $w -Expression "/agent_settled|收到/.test(document.body.innerText)" -Id (16110 + $i) -WaitSec 30
  if (($done | ConvertFrom-Json).result.result.value -eq 'true') { "  回合结束（约 $($i * 5 + 5) 秒）"; break }
}
Write-Output '=== ③ Pi 子进程用的是哪个 node ==='
Get-CimInstance Win32_Process -Filter "Name='node.exe'" -ErrorAction SilentlyContinue | ForEach-Object {
  if ($_.CommandLine -like '*zeroa-bundled*') { "  ✓ 自带 node: $($_.CommandLine.Substring(0,[Math]::Min(120,$_.CommandLine.Length)))" }
}
Write-Output '=== ④ 会话确实跑起来了（有 system 消息与回复）==='
$py = @'
import glob, json, os
files = sorted(glob.glob(os.path.expandvars(r"%TEMP%\zeroa-bundled\ZeroA\.pi\sessions\**\*.jsonl"), recursive=True), key=os.path.getmtime, reverse=True)
print("  会话数:", len(files))
if files:
    with open(files[0], encoding="utf-8", errors="replace") as fh:
        roles = []
        for line in fh:
            try: obj = json.loads(line)
            except Exception: continue
            msg = obj.get("message") or {}
            if msg.get("role"): roles.append(msg["role"])
        print("  消息角色序列:", roles[:8])
'@
$py | Out-File "$env:TEMP\smoke_check.py" -Encoding UTF8
python "$env:TEMP\smoke_check.py"
