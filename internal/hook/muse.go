package hook

import "os"

// Muse Code's hook (#461). Muse takes Claude Code's hooks in its settings.json, with Claude Code's
// payload and answers: PostToolUse's additionalContext and UserPromptSubmit's reach the model, and
// Stop's {"decision":"block"} keeps the turn going with its reason (Muse 1.4.14 on Windows,
// 2026-11-16). Its session_id is the session's MUSE_SESSION_ID, which Murmur's MCP server registers
// with, so the daemon pairs the two as it does Grok's.
//
// Muse also runs side sessions of its own inside a turn (a reminder model that calls
// submit_reminder_decision), some after the turn's Stop. Their hooks carry the side session's own
// session_id and the real session's MUSE_SESSION_ID in the environment. What the hook gave one
// would land in that side session's context, and its call after Stop would mark the session at
// work again, so the hook skips them. Where the variable isn't set, nothing is skipped here, and the
// daemon finds no session with the side session's ID.
//
// Muse gives a hook a cut-down environment: on Windows COMSPEC, HOME, PATH, PATHEXT, SYSTEMROOT,
// TEMP, TMP, WINDIR and its own variables, with no APPDATA (seen 2026-11-16). The records of the
// sessions' MCP servers are found by APPDATA (here.DefaultDir), so fillUserDirs gives the hook
// Windows' own folders for the variables it lacks.

// museSessionEnv is where Muse gives what it runs the session's ID.
const museSessionEnv = "MUSE_SESSION_ID"

// museSideSession says a Muse hook payload is a side session's, not the session's.
func museSideSession(in input) bool {
	own := os.Getenv(museSessionEnv)
	return own != "" && in.SessionID != own
}
