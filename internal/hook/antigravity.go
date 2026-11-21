package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

// Antigravity's hook (#650, #892, #894). Its payload names no event, so join's command line does,
// and join installs it on four:
//
//   - PreInvocation, before each call of the model: at a turn's start and after each tool call. It
//     stands for both UserPromptSubmit and PostToolUse, whose answer Antigravity ignores. On the
//     turn's first call (invocationNum == 0, 0-indexed in Antigravity 1.5.13, 2026-11-20) its
//     lastUserInput is the prompt, which marks a message handed over as the turn read (#611). What
//     it gives back goes in as a system message, which stays in the conversation as Claude Code's
//     additionalContext does.
//   - Stop, at the end of the work, with why it ended: only NO_TOOL_CALL with fullyIdle absent or
//     true can be kept going, by the answer "continue" and the messages as its reason.
//   - PreToolUse and PostToolUse, matched on run_command: bracket shell commands so the daemon
//     credits the commits they make (#894). PreToolUse must answer {"decision":"allow"} so
//     Antigravity delegates to the next hook / default policy without auto-approving or blocking;
//     PostToolUse answers {} and fetches no notices, leaving notices to the following PreInvocation.
//
// The session is the conversation, whose ID Antigravity puts in what it runs
// (ANTIGRAVITY_CONVERSATION_ID; seen in its shell, not yet in an MCP server, where the daemon
// otherwise pairs the hook with the server among its ancestors). Top-level cwd is kept when
// present (Antigravity 1.5.13, 2026-11-20), falling back to workspacePaths[0]; on PreToolUse and
// PostToolUse, a non-empty toolCall.args.Cwd overrides it so brackets track the command's repo.

// antigravityToolCall is the toolCall object in Antigravity's PreToolUse and PostToolUse payloads.
type antigravityToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// fromAntigravity puts what Antigravity's payload says for event in Claude Code's fields, and says
// whether the hook acts on event.
func fromAntigravity(in *input, event string) bool {
	in.SessionID, in.Event, in.Prompt, in.ToolName, in.ToolUseID = in.ConversationID, "", "", "", ""
	if in.Cwd == "" && len(in.WorkspacePaths) > 0 {
		in.Cwd = in.WorkspacePaths[0]
	}
	switch event {
	case "PreInvocation":
		in.Event = "UserPromptSubmit"
		// PreInvocation's invocationNum is 0-indexed (Antigravity 1.5.13, 2026-11-20): 0 is the
		// turn's first model call, where lastUserInput is the prompt that began the turn.
		if in.InvocationNum == 0 {
			in.Prompt = in.LastUserInput
		}
	case "Stop":
		in.Event, in.Reason = stop, "end_turn"
		if in.TerminationReason != "" && in.TerminationReason != "NO_TOOL_CALL" {
			in.Reason = in.TerminationReason
		} else if in.FullyIdle != nil && !*in.FullyIdle {
			// Background tasks are still running, so don't force a continuation.
			in.Reason = "not_idle"
		}
	case preTool, postTool:
		in.Event = event
		if event == postTool && in.Error != "" {
			in.Event = postFailure
		}
		if in.ToolCall != nil {
			in.ToolName = in.ToolCall.Name
			in.ToolUseID = in.ToolCall.ID
			if in.ToolUseID == "" && in.StepIdx != nil {
				in.ToolUseID = fmt.Sprintf("step-%d", *in.StepIdx)
			}
			var args struct {
				Cwd string `json:"Cwd"`
			}
			if len(in.ToolCall.Args) > 0 && json.Unmarshal(in.ToolCall.Args, &args) == nil && args.Cwd != "" {
				if filepath.IsAbs(args.Cwd) || in.Cwd == "" {
					in.Cwd = args.Cwd
				} else {
					in.Cwd = filepath.Join(in.Cwd, args.Cwd)
				}
			}
		}
	default:
		return false
	}
	return true
}

// antigravitySteps is PreInvocation's answer that gives the agent text.
func antigravitySteps(text string) any {
	type message struct {
		SystemMessage string `json:"systemMessage"`
	}
	type step struct {
		SystemMessage message `json:"systemMessage"`
	}
	return struct {
		InjectSteps []step `json:"injectSteps"`
	}{[]step{{message{text}}}}
}

// written passes writes on to w, and remembers whether there were any.
type written struct {
	w   io.Writer
	any bool
}

func (w *written) Write(p []byte) (int, error) {
	w.any = w.any || len(p) > 0
	return w.w.Write(p)
}
