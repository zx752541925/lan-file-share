// Package lanfile 把前端页面与本地版默认规则编译进二进制，方便单文件分发。
// 源码仍然是 src/web/ 与 deploy/local/ 那两份，改动后会随下一次编译进入可执行文件。
package lanfile

import "embed"

//go:embed all:src/web
var WebAssets embed.FS

// DefaultAgentRules 是聊天室 Codex 的默认行为规则。
// 只在 <数据目录>/agent-home/AGENTS.md 不存在时写入，之后聊天里改过的会保留。
// 要改默认值就改 deploy/local/agent-AGENTS.md 再重新编译。
//
//go:embed deploy/local/agent-AGENTS.md
var DefaultAgentRules []byte

// AgentManual 是聊天室 Codex 的操作手册（截屏、发文件、开程序的标准做法）。
// 每次任务都会注入 prompt，避免它自己去翻源码探索。
//
//go:embed deploy/local/agent-tools/agent-manual.md
var AgentManual []byte
