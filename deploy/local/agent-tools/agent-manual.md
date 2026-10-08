# 操作手册（每次任务都会给你，按这里的思路做，不要凭猜）

## 0. 先搞清楚你的环境

- 你运行在 **WSL（Linux）** 里，工作目录是 `data/sessions`
- **默认操作对象是本机 Windows**；只有对方明确说 WSL / Linux / Ubuntu 时才操作 Linux 侧
- 调 Windows 程序就走 `/mnt/c` 下的 exe，统一用这条路：

```bash
PS=/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe
"$PS" -NoProfile -Command "你的 PowerShell 命令"
```

- 路径换算（经常要用）：

```bash
wslpath -w /mnt/c/Users/Public/a.png     # → C:\Users\Public\a.png（给 Windows 程序用）
wslpath -u 'C:\Users\Public'             # → /mnt/c/Users/Public（给 Linux 侧用）
```

## 1. 怎么知道 Windows 上有什么工具（不要猜，先查）

```bash
# 1) 这个命令/程序存在吗？在哪？
"$PS" -NoProfile -Command "Get-Command <名字> -ErrorAction SilentlyContinue | Select-Object Name,Source"
/mnt/c/Windows/System32/where.exe <名字>

# 2) 它怎么用？看官方示例
"$PS" -NoProfile -Command "Get-Help <命令> -Examples"

# 3) 系统自带哪些 exe？
ls /mnt/c/Windows/System32/*.exe | head -50
```

**规则：先查（`Get-Command` / `where.exe`）、查到就用最直白的那条命令。**
失败时换一条**明确**的写法（用 `Get-Help` 查清楚再写），不要连续试五种写法。

## 2. Windows 常用内置能力（按需取用，用前先按第 1 节确认）

| 要做的事 | 内置办法 |
| --- | --- |
| 截屏 | 本项目脚本 `screenshot.ps1`（DPI 感知、整屏、不裁切）；系统自带：`SnippingTool.exe`、`explorer.exe ms-screenclip:` |
| 看进程 | cmd `tasklist`；PowerShell `Get-Process` |
| 结束进程 | cmd `taskkill /IM <名字>.exe /F`；PowerShell `Stop-Process -Name <名字> -Force` |
| 启动程序 | `Start-Process <exe>`；`explorer.exe <路径或网址>`；`cmd /c start "" <exe>` |
| 锁屏 | `rundll32.exe user32.dll,LockWorkStation` |
| 剪贴板 | PowerShell `Get-Clipboard` / `Set-Clipboard`；cmd 侧 `clip.exe` |
| 文件操作 | PowerShell `Get-ChildItem` / `Copy-Item` / `Remove-Item`；cmd `dir` / `copy` / `del` |
| 音量/媒体 | `nircmd` 等第三方工具**默认没有**，先按第 1 节查，不存在就如实说 |

## 3. 把结果送回聊天室（本项目特有）

截图、生成的文件要让聊天里的人看到，就用这个（它会走 lanfile 的上传接口，自动变成一条文件消息）：

```bash
~/projects/局域网文件传输/deploy/local/agent-tools/send-file.sh <文件路径> [说明文字]
```

例：截屏并发出来 =

```bash
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$(wslpath -w ~/projects/局域网文件传输/deploy/local/agent-tools/screenshot.ps1)" -Out 'C:\Users\Public\shot.png'
~/projects/局域网文件传输/deploy/local/agent-tools/send-file.sh /mnt/c/Users/Public/shot.png "截图"
```

## 4. 行为准则

- 默认操作 Windows；一步做完，不要"先试试这个再试试那个"
- 不确定 Windows 侧怎么做 → 按第 1 节先查，再执行；实在查不到就直说
- 需要项目背景 → 读 `README.md`、`docs/`；**不要翻项目源码探索**（除非对方要求你改代码）
- 做完只回一句话结果，不复述过程
