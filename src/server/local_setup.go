package main

// 本地版专属：聊天室 Codex 的参数注册、接线与控制台页面路由。
//
// 服务器版（ECS）不开 -agent，因此这个文件里的东西在服务器上用不到；
// 之所以仍放在同一个 package，是为了不动公共逻辑（session/hub/api/前端主体）。

import (
	"flag"
	"io/fs"
	"net/http"
	"os"
	"time"
)

// localAgentFlags 汇总 -agent* 这一组参数，方便在 main 里一次性接线。
type localAgentFlags struct {
	on        *bool
	trigger   *string
	name      *string
	cwd       *string
	home      *string
	baseURL   *string
	timeout   *int
	notify    *bool
	maxSteps  *int
	selfcheck *bool
}

// registerLocalAgentFlags 注册本地版专属参数（必须在 flag.Parse() 之前调用）。
func registerLocalAgentFlags() localAgentFlags {
	return localAgentFlags{
		on:        flag.Bool("agent", false, "开启聊天室里的 Codex 成员（需要本机能调 codex CLI）"),
		trigger:   flag.String("agent-trigger", "@codex", "触发词，消息里出现它才回复（不分大小写）"),
		name:      flag.String("agent-name", "Codex", "聊天里显示的昵称"),
		cwd:       flag.String("agent-cwd", "sessions", "Codex 的工作目录；相对路径按数据目录解析（默认 data/sessions）"),
		home:      flag.String("agent-home", "", "给 Codex 用的独立 CODEX_HOME（自带免确认的 AGENTS.md）；留空则用 <数据目录>/agent-home"),
		baseURL:   flag.String("agent-base-url", "", "把 agent 的 provider base_url 改写成这个地址，例如 http://127.0.0.1:41780/（本地 shim，可显示思考过程）"),
		timeout:   flag.Int("agent-timeout", 300, "单次执行超时（秒）"),
		notify:    flag.Bool("agent-notify", true, "Codex 执行失败时在聊天里发一条提示"),
		maxSteps:  flag.Int("agent-max-steps", 0, "单个任务最多几步（命令 + 工具调用），超限终止；0 = 不限制（默认，靠 -agent-timeout 兜底）"),
		selfcheck: flag.Bool("selfcheck", false, "自查技能覆盖：把经验库里做过的任务逐条过一遍，看哪些能/不能被固化成技能（打印完退出，不启动服务）"),
	}
}

// maybeRunSelfCheck 处理 -selfcheck：打印技能覆盖自查后退出。
// 放在 main 里尽早调用（在初始化认证/会话之前），这样输出干净、也不会碰数据。
func maybeRunSelfCheck(flags localAgentFlags, dataDir string) {
	if flags.selfcheck == nil || !*flags.selfcheck {
		return
	}
	runSelfCheck(dataDir)
	os.Exit(0)
}

// setupLocalAgent 按参数决定是否启用聊天室 Codex，并把展示用的配置记到 App 上。
func setupLocalAgent(app *App, flags localAgentFlags, dataDir string) {
	maybeRunSelfCheck(flags, dataDir)
	if !*flags.on {
		return // 默认关闭：不加 -agent 时功能不存在，聊天一切照旧
	}

	timeout := time.Duration(*flags.timeout) * time.Second
	app.agent = newAgent(*flags.name, *flags.trigger, *flags.cwd,
		*flags.home, *flags.baseURL, timeout, dataDir, *flags.notify, *flags.maxSteps)
	app.agentTrigger = *flags.trigger
	app.agentCwd = *flags.cwd
	app.agentTimeout = timeout
}

// registerLocalConsole 注册控制台页面路由（只有主机能打开）。
func registerLocalConsole(mux *http.ServeMux, app *App, pages fs.FS, version string) {
	mux.HandleFunc("/agent", app.handleConsole(pages, version))
}

// handleConsole 提供 Codex 控制台页面：只有主机能打开，其他人直接送回聊天页。
func (a *App) handleConsole(pages fs.FS, version string) http.HandlerFunc {
	serve := pageHandler(pages, version, "local/agent.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.auth.IdentityOf(nil, r).IsHost() {
			http.Redirect(w, r, a.base, http.StatusFound)
			return
		}
		serve(w, r)
	}
}
