# 局域网文件传输

PC 与手机浏览器打开同一地址，即可聊天、互传文件。服务只监听局域网，不依赖外网。

## 版本说明

同一个仓库里有两种形态，**代码同源**，差异只在「访问准入」和「会话语义」，不是两套代码：

| 形态 | 位置 | 形态与行为 | 状态 |
| --- | --- | --- | --- |
| 本地版 | tag `v1.0-local` | Windows 单文件 exe（`make build-windows`），局域网直连、无鉴权、**每次启动 = 一个新会话** | **已归档**，除修 bug 外不再演进 |
| 服务器版 | `main` 分支 | Linux 静态二进制 + systemd 常驻（`make build-linux` + `scripts/deploy.sh`），只监听回环、经 nginx 入口访问；主机用一次性链接换 cookie，访客必须由主机邀请 | 开发中 |

### 从归档点继续开发本地版

本地版归档在 tag `v1.0-local`，需要改它时从该 tag 拉分支，不要直接改 `main`：

```bash
git fetch --tags
git switch -c local-fix v1.0-local   # 基于归档点拉分支
make build-windows                   # 重新产出 dist/lanfile-server.exe
```

### 服务器版部署

```bash
make build-linux     # 静态编译（不依赖 glibc），产物 bin/lanfile-server-linux
scripts/deploy.sh    # 上传 → 安装 systemd 单元 → 重启 → 回显状态与日志
```

服务器上以专用用户 `lanfile` 运行，只监听 `127.0.0.1:41730`，数据在 `/var/lib/lanfile`（与二进制分离，重装不丢会话）。细节见 [deploy/lanfile.service](deploy/lanfile.service) 与 [scripts/deploy.sh](scripts/deploy.sh)。

## 主要文件

后端（Go）：

| 文件 | 作用 |
| --- | --- |
| [src/server/main.go](src/server/main.go) | 程序入口：配置（命令行参数 / `LANFILE_*` 环境变量）、路由注册、静态页面与资源版本号、启动横幅 |
| [src/server/api.go](src/server/api.go) | HTTP 接口（上传 / 下载 / 删除 / 定位）与 WebSocket 消息收发、广播 |
| [src/server/session.go](src/server/session.go) | 会话与存储：会话目录懒创建、消息与文件元数据、生成 `文件清单.txt` |
| [src/server/upload.go](src/server/upload.go) | 大文件分片上传与断点续传（init / status / chunk / complete）|
| [src/server/hub.go](src/server/hub.go) | WebSocket 连接管理：上下线、心跳、广播、在线设备数 |
| [src/server/auth.go](src/server/auth.go) | 权限判定（主机 / 其他设备），将来上公网时的鉴权扩展位 |
| [src/server/names.go](src/server/names.go) | 访客昵称生成（词 + 数字，同时在线不重号）|
| [webassets.go](webassets.go) | 把 `src/web` 编译进二进制，实现单文件分发 |

前端（原生 HTML/CSS/JS，无构建）：

| 文件 | 作用 |
| --- | --- |
| [src/web/index.html](src/web/index.html) | 页面结构：顶栏、消息区、输入区、文件抽屉、二维码/会话/确认浮窗 |
| [src/web/app.js](src/web/app.js) | 前端逻辑：连接与渲染、上传（含分片续传）、删除、二维码、抽屉与折叠 |
| [src/web/styles.css](src/web/styles.css) | 移动优先样式：手机单栏聊天，PC 左栏文件 + 右栏聊天 |
| [src/web/vendor/qrcode.js](src/web/vendor/qrcode.js) | 二维码生成库（qrcode-generator，MIT 协议）|

构建与脚本：

| 文件 | 作用 |
| --- | --- |
| [Makefile](Makefile) | 构建入口：`make run` / `make build` / `make build-windows` |
| [scripts/windows/portproxy.ps1](scripts/windows/portproxy.ps1) | Windows 10 下把端口映射进 WSL（用 exe 时不需要）|

文档内快速跳转：[运行](#运行) · [Windows 单文件版](#windows-单文件版) · [接口](#接口) · [会话与主机](#会话与主机) · [文件存储](#文件存储) · [断点续传](#断点续传) · [手机打不开？](#手机打不开) · [环境变量](#环境变量) · [目录](#目录)

## 技术栈

- 后端：Go（标准库 `net/http` + `gorilla/websocket`），单文件二进制，无运行时依赖
- 前端：原生 HTML/CSS/JS，无构建步骤，手机/PC 同一份代码
- 存储：一个会话一个文件夹 `data/sessions/<日期_时间>/`，内含 `session.json` 和 `uploads/`

## 运行

```bash
make run                  # 等价于 go run ./src/server
make build                # 产出 bin/lanfile-server
make build-windows        # 产出 dist/lanfile-server.exe（前端已嵌入，单文件）
```

启动后终端会打印局域网地址（如 `http://192.168.1.5:41730`），手机连同一 Wi-Fi 打开即可。

默认端口 `41730`，避开 8080/3000/8000 这类常被其他程序占用的端口。

常用参数：

```bash
go run ./src/server -addr :41731 -max-mb 8192 -web ./src/web -data ./data
```

## Windows 单文件版

`make build-windows` 产出的 `dist/lanfile-server.exe` 已经把前端页面编译进去了，
复制到任意 Windows 目录双击即可运行：

- 启动后自动打开浏览器页面（`-open=false` 可关闭）
- 数据写在 exe 同级的 `data/sessions/<日期_时间>/` 里，每次启动一个新会话
- 局域网地址打印在窗口里，手机访问该地址
- 首次运行若弹出防火墙提示，勾选「专用网络」并允许

这个版本跑在 Windows 上，与 WSL 里的版本是同一份源码编译出的两个二进制，各自独立。

## 接口

| 接口 | 说明 |
| --- | --- |
| `GET /ws` | WebSocket：`init`（会话与历史消息）、`message`（新消息广播）、`peers`（在线设备数）、`sessions`/`switch`/`delete`（仅主机）、`deleted`（删除广播） |
| `POST /api/upload` | 表单字段 `file`，返回文件元数据 `{id,name,size,type}` |
| `GET /api/files/{会话ID}/{文件ID}` | 在线预览，支持 Range 断点续传 |
| `GET /api/files/{会话ID}/{文件ID}/download` | 带文件名下载（中文名用 RFC 5987 编码） |
| `GET /api/reveal/{会话ID}/{文件ID}?host=口令` | 仅主机：在系统文件管理器里定位该文件 |
| `POST /api/upload/init` | 大文件分片上传：返回上传 ID 与分片大小（8MB）|
| `GET /api/upload/status?uploadId=` | 查询已收到的分片序号，用于断点续传 |
| `PUT /api/upload/chunk?uploadId=&index=` | 上传单个分片，可重复提交（幂等）|
| `POST /api/upload/complete` | 合并分片为正式文件，建消息并广播 |

## 手机打不开？

项目跑在 WSL2 里时，`172.31.x.x` 是 WSL 内部的 NAT 地址，手机连不上（会一直转圈）。
两种系统层面的办法，任选其一：

**A. WSL 镜像网络模式（仅 Windows 11 22H2 及以上，Windows 10 不支持）**

在 `C:\Users\<用户名>\.wslconfig` 的 `[wsl2]` 段加上：

```ini
networkingMode=mirrored
```

然后在 Windows 执行 `wsl --shutdown` 重启 WSL。之后 WSL 与 Windows 共用 IP，
手机直接访问 `http://<Windows 局域网 IP>:41730` 即可。需要 Windows 11 22H2 及以上。
若仍打不开，管理员 PowerShell 执行：

```powershell
Set-NetFirewallHyperVVMSetting -Name '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -DefaultInboundAction Allow
```

**B. Windows 端口映射（Windows 10 用这个）**

管理员 PowerShell 执行一次：

```powershell
powershell -ExecutionPolicy Bypass -File "\\wsl$\Ubuntu\home\xu\projects\局域网文件传输\scripts\windows\portproxy.ps1" -Install
```

它会建立 `netsh portproxy` 映射、放行防火墙，并注册一个计划任务，
每 10 分钟自动把映射刷新到 WSL 当前地址，之后无需再手动运行。
卸载用同一个脚本加 `-Uninstall`。

## 目录

| 路径 | 职责 |
| --- | --- |
| `src/web/` | 前端静态资源：页面、样式、交互脚本、`vendor/qrcode.js` 二维码库 |
| `src/server/` | Go 后端：入口 `main.go`、连接管理 `hub.go`、接口 `api.go`、会话与存储 `session.go` |
| `data/sessions/<日期_时间>/` | 一个会话一个文件夹：`session.json` 存聊天与文件元数据，`uploads/` 存文件本体 |
| `scripts/windows/portproxy.ps1` | Windows 10 下把端口映射进 WSL（用 exe 时不需要） |
| `webassets.go` | 把 `src/web` 编译进二进制，供单文件分发 |
| `src/server/auth.go` | 权限判定（目前只有"主机"角色），上公网时在这里扩展登录态 |

## 文件存储

- 文件实体存成 `uploads/<文件ID>`（随机 ID，不带后缀），原因：不同设备可能传同名文件、
  防止文件名里的路径注入、避免中文/emoji 在各文件系统的编码差异
- 每个会话目录里会生成一份 `文件清单.txt`，列出「原始文件名 / 大小 / 上传者 / 文件 ID」，
  方便直接在资源管理器里对着找
- 主机不需要下载：文件卡片对主机显示「打开」和「定位」（定位会在资源管理器中选中该文件），
  图片、视频、音频在所有设备上都内联预览，其他设备下载时按原始文件名返回
- 所有文件只存在主机磁盘上，其他设备只在浏览时放进浏览器缓存

## 断点续传

- 大于 8MB 的文件自动走分片上传，小文件仍是一次传完
- 切网、闪断、锁屏、切后台：前端自动重试并按已确认的分片继续，用户无感
- 页面刷新/关闭后：分片留在 `data/tmp/<上传ID>/`（保留 24 小时），
  重新打开页面会提示「有未完成的上传」，重新选择同一个文件即从断点继续
  （浏览器刷新后拿不到原文件对象，这是浏览器限制，只能重新选一次）
- 同一分片可重复提交（幂等），合并前会校验总字节数是否与文件大小一致

## 环境变量

命令行参数优先，其次读环境变量，方便以后放进容器或公网：
`LANFILE_ADDR`、`LANFILE_DATA`、`LANFILE_WEB`、`LANFILE_MAX_MB`。

服务器版还多了几个与准入有关的参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-public-url` | 空 | 生成主机/邀请链接用的对外地址，例如 `http://1.2.3.4:8080`；留空则按请求推导 |
| `-base` | `/` | 访问子路径，例如 `/lanfile/`；用于 nginx 按路径分发多个服务，链接与跳转都会自动带上它 |
| `-trust-loopback` | `true` | 直连且来源是回环地址时直接视为主机（本地开发用）。**经 nginx 等反向代理时必须设为 `false`**，否则所有访客都会被当成主机 |
| `-host-cookie-days` | `30` | 主机 cookie 有效期（天），每次上线滑动续期 |
| `-guest-cookie-days` | `7` | 访客 cookie 有效期（天），每次上线滑动续期 |
| `-invite-ttl-hours` | `24` | 邀请链接未被使用时的有效期（小时） |

## 身份与准入（服务器版）

### 按路径分发多个服务

服务器上用 nginx 把不同服务分到不同路径前缀，只对外开一个端口（当前是 8080）：

```
http://<公网IP>:8080/          → 302 跳到 /lanfile/
http://<公网IP>:8080/lanfile/  → lanfile（后端带 -base /lanfile/ 启动）
http://<公网IP>:8080/<其他>/   → 以后新增的服务
```

配置见 [deploy/nginx/lanfile.conf](deploy/nginx/lanfile.conf)：`location /lanfile/` 原样透传，
后端用 `-base` 自己剥前缀，两边不重复改写路径；`location /` 直接 404，避免未定义的服务被误转发。
前端所有请求都由 `location.pathname` 推导出前缀（`const BASE = location.pathname.replace(/[^/]*$/, '')`），
所以同一份前端在根路径（本地版）和子路径（服务器版）下都能用。

两种角色，两种凭证：

- **主机**：服务启动时随机生成一张一次性链接并打印在日志里（`journalctl -u lanfile | grep host=`），
  点开后换成 HMAC 签名的 HttpOnly cookie（30 天滑动续期）。页面「链接」面板可以重新生成新链接，
  旧的立即作废；也可以查看在线设备、按备注生成邀请。
- **访客**：必须由主机邀请。邀请链接**一人一码、24 小时内有效**，谁先打开就绑定谁，
  转发给别人打不开；点开后换成 7 天滑动续期的 cookie。主机撤销该邀请后，
  对应设备**下一次请求就会被挡在门外**，不用等 cookie 过期。

设计取舍：

- 邀请码、主机密钥、最近上线的设备列表**都只在内存**，服务重启即清空；
  签名密钥 `secret.key` 落盘（0600），所以重启后已发出的 cookie 仍然有效，主客都不掉线
- 链接里的一次性密钥在兑换后被 302 抹掉，不会留在浏览器历史和 Referer 里
- 访客只按 cookie 判定，不依赖 IP：换 Wi-Fi、切 4G 都不会掉线

## 会话与主机

- 每次启动服务就是一个新会话，文件夹名 = 启动时间（如 `2026-09-18_20-45-12`）
- **空会话不落盘**：启动后没发消息、没传文件就退出，不会留下空文件夹
- 会话目录懒创建，第一条消息或第一个文件上传时才建
- 继续历史会话只往原文件夹追加，文件夹名永远不变；列表里显示"最后对话"时间
- 会话列表按最后对话时间倒序，每个会话最多保留最近 500 条消息
- **主机 = 启动服务的那台机器**：只有主机能看会话列表、切换会话，切换后所有设备一起进入
- 主机身份由启动时打印的**一次性链接** `?host=<随机密钥>` 认定：点一次即换成 cookie，
  密钥立刻作废（只在内存里，重启生成新的）。换设备或清了浏览器缓存时，
  在页面「链接」面板里重新生成一张。手机扫码拿到的是不带密钥的地址
- 页面顶部「扫码加入」按钮生成当前局域网地址的二维码，手机扫码即进入当前会话
- **删除文件**：只有主机能删，删除会连磁盘实体一起删掉，聊天里显示为「已删除」占位
- **上传即发消息**：上传接口直接带上文字与发送者信息，服务端存完文件立刻建消息并广播，
  不依赖 WebSocket 是否在线，手机断线时上传也不会丢
- **流式上传**：`MultipartReader` 边收边写，不经过内存缓冲与系统临时文件，省一次磁盘写入
- **文件列表**：以服务端下发的文件清单为准（不是从聊天消息推导），孤儿文件同样可见
- **删除会话**：只有主机能删，删除整个会话目录（含文件）。删掉当前会话后自动开始
  一个新会话并广播给所有设备；删除确认框叠在会话列表之上，删完列表保持打开
- **设备身份**：浏览器生成一次设备 ID 长期保存，刷新后自己的消息仍在右侧；
  昵称首次分配后固定不变（主机为「主机」，访客为「海豚-27」这类词+数字，
  数字在同时在线设备间不重复），手动改名后永久生效
- **文件类型按内容判定**：上传后读文件头识别真实类型，避免"扩展名是 .jpg 其实是 PDF"
  导致预览失败；只有真实图片才内联预览，其余（含 PDF）显示为文件卡片
- **布局**：手机端是单栏聊天（DeepSeek 风格输入框），文件列表为默认收起的抽屉；
  PC 端左侧文件面板默认折叠成窄条，点击展开，状态会记住
