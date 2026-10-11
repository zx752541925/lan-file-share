package main

// 本地版专属：技能（宏）库 —— 把"做成功过的任务"固化成可直跑的步骤序列。
//
// 为什么需要它：每步都让模型决策，一次任务要 15-30 秒（模型思考 + 每次工具调用的往返）。
// 而同一件事做过之后，真正需要模型判断的地方已经没有了 —— 把这串调用记下来直跑，
// 就能到 3-5 秒（这正是 OpenClaw 那边把流程写成技能后的效果）。
//
// 玩法：
//   1) 任务成功 → 把这次成功的工具调用（工具 + 参数）记进技能库（同一意图只留一条，成功次数累加）
//   2) 同一意图成功到 2 次 → 技能"启用"，下次同意图直接串行执行，模型不参与
//   3) 直跑前看前置、跑完必须复验；任一步不符预期 → 丢掉这次直跑、退回模型，
//      并给技能记一次失败；连续失败到上限自动停用（别让坏技能一直乱跑）

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AgentMacroStep 是一次工具调用：工具名 + 原始参数（就是 MCP 调用里的 arguments）。
type AgentMacroStep struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

// AgentMacro 是一条"技能"。同一任务意图只有一条，成功的做法会覆盖旧的。
type AgentMacro struct {
	Intent    string           `json:"intent"`    // 归一化后的意图（去重键）
	Prompt    string           `json:"prompt"`    // 原始指令样例，展示与回话用
	Steps     []AgentMacroStep `json:"steps"`     // 直跑时按顺序执行
	Successes int              `json:"successes"` // 见过的成功次数
	Failures  int              `json:"failures"`  // 直跑连续失败次数（成功即清零）
	Disabled  bool             `json:"disabled,omitempty"`
	CreatedAt int64            `json:"createdAt"`
	LastRunAt int64            `json:"lastRunAt,omitempty"`
	LastError string           `json:"lastError,omitempty"`
}

const (
	macroEnableAfter = 1 // 成功几次之后才允许直跑（1 = 第二次同类任务就直接直跑）
	macroMaxFailures = 2 // 直跑连续失败几次就自动停用
	// 一条技能最多记几步。超限的做法是**不录**（不是截断）：
	// 截断会留下"半条流程"，直跑时前半段通过就报成功 —— 那是假成功，比慢一点糟得多。
	macroMaxSteps = 8
	// 子技能嵌套深度上限（技能引用技能，防止环）
	macroMaxDepth    = 3
	macroStepTimeout = 90 // 单步超时（秒）
	macroDirPrefix   = "macro-"
	// 刚把程序拉起来后等一会儿再点：界面需要渲染时间（实测 WeGame 重启后立刻 OCR 只读到零星文字）
	macroSettleAfter = 1500 * time.Millisecond
)

// macroRoot 是技能库目录：放在 agent 自己的 CODEX_HOME 里，它也能读到、能自己写。
func (a *Agent) macroRoot() string { return filepath.Join(a.home, "skills") }

// resolveGuiTools 找 Windows PowerShell 与 gui.ps1 的 Windows 路径（技能直跑要用）。
// 找不到就返回空串 —— 技能直跑自动关闭，功能退回"纯模型"，不会影响原有流程。
func resolveGuiTools() (string, string) {
	powershell := "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
	if _, err := os.Stat(powershell); err != nil {
		return "", ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	script := filepath.Join(home, ".codex", "win-gui-tools", "gui.ps1")
	if _, err := os.Stat(script); err != nil {
		return "", ""
	}
	// gui.ps1 要用 Windows 的路径形式传给 powershell.exe（WSL 路径它认不了）
	out, err := exec.Command("wslpath", "-w", script).Output()
	if err != nil {
		return "", ""
	}
	winPath := strings.TrimSpace(string(out))
	if winPath == "" {
		return "", ""
	}
	return powershell, winPath
}

// macroSlug 用意图的哈希当目录名：意图里有中文/符号也不怕，改名不会撞。
func macroSlug(intent string) string {
	sum := sha1.Sum([]byte(intent))
	return macroDirPrefix + hex.EncodeToString(sum[:4])
}

func (a *Agent) macroPath(intent string) string {
	return filepath.Join(a.macroRoot(), macroSlug(intent), "macro.json")
}

// macros 读取技能库（按最近使用排序）。
func (a *Agent) macros() []AgentMacro {
	entries, err := os.ReadDir(a.macroRoot())
	if err != nil {
		return nil
	}
	list := make([]AgentMacro, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), macroDirPrefix) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.macroRoot(), entry.Name(), "macro.json"))
		if err != nil {
			continue
		}
		var macro AgentMacro
		if err := json.Unmarshal(raw, &macro); err != nil || macro.Intent == "" {
			continue
		}
		list = append(list, macro)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastRunAt > list[j].LastRunAt })
	return list
}

// saveMacro 落盘一条技能（顺带写一份 SKILL.md，让人和模型都能看懂它是干什么的）。
func (a *Agent) saveMacro(macro AgentMacro) error {
	dir := filepath.Join(a.macroRoot(), macroSlug(macro.Intent))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(macro, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "macro.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(macroSkillDoc(macro)), 0o644)
}

// macroSkillDoc 生成技能说明：这就是"技能化"的产物 —— 模型下次也能读到它。
func macroSkillDoc(macro AgentMacro) string {
	var steps strings.Builder
	for i, step := range macro.Steps {
		if step.Tool == "skill" {
			var args map[string]string
			_ = json.Unmarshal(step.Args, &args)
			fmt.Fprintf(&steps, "%d. 调用子技能：%s\n", i+1, args["prompt"])
			continue
		}
		args := strings.TrimSpace(string(step.Args))
		if args == "" {
			args = "{}"
		}
		fmt.Fprintf(&steps, "%d. `%s %s`\n", i+1, step.Tool, args)
	}
	state := "可用"
	if macro.Disabled {
		state = "已停用（直跑连续失败过）"
	} else if macro.Successes < macroEnableAfter {
		state = fmt.Sprintf("观察中（成功 %d 次后才直跑）", macro.Successes)
	}
	return fmt.Sprintf(`---
name: %s
description: 聊天室里「%s」这类任务的固定做法，由服务在任务成功后自动记录。
---

# %s

- 状态：%s
- 成功次数：%d；直跑连续失败：%d
- 最近使用：%s

## 固定步骤（直跑时按顺序执行，模型不参与）

%s
## 注意

- 直跑任一步不符合预期（工具报失败、点击后界面没变化）就自动放弃，交回模型重新决策。
- 想改做法：直接在聊天里让 Codex 重做一次，成功后这串步骤会被覆盖成新的。
`, macroSlug(macro.Intent), macro.Prompt, macro.Prompt, state, macro.Successes, macro.Failures, formatMacroTime(macro.LastRunAt), steps.String())
}

func formatMacroTime(ms int64) string {
	if ms == 0 {
		return "从未"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// recordMacro 在一次**成功**任务后调用：把这次的工具调用序列沉淀成技能。
func (a *Agent) recordMacro(task *agentTask) {
	task.mu.Lock()
	prompt := task.Prompt
	rawSteps := append([]AgentMacroStep(nil), task.goodSteps...)
	task.mu.Unlock()

	steps, blocked := macroUsableSteps(rawSteps)
	if blocked != "" {
		// 别让这种"洞"静默存在：任务成功了却固化不下来，必须当场说出来
		task.add("warn", "这次任务没有被固化成技能："+blocked)
		log.Printf("技能：任务「%s」没有固化成技能 —— %s", prompt, blocked)
		return
	}
	if len(steps) < len(rawSteps) {
		task.add("note", fmt.Sprintf("技能：固化 %d 步（另有 %d 步是诊断/只读步骤，已丢弃）", len(steps), len(rawSteps)-len(steps)))
	}
	intent := experienceKey(prompt)
	if intent == "" {
		return
	}

	// 复用已有子技能：开头几步和某条已固化技能一模一样时，用一步「调用子技能」代替，
	// 免得换个说法就把同一段流程重新记一遍（也避免子流程变化后两处不一致）
	if folded := a.foldSubSkills(steps, intent); len(folded) < len(steps) {
		task.add("note", fmt.Sprintf("技能：开头 %d 步与已有技能重复，改成调用子技能（共 %d 步）", len(steps)-len(folded)+1, len(folded)))
		steps = folded
	}

	var macro AgentMacro
	if existing, ok := a.macroByIntent(intent); ok {
		macro = existing
	} else {
		macro = AgentMacro{Intent: intent, CreatedAt: time.Now().UnixMilli()}
	}
	macro.Prompt = prompt
	macro.Steps = steps
	macro.Successes++
	macro.Failures = 0
	macro.LastError = ""
	// 模型刚把这条路走通了一次 → 说明这套做法仍然有效，解除停用
	// （否则一次"环境不在预期状态"的失败就会把技能永久钉死）
	macro.Disabled = false
	if err := a.saveMacro(macro); err != nil {
		log.Printf("技能：写入失败（%s）：%v", intent, err)
		return
	}
	if macro.Successes == macroEnableAfter {
		log.Printf("技能：已固化「%s」（%d 步），下次同类任务直接直跑", prompt, len(steps))
	}
}

// macroUsableSteps 只保留"会改变状态"的步骤：诊断（gui_read/gui_windows）和 dry_run 不要，
// 它们在直跑里只是浪费时间。
//
// 返回 (步骤, 不能固化时的原因)。**超限时返回原因而不是截断**：截断会留下"半条流程"，
// 直跑时前半段通过就报成功，属于假成功。
func macroUsableSteps(raw []AgentMacroStep) ([]AgentMacroStep, string) {
	out := make([]AgentMacroStep, 0, len(raw))
	for _, step := range raw {
		switch step.Tool {
		case "gui_open", "gui_click", "gui_type", "gui_shot":
		case "shell":
			// 只读/探索类命令不记（ls、cat、find…），只记"干活"的命令
			var args map[string]string
			_ = json.Unmarshal(step.Args, &args)
			if !shellStepUsable(args["command"]) {
				continue
			}
		default:
			continue
		}
		if step.Tool == "gui_click" {
			var args map[string]any
			_ = json.Unmarshal(step.Args, &args)
			if dry, _ := args["dry_run"].(bool); dry {
				continue
			}
		}
		out = append(out, step)
	}
	if len(out) == 0 {
		return nil, fmt.Sprintf("记录到 %d 个步骤，全部被规则丢弃（只认 gui_open/gui_click/gui_type/gui_shot 和\"干活的\" shell 命令；gui_read/gui_windows/dry_run 以及 ls/cat/find 这类只读命令会丢）", len(raw))
	}
	if len(out) > macroMaxSteps {
		return nil, fmt.Sprintf("步骤太多（%d 步 > 上限 %d），不固化成技能：截断会留半条流程、直跑会假成功", len(out), macroMaxSteps)
	}
	return out, ""
}

// macroIgnoredShell 是"看一眼"类的命令前缀：这些留在技能里只会浪费时间。
var macroIgnoredShell = []string{
	"ls", "cat", "head", "tail", "find", "grep", "rg", "ps", "sed", "awk", "wc",
	"echo", "which", "where", "date", "pwd", "env", "file", "stat", "pgrep", "id", "whoami",
}

// shellStepUsable 判断一条 shell 命令值不值得记进技能：拆掉 `/bin/bash -lc '...'` 外壳，
// 看真正的第一个词是不是"只看不干"的命令。
func shellStepUsable(command string) bool {
	inner := shellInnerCommand(command)
	if inner == "" {
		return false
	}
	fields := strings.Fields(inner)
	if len(fields) == 0 {
		return false
	}
	first := strings.ToLower(filepath.Base(fields[0]))
	for _, ignored := range macroIgnoredShell {
		if first == ignored {
			return false
		}
	}
	return true
}

// shellInnerCommand 拆掉 `/bin/bash -lc '...'` 这层壳，拿到真正执行的命令。
func shellInnerCommand(command string) string {
	inner := strings.TrimSpace(command)
	for _, wrapper := range []string{"/bin/bash -lc ", "/bin/bash -c ", "bash -lc ", "bash -c "} {
		if strings.HasPrefix(inner, wrapper) {
			inner = strings.TrimSpace(strings.TrimPrefix(inner, wrapper))
			inner = strings.Trim(inner, "'\"")
			break
		}
	}
	return inner
}

// macroByIntent 按意图找技能（不判断是否可用，调用方各自判断）。
// foldSubSkills 把"和已有技能一样的一段步骤"折叠成一步「调用子技能」。
//
// 为什么不是只比前缀：像「打开WeGame并登录，然后截屏」这种复合任务，前半段是"登录"技能、
// 后半段是"截屏"技能 —— 匹配要能命中任意位置的一段，而且参数比较要按"身份"比，
// 不能因为 `restart:true`、截图文件名不同就认为不是同一件事。
//
// excludeIntent 是当前正在录的这条技能（别把自己折进去）。
func (a *Agent) foldSubSkills(steps []AgentMacroStep, excludeIntent string) []AgentMacroStep {
	macros := a.macros()
	for pass := 0; pass < 5; pass++ {
		changed := false
		for i := 0; i < len(steps); i++ {
			best := AgentMacro{}
			for _, macro := range macros {
				if macro.Intent == excludeIntent || macro.Disabled || len(macro.Steps) < 2 {
					continue
				}
				if i+len(macro.Steps) > len(steps) {
					continue
				}
				if !macroStepsSimilar(macro.Steps, steps[i:i+len(macro.Steps)]) {
					continue
				}
				if len(macro.Steps) > len(best.Steps) {
					best = macro
				}
			}
			if best.Intent == "" {
				continue
			}
			args, err := json.Marshal(map[string]string{"intent": best.Intent, "prompt": best.Prompt})
			if err != nil {
				continue
			}
			folded := make([]AgentMacroStep, 0, len(steps)-len(best.Steps)+1)
			folded = append(folded, steps[:i]...)
			folded = append(folded, AgentMacroStep{Tool: "skill", Args: args})
			folded = append(folded, steps[i+len(best.Steps):]...)
			if len(folded) >= len(steps) {
				continue
			}
			steps = folded
			changed = true
			break
		}
		if !changed {
			break
		}
	}
	return steps
}

// macroStepsSimilar 按"身份"比较两段步骤（见 stepCompareKey）。
func macroStepsSimilar(a, b []AgentMacroStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if stepCompareKey(a[i]) != stepCompareKey(b[i]) {
			return false
		}
	}
	return true
}

// stepCompareKey 生成"这一步在干什么"的规范化表示：
//   - gui_open 忽略 restart（那只是"要不要先强杀"的策略）
//   - gui_shot 忽略 save_to（存哪个文件名是干活细节）
//   - shell 取内层命令、归一化空白并把 4 位以上数字串折叠成 #（文件名里的时间戳不影响"是同一件事"）
func stepCompareKey(step AgentMacroStep) string {
	if step.Tool == "shell" {
		var args map[string]string
		_ = json.Unmarshal(step.Args, &args)
		inner := strings.Join(strings.Fields(shellInnerCommand(args["command"])), " ")
		return "shell " + collapseDigits(inner)
	}
	var m map[string]any
	_ = json.Unmarshal(step.Args, &m)
	switch step.Tool {
	case "gui_open":
		delete(m, "restart")
	case "gui_shot":
		delete(m, "save_to")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return step.Tool
	}
	return step.Tool + " " + string(raw)
}

var (
	digitRun  = regexp.MustCompile(`\d{4,}`)   // 时间戳本身（20261009）
	digitTail = regexp.MustCompile(`#[-_]\d+`) // 时间戳后面的序号（shot-20261009-9.png → shot-#.png）
)

// collapseDigits 把文件名里的时间戳/序号抹平，让"同一个文件的新旧两次生成"看起来是同一件事。
// 注意：这会牺牲一点区分度（数字不同的命令会被当成同一件事），但子技能本身有复验和自动停用兜底，
// 而"同一段流程因为文件名不同就重录一遍"的代价更大。
func collapseDigits(text string) string {
	out := digitRun.ReplaceAllString(text, "#")
	return digitTail.ReplaceAllString(out, "#")
}

// macroByIntent 按意图找技能（不判断是否可用，调用方各自判断）。
func (a *Agent) macroByIntent(intent string) (AgentMacro, bool) {
	for _, macro := range a.macros() {
		if macro.Intent == intent {
			return macro, true
		}
	}
	return AgentMacro{}, false
}

// findRunnableMacro 找出可以直跑的技能：同意图、没停用、成功次数够。
// deleteMacro 删掉一条技能（主机在控制台点"删除"）。
func (a *Agent) deleteMacro(intent string) error {
	return os.RemoveAll(filepath.Join(a.macroRoot(), macroSlug(intent)))
}

// findRunnableMacro 找出可以直跑的技能：同意图、没停用、成功次数够。
func (a *Agent) findRunnableMacro(prompt string) (AgentMacro, bool) {
	intent := experienceKey(prompt)
	if intent == "" {
		return AgentMacro{}, false
	}
	macro, ok := a.macroByIntent(intent)
	if !ok || macro.Disabled || macro.Successes < macroEnableAfter || len(macro.Steps) == 0 {
		return AgentMacro{}, false
	}
	return macro, true
}

// macroStepArgs 把一条记录的工具调用翻译成 gui.ps1 的参数。
func macroStepArgs(step AgentMacroStep, forceRestart bool) ([]string, error) {
	var args map[string]any
	if len(step.Args) > 0 {
		_ = json.Unmarshal(step.Args, &args)
	}
	str := func(key string) string {
		if value, ok := args[key].(string); ok {
			return value
		}
		return ""
	}
	num := func(key string) int {
		if value, ok := args[key].(float64); ok {
			return int(value)
		}
		return -1
	}
	switch step.Tool {
	case "gui_open":
		if str("path") == "" {
			return nil, errors.New("技能里的 gui_open 缺 path")
		}
		if forceRestart {
			// 第二遍：程序已经登录/停在别的页面时，"点登录"必然找不到按钮 ——
			// 这时唯一可靠的做法是把程序重启回初始界面（WeGame 重启后就是登录页）
			if forceRestart {
				return []string{"-Action", "open", "-Path", str("path"), "-Json", "-Restart", "-SkipCooldown"}, nil
			}
		}
		// 一律带 -Escalate：让 gui_open 自己"先温和唤醒、不行再强杀重启"。
		// 这样技能里不需要存两种变体，也不会撞上 30 秒重启冷却（实测踩过两次：
		// ① 冷却挡掉重启 → 窗口没重开 → 找不到按钮；② 子技能里存的是温和版 → 唤醒失败就整条失败）
		return []string{"-Action", "open", "-Path", str("path"), "-Json", "-Escalate"}, nil

	case "gui_click":
		out := []string{"-Action", "click", "-Json"}
		if text := str("text"); text != "" {
			out = append(out, "-Text", text)
		}
		if name := str("name"); name != "" {
			out = append(out, "-Name", name)
		}
		if window := str("window"); window != "" {
			out = append(out, "-Window", window)
		}
		if x := num("x"); x >= 0 {
			out = append(out, "-X", strconv.Itoa(x))
			if y := num("y"); y >= 0 {
				out = append(out, "-Y", strconv.Itoa(y))
			}
		}
		if len(out) <= 3 {
			return nil, errors.New("技能里的 gui_click 没有点击目标")
		}
		return out, nil

	case "gui_type":
		if str("text") == "" {
			return nil, errors.New("技能里的 gui_type 缺 text")
		}
		out := []string{"-Action", "type", "-Text", str("text"), "-Json"}
		if window := str("window"); window != "" {
			out = append(out, "-Window", window)
		}
		return out, nil

	case "gui_shot":
		out := []string{"-Action", "shot", "-Json"}
		if saveTo := str("save_to"); saveTo != "" {
			out = append(out, "-SaveTo", saveTo)
		}
		return out, nil
	}
	return nil, fmt.Errorf("技能里有不认识的工具：%s", step.Tool)
}

// runMacro 直跑一条技能。返回 true 表示整条技能执行并复验通过。
func (a *Agent) runMacro(job agentJob, task *agentTask, macro AgentMacro) bool {
	if a.powershell == "" || a.guiScript == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	err := a.runMacroSteps(ctx, task, macro.Steps, 0, false)
	// 第二遍：第一遍失败且技能里有"开程序"的步骤时，强制重启该程序再整条重跑。
	// 覆盖"程序已经登录/停在其他页面"这种日常情况（那时第一遍点按钮必然找不到目标）。
	if err != nil && macroHasOpenStep(macro.Steps) {
		task.add("macro", fmt.Sprintf("第一遍没跑通（%v）：强制重启程序后再跑一遍", err))
		err = a.runMacroSteps(ctx, task, macro.Steps, 0, true)
	}
	if err != nil {
		return a.macroFailed(macro, err)
	}
	a.macroSucceeded(macro)
	return true
}

// macroHasOpenStep 判断技能里（含子技能）有没有"开程序"的步骤。
func macroHasOpenStep(steps []AgentMacroStep) bool {
	for _, step := range steps {
		if step.Tool == "gui_open" {
			return true
		}
	}
	return false
}

// runMacroSteps 按顺序执行一串步骤；任一步没通过就返回错误（调用方决定是回退还是换原版再试）。
func (a *Agent) runMacroSteps(ctx context.Context, task *agentTask, steps []AgentMacroStep, depth int, forceRestart bool) error {
	for index, step := range steps {
		// 子技能：按引用去跑另一条技能（当初就是同一段流程，没必要抄一遍）
		if step.Tool == "skill" {
			var args map[string]string
			_ = json.Unmarshal(step.Args, &args)
			if depth >= macroMaxDepth {
				return fmt.Errorf("步骤 %d：子技能嵌套太深（上限 %d）", index+1, macroMaxDepth)
			}
			sub, ok := a.macroByIntent(args["intent"])
			if !ok || sub.Disabled || len(sub.Steps) == 0 {
				return fmt.Errorf("步骤 %d：子技能「%s」不可用", index+1, args["prompt"])
			}
			task.add("macro", fmt.Sprintf("步骤 %d/%d：调用子技能「%s」（%d 步）", index+1, len(steps), sub.Prompt, len(sub.Steps)))
			if err := a.runMacroSteps(ctx, task, sub.Steps, depth+1, forceRestart); err != nil {
				return fmt.Errorf("子技能「%s」没跑通：%w", sub.Prompt, err)
			}
			continue
		}
		// 点击类步骤允许重试一次：刚被拉起来的窗口界面可能还没渲染完（实测：WeGame 重启后
		// 立刻 OCR 只读到零星文字，过一两秒才出现登录页）
		attempts := 1
		if step.Tool == "gui_click" {
			attempts = 2
		}
		var stepErr error
		for attempt := 1; attempt <= attempts; attempt++ {
			stepErr = a.macroStep(ctx, task, step, index+1, len(steps), forceRestart)
			if stepErr == nil {
				break
			}
			if attempt < attempts {
				task.add("macro", fmt.Sprintf("第 %d 步没成功，等 2 秒重试一次：%v", index+1, stepErr))
				time.Sleep(2 * time.Second)
			}
		}
		if stepErr != nil {
			return stepErr
		}
		// 刚把程序拉起来时，给它一点渲染时间再点（模型自己跑时天然有这几秒间隔）
		if step.Tool == "gui_open" {
			time.Sleep(macroSettleAfter)
		}
	}
	return nil
}

// macroSucceeded 记一次直跑成功。
func (a *Agent) macroSucceeded(macro AgentMacro) {
	macro.Successes++ // 直跑成功也算一次成功经验
	macro.Failures = 0
	macro.LastRunAt = time.Now().UnixMilli()
	if err := a.saveMacro(macro); err != nil {
		log.Printf("技能：更新失败：%v", err)
	}
	log.Printf("技能：直跑成功「%s」（%d 步）", macro.Prompt, len(macro.Steps))
}

// macroStep 执行一步并判定它是否真的生效。err 非 nil 表示这一步没通过。
func (a *Agent) macroStep(ctx context.Context, task *agentTask, step AgentMacroStep, index, total int, forceRestart bool) error {
	// shell 步：直接回放当初那条命令（记录时已经滤掉"只看不干"的命令）
	if step.Tool == "shell" {
		var args map[string]string
		_ = json.Unmarshal(step.Args, &args)
		command := strings.TrimSpace(args["command"])
		if command == "" {
			return fmt.Errorf("步骤 %d（shell）没有命令", index)
		}
		stepCtx, stepCancel := context.WithTimeout(ctx, macroStepTimeout*time.Second)
		defer stepCancel()
		out, err := exec.CommandContext(stepCtx, "/bin/bash", "-lc", command).CombinedOutput()
		text := strings.TrimSpace(string(out))
		task.add("macro", fmt.Sprintf("步骤 %d/%d：%s", index, total, truncate(command, 200)))
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				task.add("macro", truncate(line, 300))
			}
		}
		if err != nil {
			return fmt.Errorf("步骤 %d（shell）执行失败：%v", index, err)
		}
		// 命令自己报告失败时也算没通过（比如 send-file.sh 拒发旧文件）
		for _, bad := range []string{"拒绝发送", "文件不存在", `"result":"failed"`} {
			if strings.Contains(text, bad) {
				return fmt.Errorf("步骤 %d（shell）报告失败：%s", index, bad)
			}
		}
		return nil
	}

	args, err := macroStepArgs(step, forceRestart)
	if err != nil {
		return err
	}
	stepCtx, stepCancel := context.WithTimeout(ctx, macroStepTimeout*time.Second)
	defer stepCancel()
	cmd := exec.CommandContext(stepCtx, a.powershell,
		append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", a.guiScript}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))

	task.add("macro", fmt.Sprintf("步骤 %d/%d：gui.ps1 %s", index, total, strings.Join(args, " ")))
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			task.add("macro", truncate(line, 300))
		}
	}

	if err != nil {
		return fmt.Errorf("步骤 %d（%s）执行失败：%v", index, step.Tool, err)
	}
	if strings.Contains(text, `"result":"failed"`) {
		return fmt.Errorf("步骤 %d（%s）工具报告失败", index, step.Tool)
	}
	// 冷却跳过会报 result=success，但**重启根本没发生** —— 宏不能把这种"什么都没做"当成功
	if strings.Contains(text, `"status":"skipped_cooldown"`) {
		return fmt.Errorf("步骤 %d（%s）被重启冷却挡掉了，实际没有执行", index, step.Tool)
	}
	// 点击类步骤必须真的改变了界面，否则就是"点了没生效"（不许盲报成功）
	if step.Tool == "gui_click" && strings.Contains(text, `"status":"unchanged"`) {
		return fmt.Errorf("步骤 %d（点击）后界面没有变化", index)
	}
	return nil
}

// macroFailed 记一次直跑失败；连续失败到上限就停用，避免坏技能一直乱跑。
func (a *Agent) macroFailed(macro AgentMacro, err error) bool {
	macro.Failures++
	macro.LastRunAt = time.Now().UnixMilli()
	macro.LastError = err.Error()
	if macro.Failures >= macroMaxFailures {
		macro.Disabled = true
	}
	if saveErr := a.saveMacro(macro); saveErr != nil {
		log.Printf("技能：更新失败：%v", saveErr)
	}
	log.Printf("技能：直跑失败「%s」：%v（连续失败 %d 次）", macro.Prompt, err, macro.Failures)
	return false
}
