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
	publicURL string
	base      string // 访问子路径，形如 / 或 /lanfile/（前后都有斜杠）
}

func main() {
	// 配置集中在这里：命令行参数优先，其次环境变量（方便以后放进容器/公网）
	addr := flag.String("addr", envOr("LANFILE_ADDR", ":41730"), "监听地址（可用环境变量 LANFILE_ADDR）")
	webDir := flag.String("web", envOr("LANFILE_WEB", ""), "前端目录（默认：项目内 src/web，否则用内置页面）")
	dataDir := flag.String("data", envOr("LANFILE_DATA", ""), "数据目录（默认：项目根目录或 exe 同级的 data）")
	maxMB := flag.Int64("max-mb", envIntOr("LANFILE_MAX_MB", 4096), "单个文件大小上限（MB）")
	base := flag.String("base", envOr("LANFILE_BASE", "/"), "访问子路径（如 /lanfile/），用于 nginx 按路径分发多个服务；默认根路径")
	publicURL := flag.String("public-url", envOr("LANFILE_PUBLIC_URL", ""), "对外访问地址，用于生成主机/邀请链接（如 http://1.2.3.4:8080，留空则用 localhost）")
	trustLoopback := flag.Bool("trust-loopback", true, "本机（回环地址）访问直接视为主机；经 nginx 等反向代理时必须设为 false")
	hostCookieDays := flag.Int("host-cookie-days", 30, "主机登录有效期（天，每次上线滑动续期）")
	guestCookieDays := flag.Int("guest-cookie-days", 7, "访客登录有效期（天，每次上线滑动续期）")
	inviteTTLHours := flag.Int("invite-ttl-hours", 24, "邀请链接未被使用时的有效期（小时）")
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

	// 签名密钥落盘：重启后已发出的 cookie 仍然有效，主客都不会掉线
	auth, err := NewAuth(filepath.Join(*dataDir, "secret.key"), *trustLoopback,
		time.Duration(*hostCookieDays)*24*time.Hour,
		time.Duration(*guestCookieDays)*24*time.Hour,
		time.Duration(*inviteTTLHours)*time.Hour)
	if err != nil {
		fatal("初始化签名密钥失败：%v", err)
	}

	app := &App{
		sessions:  sessions,
		hub:       NewHub(),
		auth:      auth,
		maxUpload: *maxMB << 20,
		port:      portOf(*addr),
		publicURL: strings.TrimRight(*publicURL, "/"),
		base:      normalizeBase(*base),
	}
	go app.hub.Run()

	chunks := newChunkedUploads(filepath.Join(*dataDir, "tmp"), app)

	pages, source := webAssets(*webDir)
	version := assetsVersion(pages)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", app.handleWS)
	mux.HandleFunc("/api/upload", app.handleUpload)
	mux.HandleFunc("/api/upload/init", chunks.handleInit)
	mux.HandleFunc("/api/upload/status", chunks.handleStatus)
	mux.HandleFunc("/api/upload/chunk", chunks.handleChunk)
	mux.HandleFunc("/api/upload/complete", chunks.handleComplete)
	mux.HandleFunc("/api/files/", app.handleFile)
	mux.HandleFunc("/api/reveal/", app.handleReveal)
	mux.HandleFunc("/api/whoami", app.handleWhoami)
	mux.HandleFunc("/api/host/link", app.handleHostLink)
	mux.HandleFunc("/api/invites", app.handleInvites)
	mux.HandleFunc("/api/invites/", app.handleInviteByID)
	mux.HandleFunc("/api/devices", app.handleDevices)
	mux.Handle("/", staticHandler(pages, version))

	server := &http.Server{
		Addr:              *addr,
		Handler:           app.withBase(app.entry(mux)),
		ReadHeaderTimeout: 15 * time.Second,
	}

	// 本机开发时用 localhost；服务器上用 -public-url 指定公网地址
	baseURL := app.publicURL
	if baseURL == "" {
		baseURL = "http://localhost:" + app.port
	}
	hostURL := app.auth.HostLink(app.siteURL(baseURL))
	printBanner(source, *dataDir, app.port, app.base, hostURL, *trustLoopback)

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

func printBanner(pageSource, dataDir, port, base, hostURL string, trustLoopback bool) {
	log.Println("局域网文件传输服务已启动")
	log.Printf("  页面来源 %s", pageSource)
	log.Printf("  数据目录 %s", dataDir)
	log.Printf("  访问路径 %s", base)
	if hostURL != "" {
		log.Printf("  主机入口 %s", hostURL)
		log.Printf("            ↑ 一次性链接：用过即失效；可在页面里生成新链接或邀请别人")
	} else {
		log.Printf("  主机入口（链接已被使用，请在页面里重新生成）")
	}
	if !trustLoopback {
		log.Printf("  准入模式 经反向代理：只认 cookie，不信任来源地址")
	}
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

// entry 是所有请求的第一道门：
//  1. 带 ?host=<一次性主机密钥> → 校验通过则下发主机 cookie，然后 302 抹掉 URL 里的密钥；
//  2. 带 ?invite=<一次性邀请码> → 校验并绑定当前浏览器，下发访客 cookie，同样抹掉参数；
//  3. 其余请求按 cookie 判定身份，未通过则返回「需要邀请」页（接口返回 403 JSON）。
//
// 把密钥从地址里抹掉是刻意的：避免它留在浏览器历史、复制分享和 Referer 里。
func (a *App) entry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		if key := query.Get("host"); key != "" {
			if a.auth.RedeemHostKey(key) {
				a.auth.GrantHost(w, r)
				log.Printf("主机已通过一次性链接登录（%s）", clientIP(r))
				a.redirectClean(w, r)
				return
			}
			log.Printf("主机链接无效或已被使用（%s）", clientIP(r))
			a.serveDenied(w, r, "主机链接无效或已被使用。请到服务器上执行 journalctl -u lanfile 取最新链接，或让已在线的用主机身份重新生成一张。")
			return
		}

		if code := query.Get("invite"); code != "" {
			identity, reason := a.auth.RedeemInvite(code, clientIP(r), r.UserAgent())
			if !identity.Valid() {
				log.Printf("邀请链接被拒绝：%s（%s）", reason, clientIP(r))
				a.serveDenied(w, r, reason)
				return
			}
			a.auth.GrantGuest(w, r, identity)
			log.Printf("访客已通过邀请加入（邀请码 %s，来自 %s）", identity.InviteID, clientIP(r))
			a.redirectClean(w, r)
			return
		}

		identity := a.auth.IdentityOf(w, r) // 顺带滑动续期
		if !identity.Valid() {
			a.serveDenied(w, r, "这个地址需要主机的邀请链接才能进入。")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// normalizeBase 把 -base 统一成「前后都有斜杠」的形式："" 和 "/" 都表示根路径。
func normalizeBase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return strings.TrimRight(value, "/") + "/"
}

// withBase 支持把服务挂在子路径下（例如 nginx 的 location /lanfile/）。
// 请求进来先剥掉前缀再交给后面的路由，所以内部仍然按 /、/api/... 、/ws 处理；
// 前缀之外的路径直接 404，避免「/lanfilex」这种误匹配。
func (a *App) withBase(next http.Handler) http.Handler {
	if a.base == "/" {
		return next
	}

	prefix := strings.TrimSuffix(a.base, "/") // /lanfile
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix {
			// 少了结尾斜杠：跳一下，否则页面里的相对路径（./app.js）会算错
			http.Redirect(w, r, a.base, http.StatusMovedPermanently)
			return
		}
		if !strings.HasPrefix(r.URL.Path, a.base) {
			http.NotFound(w, r)
			return
		}

		r.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, a.base)
		next.ServeHTTP(w, r)
	})
}

// siteURL 是当前服务对外的根地址，例如 http://1.2.3.4:8080/lanfile（结尾不带斜杠）。
func (a *App) siteURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + strings.TrimSuffix(a.base, "/")
}

// redirectClean 跳回不带查询参数的同一路径，避免密钥/邀请码留在地址栏与历史里。
// 注意此时 r.URL.Path 已被 withBase 剥掉前缀，所以要自己把 base 拼回去。
func (a *App) redirectClean(w http.ResponseWriter, r *http.Request) {
	target := a.base + strings.TrimPrefix(r.URL.Path, "/")
	if target == "" {
		target = a.base
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// serveDenied 未通过校验时的提示页；接口与 WebSocket 返回 403 JSON。
func (a *App) serveDenied(w http.ResponseWriter, r *http.Request, reason string) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": reason})
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>需要邀请</title>
<style>
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
         background:#eef1f7; color:#111827; font-family:-apple-system,"Segoe UI",Roboto,"PingFang SC","Microsoft YaHei",sans-serif; }
  .card { max-width:420px; padding:32px 28px; background:#fff; border-radius:16px;
          box-shadow:0 12px 30px rgba(17,24,39,.08); text-align:center; }
  h1 { margin:0 0 12px; font-size:20px; }
  p { margin:0; color:#6b7280; font-size:14px; line-height:1.7; }
</style></head>
<body><div class="card">
  <h1>需要主机的邀请</h1>
  <p>%s</p>
</div></body></html>`, templateEscape(reason))
}

// templateEscape 只做最小转义，提示文案里可能出现尖括号。
func templateEscape(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(text)
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
