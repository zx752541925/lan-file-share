#!/usr/bin/env bash
#
# 部署 nginx 入口（只做一次，之后改配置再跑一次即可）
#
# 做什么：
#   1. 上传 deploy/nginx/ 下的配置到服务器（snippets + conf.d）
#   2. 关掉发行版自带的 80 端口默认站点（本机只对外开放 8080）
#   3. nginx -t 校验 → 启用并重启 → firewalld 放行 8080
#
# 前置：目标机已装 nginx（dnf install -y nginx），且 ssh 免密已配好
# 幂等：可重复执行
#
# 注意：安全组放行 8080 需要在阿里云控制台操作，脚本不做（也不该做）
#
set -euo pipefail

DEPLOY_HOST="${LANFILE_DEPLOY_HOST:-myecs}"

cd "$(dirname "$0")/.."

echo "==> 上传 nginx 配置"
ssh "$DEPLOY_HOST" "mkdir -p /etc/nginx/snippets"
scp -q deploy/nginx/proxy-common.conf "$DEPLOY_HOST:/etc/nginx/snippets/proxy-common.conf"
scp -q deploy/nginx/00-common.conf "$DEPLOY_HOST:/etc/nginx/conf.d/00-common.conf"
scp -q deploy/nginx/lanfile.conf "$DEPLOY_HOST:/etc/nginx/conf.d/lanfile.conf"

echo "==> 关掉发行版自带的 80 端口默认站点（只保留 8080 入口）"
ssh "$DEPLOY_HOST" "bash -s" <<'REMOTE'
set -e
conf=/etc/nginx/nginx.conf
if grep -qE '^    server \{' "$conf"; then
  cp -n "$conf" "$conf.bak-lanfile" || true
  # 整个默认 server 块注释掉。注意：只注释 listen 行没用 —— nginx 的 server 块
  # 在没有 listen 时默认监听 *:80，等于没关掉。
  sed -i '/^    server {/,/^    }$/ s/^/#/' "$conf"
  echo "已注释默认站点（原文件备份为 nginx.conf.bak-lanfile）"
else
  echo "默认站点已是关闭状态"
fi
REMOTE

echo "==> 校验并重启 nginx"
ssh "$DEPLOY_HOST" "bash -s" <<'REMOTE'
set -e
nginx -t
systemctl enable --now nginx >/dev/null 2>&1 || true
# 必须 restart 而不是 reload：reload 不会释放已经从配置里去掉的监听端口
systemctl restart nginx
echo "nginx 状态: $(systemctl is-active nginx)"
echo "监听端口:"; ss -tlnp | grep -E ':8080|:80 ' || echo "  （没有监听 8080，检查配置）"
REMOTE

echo "==> firewalld 放行 8080"
ssh "$DEPLOY_HOST" "bash -s" <<'REMOTE'
set -e
firewall-cmd --permanent --add-port=8080/tcp >/dev/null
firewall-cmd --reload >/dev/null
echo "已放行端口: $(firewall-cmd --list-ports) / 服务: $(firewall-cmd --list-services)"
REMOTE

echo "==> 本机验证（403 = 正常：入口通了，且准入拦截生效）"
ssh "$DEPLOY_HOST" "curl -sI 127.0.0.1:8080 | head -1"

echo
echo "下一步：到阿里云控制台放行入方向 TCP 8080，然后用主机链接访问 http://<公网IP>:8080/?host=<密钥>"
