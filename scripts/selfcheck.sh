#!/usr/bin/env bash
#
# 技能覆盖自查：把经验库里做过的任务逐条过一遍，看哪些能、哪些不可能被固化成技能（宏）。
#
# 用法：scripts/selfcheck.sh [数据目录]
#
# 为什么要有它：宏只认某些步骤（gui_open/gui_click/gui_type/gui_shot + "干活的" shell 命令），
# 其余步骤会被静默丢弃。于是会出现"某个任务做一百次也不会被技能化"这种洞，而且只有人去翻
# 聊天记录才发现（实测踩过一次：截屏任务用 gui_shot + send-file.sh，一个都记不上）。
# 所以：**改完任何通用机制（工具链 / 宏 / 路径解析 / 手册里的命令）之后，先跑一遍这个**。
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="${1:-$ROOT/data}"
BIN="$ROOT/bin/lanfile-server"

if [ ! -x "$BIN" ]; then
  echo "找不到 $BIN，先编译：make build（或 go build -o bin/lanfile-server ./src/server）" >&2
  exit 1
fi

exec "$BIN" -selfcheck -data "$DATA"
