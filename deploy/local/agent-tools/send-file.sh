#!/usr/bin/env bash
#
# 把本机文件发进聊天室（走 lanfile 的上传接口，服务端会把它当成一条文件消息广播）
#
# 用法：send-file.sh [-f] <文件路径> [说明文字]
# 例：  send-file.sh /tmp/shot.png "刚截的图"
#
# -f：跳过"文件太旧"的检查。
# 为什么要有这道闸：截图命令失败时不会报错到调用方眼里，接着把**上一次留下的同名旧图**
# 发出去，用户看到的就是"过时的截屏"（实测踩过一次：截图脚本路径没找到，发的是 1 小时前的图）。
#
# 说明：服务跑在本机且 -trust-loopback 默认开启，所以从 WSL 里直接 curl 本机地址
#       就会被当成主机，不需要任何令牌。

set -euo pipefail

FILE="${1:?用法: send-file.sh <文件路径> [说明文字]}"
CAPTION="${2:-}"
BASE="${LANFILE_BASE:-http://127.0.0.1:41730}"

FORCE=""
if [ "$FILE" = "-f" ]; then
  FORCE=1
  FILE="${2:?用法: send-file.sh [-f] <文件路径> [说明文字]}"
  CAPTION="${3:-}"
fi

if [ ! -f "$FILE" ]; then
  echo "文件不存在：$FILE" >&2
  exit 1
fi

WARN_AGE="${LANFILE_SEND_WARN_AGE:-120}"      # 超过这个年龄先告警
MAX_AGE="${LANFILE_SEND_MAX_AGE:-600}"        # 超过这个年龄直接拒发（要发加 -f）
AGE=$(( $(date +%s) - $(stat -c %Y "$FILE") ))
echo "文件时间：$(date -d "@$(stat -c %Y "$FILE")" '+%F %T')（${AGE} 秒前）"
if [ -z "$FORCE" ]; then
  if [ "$AGE" -gt "$MAX_AGE" ]; then
    echo "拒绝发送：$FILE 是 ${AGE} 秒前生成的（超过 ${MAX_AGE} 秒），很可能不是这次要发的东西。" >&2
    echo "  请先生成一份新的（建议用带时间戳的文件名），确实要发旧文件就加 -f。" >&2
    exit 2
  fi
  if [ "$AGE" -gt "$WARN_AGE" ]; then
    echo "注意：这个文件是 ${AGE} 秒前生成的。如果你要发的是「刚截的图」，它很可能没被重新生成——先确认再发（继续发送）。" >&2
  fi
fi

NAME="$(basename "$FILE")"
curl -sS -X POST "$BASE/api/upload" \
  -F "file=@${FILE};filename=${NAME}" \
  -F "text=${CAPTION}" \
  -F "name=Codex" \
  -F "clientId=codex" >/dev/null

echo "已发送：${NAME}${CAPTION:+（说明：${CAPTION}）}"
