#!/usr/bin/env bash
#
# 一键把 lanfile 部署到云服务器
#
# 流程：本地静态编译 → 上传二进制与 systemd 单元 → 远端替换并重启 → 回显状态与日志
#
# 前置条件：
#   1. 在本机（WSL/Linux）执行，需要 make、go、ssh、scp
#   2. 已配好免密登录，默认用 ssh 别名 myecs（见 ~/.ssh/config），也可用环境变量指定
#   3. 目标机是 systemd 系统（Alibaba Cloud Linux 3 / Ubuntu 均可），且登录用户有 root 权限
#
# 幂等：可反复执行。用户和目录只在缺失时创建，二进制替换前会把上一版留成 .bak
#
# 回滚：ssh 到服务器执行
#   cd /opt/lanfile && cp -f lanfile-server.bak lanfile-server && systemctl restart lanfile
#
# 用法：
#   scripts/deploy.sh                                   # 部署到 myecs
#   LANFILE_DEPLOY_HOST=其他别名 scripts/deploy.sh        # 部署到别的主机
#
set -euo pipefail

DEPLOY_HOST="${LANFILE_DEPLOY_HOST:-myecs}"   # 目标主机：ssh 别名或 IP
BIN_LOCAL="bin/lanfile-server-linux"          # make build-linux 的产物
UNIT_LOCAL="deploy/lanfile.service"           # systemd 单元

REMOTE_DIR="/opt/lanfile"                     # 二进制与旧版本备份
REMOTE_DATA="/var/lib/lanfile"                # 会话数据、主机口令
REMOTE_UNIT="/etc/systemd/system/lanfile.service"

cd "$(dirname "$0")/.."                       # 无论从哪里调用，都先切回仓库根目录

echo "==> 静态编译 linux/amd64（不依赖 glibc，服务器上不需要装 Go）"
make build-linux

# 首次部署的准备：专用系统用户 + 两个目录；已存在时是空操作
echo "==> 检查服务器上的用户与目录"
ssh "$DEPLOY_HOST" "bash -s" <<REMOTE
set -e
id lanfile >/dev/null 2>&1 || useradd -r -s /sbin/nologin lanfile   # -r 系统用户，不给登录 shell
mkdir -p $REMOTE_DIR $REMOTE_DATA
chown lanfile:lanfile $REMOTE_DATA   # 数据目录归服务用户
chmod 750 $REMOTE_DATA               # 其他本机用户看不到会话数据
REMOTE
# 注意：heredoc 故意不加引号（<<REMOTE），这样上面的 \$REMOTE_* 会由本地展开后再传给远端；
# 远端脚本里如果要写 \$ 本身（如 \$REMOTE 外的变量），记得加反斜杠转义，否则会在本地被执行。

# 先传成 .new 再改名：传输中断时不会留下半个可执行文件
echo "==> 上传二进制与 systemd 单元"
scp -q "$BIN_LOCAL" "$DEPLOY_HOST:$REMOTE_DIR/lanfile-server.new"
scp -q "$UNIT_LOCAL" "$DEPLOY_HOST:$REMOTE_UNIT"

echo "==> 替换、重启并验证"
ssh "$DEPLOY_HOST" "bash -s" <<REMOTE
set -e
cd $REMOTE_DIR
if [ -f lanfile-server ]; then
  cp -f lanfile-server lanfile-server.bak   # 留一份上一版，出问题可直接回滚
fi
mv -f lanfile-server.new lanfile-server
chmod 755 lanfile-server
systemctl daemon-reload                     # 单元文件可能改了，必须重新加载
systemctl enable lanfile >/dev/null 2>&1 || true   # 开机自启；重复设置返回非 0 也不影响流程
systemctl restart lanfile
sleep 1                                     # 稍等再探活
echo "服务状态: \$(systemctl is-active lanfile)"
echo "监听端口:"; ss -tlnp | grep 41730 || echo "  (没监听，检查日志)"
echo "本机探活:"; curl -sI 127.0.0.1:41730 | head -1
REMOTE

echo "==> 最近日志"
ssh "$DEPLOY_HOST" "journalctl -u lanfile -n 8 --no-pager"
