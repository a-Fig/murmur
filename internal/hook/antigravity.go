package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

// Antigravity's hook (#650). Join names the event on the command line for compatibility with
// older payloads. It installs these events:
//
//   - PreInvocation, before each call of the model: at a turn's start and after each tool call. It
//     fetches notices. On the turn's first call its lastUserInput is the prompt, which marks a
//     message handed over as the turn read (#611). Notices go in as a system message.
//   - Stop, at the end of the work, with why it ended: only NO_TOOL_CALL, a turn's ordinary end,
//     can be kept going if background work is done, by "continue" with the messages as its reason.
//   - PreToolUse and PostToolUse identify the commits made during a run_command call (#894).
//
// Antigravity 1.5.13 supplies the conversation ID to hooks but omits it from the MCP server's
// environment. The daemon can pair the hook with the server among its ancestors.

const antigravityCommand = "run_command"

type antigravityToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// fromAntigravity puts what Antigravity's payload says for event in Claude Code's fields, and says
// whether the hook acts on event.
func fromAntigravity(in *input, event string) bool {
	in.SessionID, in.Event, in.Prompt, in.toolCallCwd = in.ConversationID, "", "", ""
	switch event {
	case "PreInvocation":
		in.Event = "UserPromptSubmit"
		// Antigravity 1.5.13 numbers invocations from zero and may omit the zero value.
		if in.InvocationNum == 0 {
			in.Prompt = in.LastUserInput
		}
	case "Stop":
		in.Event, in.Reason = stop, "end_turn"
		if in.TerminationReason != "" && in.TerminationReason != "NO_TOOL_CALL" {
			in.Reason = in.TerminationReason
		} else if in.FullyIdle != nil && !*in.FullyIdle {
			in.Reason = "not_idle"
		}
	case preTool, postTool:
		if in.ToolCall == nil {
			return false
		}
		in.Event = event
		if event == postTool && in.Error != "" {
			in.Event = postFailure
		}
		in.ToolName, in.ToolUseID = in.ToolCall.Name, in.ToolCall.ID
		if in.ToolUseID == "" && in.StepIdx != nil {
			in.ToolUseID = fmt.Sprintf("step-%d", *in.StepIdx)
		}
	default:
		return false
	}
	if in.Cwd == "" {
		for _, p := range in.WorkspacePaths {
			if p != "" {
				in.Cwd = p
				break
			}
		}
	}
	if (event == preTool || event == postTool) && in.ToolName == antigravityCommand && len(in.ToolCall.Args) > 0 {
		var args struct {
			Cwd string `json:"Cwd"`
		}
		if json.Unmarshal(in.ToolCall.Args, &args) == nil && args.Cwd != "" {
			cwd := args.Cwd
			if in.Cwd != "" && !filepath.IsAbs(cwd) {
				cwd = filepath.Join(in.Cwd, cwd)
			}
			in.toolCallCwd = cwd
		}
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
