package daemon

import (
	"context"
	"time"
)

// Sessions the shell commands register (#913). A harness Murmur has no tools for can still run
// `mur who` and `mur send` in its T3 thread's shell, which has the thread's ID in T3CODE_THREAD_ID.
// The command's hello (proto.Hello.Command) can't stay open, as an MCP server's does, so the session
// is held instead: it stays live, with its name and its route into the thread, for as long as the
// thread is in use. That is until the thread has been idle for CommandIdle (no run in T3's database,
// no command), or T3 shows it settled, archived or deleted. Then it stops as any session does, a
// minute's grace included, and its route stays kept (T3Routes), so a message for it still wakes the
// thread, as it does a thread whose MCP server T3 stopped. The next command registers it again, at
// the same address and name: the ID is made from the thread.
//
// Not held across a daemon restart: a restarted daemon has the session stopped, as it has any
// session whose server didn't come back, and the next command registers it again.

// CommandIdle is how long a command's session is held with its thread idle: about as long as T3
// keeps an idle thread's provider running, and so its MCP server and session (docs/design/handoff.md,
// "A thread that is idle"). A variable, for tests.
var CommandIdle = 30 * time.Minute

// holdCheck is how often a held session's thread is looked at. A variable, for tests.
var holdCheck = time.Minute

// commandHold is a held session's hold. end is closed when it is let go; taken says another hello
// took the session over, which then carries it on, and not that it ended.
type commandHold struct {
	end   chan struct{}
	taken bool
}

// liveInThreadLocked is a session live in T3 thread (home, thread), nil if none is. Caller holds mu.
func (d *Daemon) liveInThreadLocked(home, thread string) *session {
	var found *session
	for _, s := range d.sessions {
		if s.dropped.IsZero() && s.route.T3Thread == thread && s.route.T3Home == home {
			if s.hold == nil {
				return s // a session with a server of its own over one a command holds
			}
			found = s
		}
	}
	return found
}

// releaseHoldLocked lets s's hold go: taken if another hello takes s over, else s ends. Caller holds
// mu.
func (d *Daemon) releaseHoldLocked(s *session, taken bool) {
	if s == nil || s.hold == nil {
		return
	}
	s.hold.taken = taken
	close(s.hold.end)
	s.hold = nil
}

// holdOverLocked says whether the thread of held session s is done with: settled in T3's database,
// or idle for CommandIdle. Caller holds mu.
func (d *Daemon) holdOverLocked(s *session, now time.Time) bool {
	if st, ok := d.threadStateLocked(s.id); ok && st.Settled {
		return true
	}
	since, busy := d.idleSinceLocked(s.id)
	if busy {
		return false
	}
	return now.Sub(later(since, s.started, s.lastActive)) > CommandIdle
}

// waitHold holds session s until its hold h ends, and reports whether s ends with it: false when
// another hello took s over.
func (d *Daemon) waitHold(ctx context.Context, s *session, h *commandHold) bool {
	tick := time.NewTicker(holdCheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-h.end:
			d.mu.Lock()
			defer d.mu.Unlock()
			return !h.taken
		case <-tick.C:
			d.mu.Lock()
			over := s.hold == h && d.holdOverLocked(s, time.Now())
			if over {
				d.releaseHoldLocked(s, false)
				d.log.Printf("session %s, which commands registered, is done with: its thread is idle or settled", s.id)
			}
			d.mu.Unlock()
			if over {
				return true
			}
		}
	}
}

// byCommandLocked says whether session id has only the shell commands, so that its turns say to
// answer with `mur send`: a session a command registered, or a stopped one whose kept route says
// so. Caller holds mu.
func (d *Daemon) byCommandLocked(id string) bool {
	if s := d.sessions[id]; s != nil {
		return s.byCommand
	}
	return d.st.T3Routes[id].Command
}
