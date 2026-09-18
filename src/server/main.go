// 局域网文件传输服务端：静态页面 + WebSocket 聊天 + 文件上传/下载 + 会话管理
package main

import (
	"bytes"
	"flag"
	"fmt"
	"hash/fnv"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"lanfile"
)

type App struct {
	sessions  *Manager
	hub       *Hub
	auth      *Auth
	maxUpload int64
	port      string
}

func main() {
	// 配置集中在这里：命令行参数优先，其次环境变量（方便以后放进容器/公网）
	addr := flag.String("addr", envOr("LANFILE_ADDR", ":41730"), "监听地址（可用环境变量 LANFILE_ADDR）")
	webDir := flag.String("web", envOr("LANFILE_WEB", ""), "前端目录（默认：项目内 src/web，否则用内置页面）")
	dataDir := flag.String("data", envOr("LANFILE_DATA", ""), "数据目录（默认：项目根目录或 exe 同级的 data）")
	maxMB := flag.Int64("max-mb", envIntOr("LANFILE_MAX_MB", 4096), "单个文件大小上限（MB）")
	openBrowser := flag.Bool("open", true, "启动后自动打开浏览器")
	flag.Parse()

	if *dataDir == "" {
		if fileExists("go.mod") {
			*dataDir = "data"
		} else {
			*dataDir = filepath.Join(exeDir(), "data")
		}
	}

	// 一次启动 = 一个会话；会话目录在产生第一条内容时才创建
	sessions, err := NewManager(filepath.Join(*dataDir, "sessions"))
	if err != nil {
		fatal("初始化会话目录失败：%v", err)
	}
	sessions.BeginNew()

	hostToken := loadOrCreateToken(filepath.Join(*dataDir, "host-token.txt"))
	app := &App{
		sessions:  sessions,
		hub:       NewHub(),
		auth:      NewAuth(hostToken),
		maxUpload: *maxMB << 20,
		port:      portOf(*addr),
	}
	go app.hub.Run()

	pages, source := webAssets(*webDir)
	version := assetsVersion(pages)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", app.handleWS)
	mux.HandleFunc("/api/upload", app.handleUpload)
	mux.HandleFunc("/api/files/", app.handleFile)
	mux.HandleFunc("/api/reveal/", app.handleReveal)
	mux.Handle("/", staticHandler(pages, version))

	server := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	hostURL := fmt.Sprintf("http://localhost:%s/?host=%s", app.port, hostToken)
	printBanner(source, *dataDir, app.port, hostURL)

	if *openBrowser {
		go openInBrowser(hostURL)
	}

	if err := server.ListenAndServe(); err != nil {
		fatal("服务启动失败：%v", err)
	}
}

// LANAddress 是可以给手机访问的地址，带上网卡名方便辨认。
type LANAddress struct {
	Name    string `json:"name"`
	IP      string `json:"ip"`
	URL     string `json:"url"`
	Virtual bool   `json:"virtual"`
}

// 这些网卡是虚拟机/容器用的，手机连不上，排序时压到最后并标注出来
var virtualAdapterPattern = regexp.MustCompile(`(?i)wsl|hyper-v|vethernet|vmware|virtualbox|docker|loopback|bluetooth|tailscale|zerotier|radmin|tap`)

// lanAddresses 返回可给手机访问的地址，真实 Wi-Fi 网段排前面。主机口令不会出现在这里。
func (a *App) lanAddresses() []LANAddress {
	var list []LANAddress

	ifaces, err := net.Interfaces()
	if err != nil {
		return list
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
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
			if ip == nil {
				continue
			}
			ipText := ip.String()
			if strings.HasPrefix(ipText, "169.254.") {
				continue // 自动私有地址，没有意义
			}
			list = append(list, LANAddress{
				Name:    iface.Name,
				IP:      ipText,
				URL:     fmt.Sprintf("http://%s:%s", ipText, a.port),
				Virtual: virtualAdapterPattern.MatchString(iface.Name),
			})
		}
	}

	sort.SliceStable(list, func(i, j int) bool {
		left, right := addressScore(list[i]), addressScore(list[j])
		if left != right {
			return left < right
		}
		return list[i].IP < list[j].IP
	})
	return list
}

// addressScore 越小越可能是手机能用的地址。
func addressScore(address LANAddress) int {
	score := 4
	switch {
	case strings.HasPrefix(address.IP, "192.168."):
		score = 0
	case strings.HasPrefix(address.IP, "10."):
		score = 1
	case strings.HasPrefix(address.IP, "172."):
		score = 2
	case strings.HasPrefix(address.IP, "100."):
		score = 3
	}
	if address.Virtual {
		score += 10
	}
	return score
}

// webAssets 优先用磁盘目录（开发时改前端立即生效），否则用编译进二进制的页面。
func webAssets(dir string) (fs.FS, string) {
	if dir == "" && fileExists(filepath.Join("src", "web", "index.html")) {
		dir = filepath.Join("src", "web")
	}

	if dir != "" {
		if fileExists(filepath.Join(dir, "index.html")) {
			return os.DirFS(dir), dir
		}
		log.Printf("提示：%s 里没有 index.html，改用内置页面", dir)
	}

	sub, err := fs.Sub(lanfile.WebAssets, "src/web")
	if err != nil {
		fatal("内置页面不可用：%v", err)
	}
	return sub, "内置页面（编译进程序）"
}

// staticHandler 提供前端静态文件，未知路径回落到 index.html。
func staticHandler(fsys fs.FS, version string) http.Handler {
	fileServer := http.FileServerFS(fsys)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fsys.Open(path); err != nil {
			path = "index.html"
			r.URL.Path = "/"
		}
		w.Header().Set("Cache-Control", "no-cache")

		// 首页里把 __V__ 换成资源版本号，更新程序后浏览器不会再拿旧的 js/css
		if path == "index.html" {
			if raw, err := fs.ReadFile(fsys, "index.html"); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write(bytes.ReplaceAll(raw, []byte("__V__"), []byte(version)))
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// assetsVersion 用前端文件内容算出版本号，内容不变则版本号不变。
func assetsVersion(fsys fs.FS) string {
	hash := fnv.New64a()
	for _, name := range []string{"index.html", "styles.css", "app.js"} {
		if raw, err := fs.ReadFile(fsys, name); err == nil {
			_, _ = hash.Write(raw)
		}
	}
	return strconv.FormatUint(hash.Sum64(), 36)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envIntOr(key string, fallback int64) int64 {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func printBanner(pageSource, dataDir, port, hostURL string) {
	log.Println("局域网文件传输服务已启动")
	log.Printf("  页面来源 %s", pageSource)
	log.Printf("  数据目录 %s", dataDir)
	log.Printf("  主机入口 %s", hostURL)
	log.Printf("            ↑ 只有这台机器打开这个地址才能切换历史会话")
	log.Printf("  本机地址 http://localhost:%s", port)

	app := &App{port: port}
	for _, address := range app.lanAddresses() {
		mark := ""
		if address.Virtual {
			mark = "（虚拟网卡，手机连不上）"
		}
		log.Printf("  局域网   %s  %s%s", address.URL, address.Name, mark)
	}

	if runtime.GOOS == "windows" {
		log.Println("  提示：关闭本窗口即停止服务，可最小化后继续使用")
	}
}

func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return strings.TrimPrefix(addr, ":")
}

func openInBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("自动打开浏览器失败：%v（请手动访问 %s）", err, target)
	}
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// loadOrCreateToken 生成并记住主机口令，这样带口令的主机地址每次启动都一致，
// 主机浏览器只需第一次用它打开，之后直接访问 localhost 即可。
func loadOrCreateToken(path string) string {
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); len(token) >= 8 {
			return token
		}
	}

	token := newID()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte(token+"\n"), 0o600)
	}
	return token
}

// fatal 出错时在 Windows 上等一次回车，避免双击运行时窗口一闪而过看不到原因。
func fatal(format string, args ...any) {
	log.Printf("错误："+format, args...)
	if runtime.GOOS == "windows" {
		fmt.Print("按回车键退出…")
		_, _ = fmt.Scanln()
	}
	os.Exit(1)
}
