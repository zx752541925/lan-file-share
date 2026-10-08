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

## 2. 操作界面：用 gui_* 工具，不要自己写 PowerShell

你有 5 个现成的 GUI 工具（MCP 工具，直接调用）：

| 工具 | 参数 | 用途 |
| --- | --- | --- |
| `gui_open` | `path` | 打开程序 / 网址 |
| `gui_click` | `text`（界面文字）或 `name`（控件名）、`window?`、`dry_run?` | 点击。内部自动处理：找窗口 → 查权限 → 置前 → 控件树 → 找不到就 OCR → 需要时用提权进程点 |
| `gui_type` | `text`、`window?` | 往窗口输入文本 |
| `gui_read` | — | 读屏：把屏幕 OCR 成文字（**你无法看图片，用这个了解界面**） |
| `gui_shot` | `save_to?` | 截图，返回文件路径（要发给人看就交给发文件工具） |

**标准流程**（照这个顺序，不要跳步）：

1. 先 `gui_read()`（或 `gui_click(dry_run=true)`）**确认现状**：界面上有什么、目标窗口在不在最前面
2. 再 `gui_click` 正式点击；输入用 `gui_type`；开程序用 `gui_open`
3. 看返回的逐步 JSON：`result=success` 就完事；`result=failed` 就看 **`at_step`（卡在哪一步）** 和 **`suggestion`（建议怎么做）**，照建议来
4. 同一个动作**最多试 2 次**；还是不行就停下来，把 `gui_shot` 的截图发给人并说明卡在哪 —— 不要连试几十次

**禁止事项**：

- **不要**用 shell 手写 PowerShell / 内联 C# 去点界面（那是没有工具时的老办法，慢且容易走偏）
- **不要**去读 `~/.codex/win-gui-tools/` 里的脚本源码（那是给人看的，不是给你读的）
- **不要**盲目猜坐标点击；坐标应该来自 `gui_read` 的输出

> 底层实现：`win-gui-tools/gui.ps1` 是一条确定性流水线，`win-gui-mcp` 把它暴露成上面的工具。
> 如果你的工具列表里没有 `gui_*`（说明 MCP 没加载），再用 shell 调用
> `powershell.exe -File ~/.codex/win-gui-tools/gui.ps1 -Action ... -Json` 作为兜底。

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
