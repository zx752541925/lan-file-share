# 云服务器配置清单

> 用途：换新对话/新同事接手时，读这一份就能掌握服务器全貌。
> 服务器本地也有一份完全相同的副本：`/root/SERVER.md`。
> 最后更新：2026-09-21

## 1. 主机与系统

| 项 | 值 |
| --- | --- |
| 主机名 | `iZ7xvbznarcsi4y33fbv9rZ` |
| 公网 IP | `8.138.247.252`（阿里云 ECS） |
| 系统 | Alibaba Cloud Linux 3.2104 U13.4（OpenAnolis Edition，RHEL8 系） |
| 内核 | `5.10.134-19.8.al8.x86_64` |
| 规格 | 2 核 / 1870 MB 内存 / 40 GB 磁盘（已用约 5.8 G，16%） |
| SSH | `root@8.138.247.252:22`，仅密钥登录（密钥 `WSL-ssh.pem`，RSA 2048） |
| 时间同步 / 监控 | 阿里云自带 agent：aegis（云盾）、cloudmonitor、hbrclient、loongcollectord |

## 2. 登录方式

本机 WSL 已配置 ssh 别名（`~/.ssh/config`）：

```
Host myecs
    HostName 8.138.247.252
    User root
    Port 22
    IdentityFile ~/.ssh/wsl-ssh.pem
    ServerAliveInterval 30
```

所以任意命令都可以写成 `ssh myecs '命令'`。Windows 侧 VS Code Remote-SSH 用的是
`C:\Users\75254\.ssh\config` 里的 `Host MyECS`（同一台机器、同一把密钥）。

## 3. 访问入口

```
手机/浏览器
  └── http://8.138.247.252:8080/            ← 唯一对外端口（阿里云安全组需放行 TCP 8080）
        ├── /            → 302 跳到 /lanfile/
        ├── /lanfile/    → 反代到 127.0.0.1:41730（lanfile 服务）
        └── 其他路径      → 404（预留：以后新增服务各占一个前缀）
```

**主机身份**：服务每次启动随机生成一张一次性链接，打印在启动日志里，取法：

```bash
ssh myecs 'journalctl -u lanfile | grep 主机入口 | tail -1'
```

链接形如 `http://8.138.247.252:8080/lanfile/?host=<32位十六进制>`，点一次即换成
HttpOnly 签名 cookie（30 天滑动续期），链接随即失效。主机可在页面右上角「链接」面板
重新生成主机链接、管理邀请、查看在线设备。

**访客**：必须由主机邀请。邀请「一人一码」，24 小时内未使用即过期，谁先打开就绑定谁
（转发给别人打不开），换来的 cookie 有效期 7 天且滑动续期；主机撤销邀请后该设备下一次
请求即被拒绝。

## 4. 运行的服务

| 服务 | 状态 | 说明 |
| --- | --- | --- |
| `lanfile` | active / enabled | 本项目服务，systemd 常驻，专用系统用户 `lanfile`（无登录 shell） |
| `nginx` | active / enabled | 统一入口，监听 8080，反代到 lanfile |
| `firewalld` | active / enabled | 只放行 `ssh` 与 `8080/tcp`（`dhcpv6-client` 为默认保留） |
| `sshd` | active / enabled | 仅密钥登录 |

## 5. 监听端口

| 端口 | 监听地址 | 进程 | 对外 |
| --- | --- | --- | --- |
| 22 | 0.0.0.0 | sshd | 是（建议安全组限制来源 IP） |
| 8080 | 0.0.0.0 / [::] | nginx | 是（nginx 唯一入口） |
| 41730 | 127.0.0.1 | lanfile-server | 否（只允许本机 nginx 访问） |
| 80 | — | — | 已关闭（nginx 默认站点整块注释掉） |

## 6. 安全配置

| 项 | 值 |
| --- | --- |
| `PermitRootLogin` | `prohibit-password`（只允许密钥） |
| `PasswordAuthentication` | `no` |
| `MaxAuthTries` | `3` |
| `LoginGraceTime` | `30` |
| root 密码 | 已锁定（`passwd -S root` → `LK`） |
| 可登录账号 | 只有 root；`authorized_keys` 中只有一把公钥（对应 `WSL-ssh.pem`） |
| 防火墙 | firewalld 默认 zone `public`：services `dhcpv6-client ssh`，ports `8080/tcp` |
| SELinux | Disabled |
| 系统更新 | 已执行 `dnf update`，当前无待更新包 |

sshd 原配置备份：`/etc/ssh/sshd_config.bak-20260920`。

**待办（安全）**：阿里云账号开启 MFA；安全组把 22 的来源收窄到自己的出口 IP。

## 7. 目录与服务参数

```
/opt/lanfile/
├── lanfile-server          当前二进制（root:root 755，约 7.8 MB，静态链接）
└── lanfile-server.bak      上一版，回滚用

/var/lib/lanfile/           数据目录（lanfile:lanfile，750）
├── secret.key              cookie 签名密钥（600）—— 删除它等于让所有设备重新登录
├── sessions/               会话数据：聊天记录 session.json、上传文件 uploads/、文件清单.txt
└── host-token.txt          早期版本遗留文件，当前版本已不使用（可删）

/etc/systemd/system/lanfile.service
/etc/nginx/nginx.conf                     （默认 80 站点已注释，备份 nginx.conf.bak-lanfile）
/etc/nginx/conf.d/00-common.conf          （$connection_upgrade 映射 + 日志格式）
/etc/nginx/conf.d/lanfile.conf            （8080 入口与 /lanfile/ 反代）
/etc/nginx/snippets/proxy-common.conf     （公共反代参数：WebSocket、真实 IP、大文件不缓冲、超时 1h）
/var/log/nginx/lanfile.access.log         （访问日志，含真实客户端 IP）
```

**lanfile 启动参数**（systemd unit 内）：

```
/opt/lanfile/lanfile-server \
  -addr 127.0.0.1:41730      只监听回环，公网必须经 nginx 进来
  -data /var/lib/lanfile     数据目录（与二进制分离，升级不影响数据）
  -open=false                服务器无图形界面，不自动开浏览器
  -max-mb 500                单文件上限 500 MB
  -public-url http://8.138.247.252:8080   生成主机/邀请链接用的对外地址
  -base /lanfile/            服务挂在 nginx 的子路径下
  -trust-loopback=false      经代理后所有请求都来自回环，不能把回环当成主机
  -host-cookie-days 30 -guest-cookie-days 7 -invite-ttl-hours 24
```

## 8. 代码与部署

| 项 | 值 |
| --- | --- |
| 仓库 | `git@github.com:zx752541925/lan-file-share.git`（私有） |
| 分支 / 归档 | `main` = 服务器版；tag `v1.0-local` = 本地版（Windows exe，已归档） |
| 本地开发 | WSL，`/home/xu/projects/局域网文件传输` |

仓库里的部署脚本（在项目根目录执行）：

```bash
scripts/deploy.sh          # 静态编译 linux 二进制 → 上传 → 重启 lanfile → 回显状态
scripts/deploy-nginx.sh    # 上传 nginx 配置 → 关默认站点 → nginx -t → 重启 → 放行 8080
make build-linux           # 只编译：bin/lanfile-server-linux（CGO_ENABLED=0 静态链接）
make build-windows         # 本地版 exe（从 tag v1.0-local 拉分支后使用）
```

回滚 lanfile：

```bash
ssh myecs 'cd /opt/lanfile && cp -f lanfile-server.bak lanfile-server && systemctl restart lanfile'
```

## 9. 常见运维命令

```bash
ssh myecs 'systemctl status lanfile --no-pager'          # 服务状态
ssh myecs 'journalctl -u lanfile -n 50 --no-pager'       # 服务日志（含主机入口）
ssh myecs 'systemctl restart lanfile'                    # 重启（会换新主机链接，cookie 仍有效）
ssh myecs 'tail -20 /var/log/nginx/lanfile.access.log'   # 反代访问日志
ssh myecs 'nginx -t && systemctl reload nginx'           # 改 nginx 配置后校验并生效
ssh myecs 'du -sh /var/lib/lanfile; df -h /'             # 数据与磁盘占用
```

## 10. 常见变更怎么做

| 需求 | 改哪里 |
| --- | --- |
| 换对外端口（如 8080 → 8081） | `deploy/nginx/lanfile.conf` 的 `listen`、`deploy/lanfile.service` 的 `-public-url`；再跑两个部署脚本；安全组放行新端口 |
| 上域名 + HTTPS | nginx 加 `server_name` 与 443 证书（Let's Encrypt），`-public-url` 改成 `https://域名`；`-base` 可保留或改成根路径 |
| 新增一个服务 | 给它一个内部端口，在 `deploy/nginx/` 加一个 `location /<名字>/` 块，`scripts/deploy-nginx.sh` 跑一次（安全组不用动） |
| 让某人不能再用 | 主机页面「链接」面板里撤销对应邀请（立即生效） |
| 清空所有身份 | `ssh myecs 'rm -f /var/lib/lanfile/secret.key && systemctl restart lanfile'`（所有人需重新邀请；谨慎） |

## 11. 注意事项

- **阿里云安全组**：当前放行了 22 与 8080；如收窄 22 的来源 IP，注意别把自己关在门外。
- **80 端口保持关闭**：nignx 默认站点已整块注释（只注释 `listen` 行无效，nginx 会默认监听 `*:80`）。
- **HTTPS 未启用**：当前是明文 HTTP，主机/邀请链接与聊天内容在公网可被嗅探；建议尽快上域名 + 证书。
- **磁盘**：40 G，当前用 5.8 G；上传文件会持续占用，必要时清理旧的 `sessions/`。
- **资源占用**：空闲时 lanfile 约 15 MB 内存、CPU 接近 0，无需考虑休眠。
- **敏感文件**：`secret.key`、`host-token.txt`（遗留）内容不要外泄；本文档不记录任何密钥内容。
