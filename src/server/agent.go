package main

// 聊天室里的 Codex：把本机 codex CLI 当成一个聊天参与者。
//
// 规则：
//   - 只有主机和「高级邀请」用户能触发（权限判断在 api.go 里做，这里再兜一层）
//   - 消息文本里出现触发词（默认 @codex，不分大小写）才回复
//   - 串行执行：一次只跑一个，多余的排队；队列满就丢弃并记日志（绝不阻塞聊天）
//   - 每次调用都是全新的 codex 会话（无状态），上下文靠把最近若干条消息拼进 prompt
//
// 安全提示：完全授权意味着拿到触发权的人可以让它在工作目录里任意读写、执行命令。
// 工作目录默认收窄到 data/sessions，但 danger-full-access 下它仍能读到目录之外
// （例如 data/secret.key），所以「谁能触发」是唯一有效的防线。

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	agentQueueSize    = 3                // 排队上限，满了直接丢弃
	agentHistoryLimit = 20               // 拼进 prompt 的最近消息数
	agentReplyLimit   = 4000             // 回复最大字符数，超出截断
	agentSeenLimit    = 200              // 去重表保留的消息 ID 数
	agentCodexBinary  = "codex"          // 默认从 PATH 里找
)

// Agent 是聊天室里那个由 codex CLI 扮演的成员。
type Agent struct {
	name    string        // 显示昵称
	trigger string        // 触发词
	cwd     string        // 工作目录（codex -C）
	timeout time.Duration // 单次执行超时
	bin     string        // codex 可执行文件的绝对路径
	notify  bool          // 失败时是否在聊天里发一条提示
	home    string        // 独立的 CODEX_HOME：自带一份 config.toml 和 AGENTS.md

	jobs chan agentJob

	mu   sync.Mutex
	seen map[string]bool // 已处理过的消息 ID（去重，避免同一消息跑两次）
	seq  []string        // seen 的淘汰顺序
}

type agentJob struct {
	app       *App
	sessionID string
	message   Message
}

// agentHomeAGENTS 是这个成员自己的行为规范。必须用独立 CODEX_HOME 的原因：
// ~/.codex/AGENTS.md 里有「动手前先确认，要用户回复"执行修改"」这类规则，
// 聊天室里不可能满足它，嵌套的 codex 会拒绝干活（实测过）。给它一份自己的规范即可。
const agentHomeAGENTS = `# 聊天室成员 Codex

你是局域网聊天室里的一个成员，昵称 Codex，可以直接操作这台机器的文件。

- 主机已经在聊天里授权你直接执行任务，**不要再请求任何确认**（不要要求对方回复「执行修改」之类的话）。
- 需要跑命令就直接跑，不要只给计划；做完用一两句话说明结果。
- 只做被明确要求的事，不要扩大范围，也不要执行与聊天内容无关的操作。
- 回复保持简短，用中文。
`

// newAgent 准备 agent：解析 codex 路径、确定工作目录并启动串行执行协程。
// 找不到 codex 时返回 nil（调用方据此关闭功能），不会让服务起不来。
func newAgent(name, trigger, cwd, homeDir string, timeout time.Duration, dataDir string, notify bool) *Agent {
	bin, err := exec.LookPath(agentCodexBinary)
	if err != nil {
		log.Printf("agent：找不到 codex 可执行文件（%v），聊天室 Codex 功能已关闭", err)
		return nil
	}

	// 相对路径按数据目录解析：不依赖进程的当前目录（systemd 起来时 CWD 是 /）
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(dataDir, cwd)
	}
	// 必须转成绝对路径：子进程的 cwd 是 a.cwd，相对的 CODEX_HOME 会被解析错
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		log.Printf("agent：创建工作目录 %s 失败：%v，聊天室 Codex 功能已关闭", cwd, err)
		return nil
	}

	// 独立的 CODEX_HOME：自带 AGENTS.md（免确认）+ 一份 config.toml（沿用你本机的 provider 配置）
	if homeDir == "" {
		homeDir = filepath.Join(dataDir, "agent-home")
	} else if !filepath.IsAbs(homeDir) {
		homeDir = filepath.Join(dataDir, homeDir)
	}
	if abs, err := filepath.Abs(homeDir); err == nil {
		homeDir = abs
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		log.Printf("agent：创建 CODEX_HOME %s 失败：%v，聊天室 Codex 功能已关闭", homeDir, err)
		return nil
	}
	if err := os.WriteFile(filepath.Join(homeDir, "AGENTS.md"), []byte(agentHomeAGENTS), 0o644); err != nil {
		log.Printf("agent：写入 AGENTS.md 失败：%v", err)
	}

	agent := &Agent{
		name:    name,
		trigger: strings.ToLower(strings.TrimSpace(trigger)),
		cwd:     cwd,
		timeout: timeout,
		bin:     bin,
		notify:  notify,
		home:    homeDir,
		jobs:    make(chan agentJob, agentQueueSize),
		seen:    make(map[string]bool),
	}
	agent.syncConfig()
	go agent.run()

	log.Printf("agent：聊天室 Codex 已启用，触发词 %q，工作目录 %s，CODEX_HOME %s，单次超时 %s",
		trigger, cwd, homeDir, timeout)
	return agent
}

// syncConfig 把 ~/.codex/config.toml 同步到自己的 CODEX_HOME。
// 源文件更新过（比如换了模型或密钥）就重新复制，保证 agent 用的配置跟你本机一致。
func (a *Agent) syncConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	src := filepath.Join(home, ".codex", "config.toml")
	dst := filepath.Join(a.home, "config.toml")

	srcInfo, err := os.Stat(src)
	if err != nil {
		log.Printf("agent：读不到 %s（%v），Codex 可能因为没有 provider 配置而失败", src, err)
		return
	}
	if dstInfo, err := os.Stat(dst); err == nil && !srcInfo.ModTime().After(dstInfo.ModTime()) {
		return
	}

	raw, err := os.ReadFile(src)
	if err != nil {
		log.Printf("agent：读取 %s 失败：%v", src, err)
		return
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		log.Printf("agent：写入 %s 失败：%v", dst, err)
		return
	}
	log.Printf("agent：已同步 Codex 配置到 %s", dst)
}

// Handle 判断这条消息要不要交给 Codex，需要就入队后立刻返回（绝不阻塞调用方）。
func (a *Agent) Handle(app *App, sessionID string, message Message) {
	if a == nil {
		return
	}
	// 避免自触发：Codex 自己的消息、以及不含触发词的普通聊天
	if message.ClientID == "codex" || a.trigger == "" {
		return
	}
	if !strings.Contains(strings.ToLower(message.Text), a.trigger) {
		return
	}
	if !a.markSeen(message.ID) {
		return
	}

	select {
	case a.jobs <- agentJob{app: app, sessionID: sessionID, message: message}:
	default:
		log.Printf("agent：队列已满，丢弃消息 %s", message.ID)
	}
}

// markSeen 记录已处理的消息，重复的返回 false；超出上限时淘汰最旧的。
func (a *Agent) markSeen(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.seen[id] {
		return false
	}
	a.seen[id] = true
	a.seq = append(a.seq, id)
	for len(a.seq) > agentSeenLimit {
		delete(a.seen, a.seq[0])
		a.seq = a.seq[1:]
	}
	return true
}

// run 串行消费队列：一次只跑一个 codex，避免并发把机器和额度打满。
func (a *Agent) run() {
	for job := range a.jobs {
		a.process(job)
	}
}

func (a *Agent) process(job agentJob) {
	a.syncConfig() // 配置可能被改过，跑之前对一次

	session, ok := job.app.sessions.ByID(job.sessionID)
	if !ok {
		log.Printf("agent：会话 %s 已不存在，跳过", job.sessionID)
		return
	}
	history := job.app.sessions.Messages(session)
	prompt := a.buildPrompt(history)

	started := time.Now()
	log.Printf("agent：开始处理消息 %s（会话 %s，历史 %d 条）", job.message.ID, job.sessionID, len(history))

	reply, err := a.exec(prompt)
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		log.Printf("agent：执行失败（耗时 %s）：%v", elapsed, err)
		if a.notify {
			a.reply(job, fmt.Sprintf("（Codex 执行失败：%v）", err))
		}
		return
	}
	if reply == "" {
		reply = "（Codex 没有返回内容）"
	}
	log.Printf("agent：处理完成（耗时 %s，回复 %d 字）", elapsed, len([]rune(reply)))
	a.reply(job, reply)
}

// reply 把 Codex 的回复写回「触发它的那个会话」并广播。
func (a *Agent) reply(job agentJob, text string) {
	message := Message{
		ID:       newID(),
		ClientID: "codex",
		Name:     a.name,
		TS:       time.Now().UnixMilli(),
		Text:     text,
	}
	if err := job.app.sessions.AddMessageTo(job.sessionID, message); err != nil {
		log.Printf("agent：写入回复失败：%v", err)
		return
	}
	job.app.broadcast(Event{Type: "message", Message: &message})
}

// buildPrompt 把最近的聊天记录拼成一次性的上下文（codex exec 是无状态的）。
func (a *Agent) buildPrompt(history []Message) string {
	var builder strings.Builder
	builder.WriteString("你是局域网聊天室里的成员，昵称 " + a.name + "，可以直接操作这台机器的文件。\n\n")
	builder.WriteString("最近的对话：\n")

	if len(history) > agentHistoryLimit {
		history = history[len(history)-agentHistoryLimit:]
	}
	for _, message := range history {
		name := message.Name
		if name == "" {
			name = "某设备"
		}
		text := strings.TrimSpace(message.Text)
		if message.File != nil {
			file := message.File.Name
			if message.File.Deleted {
				file += "（已删除）"
			}
			if text != "" {
				text = fmt.Sprintf("%s［附带文件：%s］", text, file)
			} else {
				text = fmt.Sprintf("［发送了文件：%s］", file)
			}
		}
		builder.WriteString(fmt.Sprintf("%s: %s\n", name, text))
	}

	builder.WriteString("\n请用中文简短回复最后一条 @你 的消息，直接给结论，不要复述这段说明。")
	return builder.String()
}

// exec 调用一次 codex exec，返回它最后一条回复。
func (a *Agent) exec(prompt string) (string, error) {
	outFile, err := os.CreateTemp("", "lanfile-agent-*.txt")
	if err != nil {
		return "", fmt.Errorf("创建输出文件失败：%w", err)
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer func() { _ = os.Remove(outPath) }()

	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	args := []string{
		"exec",
		"--skip-git-repo-check", // 工作目录不是 git 仓库也能跑
		"--ephemeral",           // 不把这次会话写进 ~/.codex/sessions
		"-s", "danger-full-access",
		"-c", `approval_policy="never"`,
		"-C", a.cwd,
		"-o", outPath, // 只要最后一条回复，不解析 stdout
		prompt,
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = a.cwd
	// 用独立的 CODEX_HOME（自带免确认的 AGENTS.md），其余环境原样继承
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "CODEX_HOME=") {
			env = append(env, item)
		}
	}
	cmd.Env = append(env, "CODEX_HOME="+a.home)
	// 单独进程组：超时后连同 codex 拉起的子进程一起杀，避免留下孤儿继续改文件
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("超过 %s 未完成，已终止", a.timeout)
	}
	if err != nil {
		tail := strings.TrimSpace(string(output))
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return "", fmt.Errorf("codex 退出异常：%v %s", err, tail)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		return "", fmt.Errorf("读取回复失败：%w", err)
	}
	return truncate(strings.TrimSpace(string(raw)), agentReplyLimit), nil
}

// truncate 按字符（rune）截断，避免中文被切坏。
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…（回复过长，已截断）"
}
