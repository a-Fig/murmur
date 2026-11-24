package daemon

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/a-Fig/murmur/internal/msgsock"
)

// Sessions the shell commands register (#913). A harness Murmur has no tools for can still run
// `mur who` and `mur send` in its T3 thread's shell, which has the thread's ID in T3CODE_THREAD_ID.
// The command's hello (proto.Hello.Command) can't stay open, as an MCP server's does, so the session
// is held instead: it stays live, with its name and its route into the thread, for as long as the
// thread is in use. That is until the thread has been idle for commandIdle (no run in T3's database,
// no command), or T3 shows it settled, archived or deleted. Then it stops as any session does, a
// minute's grace included, and its route stays kept (T3Routes), so a message for it still wakes the
// thread, as it does a thread whose MCP server T3 stopped. The next command registers it again, at
// the same address and name: the ID is made from the thread.
//
// `mur tool` and `mur hook`, which Murmur's Hermes plugin runs for each tool call and hook (#975,
// #1184), register its session the same way, keyed by the session's own ID (proto.Route.Native) with
// no thread. Its hold lasts for as long as the socket the plugin opened for it answers (socketHeld)
// and it hasn't been idle for socketIdle, or where it names none, until the session has been idle for
// commandIdle. With no T3 thread to read, idle is since its last call or hook.
//
// Not held across a daemon restart: a restarted daemon has the session stopped, as it has any
// session whose server didn't come back, and the next command registers it again.

// commandIdle is how long a command's session is held with its thread idle: about as long as T3
// keeps an idle thread's provider running, and so its MCP server and session (docs/design/handoff.md,
// "A thread that is idle"). Atomic, for tests: one sets it while a daemon's holds read it (#1189).
var commandIdle = durationVar(30 * time.Minute)

// socketIdle is how long a session whose plugin's socket still answers is held idle. Hermes keeps a
// session live after its ACP client goes, for as long as the gateway runs, so the socket alone would
// hold it for days; a day's work is about as long as a person leaves a session they will come back to.
var socketIdle = durationVar(8 * time.Hour)

// holdCheck is how often a held session's thread is looked at. Atomic, as commandIdle is.
var holdCheck = durationVar(time.Minute)

// durationVar is d, in nanoseconds, in a variable read and set atomically.
func durationVar(d time.Duration) *atomic.Int64 {
	v := new(atomic.Int64)
	v.Store(int64(d))
	return v
}

// commandHold is a held session's hold. end is closed when it is let go; taken says another hello
// took the session over, which then carries it on, and not that it ended. A hold let go otherwise
// starts the session's grace there and then, under mu, with takeover the grace's channel: a session
// that finds the thread at that moment (retireRoutesLocked) then takes its name, which a session
// still live would keep from it (uniqueKeyLocked).
type commandHold struct {
	end      chan struct{}
	taken    bool
	takeover chan struct{}
}

// liveInThreadLocked is the session live in T3 thread (home, thread), nil if none is. Where there
// are two, one with a server of its own stands over one a command holds, and one keyed by the thread
// (so with its name) over another, then the lower ID: the same one each time. Caller holds mu.
func (d *Daemon) liveInThreadLocked(home, thread string) *session {
	if thread == "" {
		return nil
	}
	key := nameKey(thread, "", "", "")
	rank := func(s *session) int {
		r := 0
		if s.hold != nil {
			r += 2
		}
		if s.key != key {
			r++
		}
		return r
	}
	var found *session
	for _, s := range d.sessions {
		if !s.dropped.IsZero() || s.route.T3Thread != thread || s.route.T3Home != home {
			continue
		}
		if found == nil || rank(s) < rank(found) || rank(s) == rank(found) && s.id < found.id {
			found = s
		}
	}
	return found
}

// releaseHeldInThreadLocked lets go every session a command holds in T3 thread (home, thread) but
// except. Caller holds mu.
func (d *Daemon) releaseHeldInThreadLocked(home, thread, except string) {
	for id, s := range d.sessions {
		if id != except && s.hold != nil && s.route.T3Thread == thread && s.route.T3Home == home {
			d.releaseHoldLocked(s, false)
		}
	}
}

// releaseHoldLocked lets s's hold go: taken if another hello takes s over, else s ends. Caller holds
// mu.
func (d *Daemon) releaseHoldLocked(s *session, taken bool) {
	if s == nil || s.hold == nil {
		return
	}
	s.hold.taken = taken
	if !taken {
		s.hold.takeover = make(chan struct{})
		s.dropped, s.takeover = time.Now(), s.hold.takeover
	}
	close(s.hold.end)
	s.hold = nil
}

// holdOverLocked says whether the thread of held session s is done with: settled in T3's database,
// or idle for longer than idle. Caller holds mu.
func (d *Daemon) holdOverLocked(s *session, now time.Time, idle time.Duration) bool {
	if st, ok := d.threadStateLocked(s.id); ok && st.Settled {
		return true
	}
	since, busy := d.idleSinceLocked(s.id)
	if busy {
		return false
	}
	return now.Sub(later(since, s.started, s.lastActive)) > idle
}

// socketHeld says held session s is one `mur tool` or `mur hook` keyed by its own ID with a socket
// its plugin opened for it (Hermes, #1184), and gives the socket. Such a session is held for as long
// as the socket answers, however long it is idle: Hermes can start a turn in a session for as long
// as a client has it open, and the plugin closes the socket when the session closes or Hermes
// refuses it a turn. The gateway's going takes the socket with it. socketIdle caps it. Caller holds
// mu.
func socketHeld(s *session) (string, bool) {
	r := s.route
	if !s.byCommand || r.T3Thread != "" || r.Native == "" || r.MsgSocket == "" || r.MsgToken == "" {
		return "", false
	}
	return r.MsgSocket, true
}

// waitHold holds session s until its hold h ends, and reports whether s ends with it: false when
// another hello took s over.
func (d *Daemon) waitHold(ctx context.Context, s *session, h *commandHold) bool {
	tick := time.NewTicker(time.Duration(holdCheck.Load()))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			d.mu.Lock()
			defer d.mu.Unlock()
			return !h.taken // a hello that took the session over just now stops it itself
		case <-h.end:
			d.mu.Lock()
			defer d.mu.Unlock()
			return !h.taken
		case <-tick.C:
			d.mu.Lock()
			sock, bySocket := "", false
			if s.hold == h {
				sock, bySocket = socketHeld(s)
			}
			idle := time.Duration(commandIdle.Load())
			if bySocket {
				idle = time.Duration(socketIdle.Load())
			}
			over := s.hold == h && d.holdOverLocked(s, time.Now(), idle)
			if over {
				d.releaseHoldLocked(s, false)
				d.log.Printf("session %s, which commands registered, is done with: it is idle, or its thread settled", s.id)
			}
			d.mu.Unlock()
			if over {
				return true
			}
			// Asked outside the lock: a socket answers at once, or not at all within msgsock's limit.
			if bySocket && !msgsock.Answers(ctx, sock) && ctx.Err() == nil {
				d.mu.Lock()
				gone := s.hold == h && s.route.MsgSocket == sock
				if gone {
					d.releaseHoldLocked(s, false)
					d.log.Printf("session %s, which commands registered, is done with: its plugin's socket doesn't answer", s.id)
				}
				d.mu.Unlock()
				if gone {
					return true
				}
			}
		}
	}
}

// byCommandLocked says whether session id has only the shell commands, so that its turns say to
// answer with `mur send`: a session a command registered in a T3 thread, or a stopped one whose kept
// route says so. One `mur tool` registered by its own ID, for Hermes (#975), has Murmur's tools.
// Caller holds mu.
func (d *Daemon) byCommandLocked(id string) bool {
	if s := d.sessions[id]; s != nil {
		return s.byCommand && s.route.T3Thread != ""
	}
	return d.st.T3Routes[id].Command
}
