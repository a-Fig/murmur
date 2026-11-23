// This file is the link: the session's registration with the daemon, which reconnects when the
// daemon goes away, tells it route changes, and holds a first hello for Codex's first call or
// Cursor's hook.

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
)

// link keeps the session registered: a hello connection that stays open while this process
// lives, re-made if the daemon restarts.
type link struct {
	c       *client.Client
	hello   proto.Hello
	allowed func() bool // asked before every registration; nil means always
	// find looks up the T3 thread of a client session ID, where the server is inside T3; nil
	// elsewhere.
	find func(ctx context.Context, native string) (thread string, err error)
	// seek is how find is retried, taken when the link is made.
	seek seekTimes

	mu      sync.Mutex
	up      bool
	err     error
	changed chan struct{} // closed and replaced whenever up or err changes
	// native is the client's own ID for the session where the environment doesn't have one: a
	// Codex thread ID, which Codex gives each tool call (see noteMeta). seeking says a goroutine
	// is looking for the T3 thread.
	native  string
	seeking bool
	// nativeSeen is closed once a tool call has given native, which a first hello may wait for
	// (holdFirstHello); made on first use, and nativeClosed says it is closed.
	nativeSeen   chan struct{}
	nativeClosed bool
	// looked is closed once the server, inside T3, has looked for the thread of the native ID it was
	// last given and told the daemon what it found; nil until a tool call gives one (see
	// classifyFirst).
	looked chan struct{}
	// readThread is the T3 thread the daemon's last reply to a hello or a route said it had read; ""
	// if none has (see classifyFirst).
	readThread string
	// retry is closed once the session has looked for the thread of native again, and told the
	// daemon, after the first look failed to get it classified; nil until a call starts that (see
	// classifyFirst), and again for a new native ID or a new registration with the daemon.
	retry chan struct{}
	// named is the conversation Cursor's hook named for this session's client, "" until it has
	// (here.Named, #921), and shared where the client runs other servers of Cursor's too, and so no
	// name will come; nil for every other agent.
	named func() (native string, shared bool)
	// called is closed at the session's first tool call, which ends the wait for named
	// (holdForName); calledOnce closes it.
	called     chan struct{}
	calledOnce sync.Once
	// childMu holds a subagent's calls while the first tells the daemon it is a child.
	childMu sync.Mutex
	// routeMu keeps the routes told to the daemon in order: each is read while it is held, and sent
	// before it is let go. The daemon takes a route whole, so an older one landing last would take
	// away a thread a newer one gave, and a delegate's sends with it (#966).
	routeMu sync.Mutex
	// routeWarned says the daemon's refusal of the route has been logged, which is done once. logf
	// writes the line; nil writes it to standard error.
	routeWarned bool
	logf        func(format string, args ...any)
	// ctx is the context the link runs in, and goBG starts its goroutines as work Serve waits for:
	// a route update can reach the daemon, and so start one. Both are nil in a link a test makes;
	// goroutines are then plain ones.
	ctx  context.Context
	goBG func(func())
}

// spawn runs f in a goroutine of the link's (see goBG).
func (l *link) spawn(f func()) {
	if l.goBG != nil {
		l.goBG(f)
		return
	}
	go f()
}

func (l *link) set(up bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.setLocked(up, err)
}

// setLocked is set for a caller that holds mu.
func (l *link) setLocked(up bool, err error) {
	l.up, l.err = up, err
	close(l.changed)
	l.changed = make(chan struct{})
}

// learn takes the Claude Code session ID the daemon holds for this session, which is the live one
// once a hook has corrected it, for any later hello. Without it a reconnect, after the daemon
// restarted, would bring back the ID this process's environment has, which went stale at the first
// /clear. Only the Claude ID changes: the session's own ID is the one it registered with.
func (l *link) learn(claude string) {
	if claude == "" {
		return
	}
	l.mu.Lock()
	l.hello.ClaudeSession = claude
	l.mu.Unlock()
}

// tellRoute gives the daemon the route as it is now, if the session is registered. A server that
// isn't yet, or whose daemon has gone, sends it in its next hello instead.
func (l *link) tellRoute(ctx context.Context) {
	l.routeMu.Lock()
	defer l.routeMu.Unlock()
	l.mu.Lock()
	up, id, route := l.up, l.hello.ID, l.hello.Route
	l.mu.Unlock()
	if !up {
		return
	}
	l.sendRoute(ctx, id, route)
}

// sendRoute tells the daemon route for session id. A daemon that doesn't take it leaves the session
// as it was, a child included, which Codex's state then makes one later: it fails open.
func (l *link) sendRoute(ctx context.Context, id string, route proto.Route) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := l.c.Do(cctx, proto.Request{Op: proto.OpRoute, Session: id, Route: &route})
	if err == nil && resp.Error != "" {
		err = errors.New(resp.Error)
	}
	if err == nil {
		l.mu.Lock()
		l.noteReadLocked(route, resp)
		l.mu.Unlock()
		return
	}
	// The daemon didn't take it: most likely one left running by an older mur that has no such request.
	// The session works without, but its messages arrive as notices and not as turns, which is worth
	// one line for whoever wonders why.
	l.mu.Lock()
	first := !l.routeWarned
	l.routeWarned = true
	l.mu.Unlock()
	if first {
		l.warn("the %s daemon didn't take the route to this session's client (%v); messages for it will show as notices instead of arriving as turns", brand.Product, err)
	}
}

// warn writes a line to the server's standard error, where MCP clients keep a server's log.
func (l *link) warn(format string, args ...any) {
	if l.logf != nil {
		l.logf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, brand.Command+" mcp: "+format+"\n", args...)
}

// wait returns the session ID once registered, or the last error after timeout.
func (l *link) wait(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.After(timeout)
	for {
		l.mu.Lock()
		up, err, id, changed := l.up, l.err, l.hello.ID, l.changed
		l.mu.Unlock()
		if up {
			return id, nil
		}
		if errors.Is(err, errPassive) {
			return "", err // no point waiting for a registration that isn't coming
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			if err == nil {
				err = errors.New("still connecting to the daemon")
			}
			return "", fmt.Errorf("not registered with the %s daemon: %w", brand.Product, err)
		}
	}
}

// codexFirstCallWait is how long a Codex session's server outside T3 holds its first hello for a
// tool call's _meta (see holdFirstHello). A variable, for tests.
var codexFirstCallWait = time.Second

// cursorNameWait is how long a Cursor session's server holds its first hello for the hook to name
// the conversation (holdForName), and cursorNamePoll how often it looks. A new chat's sessionStart
// names it as Cursor starts; a resumed chat has no sessionStart, and is named by its first prompt.
// Variables, for tests.
var cursorNameWait, cursorNamePoll = 2 * time.Minute, 100 * time.Millisecond

// holdForName keeps a Cursor session from registering until its hook has named the conversation
// (here.Named), for up to cursorNameWait, and then registers it by that name's ID, as Muse's and
// Grok's servers register by the ID in their environment: the same conversation, resumed in
// another process, is the same session, with its name and its unread messages. Cursor gives its
// MCP servers no ID of its own (cursor.go in internal/hook). A session not named by then registers
// under an ID of its own, and the daemon still pairs it with its hook through the process
// (agentHookSessionsLocked). Held, the session isn't on the mesh: before a resumed chat's first
// prompt, or where the hook doesn't run. One of several in its process doesn't wait, and a tool
// call ends the wait: the call waits for the registration, and the hook that names the chat runs
// only after it (headless `agent -p` can start the chat before the server is up). A session
// registered unnamed goes on looking (awaitLateName).
func (l *link) holdForName(ctx context.Context) {
	if l.named == nil {
		return
	}
	deadline := time.Now().Add(cursorNameWait)
	for {
		native, shared := l.named()
		if native != "" {
			l.takeName(native, true)
			return
		}
		if shared {
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-l.calledChan():
			if native, _ := l.named(); native != "" {
				l.takeName(native, true)
				return
			}
			deadline = time.Time{}
		case <-time.After(cursorNamePoll):
		}
	}
	l.spawn(func() { l.awaitLateName(ctx) })
}

// awaitLateName looks for the hook's name for a session that registered without one, and tells the
// daemon when it comes, which keys the session by the chat from then on, its name kept. It looks
// less often as time goes on, every cursorLatePoll at most.
func (l *link) awaitLateName(ctx context.Context) {
	wait := cursorNamePoll
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		native, shared := l.named()
		if shared {
			return
		}
		if native != "" {
			if l.takeName(native, false) {
				l.tellRoute(ctx)
			}
			return
		}
		wait = min(2*wait, cursorLatePoll)
	}
}

// cursorLatePoll is the longest awaitLateName waits between looks. A variable, for tests.
var cursorLatePoll = 10 * time.Second

// callCame notes a tool call of the session's (holdForName).
func (l *link) callCame() {
	if l.named != nil {
		l.calledOnce.Do(func() { close(l.calledChan()) })
	}
}

// calledChan is called, made on first use.
func (l *link) calledChan() chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.called == nil {
		l.called = make(chan struct{})
	}
	return l.called
}

// takeName takes the conversation's ID the hook named, for the hello's Native, and for its ID where
// the session hasn't registered yet (first). Once registered, a session keeps its ID. It says
// whether it took one.
func (l *link) takeName(native string, first bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.native != "" {
		return false
	}
	l.native, l.hello.Native = native, native
	if first && !l.hello.ByThread {
		l.hello.ID = nativeID(native)
	}
	return true
}

// nativeSeenLocked is nativeSeen, made if it isn't yet. Caller holds mu.
func (l *link) nativeSeenLocked() chan struct{} {
	if l.nativeSeen == nil {
		l.nativeSeen = make(chan struct{})
	}
	return l.nativeSeen
}

// holdFirstHello keeps a Codex session outside T3 from registering until a tool call has said
// which thread it is, for up to codexFirstCallWait. Codex starts a subagent's MCP server only
// for the subagent's first call to one of its tools, and that call comes within milliseconds and
// says the thread is a subagent's (Codex 0.164 and 0.167, 2026-11-12, #487). Held, the server
// registers as a child, so the subagent is never listed. A person's thread gets its server when
// the thread starts, with no call, and registers a moment later than it did.
func (l *link) holdFirstHello(ctx context.Context) {
	l.mu.Lock()
	hold := l.hello.Agent == "codex" && l.hello.T3CLI == "" && l.native == ""
	seen := l.nativeSeenLocked()
	l.mu.Unlock()
	if !hold {
		return
	}
	t := time.NewTimer(codexFirstCallWait)
	defer t.Stop()
	select {
	case <-seen:
	case <-t.C:
	case <-ctx.Done():
	}
}

func (l *link) run(ctx context.Context) {
	backoff := time.Second
	inUse := 0
	// Inside T3, the session is registered with its thread if it can be found at once, so its name
	// never changes under it. The lookup takes milliseconds, and is bounded.
	l.lookupThread(ctx, 3*time.Second)
	l.startSeek(ctx)
	l.holdFirstHello(ctx)
	l.holdForName(ctx)
	for ctx.Err() == nil {
		// The user can leave while this runs, and leaving stops the daemon: the next thing this
		// loop would do is start another. So ask before every hello, not once.
		if l.allowed != nil && !l.allowed() {
			l.set(false, errPassive)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		if l.named != nil {
			if native, _ := l.named(); native != "" {
				l.takeName(native, false) // named after the hold: the ID stays, the daemon learns the conversation
			}
		}
		l.mu.Lock()
		h := l.hello
		l.mu.Unlock()
		conn, resp, err := l.c.Hello(ctx, h)
		if err != nil {
			l.set(false, err)
			// The ID is taken: a previous server process for this session hasn't been noticed
			// gone yet, or two sessions collided. Wait it out a few times, then take a new ID.
			if strings.Contains(err.Error(), "in use") {
				if inUse++; inUse >= 3 {
					l.mu.Lock()
					byThread := l.hello.ByThread
					if byThread {
						// The thread's session is live and stays so: this process isn't the
						// thread's own (a claude or agy run from the thread's shell inherits
						// T3CODE_THREAD_ID), or the thread's last provider session hasn't let
						// go. It is a session of its own, whose thread T3's database may name,
						// as before #642.
						l.hello.ByThread, l.hello.T3Thread = false, ""
						if l.hello.Native != "" {
							l.hello.ID = nativeID(l.hello.Native)
						} else {
							l.hello.ID = sessionID(l.hello.ClaudeSession)
						}
					} else if l.hello.From != "" {
						// The session Pi left is live still, past the wait: the session is one
						// of its own, at the ID made from its file, as before #1145.
						l.hello.From = ""
					} else {
						l.hello.ID = bus.RandHex(6)
					}
					l.mu.Unlock()
					inUse = 0
					if byThread {
						l.startSeek(ctx)
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 10*time.Second)
			continue
		}
		backoff, inUse = time.Second, 0
		l.mu.Lock()
		l.noteReadLocked(h.Route, resp)
		// A retry belongs to one registration (#1000). One used before this hello was told to a
		// daemon that has gone, or found the link down and told none, so if this reply says the
		// daemon couldn't read the thread, the first changing call after it looks again. It is
		// cleared here, before the link is up, and not when the connection ends: a call that got
		// past the wait just before the drop may yet use one up while the link is down.
		l.retry = nil
		l.hello.Reconnect = true // any later hello is a reconnection
		if _, id := bus.SplitAddr(resp.Self); (h.ByThread || h.From != "") && id != "" {
			// The daemon may give the thread the address its name has had since before #642
			// (see Hello.ByThread), and a Pi session the address of the one it left (Hello.From).
			l.hello.ID = id
		}
		l.mu.Unlock()
		l.learn(resp.ClaudeSession)
		// The route may have changed after this hello was made, when tellRoute had no session to tell:
		// the daemon hasn't got it. A subagent's call that came meanwhile, or a T3 thread found then, is
		// told before the link is up, so the call, which waits for this registration, goes on after the
		// daemon has made the session a child or read whether its thread is a delegate's (#487, #966).
		// The route is checked again in the hold of mu that puts the link up: one changed after that
		// finds the link up, and its tellRoute sends it before the call goes on.
		told := h.Route
		l.routeMu.Lock()
		for {
			l.mu.Lock()
			route, id := l.hello.Route, l.hello.ID
			if route == told || !route.Child && route.T3Thread == told.T3Thread {
				l.setLocked(true, nil)
				l.mu.Unlock()
				l.routeMu.Unlock()
				break
			}
			l.mu.Unlock()
			l.sendRoute(ctx, id, route)
			told = route
		}
		// Anything else learned since the hello was made is sent now.
		l.mu.Lock()
		stale := l.hello.Route != told
		l.mu.Unlock()
		if stale {
			l.spawn(func() { l.tellRoute(ctx) })
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
			case <-done:
			}
			conn.Close()
		}()
		io.Copy(io.Discard, conn) // returns when the daemon goes away
		close(done)
		l.mu.Lock()
		l.readThread = "" // what that daemon read; the next one says again in its reply to the hello
		l.setLocked(false, errors.New("the daemon went away; reconnecting"))
		l.mu.Unlock()
	}
}
