package main

// 技能（宏）机制的回归测试：这几个判断以前是靠"跑一遍真任务才发现不对"的，
// 现在钉成测试，改完先跑 `go test ./src/server`。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func step(tool string, args string) AgentMacroStep {
	return AgentMacroStep{Tool: tool, Args: json.RawMessage(args)}
}

// 新建一个只有技能库的 Agent（home 指向临时目录）。
func newMacroTestAgent(t *testing.T, macros ...AgentMacro) *Agent {
	t.Helper()
	home := t.TempDir()
	agent := &Agent{home: home}
	for _, macro := range macros {
		if err := agent.saveMacro(macro); err != nil {
			t.Fatalf("写入技能失败：%v", err)
		}
	}
	return agent
}

// 复合任务应该折叠成"子技能引用"，而不是把子流程抄一遍。
func TestFoldSubSkills(t *testing.T) {
	login := AgentMacro{
		Intent: "打开wegame并登录", Prompt: "@Codex 打开WeGame并登录",
		Steps: []AgentMacroStep{
			step("gui_open", `{"path":"wegame","restart":true}`),
			step("gui_click", `{"text":"登录","window":"WeGame"}`),
		},
	}
	shot := AgentMacro{
		Intent: "截屏", Prompt: "截屏 @Codex",
		Steps: []AgentMacroStep{
			step("gui_shot", `{"save_to":"C:\\Users\\Public\\shot-20261009-1.png"}`),
			step("shell", `{"command":"/bin/bash -lc 'send-file.sh /mnt/c/Users/Public/shot-20261009-1.png \"截屏\"'"}`),
		},
	}
	agent := newMacroTestAgent(t, login, shot)

	// 复合任务的原始步骤：restart 与文件名都和子技能不完全一样，仍应被认成同一段流程
	raw := []AgentMacroStep{
		step("gui_open", `{"path":"wegame"}`),
		step("gui_click", `{"window":"WeGame","text":"登录"}`),
		step("gui_shot", `{"save_to":"C:\\Users\\Public\\shot-20261009-9.png"}`),
		step("shell", `{"command":"/bin/bash -lc 'send-file.sh /mnt/c/Users/Public/shot-20261009-9.png \"截屏\"'"}`),
	}
	folded := agent.foldSubSkills(raw, "打开wegame并登录然后截屏")

	if len(folded) != 2 {
		t.Fatalf("期望折叠成 2 步（两个子技能），实际 %d 步：%+v", len(folded), folded)
	}
	for _, want := range []string{"打开wegame并登录", "截屏"} {
		found := false
		for _, s := range folded {
			var args map[string]string
			_ = json.Unmarshal(s.Args, &args)
			if s.Tool == "skill" && args["intent"] == want {
				found = true
			}
		}
		if !found {
			t.Errorf("折叠结果里缺少子技能 %q：%+v", want, folded)
		}
	}
}

// 步骤超上限时不能截断成"半条流程"，而是拒录并给出原因。
func TestMacroUsableStepsRefusesOverflow(t *testing.T) {
	raw := make([]AgentMacroStep, 0, macroMaxSteps+1)
	for i := 0; i <= macroMaxSteps; i++ {
		raw = append(raw, step("gui_click", `{"text":"x","window":"W"}`))
	}
	steps, reason := macroUsableSteps(raw)
	if len(steps) != 0 || reason == "" {
		t.Fatalf("超过上限 %d 步时应拒录并给原因，实际 steps=%d reason=%q", macroMaxSteps, len(steps), reason)
	}
}

// 全是只读命令的任务：不给假技能，要说清为什么。
func TestMacroUsableStepsRejectsReadOnlyOnly(t *testing.T) {
	raw := []AgentMacroStep{step("shell", `{"command":"date '+%F %T'"}`)}
	steps, reason := macroUsableSteps(raw)
	if len(steps) != 0 || reason == "" {
		t.Fatalf("全是只读命令时应拒录并给原因，实际 steps=%d reason=%q", len(steps), reason)
	}
}

// 干活的 shell 命令要能被固化（截屏任务的关键一步）。
func TestMacroUsableStepsKeepsWorkingShell(t *testing.T) {
	raw := []AgentMacroStep{
		step("gui_shot", `{"save_to":"C:\\Users\\Public\\a.png"}`),
		step("shell", `{"command":"/bin/bash -lc 'send-file.sh /mnt/c/Users/Public/a.png \"截屏\"'"}`),
		step("gui_read", `{}`),
		step("gui_click", `{"text":"登录","dry_run":true}`),
	}
	steps, reason := macroUsableSteps(raw)
	if reason != "" {
		t.Fatalf("这组步骤应该能固化，却给了原因：%s", reason)
	}
	if len(steps) != 2 {
		t.Fatalf("应该只留 gui_shot + shell 两步，实际 %d 步：%+v", len(steps), steps)
	}
}

// 技能里的 gui_open 必须带 -Escalate：让它自己"先温和唤醒、不行再重启"。
// 技能里不用存两种变体，也不会被 30 秒重启冷却挡掉（这两点都实测踩过）。
func TestMacroOpenArgsEscalate(t *testing.T) {
	args, err := macroStepArgs(step("gui_open", `{"path":"wegame","restart":true}`), false)
	if err != nil {
		t.Fatalf("生成参数失败：%v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-Escalate") {
		t.Fatalf("gui_open 的参数里应带 -Escalate，实际：%s", joined)
	}
	if strings.Contains(joined, "-Restart") {
		t.Fatalf("不该再硬编码 -Restart（交给 -Escalate 自己决定），实际：%s", joined)
	}
}

// 技能文件能读回来（落盘格式没坏）。
func TestMacroRoundTrip(t *testing.T) {
	agent := newMacroTestAgent(t)
	macro := AgentMacro{Intent: "测试意图", Prompt: "@Codex 测试", Steps: []AgentMacroStep{
		step("gui_open", `{"path":"wegame"}`),
		step("gui_click", `{"text":"登录","window":"WeGame"}`),
	}}
	if err := agent.saveMacro(macro); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(agent.macroRoot(), macroSlug(macro.Intent), "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 没生成：%v", err)
	}
	got, ok := agent.macroByIntent("测试意图")
	if !ok || len(got.Steps) != 2 {
		t.Fatalf("读回的技能不对：%+v", got)
	}
}
