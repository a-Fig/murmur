package mcpserver

import (
	"context"
	"os"

	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/t3"
)

// initHere sets up the server's record in the per-user directory, which lets `mur join` find this
// session and move it to the repo it joined (package here).
func (s *server) initHere() {
	dir := s.opt.HereDir
	if dir == "" {
		var err error
		if dir, err = here.DefaultDir(); err != nil {
			dir = "-"
		}
	}
	if dir == "-" {
		return
	}
	var claude string
	if isClaude(s.opt.Agent) {
		claude = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	thread := os.Getenv(t3.ThreadEnv)
	if thread != "" && agentAbove(s.opt.Agent) {
		thread = "" // A client started by another agent may inherit its thread ID.
	}
	s.hereDir = dir
	s.self = here.Session{ServerPID: os.Getpid(), ClientPID: os.Getppid(), ClientStarted: here.Started(os.Getppid()),
		ClaudeSession: claude, T3Thread: thread, Agent: agentName(s.opt.Agent), Fixed: sharedAgent(s.opt.Agent)}
}

func (s *server) withdraw() {
	if s.hereDir != "" {
		here.Withdraw(s.hereDir, s.self.ServerPID)
	}
}

// follow moves the server to the repo `mur join` asked it to, if it asked since the last look.
func (s *server) follow(ctx context.Context) {
	if s.hereDir == "" || s.self.Fixed {
		return
	}
	m, ok := here.Asked(s.hereDir, s.self)
	if !ok || !m.At.After(s.followed) {
		return
	}
	s.followed = m.At
	p, err := join.Locate(ctx, m.Dir)
	if err != nil {
		return
	}
	s.mu.Lock()
	same := p == s.place
	s.mu.Unlock()
	if same {
		return
	}
	// Off in the old repo first: its registration goes, and its daemon sees the session end.
	s.deactivate()
	s.mu.Lock()
	s.place, s.cwd = p, m.Dir
	s.mu.Unlock()
}

// publish rewrites the server's record when what it says has changed.
func (s *server) publish() {
	if s.hereDir == "" {
		return
	}
	s.mu.Lock()
	rec := s.self
	rec.GitDir, rec.On = s.place.GitDir, s.on
	l := s.link
	s.mu.Unlock()
	if l != nil {
		// The agent's own ID for the session, once known: the hook finds its session's record by
		// it (#422).
		l.mu.Lock()
		rec.Native = l.native
		if l.hello.T3Thread != "" {
			rec.T3Thread = l.hello.T3Thread
		}
		l.mu.Unlock()
	}
	// Written again if it went missing: a command that couldn't tell it from a dead server's may
	// have removed it.
	if s.published != nil && *s.published == rec && here.Exists(s.hereDir, rec.ServerPID) {
		return
	}
	if here.Publish(s.hereDir, rec) == nil {
		s.published = &rec
	}
}

// placeNow is the repo the server is in, which a move can change.
func (s *server) placeNow() join.Place {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.place
}
