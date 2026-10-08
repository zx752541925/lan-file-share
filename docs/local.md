# 本地版说明（本机 WSL + Windows）

> 和服务器版共用同一份代码，差别只在启动参数与部署资产：
> 服务器版见 [server.md](server.md)，部署资产在 `deploy/server/`。
> 最后更新：2026-10-08

## 1. 组成

| 组件 | 位置 | 说明 |
| --- | --- | --- |
| 服务本体 | `bin/lanfile-server` | `make build` 产出；用 systemd 用户服务常驻 |
| systemd 单元 | `~/.config/systemd/user/lanfile.service`（模板见 `deploy/local/lanfile.service`） | **刻意不 enable**：双击启动器才启动 |
| 双击启动器 | `deploy/local/winlauncher/`（源码）→ `dist/lanfile-start.exe` → 拷到 `D:\project\lan-file-share\` | 唤醒 WSL → 起 shim 与服务 → 弹窗显示主机地址 → 打开浏览器 |
| 端口映射脚本 | `deploy/local/portproxy.ps1` | Windows 10（NAT 模式）下把 Windows:41730 映射进 WSL，一次安装、计划任务自动刷新 |
| 推理摘要中转 | `~/.codex/deepseek-shim/`（源码副本在 `codex-config` 仓库） | 让控制台能显示 Codex 的思考过程 |
| 控制台页面 | `src/web/local/agent.html`，访问 `<base>/agent` | 仅主机可开：看任务、看命令与思考、终止任务 |

## 2. 日常使用

```powershell
# Windows：双击 D:\project\lan-file-share\lanfile-start.exe
#   → 弹窗显示 http://<局域网IP>:41730/?host=<一次性密钥>，并自动打开浏览器
```

启动器做的事：`wsl -d Ubuntu -u xu -e true` → `systemctl --user start deepseek-shim` →
`systemctl --user restart lanfile` → 读日志里的主机链接 → 取 Windows 局域网 IP → 弹窗 + 打开浏览器。

**用局域网 IP 打开**很关键：服务端按请求 Host 推导邀请链接地址，用 localhost 打开会生成手机打不开的链接。

## 3. 启动参数（本地常用）

```bash
bin/lanfile-server \
  -agent \                      # 开启聊天室 Codex
  -open=false \                 # 后台运行，不自动开浏览器
  -agent-cwd sessions \         # Codex 工作目录，相对数据目录（= data/sessions）
  -agent-timeout 300 \          # 单次执行超时（秒）
  -agent-base-url http://127.0.0.1:41780/   # 经 shim 才能显示思考
```

其他参数（触发词、昵称、邀请有效期等）见 README 的参数表。

## 4. 聊天室 Codex

- 只有**主机**和**高级邀请**访客能触发；普通访客发 `@codex` 不会有反应
- 输入框上方有 `@Codex` 快捷按钮（仅上述两类人可见），点一下就把触发词填进输入框
- 完全授权（`danger-full-access`）：能读写文件、执行命令，默认工作目录 `data/sessions`
- 每次执行都是全新会话，上下文取聊天记录最近 20 条；串行执行，队列上限 3
- 执行过程在**控制台**（`<base>/agent`）实时可见，可随时终止

## 5. 依赖的服务（用户级）

```bash
systemctl --user status lanfile          # 服务（由启动器拉起）
systemctl --user status deepseek-shim    # 推理摘要中转（已 enable，随 WSL 启动）
sudo loginctl enable-linger xu           # 让用户级服务在无登录会话时也活着（已设置）
```

## 6. 常见操作

| 需求 | 做法 |
| --- | --- |
| 重新编译并重启服务 | `make build && systemctl --user restart lanfile` |
| 重新生成启动器 exe | `make build-launcher && cp dist/lanfile-start.exe /mnt/d/project/lan-file-share/` |
| 手机连不上 | 管理员 PowerShell 跑一次 `deploy/local/portproxy.ps1 -Install`；验证 `netsh interface portproxy show v4tov4` |
| 看服务日志 | `journalctl --user -u lanfile -f` |
| 拿新的主机链接 | 页面「链接」面板点「重新生成」，或重启服务后 `journalctl --user -u lanfile \| grep 主机入口` |
| 控制台看不到思考 | 确认 `deepseek-shim` 在跑、且服务带 `-agent-base-url http://127.0.0.1:41780/` |
