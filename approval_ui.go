package main

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"
)

// Telegram limits message length after entity parsing; 1,400 runes also leaves
// room for supplementary Unicode characters (two UTF-16 units each).
func approvalDetails(details []byte, id string) []string {
	runes := []rune(string(details))
	var messages []string
	for start := 0; start < len(runes); start += 1400 {
		end := min(start+1400, len(runes))
		messages = append(messages, fmt.Sprintf("<b>Request details · %d/%d</b>\n<code>%s</code>\n<blockquote expandable>%s</blockquote>", start/1400+1, (len(runes)+1399)/1400, html.EscapeString(id[:8]), html.EscapeString(string(runes[start:end]))))
	}
	return messages
}

func preview(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit]) + "\n… Full content in request details above."
	}
	return s
}

func approvalCard(a approval, id string, expires time.Time) string {
	var params map[string]any
	_ = json.Unmarshal(a.Params, &params)
	input, _ := params["tool_input"].(map[string]any)
	text := func(m map[string]any, keys ...string) string {
		for _, key := range keys {
			if value, ok := m[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	tool := text(params, "tool_name")
	title := "Run command"
	switch {
	case a.Method == "item/permissions/requestApproval":
		title = "Grant permissions"
	case a.Method == "item/fileChange/requestApproval" || tool == "apply_patch" || tool == "Edit" || tool == "Write":
		title = "Change files"
	case tool != "" && tool != "Bash":
		title = "Use tool: " + preview(tool, 80)
	}
	workspace := text(params, "cwd", "workdir")
	if workspace == "" {
		workspace = text(input, "cwd", "workdir")
	}
	reason := text(params, "reason", "description")
	if reason == "" {
		reason = text(input, "description", "reason", "justification")
	}
	command := text(input, "command")
	if command == "" {
		command = text(params, "command")
	}
	var card strings.Builder
	card.WriteString("<b>Approval needed · " + html.EscapeString(title) + "</b>\n")
	if workspace != "" {
		card.WriteString("\n<b>Workspace</b>\n<code>" + html.EscapeString(preview(workspace, 240)) + "</code>\n")
	}
	if reason != "" {
		card.WriteString("\n<b>Why</b>\n" + html.EscapeString(preview(reason, 400)) + "\n")
	}
	if command != "" {
		card.WriteString("\n<b>" + map[bool]string{true: "Patch", false: "Command"}[title == "Change files"] + "</b>\n<pre>" + html.EscapeString(preview(command, 1000)) + "</pre>\n")
	}
	scope := "This request only"
	if a.Method == "item/permissions/requestApproval" {
		scope = "Requested permissions for this turn"
	}
	card.WriteString("\n<b>Scope:</b> " + scope + "\n<b>Expires:</b> " + expires.UTC().Format("15:04:05 UTC") + "\n")
	card.WriteString("<b>Host:</b> <code>" + html.EscapeString(preview(a.Host, 100)) + "</code>\n")
	card.WriteString("<code>" + html.EscapeString(id[:8]) + "</code> · Review full request details above.")
	return card.String()
}
