package main

// 本地版专属：Codex 控制台的两个 HTTP 端点（仅主机可访问）。
// 服务器版不需要它 —— 那里不开 -agent，页面上也不会出现控制台入口。

import (
	"log"
	"net/http"
	"strings"
)

/* ---------------- Codex 控制台（仅主机） ---------------- */

// handleAgentTasks 返回最近的任务列表（含实时输出），只给主机看。
func (a *App) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	if !a.auth.IdentityOf(nil, r).IsHost() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只有主机能查看 Codex 控制台"})
		return
	}

	tasks := a.agent.Tasks() // agent 为 nil 时返回空列表
	running, queued := 0, 0
	var todayTokens int64
	for _, task := range tasks {
		switch task.Status {
		case "running":
			running++
		case "queued":
			queued++
		}
		if task.Usage != nil {
			todayTokens += task.Usage.InputTokens + task.Usage.OutputTokens
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     a.agent != nil,
		"trigger":     a.agentTrigger,
		"cwd":         a.agentCwd,
		"timeout":     int(a.agentTimeout.Seconds()),
		"running":     running,
		"queued":      queued,
		"tasks":       tasks,
		"totalTokens": todayTokens,
	})
}

// handleAgentMacros 处理 /api/agent/macros：
//
//	GET                → 列出技能（宏）
//	DELETE ?intent=... → 删除某条技能
//
// 技能由服务在任务成功后自动沉淀（同一意图成功 2 次即可直跑），这里只做查看与清理。
func (a *App) handleAgentMacros(w http.ResponseWriter, r *http.Request) {
	if !a.auth.IdentityOf(nil, r).IsHost() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只有主机能管理技能库"})
		return
	}
	if a.agent == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "macros": []AgentMacro{}})
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "macros": a.agent.macros()})

	case http.MethodDelete:
		intent := r.URL.Query().Get("intent")
		if intent == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 intent"})
			return
		}
		if err := a.agent.deleteMacro(intent); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("主机删除了技能：%s", intent)
		writeJSON(w, http.StatusOK, map[string]string{"ok": "已删除"})

	default:
		http.NotFound(w, r)
	}
}

// handleAgentTaskAction 处理 /api/agent/tasks/{id}/kill。
func (a *App) handleAgentTaskAction(w http.ResponseWriter, r *http.Request) {
	if !a.auth.IdentityOf(nil, r).IsHost() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只有主机能操作 Codex 任务"})
		return
	}

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "kill" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	if a.agent == nil || !a.agent.Kill(parts[0]) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在或已经结束"})
		return
	}
	log.Printf("主机终止了 Codex 任务 %s", parts[0])
	writeJSON(w, http.StatusOK, map[string]string{"ok": "已终止"})
}

// handleWhoami 返回当前请求的身份，前端判断与排查都用得上。
