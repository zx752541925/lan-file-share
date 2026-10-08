#!/usr/bin/env bash
#
# 把本机文件发进聊天室（走 lanfile 的上传接口，服务端会把它当成一条文件消息广播）
#
# 用法：send-file.sh <文件路径> [说明文字]
# 例：  send-file.sh /tmp/shot.png "刚截的图"
#
# 说明：服务跑在本机且 -trust-loopback 默认开启，所以从 WSL 里直接 curl 本机地址
#       就会被当成主机，不需要任何令牌。

set -euo pipefail

FILE="${1:?用法: send-file.sh <文件路径> [说明文字]}"
CAPTION="${2:-}"
BASE="${LANFILE_BASE:-http://127.0.0.1:41730}"

if [ ! -f "$FILE" ]; then
  echo "文件不存在：$FILE" >&2
  exit 1
fi

NAME="$(basename "$FILE")"
curl -sS -X POST "$BASE/api/upload" \
  -F "file=@${FILE};filename=${NAME}" \
  -F "text=${CAPTION}" \
  -F "name=Codex" \
  -F "clientId=codex" >/dev/null

echo "已发送：${NAME}${CAPTION:+（说明：${CAPTION}）}"
