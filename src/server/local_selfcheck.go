package main

// 本地版专属：技能覆盖自查（`bin/lanfile-server -selfcheck`）。
//
// 为什么需要它：宏（技能）只认某些步骤（gui_open/click/type/shot + 干活的 shell 命令），
// 别的步骤会被静默丢掉。于是会出现"某个任务做一百次也不会被技能化"这种洞，而且只有人去翻
// 聊天记录才发现。这个自查把历史上做过的任务逐条过一遍，直接告诉人：哪些能固化、哪些不可能、
// 为什么不可能。
//
// 关键点：它**复用服务端同一套规则**（macroUsableSteps），所以不会出现"脚本说能、实际不能"的漂移。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type selfCheckRow struct {
	Prompt   string
	Commands int
	Keepable int
	Macro    *AgentMacro
	Dropped  []string
	LastUsed int64
	Runnable bool
}

// dropReason 说明这一步为什么不会被固化 —— 报告里要能一眼看出原因，
// 而不是丢一堆原始命令（以前那种输出没人看得下去）。
func dropReason(step AgentMacroStep) string {
	switch step.Tool {
	case "gui_read", "gui_windows":
		return "诊断步骤（" + step.Tool + "）"
	case "gui_click", "gui_open", "gui_type", "gui_shot":
		var args map[string]any
		_ = json.Unmarshal(step.Args, &args)
		if dry, _ := args["dry_run"].(bool); dry {
			return "dry_run 试点击"
		}
		return "被规则过滤（" + step.Tool + "）"
	case "shell":
		var args map[string]string
		_ = json.Unmarshal(step.Args, &args)
		first := ""
		if fields := strings.Fields(shellInnerCommand(args["command"])); len(fields) > 0 {
			first = filepath.Base(fields[0])
		}
		return "只读命令（" + first + "）"
	default:
		return "不认识的工具（" + step.Tool + "）"
	}
}

// runSelfCheck 打印技能覆盖自查表。
func runSelfCheck(dataDir string) {
	home := filepath.Join(dataDir, "agent-home")
	expPath := filepath.Join(home, "experiences.json")
	skillsDir := filepath.Join(home, "skills")

	raw, err := os.ReadFile(expPath)
	if err != nil {
		fmt.Printf("读不到经验库：%s（%v）\n", expPath, err)
		fmt.Println("提示：服务至少成功跑过一次任务才会有这个文件。")
		return
	}
	var experiences []AgentExperience
	if err := json.Unmarshal(raw, &experiences); err != nil {
		fmt.Printf("经验库解析失败：%v\n", err)
		return
	}
	sort.SliceStable(experiences, func(i, j int) bool { return experiences[i].LastUsed > experiences[j].LastUsed })

	macros := loadMacrosFrom(skillsDir)
	byIntent := make(map[string]*AgentMacro, len(macros))
	for i := range macros {
		byIntent[macros[i].Intent] = &macros[i]
	}

	rows := make([]selfCheckRow, 0, len(experiences))
	for _, exp := range experiences {
		row := selfCheckRow{Prompt: exp.Prompt, Commands: len(exp.Commands), LastUsed: exp.LastUsed}
		for _, command := range exp.Commands {
			step, ok := classifyRecordedCommand(command)
			if !ok {
				row.Dropped = append(row.Dropped, "（无法解析）"+truncate(command, 60))
				continue
			}
			if kept, _ := macroUsableSteps([]AgentMacroStep{step}); len(kept) > 0 {
				row.Keepable++
			} else {
				row.Dropped = append(row.Dropped, dropReason(step))
			}
		}
		if macro, ok := byIntent[experienceKey(exp.Prompt)]; ok {
			row.Macro = macro
			row.Runnable = !macro.Disabled && macro.Successes >= macroEnableAfter
		}
		rows = append(rows, row)
	}

	fmt.Println("技能覆盖自查（数据目录：" + dataDir + "）")
	fmt.Println(strings.Repeat("-", 100))
	fmt.Printf("%-28s %-6s %-8s %-14s %s\n", "任务意图", "命令数", "可固化", "技能状态", "被丢弃的步骤")
	fmt.Println(strings.Repeat("-", 100))

	never := 0
	for _, row := range rows {
		status := "未固化"
		switch {
		case row.Macro != nil && row.Runnable:
			status = fmt.Sprintf("可直跑(成功%d)", row.Macro.Successes)
		case row.Macro != nil && row.Macro.Disabled:
			status = "已停用(直跑连续失败)"
		case row.Macro != nil:
			status = "已记录(待观察)"
		case row.Keepable == 0:
			status = "**永远不会固化**"
			never++
		}
		fmt.Printf("%-28s %-6d %-8d %-14s %s\n",
			truncate(row.Prompt, 26), row.Commands, row.Keepable, status, truncate(strings.Join(row.Dropped, " ｜ "), 46))
	}
	fmt.Println(strings.Repeat("-", 100))
	if never > 0 {
		fmt.Printf("⚠️  有 %d 个意图不可能被固化成技能（所有步骤都是只读/诊断类）——这些任务每次都要模型跑\n", never)
	}
	fmt.Println("说明：可固化 = 步骤里有 gui_open/gui_click/gui_type/gui_shot 或\"干活的 shell 命令\"。")
	fmt.Println("      未固化但可固化 = 再成功跑一次就会自动沉淀。")
}

// loadMacrosFrom 直接读技能库（不依赖 Agent 实例）。
func loadMacrosFrom(skillsDir string) []AgentMacro {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	list := make([]AgentMacro, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), macroDirPrefix) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(skillsDir, entry.Name(), "macro.json"))
		if err != nil {
			continue
		}
		var macro AgentMacro
		if err := json.Unmarshal(raw, &macro); err == nil && macro.Intent != "" {
			list = append(list, macro)
		}
	}
	return list
}

// classifyRecordedCommand 把经验库里记的一条命令还原成"一步"：
// 以 gui_ 开头的是工具调用（后面跟 JSON 参数），其余按 shell 命令算。
func classifyRecordedCommand(command string) (AgentMacroStep, bool) {
	text := strings.TrimSpace(command)
	if text == "" {
		return AgentMacroStep{}, false
	}
	name, rest, _ := strings.Cut(text, " ")
	if !strings.HasPrefix(name, "gui_") {
		args, _ := json.Marshal(map[string]string{"command": text})
		return AgentMacroStep{Tool: "shell", Args: args}, true
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "{") {
		rest = "{}"
	}
	return AgentMacroStep{Tool: name, Args: json.RawMessage(rest)}, true
}

// formatSelfCheckTime 让自查表里的时间好读。
func formatSelfCheckTime(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).Format("01-02 15:04")
}
