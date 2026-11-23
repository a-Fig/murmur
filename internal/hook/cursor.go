package hook

import (
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/here"
)

// Cursor's hook (#922). Join puts `mur hook cursor` in ~/.cursor/hooks.json, which Cursor runs in
// every project. Read in the source of cursor-agent 2026.11.22-e9042b2 and of @cursor/sdk 1.3.1, the
// SDK T3 Code runs Cursor with, and in cursor.com/docs/agent/hooks (2026-11-22); not yet seen run,
// as no Cursor account was at hand:
//
//   - Every payload has conversation_id, the chat's ID across its turns (Cursor sets session_id to
//     it too), hook_event_name in Cursor's camelCase, cursor_version and workspace_roots. Tool
//     events add cwd, tool_name, tool_input, tool_use_id, and parent_tool_call_id for a call made
//     inside a subagent.
//   - sessionStart comes as a new chat starts, not a resumed one, and beforeSubmitPrompt before each
//     prompt, with the prompt. Both, and postToolUse, take additional_context, which goes into the
//     model's context.
//   - stop comes at a turn's end, with status "completed", "aborted" or "error". A followup_message
//     in its answer is submitted as the next user message, so the chat goes on; Cursor stops asking
//     after loop_limit of them in a row (5 by default).
//
// Cursor also runs Claude Code's hooks from ~/.claude/settings.json, the "third-party" setting
// that is on by default, under its own event names, so Claude Code's hook (agent "") steps aside
// for a payload with cursor_version. Cursor gives its MCP servers no conversation ID, only a few
// variables (PATH, the home's), so this hook names the conversation to the session's server
// (nameServer), which takes its ID from it.

// fromCursor puts what Cursor's payload says in Claude Code's fields, and says whether the hook
// acts on the event.
func fromCursor(in *input) bool {
	if in.ConversationIDSnake != "" {
		in.SessionID = in.ConversationIDSnake
	}
	switch in.Event {
	case "sessionStart":
		in.Event = "SessionStart"
	case "beforeSubmitPrompt":
		in.Event = "UserPromptSubmit"
	case "postToolUse":
		in.Event = postTool
	case "stop":
		in.Event, in.Reason = stop, "end_turn"
		if in.Status != "" && in.Status != "completed" {
			in.Reason = in.Status // aborted or error: nothing can be kept going
		}
	default:
		return false
	}
	if in.ParentToolCallID != "" {
		in.AgentID = in.ParentToolCallID // a subagent's call: what the hook prints goes to it
	}
	if in.Cwd == "" && len(in.WorkspaceRoots) > 0 {
		in.Cwd = in.WorkspaceRoots[0]
	}
	// Cursor's shell output isn't Claude Code's tool_response, so no issue is credited from it.
	in.ToolInput, in.ToolResponse = nil, nil
	return true
}

// cursorAnswer is what Cursor reads from the hook: additional_context on a prompt, a session's start
// or a tool call, followup_message at stop.
type cursorAnswer struct {
	AdditionalContext string `json:"additional_context,omitempty"`
	FollowupMessage   string `json:"followup_message,omitempty"`
}

// nameServer leaves the conversation's ID for the session's MCP server (here.Name), which waits for
// it before it registers (internal/mcpserver). The server is the one Cursor's process among the
// hook's ancestors started. Only where that process has one server of Cursor's, with no
// conversation yet: T3 Code runs every Cursor thread in its own process, and a hook there can't
// say which server is its thread's. A subagent's call names nothing, being another conversation.
func nameServer(in input, pids []int) {
	if in.SessionID == "" || in.AgentID != "" {
		return
	}
	dir, err := here.DefaultDir()
	if err != nil {
		return
	}
	var mine []here.Session
	for _, r := range here.Of(dir, pids) {
		if r.Agent == Cursor || strings.HasPrefix(r.Agent, Cursor+" ") {
			mine = append(mine, r)
		}
	}
	if len(mine) != 1 || mine[0].Native != "" {
		return
	}
	here.Name(dir, mine[0], in.SessionID, time.Now())
}
