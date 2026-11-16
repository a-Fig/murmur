package hook

import "io"

// Antigravity's hook (#650). Its payload names no event, so join's command line does, and join
// installs it on two:
//
//   - PreInvocation, before each call of the model: at a turn's start and after each tool call. It
//     stands for both UserPromptSubmit and PostToolUse, whose answer Antigravity ignores. On the
//     turn's first call its lastUserInput is the prompt, which marks a message handed over as the
//     turn read (#611). What it gives back goes in as a system message, which stays in the conversation as
//     Claude Code's additionalContext does.
//   - Stop, at the end of the work, with why it ended: only NO_TOOL_CALL, a turn's ordinary end,
//     can be kept going, by the answer "continue" and the messages as its reason.
//
// The session is the conversation, whose ID Antigravity puts in what it runs
// (ANTIGRAVITY_CONVERSATION_ID; seen in its shell, not yet in an MCP server, where the daemon
// otherwise pairs the hook with the server among its ancestors). The hook runs in the folder of hooks.json, so the workspace it
// names stands for the working directory.

// fromAntigravity puts what Antigravity's payload says for event in Claude Code's fields, and says
// whether the hook acts on event.
func fromAntigravity(in *input, event string) bool {
	in.SessionID, in.Event, in.Prompt = in.ConversationID, "", ""
	if len(in.WorkspacePaths) > 0 {
		in.Cwd = in.WorkspacePaths[0]
	}
	switch event {
	case "PreInvocation":
		in.Event = "UserPromptSubmit"
		if in.InvocationNum <= 1 {
			in.Prompt = in.LastUserInput
		}
	case "Stop":
		in.Event, in.Reason = stop, "end_turn"
		if in.TerminationReason != "" && in.TerminationReason != "NO_TOOL_CALL" {
			in.Reason = in.TerminationReason
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
