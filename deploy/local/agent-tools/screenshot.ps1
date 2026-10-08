# 整屏截图（DPI 感知，不会只截到一部分）
#
# 用法：powershell.exe -NoProfile -ExecutionPolicy Bypass -File screenshot.ps1 -Out C:\path\shot.png
# 输出：把 PNG 写到 -Out 指定的路径，同时在 stdout 打印一行 "OK <路径> <宽>x<高>"

param(
    [Parameter(Mandatory = $true)][string]$Out
)

$ErrorActionPreference = 'Stop'

# 让进程 DPI 感知：否则在缩放 125%/150% 的屏幕上只会截到一部分画面
Add-Type @"
using System.Runtime.InteropServices;
public class DpiHelper {
    [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
}
"@
[void][DpiHelper]::SetProcessDPIAware()

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$bounds = [System.Windows.Forms.SystemInformation]::VirtualScreen
$bitmap = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$graphics.CopyFromScreen($bounds.Left, $bounds.Top, 0, 0, $bitmap.Size)

$dir = Split-Path -Parent $Out
if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }

$bitmap.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
$graphics.Dispose()
$bitmap.Dispose()

Write-Output ("OK {0} {1}x{2}" -f $Out, $bounds.Width, $bounds.Height)
