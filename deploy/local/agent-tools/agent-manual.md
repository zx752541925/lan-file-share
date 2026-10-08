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

## 2. 操作界面：先控件树，再 OCR，最后才坐标

**Windows 界面上要点击/输入任何东西，都按这个顺序做，不要上来就猜坐标：**

```bash
PS=/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe
GUI="$(wslpath -w ~/.codex/win-gui-tools)"      # 公共 GUI 工具包

# ① 有哪些窗口？
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\list-windows.ps1"

# ② 目标窗口里有哪些控件？（按钮的名字、AutomationId、坐标都在这）
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\dump-controls.ps1" -Window WeGame -Depth 4

# ③ 按名字点击（先 DryRun 确认要点哪个，再真点）
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\click-control.ps1" -Window WeGame -Name "登录" -DryRun
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\click-control.ps1" -Window WeGame -Name "登录"

# ④ 控件树里没有按钮（CEF/游戏/自绘界面，例如 WeGame 只暴露一个 Chrome Legacy Window）
#    改用 OCR 找文字，再点它的坐标：
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\find-text.ps1" -Text "登录"
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\click-text.ps1" -Text "登录" -Activate WeGame -DryRun
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\click-text.ps1" -Text "登录" -Activate WeGame

# ⑤ 实在找不到：截图发给人看，问清楚再动，不要连试几十次
```

截图（DPI 感知、整屏）：

```bash
"$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\screenshot.ps1" -Out 'C:\Users\Public\shot.png'
```

输入文本：`type-text.ps1 -Window WeGame -Text "hello"`；坐标兜底：`click-point.ps1 -X 640 -Y 480`。

**这些工具的用法细节看 `~/.codex/win-gui-tools/README.md`。**

三条重要提醒（都是实测踩出来的）：

- **你（模型）看不了图片**：`view_image` 之类不可用。要"看"界面就用
  `find-text.ps1 -All`（把整屏 OCR 成文字）或 `find-text.ps1 -Text "关键词"`。
- **目标窗口被别的窗口挡住时**，OCR 读到的会是压在上面的那个窗口。先置前：
  `click-text.ps1 -Text "..." -Activate <窗口名>`，或先把遮挡的窗口最小化。
  工具找不到文字时会自动把"当前屏幕识别到的文字"列出来，据此判断是谁在最前面。
  置前结果工具会**如实报告**（校验过前后台窗口），报告失败就别硬点。
- **点不动 ≠ 坐标错，先查权限**：如果坐标确认没错（`dump-controls` 或 OCR 给的），
  但点击毫无反应，多半是对方**以管理员运行**，普通进程的鼠标注入被 Windows 拦掉（UIPI）。
  `dump-controls.ps1` 现在会打印目标进程权限；是"管理员"就改用：
  ```bash
  "$PS" -NoProfile -ExecutionPolicy Bypass -File "$GUI\\click-elevated.ps1" -Text "登录" -Activate WeGame
  ```
  它会用提权子进程去点（本机 UAC 策略是"不提示直接提升"，不会弹窗）。

## 2.1 其他常用内置能力（先按第 1 节查，再用）

| 要做的事 | 内置办法 |
| --- | --- |
| 看进程 | cmd `tasklist`；PowerShell `Get-Process` |
| 结束进程 | cmd `taskkill /IM <名字>.exe /F`；PowerShell `Stop-Process -Name <名字> -Force` |
| 启动程序 | `Start-Process <exe>`；`explorer.exe <路径或网址>` |
| 锁屏 | `rundll32.exe user32.dll,LockWorkStation` |
| 剪贴板 | PowerShell `Get-Clipboard` / `Set-Clipboard` |
| 文件操作 | PowerShell `Get-ChildItem` / `Copy-Item` / `Remove-Item`；cmd `dir` / `copy` / `del` |

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
