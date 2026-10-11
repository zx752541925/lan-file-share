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
| `gui_open` | `path`、`restart?` | 打开程序 / 网址。窗口缩在托盘时，它**先用程序自己的单实例交接把窗口叫出来**（再跑一次 exe，不杀进程，通常 1 秒可见）；**只有叫不出来才用 `restart=true`**（强杀重启，会杀掉该程序所有进程）。启动后确认窗口真的出现 |
| `gui_click` | `text`（界面文字）或 `name`（控件名）或 `x`/`y`（坐标）、`window?`、`dry_run?` | 点击。内部自动处理：找窗口 → 查权限 → 置前 → 控件树 → 找不到就**只截目标窗口那块**做 OCR → 需要时用提权进程点；**点完会自动校验**并给出 `verify` 结论。`window` 必须能唯一定位（精确标题或进程名），模糊命中会被直接拒绝并列出候选 |
| `gui_type` | `text`、`window?` | 往窗口输入文本 |
| `gui_read` | — | 读屏：把屏幕 OCR 成文字（**你无法看图片，用这个了解界面**） |
| `gui_windows` | `process?` | 列出窗口**及状态**：可见 / 最小化 / 隐藏（托盘）。找窗口、判断程序有没有窗口，先用它 |
| `gui_shot` | `save_to?` | 截图，返回文件路径（要发给人看就交给发文件工具） |

**开程序不要去找路径**：`gui_open` 的 `path` 可以直接写**程序名或显示名**（`wegame`、`微信`、`notepad`）。
它按 Windows 自己的方式解析：PATH → App Paths 注册表 → 卸载注册表 DisplayName →
开始菜单/桌面快捷方式 → Get-StartApps。返回里会告诉你 `source`（从哪儿解析到的）。
**不要用 shell 的 find/ls 满盘找 exe**（实测一次 30 秒），也**不要**把路径写在记忆里 ——
程序升级换目录后记忆就是错的，注册表不会错。

**标准流程**（照这个顺序，不要跳步）：
1. 先 `gui_read()`（或 `gui_click(dry_run=true)`）**确认现状**：界面上有什么、目标窗口在不在最前面
2. 再 `gui_click` 正式点击；输入用 `gui_type`；开程序用 `gui_open`
3. 看返回的逐步 JSON：
   - `verify` 三档：`changed`（明显变化，多半生效）/ `minor_change`（只有小变化，可能只是选中态，不确定）/ `unchanged`（没变化 → 可能点空了、按钮无响应、或权限不够）
   - 找不到文字时，返回里会**列出窗口里的文字及坐标**（如 `'三角洲行动'@400,738`）。OCR 会把中文读错
     （实测"三角洲"→"三甬洲"、"商店"→"囱商店"），所以：先换页面/滚动确认目标真在画面上；
     确实看到目标但文字被认错，就用相近候选的坐标走 `gui_click(x=…, y=…)`
   - 屏幕上有**多处相同文字**时会给 `text_alternatives`（候选 + 实际选中了哪个），必要时用 `gui_click(x=…, y=…)` 指定
   - `activate=already_foreground` 表示窗口本来就在最前面：不要再用别的方式去"置前"，某些启动器（WeGame）被置前会把窗口缩回托盘
   - `result=failed` 就看 **`at_step`（卡在哪一步）** 和 **`suggestion`**，照建议来
4. 同一个动作**最多试 2 次**；还是不行就停下来，把 `gui_shot` 的截图发给人并说明卡在哪 —— 不要连试几十次

**禁止事项**：

- **不要**用 shell 手写 PowerShell / 内联 C# 去点界面（那是没有工具时的老办法，慢且容易走偏）
- **不要**去读 `~/.codex/win-gui-tools/` 里的脚本源码（那是给人看的，不是给你读的）
- **不要**盲目猜坐标点击；坐标应该来自 `gui_read` 的输出
- **不要**自己写 PowerShell 去枚举窗口、结束进程、或操作窗口状态 —— 用 `gui_windows` / `gui_open -restart`。
  原因：Windows 的 UIPI 会拦掉普通权限进程对**管理员程序**的一切窗口操作（实测 `ShowWindow`/`taskkill` 都返回 False 或拒绝访问），自己写脚本只会白折腾
- **同一个目标连续失败 2 次就停下**，用 `gui_shot` 截图 + 一句话说明卡在哪，让人判断

> 底层实现：`win-gui-tools/gui.ps1` 是一条确定性流水线，`win-gui-mcp` 把它暴露成上面的工具。

## 技能库（直跑，不经过你）

同一个任务做成功之后，服务会把这串工具调用自动固化成一条技能，存在你 CODEX_HOME 的
`skills/macro-*/` 下（含 `macro.json` 和一份 `SKILL.md`）。**下次同类任务由服务直接串行执行，
不经过你**（3-5 秒完成）。所以你只要第一次把它做对、做得干净：

- 步骤会被自动记录成技能：`gui_open` / `gui_click` / `gui_type` / `gui_shot`，以及**真正干活的 shell 命令**
  （`ls` / `cat` / `find` / `grep` 这类"只看一眼"的命令不会记，不用刻意回避）
- 别把 `dry_run` 当正式步骤（宏会自动忽略它），正式点击要真的生效
- 直跑时任一步不符合预期（工具报失败、点击后界面没变化）会自动放弃并交回给你重新决策
- 自己写技能：如果一件事以后一定会重复，而你用的是 shell 命令，用 `skill-creator` 写成技能，脚本放技能目录
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

例：截屏并发出来 = **先 `gui_shot` 截图，再用 `send-file.sh` 发**（这是唯一推荐写法）：

```
gui_shot(save_to="C:\Users\Public\shot-<当前时间戳>.png")   → 返回 path
```

```bash
~/projects/局域网文件传输/deploy/local/agent-tools/send-file.sh "$(wslpath -u 'C:\Users\Public\shot-<时间戳>.png')" "截图"
```

两条硬性要求（都踩过坑）：

1. **文件名带时间戳**，别每次都用同一个 `shot.png`：截图失败时旧文件还在原地，
   `send-file.sh` 会把它当成新图发出去，用户看到的就是"过时的截屏"。
   `send-file.sh` 现在默认拒发超过 120 秒的文件（确实要发旧文件加 `-f`）。
2. **不要用 `$(wslpath -w ~/projects/局域网文件传输/...)` 这种自己拼的 UNC 路径**去调
   PowerShell：路径里的中文经 WSL→Windows 传参会变成乱码，脚本直接找不到（实测）。
   要跑脚本就用 `~/.codex/win-gui-tools/` 下的副本（纯 ASCII 路径），或者直接用上面的 MCP 工具。

## 4. 行为准则

- 默认操作 Windows；一步做完，不要"先试试这个再试试那个"
- 不确定 Windows 侧怎么做 → 按第 1 节先查，再执行；实在查不到就直说
- 需要项目背景 → 读 `README.md`、`docs/`；**不要翻项目源码探索**（除非对方要求你改代码）
- 做完只回一句话结果，不复述过程
