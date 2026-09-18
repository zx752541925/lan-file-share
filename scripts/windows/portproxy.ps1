# 让手机/平板能访问运行在 WSL2 里的服务（Windows 10/11 通用）
#
# 一次性安装（管理员 PowerShell）：
#   powershell -ExecutionPolicy Bypass -File portproxy.ps1 -Install
#   —— 会建立端口映射 + 防火墙规则，并注册计划任务，之后每次登录自动刷新映射，
#      不用再手动运行。
#
# 卸载：
#   powershell -ExecutionPolicy Bypass -File portproxy.ps1 -Uninstall
#
# 背景：WSL2 默认是 NAT 网络，172.x 地址只有 Windows 自己能看到，手机访问会一直转圈。
# 本脚本在 Windows 侧做端口映射（netsh portproxy），把 Windows 的端口转发到 WSL。

param(
    [int]$Port = 41730,
    [switch]$Install,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
$taskName = 'LAN File Share PortProxy'
$installDir = Join-Path $env:USERPROFILE 'lan-file-share'
$ruleName = "LAN File Share $Port"

function Get-WslAddress {
    $raw = & wsl.exe hostname -I 2>$null
    if (-not $raw) { return $null }
    return ($raw.Trim() -split '\s+')[0]
}

function Sync-Mapping {
    $wslIp = Get-WslAddress
    if (-not $wslIp) {
        Write-Warning '没读到 WSL 地址（WSL 可能没在运行），本次跳过。'
        return
    }

    netsh interface portproxy delete v4tov4 listenport=$Port listenaddress=0.0.0.0 2>$null | Out-Null
    netsh interface portproxy add v4tov4 listenport=$Port listenaddress=0.0.0.0 connectport=$Port connectaddress=$wslIp | Out-Null

    if (-not (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Protocol TCP -LocalPort $Port -Action Allow | Out-Null
    }

    Write-Host "端口映射已建立：Windows 0.0.0.0:$Port  ->  ${wslIp}:$Port" -ForegroundColor Green
}

function Show-LanAddresses {
    Write-Host '手机请访问下面属于当前 Wi-Fi 的地址（一般是以 192.168 或 10 开头那个）：' -ForegroundColor Cyan
    Get-NetIPAddress -AddressFamily IPv4 |
        Where-Object {
            $_.IPAddress -notlike '127.*' -and
            $_.IPAddress -notlike '169.254.*' -and
            $_.InterfaceAlias -notmatch 'WSL|Loopback|vEthernet'
        } |
        Select-Object InterfaceAlias, IPAddress |
        Format-Table -AutoSize
    Write-Host "例：http://192.168.1.5:$Port"
}

if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    netsh interface portproxy delete v4tov4 listenport=$Port listenaddress=0.0.0.0 2>$null | Out-Null
    Remove-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue
    Write-Host '已卸载：端口映射、防火墙规则、计划任务。'
    exit 0
}

if ($Install) {
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    $installedScript = Join-Path $installDir 'portproxy.ps1'
    Copy-Item -Path $PSCommandPath -Destination $installedScript -Force

    $action = New-ScheduledTaskAction -Execute 'powershell.exe' `
        -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$installedScript`" -Port $Port"
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $trigger.Delay = 'PT30S'
    # 每 10 分钟对一次映射，WSL 换了地址也能自动跟上
    $trigger.Repetition = (New-ScheduledTaskTrigger -Once -At (Get-Date) `
        -RepetitionInterval (New-TimeSpan -Minutes 10) `
        -RepetitionDuration (New-TimeSpan -Days 3650)).Repetition
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 5)

    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
        -Settings $settings -RunLevel Highest -Force | Out-Null

    Write-Host "已安装计划任务「$taskName」，登录后会自动刷新端口映射。" -ForegroundColor Green
}

Sync-Mapping
Show-LanAddresses

Write-Host ''
Write-Host "手动重新执行：powershell -ExecutionPolicy Bypass -File `"$installDir\portproxy.ps1`""
Write-Host "查看当前映射：netsh interface portproxy show v4tov4"
