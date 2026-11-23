package mcpserver

import (
	"context"
	"os"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/title"
)

// mutates says whether a call of op is one the daemon refuses a delegate (childRefusal): send,
// status, tags and mute. Those wait for the session's thread (see classifyFirst); who, lookup and
// inbox don't.
func mutates(op string) bool {
	return op == proto.OpSend || op == proto.OpStatus || op == proto.OpTags || op == proto.OpMute
}

// classifyFirst holds a call that changes something, in a Codex session in T3, while the server
// looks up the T3 thread of the Codex thread a tool call named and tells the daemon (#966): the
// daemon reads the thread's lineage as it hears the route, so a delegate's first send is refused
// rather than sent. A call that comes while the first look is going waits for it, which is
// bounded, the lookup by its timeout and the route by sendRoute's; the wait ends with the link
// too, since a server shutting down may never start the look. The caller has waited for the
// registration, whose reply is one in which the daemon says whether it read the thread.
//
// If the lookup failed, or the daemon couldn't read the thread (#980), the session looks again and
// tells the daemon again, once, which gives a thread whose row T3 wrote late its classification;
// every call that comes meanwhile waits for that one retry. Each registration gets its own: after
// a reconnect, the first such call may look once more (#1000). Whatever it finds, the calls then go
// on: they fail open, as a Claude Code session in T3 does (#945). A delegate's message going out is awkward, not harmful
// (Tyler, 2026-11-22, #993), and a T3 database Murmur can't read mustn't stop T3's sessions from
// sending. Later calls don't look again; the seek goes on doing that in the background.
func (l *link) classifyFirst(ctx context.Context) error {
	l.mu.Lock()
	looked, native := l.looked, l.native
	l.mu.Unlock()
	if looked == nil {
		return nil // not a Codex session in T3, or no call has named its thread
	}
	var gone <-chan struct{}
	if l.ctx != nil {
		gone = l.ctx.Done()
	}
	wait := func(c <-chan struct{}) error {
		select {
		case <-c:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-gone:
			return errPassive
		}
	}
	if err := wait(looked); err != nil {
		return err
	}
	l.mu.Lock()
	if l.native != native || l.classifiedLocked() {
		// Classified, or another conversation's ID came meanwhile, whose own look its calls wait
		// for: this call goes on.
		l.mu.Unlock()
		return nil
	}
	retry, first := l.retry, l.retry == nil
	if first {
		retry = make(chan struct{})
		l.retry = retry
	}
	l.mu.Unlock()
	if first {
		// In the link's context, not the call's: a call given up on mustn't use up the one retry.
		rctx := ctx
		if l.ctx != nil {
			rctx = l.ctx
		}
		l.spawn(func() {
			defer close(retry)
			if l.lookupThread(rctx, 3*time.Second); l.threadKnown() {
				l.tellRoute(rctx)
			}
		})
	}
	return wait(retry)
}

// codexCall is what a Codex tool call's _meta says of its session: the thread (`threadId`), and,
// in `x-codex-turn-metadata`, whether Codex started the thread for another (`thread_source`
// "subagent") and which thread did (`parent_thread_id`). Seen on Codex 0.164 and 0.167,
// 2026-11-12, for a thread spawn_agent made; a person's thread says "user".
type codexCall struct {
	thread, parent string
	child          bool
}

func codexCallOf(meta map[string]any) codexCall {
	c := codexCall{}
	c.thread, _ = meta["threadId"].(string)
	turn, _ := meta["x-codex-turn-metadata"].(map[string]any)
	if src, _ := turn["thread_source"].(string); src == "subagent" {
		c.child = true
		c.parent, _ = turn["parent_thread_id"].(string)
	}
	return c
}

// clientSession is the client's own ID for this session, which T3's database knows it by: a Codex
// thread ID if a tool call gave one, else the Claude Code session ID, the live one once a hook has
// corrected it. "" if there is none.
func (l *link) clientSession() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.clientSessionLocked()
}

// clientSessionLocked is clientSession for a caller that holds mu.
func (l *link) clientSessionLocked() string {
	if l.native != "" {
		return l.native
	}
	if l.hello.Native != "" {
		return l.hello.Native
	}
	return l.hello.ClaudeSession
}

// threadOf finds a T3 thread in T3's database; a variable, so that tests needn't have one.
var threadOf = t3db.ThreadOf

// findInT3 lets the link look for its T3 thread, if it runs inside T3 (its hello has T3's command
// line), which only the link's setup does: a link made in activate, on the first activation or on
// a reactivation after a leave and a rejoin, gets its finder here, since nothing else sets one.
func (l *link) findInT3() {
	cli, home := l.hello.T3CLI, l.hello.T3Home
	if cli == "" {
		return
	}
	find := threadOf
	l.find = func(ctx context.Context, native string) (string, error) {
		return find(ctx, cli, home, native)
	}
}

// classifiedLocked says whether the daemon has said it read the T3 thread the session is in now.
// Caller holds mu.
func (l *link) classifiedLocked() bool {
	return l.hello.T3Thread != "" && l.readThread == l.hello.T3Thread
}

// noteReadLocked takes what a reply to a hello or a route carrying route said of its T3 thread.
// Caller holds mu.
func (l *link) noteReadLocked(route proto.Route, resp proto.Response) {
	l.readThread = route.T3Thread
	if resp.ThreadUnread {
		l.readThread = ""
	}
}

// threadKnown says whether the T3 thread has been found.
func (l *link) threadKnown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hello.T3Thread != ""
}

// lookupThread makes one try at the T3 thread, for at most timeout, and takes it if found. T3's
// database is T3's, so any failure (no database, a lock, another schema, no row yet) only means
// the session keeps the keys it had before.
func (l *link) lookupThread(ctx context.Context, timeout time.Duration) {
	native := l.clientSession()
	if l.find == nil || native == "" || l.threadKnown() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	thread, err := l.find(ctx, native)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clientSessionLocked() != native {
		return // another conversation's ID came meanwhile, whose thread this isn't
	}
	if err == nil && thread != "" {
		l.hello.T3Thread = thread
	}
}

// The server goes on looking for its thread for seekFor, pausing seekFirst, then twice that, up to
// seekMax: T3 may not have written the row for a new session when the session starts its MCP
// servers, and writes it by the time the agent has anything to do. Variables, so tests needn't
// wait. A link copies them when it is made (see seekTimes): the goroutine that looks never reads
// them, so a test that puts them back when it ends doesn't race with a lookup still running.
var (
	seekFor   = 3 * time.Minute
	seekFirst = time.Second      // the pause before the first retry, doubled each time
	seekMax   = 15 * time.Second // and kept to this
)

// seekTimes is how a link looks for its thread: for how long, with what first pause, and the
// longest pause.
type seekTimes struct{ total, first, max time.Duration }

// currentSeek is the timings above as they are now, for a link that is being made.
func currentSeek() seekTimes { return seekTimes{seekFor, seekFirst, seekMax} }

// startSeek looks for the T3 thread in the background, with growing pauses, until it finds it, the
// time is up, or there is no client session ID to look for. When found it tells the daemon, which
// moves the session's name onto the thread. It does nothing outside T3, or while one is looking.
func (l *link) startSeek(ctx context.Context) {
	l.mu.Lock()
	if l.find == nil || l.seeking || l.hello.T3Thread != "" {
		l.mu.Unlock()
		return
	}
	l.seeking = true
	seek := l.seek
	l.mu.Unlock()
	l.spawn(func() {
		defer func() {
			l.mu.Lock()
			l.seeking = false
			l.mu.Unlock()
		}()
		deadline := time.Now().Add(seek.total)
		delay := seek.first
		for ctx.Err() == nil && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, seek.max)
			if l.lookupThread(ctx, 3*time.Second); l.threadKnown() {
				l.tellRoute(ctx)
				return
			}
		}
	})
}

// learnNative takes the client's own ID for the session when a tool call carries one (Codex's
// thread ID), and looks for the T3 thread with it.
func (l *link) learnNative(ctx context.Context, c codexCall) {
	id := c.thread
	if id == "" {
		return
	}
	if c.child {
		// Calls that come together wait for the first to have told the daemon (see below).
		l.childMu.Lock()
		defer l.childMu.Unlock()
	}
	l.mu.Lock()
	changed := l.native != id
	l.native = id
	l.hello.Native = id
	l.hello.Child, l.hello.ParentNative = c.child, c.parent
	var looked chan struct{}
	if changed {
		l.hello.T3Thread = "" // an ID for another conversation finds another thread
		l.retry = nil
		if l.find != nil {
			looked = make(chan struct{})
			l.looked = looked
		}
	}
	if !l.nativeClosed {
		close(l.nativeSeenLocked()) // a first hello held back for it goes now (see holdFirstHello)
		l.nativeClosed = true
	}
	l.mu.Unlock()
	if !changed {
		return
	}
	if l.find == nil {
		// Outside T3 the ID is for the daemon, to find the conversation's name in Codex's state.
		// Codex starts this server with a bare environment, but join's env_vars passes on
		// CODEX_HOME when the session has one (#88). So an unset one means the default home, and
		// the server names it: the daemon reads a route that names none, from a server older than
		// this, in its own CODEX_HOME, as that server expects.
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			home = title.DefaultCodexDir()
		}
		if home != "" {
			l.mu.Lock()
			l.hello.CodexHome = home
			l.mu.Unlock()
		}
		if c.child {
			// Before the call that brought it goes on: the daemon makes the session a child as it
			// hears this, and the call is then a child's (a send of its own is refused, #487).
			l.tellRoute(ctx)
			return
		}
		l.spawn(func() { l.tellRoute(ctx) })
		return
	}
	// The call that brought the ID goes on now, and so does any later one that only reads; one that
	// changes something waits for this (classifyFirst). A session that isn't registered yet gets the
	// thread into its hello instead, which the daemon reads the same way.
	l.spawn(func() {
		defer close(looked)
		if l.lookupThread(ctx, 3*time.Second); l.threadKnown() {
			l.tellRoute(ctx)
		} else {
			l.startSeek(ctx)
		}
	})
}
