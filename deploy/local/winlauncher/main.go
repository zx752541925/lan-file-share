//go:build windows

// lanfile-start.exe —— 双击这个启动器就能用上聊天室。
//
// 流程：
//  1. 唤醒 WSL（wsl.exe -d <发行版> -u <用户> -e true）
//  2. 在 WSL 里重启 lanfile 用户服务（每次都是全新进程 → 全新的主机链接）
//  3. 从 journal 里读出这次的主机入口链接
//  4. 枚举本机网卡，取 Windows 的局域网 IP（跳过 vEthernet / 虚拟网卡）
//  5. 弹窗显示主机地址（同时复制到剪贴板）
//  6. 打开浏览器访问该地址
//
// 为什么要用局域网 IP 打开：服务端生成邀请链接/二维码时按请求的 Host 推导地址，
// 用 localhost 打开会生成手机打不开的 localhost 链接；用局域网 IP 打开就自动正确。
//
// 编译：make build-launcher（GOOS=windows + -H=windowsgui，无控制台窗口）

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	distroName  = "Ubuntu" // WSL 发行版名（wsl -l -v 里显示的名字）
	wslUser     = "xu"
	serviceName = "lanfile"
	listenPort  = "41730"
)

// 这些网卡名是虚拟机/容器用的，手机连不上，挑 IP 时要跳过
var virtualAdapter = []string{"vethernet", "wsl", "hyper-v", "vmware", "virtualbox", "docker", "loopback", "bluetooth", "tailscale", "zerotier"}

func main() {
	if err := run(); err != nil {
		messageBox("lanfile 启动失败", err.Error(), mbOK|mbIconError)
	}
}

func run() error {
	// 1) 唤醒 WSL：这条命令本身会立刻退出，但 WSL 发行版会被拉起来并保持运行
	if out, err := wsl("-e", "true"); err != nil {
		return fmt.Errorf("唤醒 WSL 失败：%v\n%s", err, out)
	}

	// 2) 先确保推理摘要中转（shim）在跑 —— agent 的 provider 指向它，缺了它 Codex 会连不上
	//    仅当装了该服务时才存在，失败不影响后续
	if _, err := wsl("-e", "systemctl", "--user", "start", "deepseek-shim"); err != nil {
		_ = err // 没装 shim 就跳过
	}

	// 3) 重启服务（没在跑就是启动），拿到一张没用过的主机链接
	if out, err := wsl("-e", "systemctl", "--user", "restart", serviceName); err != nil {
		return fmt.Errorf("启动 lanfile 服务失败：%v\n%s", err, out)
	}

	// 4) 等服务把链接打印进日志（最多等 15 秒）
	key := ""
	for i := 0; i < 15; i++ {
		out, err := wsl("-e", "bash", "-lc",
			"journalctl --user -u "+serviceName+" --no-pager | grep 主机入口 | tail -1")
		if err == nil {
			key = extractKey(out)
			if key != "" {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if key == "" {
		return fmt.Errorf("没能从 journalctl 里读到主机链接。\n手动排查：wsl -d %s -u %s -e journalctl --user -u %s -n 30",
			distroName, wslUser, serviceName)
	}

	// 5) 取本机局域网 IP
	ip := lanIP()
	if ip == "" {
		return fmt.Errorf("找不到可用的局域网 IP（网卡都断开了？）")
	}

	url := fmt.Sprintf("http://%s:%s/?host=%s", ip, listenPort, key)

	// 6) 静默完成：地址复制到剪贴板 + 写一行日志，不再弹窗打断
	copyToClipboard(url)
	appendLog(fmt.Sprintf("已启动：%s（本机 IP %s）", url, ip))

	// 7) 打开浏览器
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		return fmt.Errorf("打开浏览器失败：%v\n主机地址（已复制到剪贴板）：%s", err, url)
	}
	return nil
}

// wsl 在 WSL 里执行命令，返回合并后的输出。
func wsl(args ...string) (string, error) {
	base := []string{"-d", distroName, "-u", wslUser}
	cmd := exec.Command("wsl.exe", append(base, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// extractKey 从 "主机入口 http://...?host=xxxx" 这样的日志行里取出密钥。
func extractKey(log string) string {
	index := strings.Index(log, "host=")
	if index < 0 {
		return ""
	}
	rest := log[index+len("host="):]
	end := strings.IndexAny(rest, " \r\n\t")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// lanIP 挑一个最像"手机能访问"的 IPv4 地址。
func lanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	type candidate struct {
		ip    net.IP
		score int
	}
	var list []candidate

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		virtual := false
		for _, bad := range virtualAdapter {
			if strings.Contains(name, bad) {
				virtual = true
				break
			}
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || strings.HasPrefix(ip.String(), "169.254.") {
				continue
			}
			score := 3
			switch {
			case strings.HasPrefix(ip.String(), "192.168."):
				score = 0
			case strings.HasPrefix(ip.String(), "10."):
				score = 1
			case strings.HasPrefix(ip.String(), "172."):
				score = 2
			}
			if virtual {
				score += 10
			}
			list = append(list, candidate{ip: ip, score: score})
		}
	}

	sort.SliceStable(list, func(i, j int) bool { return list[i].score < list[j].score })
	if len(list) == 0 {
		return ""
	}
	return list[0].ip.String()
}

/* ---------------- Windows 小工具（消息框 / 剪贴板） ---------------- */

const (
	mbOK        = 0x00000000
	mbIconError = 0x00000010
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

func messageBox(title, text string, flags uintptr) {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		flags)
}

// copyToClipboard 借 PowerShell 的 Set-Clipboard 实现（避免自己写剪贴板 API）。
func copyToClipboard(text string) {
	script := fmt.Sprintf("Set-Clipboard -Value '%s'", strings.ReplaceAll(text, "'", "''"))
	_ = exec.Command("powershell.exe", "-NoProfile", "-Command", script).Run()
}

// appendLog 在用户主目录追加一行启动记录（没有弹窗，但出问题时有据可查）。
func appendLog(line string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(home, "lanfile-start.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.WriteString(time.Now().Format("2006-01-02 15:04:05 ") + line + "\r\n")
}
