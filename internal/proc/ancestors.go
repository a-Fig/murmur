package proc

import (
	"io"
	"os"
	"strconv"
	"strings"
)

// maxStatBytes bounds reads of Linux's /proc/<pid>/stat (typically ~300-500 bytes).
const maxStatBytes = 8 << 10

// Ancestors is pid and the processes above it, nearest first, at most max of them: pid's parent,
// that one's parent, and so on. It stops early where a parent can't be found, and stops at pid
// alone where this system has no way to look (see parentOf).
//
// The hook of an agent that gives hooks no process of its own to name (Codex, Grok) sends these, so
// the daemon can find the MCP server the same agent process started: Codex runs a hook through a
// shell, so the agent is its grandparent, not its parent.
func Ancestors(pid, max int) []int {
	return ancestorsOf(pid, max)
}

func walkAncestors(pid, max int, parent func(int) (int, bool)) []int {
	var out []int
	seen := map[int]bool{}
	for len(out) < max && pid > 0 && !seen[pid] {
		seen[pid] = true
		out = append(out, pid)
		if len(out) >= max {
			break
		}
		p, ok := parent(pid)
		if !ok || p <= 0 {
			break
		}
		pid = p
	}
	return out
}

// readLinuxStatPPID reads at most maxStatBytes from path and parses the Linux /proc/<pid>/stat
// PPID field. Files larger than maxStatBytes are rejected rather than truncated so a huge comm
// field containing ')' cannot be misparsed.
func readLinuxStatPPID(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxStatBytes+1))
	if err != nil || len(b) > maxStatBytes {
		return 0, false
	}
	return parseLinuxStatPPID(b)
}

// parseLinuxStatPPID parses the fourth field (PPID) of Linux's /proc/<pid>/stat. The second field,
// the command's name in parentheses, may hold spaces, parentheses, and newlines, so fields are
// counted from the last closing parenthesis.
func parseLinuxStatPPID(b []byte) (int, bool) {
	s := string(b)
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close <= open {
		return 0, false
	}
	f := strings.Fields(s[close+1:])
	if len(f) < 2 || len(f[0]) != 1 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil || ppid <= 0 {
		return 0, false
	}
	return ppid, true
}

// parentNewerThanChild reports true only when both creation times are known and parent was created
// strictly after child, proving Windows reused the dead parent's PID for an unrelated newer
// process. Any failure to read either creation time returns false so harness detection continues.
func parentNewerThanChild(child, parent int, creationOf func(int) (uint64, bool)) bool {
	childCreated, ok := creationOf(child)
	if !ok || childCreated == 0 {
		return false
	}
	parentCreated, ok := creationOf(parent)
	if !ok || parentCreated == 0 {
		return false
	}
	return parentCreated > childCreated
}
