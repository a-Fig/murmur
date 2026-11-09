// Package here lets a session say which repo it works in, when that isn't where it started (#200).
//
// The MCP server picks its repo from the directory its agent started it in. A session that started
// in a parent folder, the home directory or a workspace that holds several repos, and then works in
// one of them, would otherwise never get a name there. So each MCP server publishes a small record
// of itself in a per-user directory, and `mur join`, run by the agent, finds the record of its own
// session by process (the agent's process is the server's parent, and an ancestor of the command)
// and leaves a request for that server to move to the repo it joined. The server looks for one
// every time it looks for a join or a leave.
//
// A session is in one repo at a time: the tools take no repo, so a session in two meshes would
// need one on every call and every message. Moving leaves the repo it was in.
package here

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
)

// Session is what an MCP server says about itself.
type Session struct {
	ServerPID     int    `json:"serverPid"`
	ClientPID     int    `json:"clientPid"`               // its parent: the agent's process
	ClientStarted string `json:"clientStarted,omitempty"` // when that process started, to tell it from a later one with its PID
	ClaudeSession string `json:"claudeSession,omitempty"` // Claude Code's session ID, as the server started with it
	Agent         string `json:"agent,omitempty"`
	GitDir        string `json:"gitDir,omitempty"` // the clone it is placed in, "" if none
	On            bool   `json:"on,omitempty"`     // registered with that clone's daemon, tools listed
	// Fixed says the server can't be moved: one OpenCode server serves every session in its
	// directory, so moving it for one would move them all. Its record still stops the search up
	// the process tree, or join would move an agent further up (a Claude Code that started it).
	Fixed bool `json:"fixed,omitempty"`
}

// Move asks the server of the agent process ClientPID to work in Dir.
type Move struct {
	ClientPID int `json:"clientPid"`
	// ClientStarted and ClaudeSession are the session's, as its server published them; a server
	// with others, which would be of a new process that took a dead one's PID, ignores the move.
	ClientStarted string    `json:"clientStarted,omitempty"`
	ClaudeSession string    `json:"claudeSession,omitempty"`
	Dir           string    `json:"dir"`
	At            time.Time `json:"at"`
}

// DefaultDir is the user's: in their config directory, unless
// brand.SessionsDirEnv names another.
func DefaultDir() (string, error) {
	if dir := os.Getenv(brand.SessionsDirEnv); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, brand.Name, "sessions"), nil
}

func sessionPath(dir string, serverPID int) string {
	return filepath.Join(dir, "server-"+strconv.Itoa(serverPID)+".json")
}

func movePath(dir string, clientPID int) string {
	return filepath.Join(dir, "move-"+strconv.Itoa(clientPID)+".json")
}

// Publish writes the server's record, replacing its last one.
func Publish(dir string, s Session) error {
	return writeJSON(sessionPath(dir, s.ServerPID), s)
}

// Withdraw removes the server's record, as it exits.
func Withdraw(dir string, serverPID int) { os.Remove(sessionPath(dir, serverPID)) }

// Sessions are the records of servers still running. Records and moves of processes that have
// exited are removed on the way. A file that can't be read is passed over, not removed: on Windows a
// read fails while the file is being replaced, and removing a live server's record would hide it
// until it next changed (seen in CI, 2026-11-08).
func Sessions(dir string) ([]Session, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(dir, name)
		if !strings.HasSuffix(name, ".json") {
			continue // a record or move still being written (writeJSON's temporary file)
		}
		switch {
		case strings.HasPrefix(name, "server-"):
			var s Session
			if readJSON(path, &s) != nil {
				continue
			}
			if !alive(s.ServerPID) {
				os.Remove(path)
				continue
			}
			out = append(out, s)
		case strings.HasPrefix(name, "move-"):
			var m Move
			if readJSON(path, &m) == nil && !alive(m.ClientPID) {
				os.Remove(path)
			}
		}
	}
	return out, nil
}

// Caller picks, from the running servers, the one of the session a command runs in: the one whose
// agent is the nearest of the command's ancestors. claudePID (Claude Code's CLAUDE_PID, in the
// environment of the commands it runs) counts only where the ancestors couldn't be read: another
// agent that Claude Code started inherits it, and its commands would move Claude's session instead
// of its own. Where one agent process runs several
// sessions (Codex's app server in T3), claudeSession tells them apart if it is known; otherwise
// they all come back, and the caller can't say which is its own.
func Caller(sessions []Session, ancestors []int, claudePID int, claudeSession string) []Session {
	pids := ancestors
	if len(pids) == 0 && claudePID != 0 {
		pids = []int{claudePID}
	}
	for _, pid := range pids {
		var found []Session
		for _, s := range sessions {
			if s.ClientPID == pid {
				found = append(found, s)
			}
		}
		if len(found) > 1 && claudeSession != "" {
			found = slices.DeleteFunc(found, func(s Session) bool { return s.ClaudeSession != claudeSession })
		}
		if len(found) > 0 {
			return found
		}
	}
	return nil
}

// Ask leaves the move for the session's server to find.
func Ask(dir string, s Session, workDir string, now time.Time) error {
	return writeJSON(movePath(dir, s.ClientPID), Move{ClientPID: s.ClientPID, ClientStarted: s.ClientStarted,
		ClaudeSession: s.ClaudeSession, Dir: workDir, At: now})
}

// Asked is the move left for the server whose record is self, if there is one for it.
func Asked(dir string, self Session) (Move, bool) {
	var m Move
	if readJSON(movePath(dir, self.ClientPID), &m) != nil || m.ClientPID != self.ClientPID || m.Dir == "" {
		return Move{}, false
	}
	if m.ClientStarted != self.ClientStarted || m.ClaudeSession != self.ClaudeSession {
		return Move{}, false
	}
	return m, true
}

// Ancestors are the command's ancestors, its parent first.
func Ancestors() []int {
	parents, err := parentsOf()
	if err != nil {
		return nil
	}
	var out []int
	seen := map[int]bool{os.Getpid(): true}
	for pid := parents[os.Getpid()]; pid > 0 && !seen[pid] && len(out) < 64; pid = parents[pid] {
		seen[pid] = true
		out = append(out, pid)
	}
	return out
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// Written whole, then renamed, so a reader never sees half of one.
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	// Windows refuses to replace a file another process has open, as a reader does for a moment.
	for try := 0; ; try++ {
		err := os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if try == 20 {
			os.Remove(tmp)
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Exists reports whether the server's record is in dir.
func Exists(dir string, serverPID int) bool {
	_, err := os.Stat(sessionPath(dir, serverPID))
	return err == nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
