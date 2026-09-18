// Package lanfile 只做一件事：把前端页面编译进二进制，方便单文件分发。
// 网页源码仍然是 src/web/ 这一份，改动后会随下一次编译进入可执行文件。
package lanfile

import "embed"

//go:embed all:src/web
var WebAssets embed.FS
