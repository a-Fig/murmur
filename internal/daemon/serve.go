package daemon

import (
	"bufio"
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/scrub"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

const maxBody = bus.MaxBody

// maxRequest caps a request's line, which is read before its token is checked: any local process,
// of any user, can connect, and a line read whole however long would let one fill the daemon's
// memory (about twice what it sent; a 1 GiB line, 2 GiB of heap). The largest legal request is a
// send, its body at most maxBody, which JSON escaping can make six times longer (`<` for `<`),
// with a subject and a few paths beside it. A body a little over maxBody still gets the reply that
// says why it wasn't sent.
const maxRequest = 8 * maxBody

var errTooLong = errors.New("line too long")

// readLine reads a line of at most max bytes, its newline included.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > max {
			return nil, errTooLong
		}
		line = append(line, part...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

func fail(msg string) proto.Response { return proto.Response{Error: msg} }

// refuse is fail for a send, saying what the refusal is about (proto.RefusedRecipient or
// proto.RefusedSend).
func refuse(about, msg string) proto.Response { return proto.Response{Error: msg, Refused: about} }

func (d *Daemon) serve(ctx context.Context, ln net.Listener) {
	var wait time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A daemon nobody can reach would still hold the clone's lock, so no replacement
			// could start. Exit instead; the next client starts a new one.
			if errors.Is(err, net.ErrClosed) {
				d.log.Printf("accepting connections: %v; exiting", err)
				d.stop()
				return
			}
			// Anything else passes, such as running out of file descriptors (EMFILE), which any
			// local user can cause by holding connections open, and which once stopped the daemon.
			// Wait and try again, as net/http's server does: 5 ms, doubling up to a second.
			if wait == 0 {
				d.log.Printf("accepting connections: %v; trying again", err)
				wait = 5 * time.Millisecond
			} else {
				wait = min(2*wait, time.Second)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		wait = 0
		d.bg.Add(1)
		go func() {
			defer d.bg.Done()
			d.handleConn(ctx, conn)
		}()
	}
}

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	req, ok := readRequest(conn, r)
	if !ok {
		return
	}
	// A client proves the daemon before it sends anything else (#371): a ping with a nonce, and no
	// token, which is answered with the proof that this is the daemon whose token is in
	// daemon.json. The request it was opened for follows on the same connection, so nothing can
	// take the port between the two. The proof tells whoever asks nothing they can use: the nonce
	// is the client's, fresh each time, and only this listener answers at this address while it
	// lives.
	if req.Op == proto.OpPing && req.Nonce != "" {
		writeResp(conn, proto.Response{OK: true, Protocol: proto.Protocol, Proof: proto.Proof(d.token, conn.LocalAddr().String(), req.Nonce)})
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if req, ok = readRequest(conn, r); !ok || req.Nonce != "" {
			return
		}
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(d.token)) != 1 {
		writeResp(conn, fail("bad token"))
		return
	}
	conn.SetReadDeadline(time.Time{})
	// A ping and a stop reveal nothing, and are how a leave finds the daemon and ends it: Stop
	// connects with a ping first.
	if req.Op != proto.OpStop && req.Op != proto.OpPing && !d.consented() {
		writeResp(conn, fail(ErrNoConsent.Error()+", or has left since"))
		return
	}
	if req.Op == proto.OpHello {
		d.hello(ctx, conn, r, req.Hello)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if req.Op == proto.OpLookup {
		d.wantHistory() // read back while the first sync runs, not after it
	}
	if req.Op == proto.OpWho || req.Op == proto.OpSend || req.Op == proto.OpLookup {
		d.awaitFirstSync(ctx)
	}
	if req.Op == proto.OpLookup {
		d.awaitHistory(ctx) // its messages are the page's, earlier ones included
	}
	resp := d.handle(ctx, req)
	if req.Op == proto.OpSend && resp.Error != "" && resp.Refused == "" {
		resp.Refused = proto.RefusedSend // refused before send: the session isn't registered, or is a delegate
	}
	resp.ClaudeSession = d.claudeSessionOf(req.Session)
	writeResp(conn, resp)
}

// readRequest reads one request line, answering one that is too long or isn't JSON with why.
func readRequest(conn net.Conn, r *bufio.Reader) (proto.Request, bool) {
	line, err := readLine(r, maxRequest)
	if errors.Is(err, errTooLong) {
		writeResp(conn, fail(fmt.Sprintf("bad request: over %d KB", maxRequest>>10)))
		return proto.Request{}, false
	}
	if err != nil {
		return proto.Request{}, false
	}
	var req proto.Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(conn, fail("bad request: "+err.Error()))
		return proto.Request{}, false
	}
	return req, true
}

// claudeSessionOf is the Claude Code session ID held for the session id, which an MCP server
// asks for with every call: a hook may have corrected it since the server started. "" for the CLI,
// the hook, and a session that isn't registered.
func (d *Daemon) claudeSessionOf(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.sessions[id]; s != nil {
		return s.claudeSession
	}
	return ""
}

func writeResp(conn net.Conn, resp proto.Response) {
	resp.OK = resp.OK || resp.Error == ""
	b, _ := json.Marshal(resp)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	conn.Write(append(b, '\n'))
}

// hello registers a session and holds its connection open until the session's process exits.
// The connection closing starts the grace: the session's MCP server restarts while its
// conversation carries on, so the session stays registered, with its inbox, for SessionGrace, and
// a hello with the same ID in that time takes it over without anything going on the bus. Only
// when the grace runs out is the session stopped. Its inbox is then kept for StoppedInbox, for a
// session that comes back.
func (d *Daemon) hello(ctx context.Context, conn net.Conn, r *bufio.Reader, h *proto.Hello) {
	if h == nil || !validName(h.ID) || h.ID == proto.CLISession || h.ID == bus.PersonSession {
		writeResp(conn, fail("hello needs a session ID of lowercase letters, digits and dashes"))
		return
	}
	agent := bus.OneLine(h.Agent, 40)
	if agent == "" {
		agent = "unknown"
	}
	// The branch and the worktree are read while the registration waits for the first sync (see
	// awaitFirstSync), not after it: one after the other, the wait's 10 s and git's could run past
	// the 15 s a client gives a hello (Sol's review of #377).
	var branch, worktree string
	read := make(chan struct{})
	go func() {
		defer close(read)
		branch, worktree = branchOf(ctx, h.Cwd), worktreeOf(ctx, h.Cwd)
	}()
	d.awaitFirstSync(ctx)
	<-read
	// Resolving the branch runs git, long enough for a leave to land after handleConn's check.
	if !d.consented() {
		writeResp(conn, fail(ErrNoConsent.Error()+", or has left since"))
		return
	}
	d.mu.Lock()
	h.ID = d.threadIDLocked(h)
	s := d.sessions[h.ID]
	if s != nil && s.dropped.IsZero() {
		d.mu.Unlock()
		writeResp(conn, fail("session ID "+h.ID+" is in use"))
		return
	}
	// A session in its grace, taken over by a server of the same process, keeps the Claude ID the
	// daemon has for it: the server may be a new one, started with the ID its process had at first,
	// which a /clear or a /resume since has made stale. The daemon's is never older. It came from a
	// hook, or from a hello, which carries the environment's ID or one the daemon gave out, and
	// a hook may have moved it since: after a daemon restart, a hello with the live ID is followed by
	// a takeover with the stale one. A server of another process is trusted.
	claude := h.ClaudeSession
	if s != nil && h.ClientPID != 0 && h.ClientPID == s.clientPID && s.claudeSession != "" {
		claude = s.claudeSession
	}
	// Two live sessions of one client session (two MCP servers in it) would share a name, and
	// a name is an address. The later one is a session of its own.
	route := h.Route
	if s != nil && route.T3Thread == "" && claude == s.claudeSession {
		// The same client session, back after its server restarted without finding its T3 thread
		// this time: it is still that thread.
		route.T3Thread = s.route.T3Thread
	}
	base := nameKey(route.T3Thread, claude, route.Native, h.ID)
	// A Codex thread that another program had goes on here (#568), if this is a server's first
	// registration: its tool call has just carried the thread. One back from its grace, or
	// registering again with a daemon that restarted, says nothing new, and would take the
	// thread back from a session that got it since.
	var left []string
	if s == nil && !h.Reconnect {
		if isClaudeCode(agent) && d.st.ThreadLeft[h.ID] != "" {
			// A Claude session in T3 keeps its ID across its server's restarts, as the ID is made
			// from the thread (#642): starting again, it has the conversation again, though it gave
			// it up to the command line once (#643).
			delete(d.st.ThreadLeft, h.ID)
		}
		left = d.yieldThreadLocked(h.ID, agent, h.ClientPID, base, claude)
	}
	d.shareNameLocked(base, claudeAlias(agent, route.T3Thread, claude), h.ID)
	key := d.uniqueKeyLocked(base, h.ID, h.ClientPID)
	taken := false
	if s != nil {
		// A session in its grace: this connection takes it over, with its inbox and its unread
		// notices. Everyone else was never told it was gone.
		taken = true
		if s.agent != agent || s.branch != branch || s.key != key {
			d.kickPresence() // what presence shows changed; otherwise nothing is written
		}
		s.agent, s.cwd, s.claudeSession, s.branch, s.reconnect = agent, h.Cwd, claude, branch, h.Reconnect
		s.clientPID = h.ClientPID
		s.route = route
		d.rememberRouteLocked(s)
		s.worktree = worktree
		// The name is set again even when the key hasn't changed: while s was in its grace another
		// session may have taken its key and name, and s, back, holds the name now. As any taking
		// of a name does, that also takes what was kept for the name (see adoptLocked).
		s.key = key
		d.setNameLocked(s, d.nameForKeyLocked(s, key))
		s.dropped = time.Time{}
		close(s.takeover)
		s.takeover = nil
	} else {
		// Before the session takes a name from the pool, which wakes the assigner: the session's
		// presence goes out ahead of its name's assignment and its start on the log.
		if d.presenceFirst == nil {
			d.presenceFirst = make(chan struct{})
		}
		s = d.newSessionLocked(h, agent, branch, key)
		s.worktree = worktree
		s.route = route
		d.rememberRouteLocked(s)
	}
	for _, id := range left {
		d.takeInboxLocked(s, id) // sent to the conversation, which is here now
	}
	if len(left) > 0 {
		d.saveLocked()
	}
	if parent, named := d.kidNative[route.Native]; named && route.Native != "" && claude == "" {
		// A hook named this session a child before its server registered (an OpenCode child's
		// plugin does as the session is made, #455): it is one from here, not from the next
		// refresh, so it is never listed (#487).
		if d.markKidLocked(s.id, parent) && taken {
			d.kickPresence() // one back from its grace, named while it was away, was listed
		}
	}
	if d.routeChildLocked(s) && taken {
		d.kickPresence()
	}
	if d.readHeldPromptsLocked(s) {
		d.saveLocked() // a turn handed over started while the session was away (#658)
	}
	_, kid := d.kids[s.id]
	d.lastActive = time.Now()
	d.kickPoll() // a session is live: look at the remote at once, and at the faster rate
	if h.Reconnect {
		d.catchUpLocked(h.ID)
		// A catch-up can put back a copy another session of the thread has already handed over
		// (#70), so the thread's handed messages are taken out again after it.
		d.dropHandedLocked(h.ID)
	}
	name, live := s.name, s.claudeSession
	d.mu.Unlock()
	if name == "" {
		d.kickNames()
	}
	d.kickPush() // a session back may have messages waiting that its route can now hand over

	addr := bus.Addr(d.device, h.ID)
	reply := proto.Response{OK: true, Self: addr, Name: name, NamePending: name == "", ClaudeSession: live}
	if taken {
		d.log.Printf("session %s reconnected", addr)
		writeResp(conn, reply)
	} else {
		d.log.Printf("session %s started (%s, %s)", addr, agent, branch)
		writeResp(conn, reply)
		d.kickPresence()
		if kid { // a child registers off the mesh, and its start and stop are nobody's news (#487)
			d.mu.Lock()
			s.unlogged = true
			d.mu.Unlock()
		} else {
			d.appendSoon(bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.SessionStarted, From: addr,
				At: time.Now().UTC(), Agent: agent, Branch: branch})
		}
	}

	stopWatch := context.AfterFunc(ctx, func() { conn.Close() })
	io.Copy(io.Discard, r) // returns when the session's process closes the connection, or dies
	stopWatch()

	d.mu.Lock()
	d.lastActive = time.Now()
	if ctx.Err() != nil {
		d.stoppedWithDaemonLocked(h.ID)
		d.mu.Unlock()
		return
	}
	takeover := make(chan struct{})
	s.dropped, s.takeover = time.Now(), takeover
	d.mu.Unlock()

	var over <-chan struct{} // nil, so never, unless a test ends the grace
	if graceBegins != nil {
		over = graceBegins(h.ID)
	}
	grace := time.NewTimer(d.opt.SessionGrace)
	defer grace.Stop()
	select {
	case <-takeover:
		return // a new connection carries the session on
	case <-ctx.Done():
		d.mu.Lock()
		if s.takeover == takeover {
			d.stoppedWithDaemonLocked(h.ID)
		}
		d.mu.Unlock()
		return
	case <-grace.C:
	case <-over:
	}

	d.mu.Lock()
	if s.takeover != takeover {
		d.mu.Unlock() // taken over just as the grace ran out
		return
	}
	delete(d.sessions, h.ID)
	delete(d.st.Registered, h.ID)
	d.lastActive = time.Now()
	d.st.Stopped[h.ID] = time.Now().Round(0)
	// A session that took this one's name while it was in its grace, as a conversation resumed at
	// another ID does (a command-line one after #51 hashed its ID), passed its inbox by: it was
	// still among the sessions then. It takes it now (Opus's review of #51), and is handed it: its
	// registration's look for messages to hand over is long done, and nothing after this kicks one.
	kept := d.keptForLocked(h.ID)
	adopted := false
	if holder := d.sessions[d.st.NameHolders[kept]]; kept != "" && holder != nil && holder.id != h.ID &&
		holder.dropped.IsZero() && holder.name == kept {
		adopted = d.adoptLocked(holder, kept)
	}
	d.saveLocked()
	unlogged := s.unlogged
	d.mu.Unlock()
	d.log.Printf("session %s stopped", addr)
	if adopted {
		d.kickPush()
	}
	d.kickPresence()
	if !unlogged {
		d.appendSoon(bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.SessionStopped, From: addr,
			At: time.Now().UTC()})
	}
}

// graceBegins, if a test sets it, is called once a session whose connection dropped is recorded
// as dropped, as its grace begins, and closing the channel it returns ends that grace as
// SessionGrace's timer would. Tests take a long grace and end it when they are ready, instead of
// racing a short one (Sol's review of #51).
var graceBegins func(id string) <-chan struct{}

// stoppedWithDaemonLocked takes session id out as the daemon stops, and records it as stopped
// then (#736). Its server may reconnect to the next daemon, which gives it back its inbox as it
// does any session back from stopped (newSessionLocked). One that doesn't, a thread left open on
// a laptop shut overnight, is then a stopped session like any other: its inbox, credited commits
// and T3 route are kept for StoppedInbox, and what is sent meanwhile is queued for it
// (keptSinceLocked). Unrecorded, the next daemon's pruneInboxes took all of that two minutes
// after it started. No stop goes on the log: the goodbye deletes this device's presence, and a
// session that reconnects never stopped as far as anyone else knows. Caller holds mu.
func (d *Daemon) stoppedWithDaemonLocked(id string) {
	delete(d.sessions, id)
	delete(d.st.Registered, id)
	d.st.Stopped[id] = time.Now().Round(0)
	if d.st.Downed == nil {
		d.st.Downed = map[string]bool{}
	}
	d.st.Downed[id] = true
	d.saveLocked()
}

// stopKilledLocked records as stopped the sessions the last daemon left registered: it was
// killed, with no chance to record them itself (#762). A laptop whose battery died with threads
// open then starts, the next morning, with those sessions stopped, as a daemon that was shut
// down leaves them (stoppedWithDaemonLocked): their inboxes, credited commits and T3 routes are
// kept for StoppedInbox, what was sent meanwhile is queued for them, and their servers are
// waited for a little (Downed). They stopped at written, when that daemon last wrote its state,
// which is the latest it is known to have run. A daemon that reaches the remote writes at least
// once a minute (LastSync), so that is at most about a minute before it died; one that was
// offline may have written long before. A time that early has a catch-up read more, and takes
// that much off StoppedInbox, though never all of it for a session whose server is back as this
// daemon starts (stopSpentLocked). A time ahead of the clock, from a clock since put right, counts as now: a stop
// in the future never runs out. Nothing is saved here: the state isn't whole yet, and a daemon
// killed again before its first save finds the same sessions with the same time. It runs as
// the state is loaded, before any goroutine, so what its name says of mu holds.
func (d *Daemon) stopKilledLocked(written time.Time) {
	if len(d.st.Registered) == 0 {
		return
	}
	// Not later than this daemon's start either, by which the next one knows the stop for an
	// earlier daemon's (pruneInboxes).
	if latest := cmp.Or(d.started, time.Now()); written.IsZero() || written.After(latest) {
		written = latest
	}
	n := 0
	for id := range d.st.Registered {
		if _, stopped := d.st.Stopped[id]; !stopped {
			d.st.Stopped[id] = written.Round(0)
			if d.st.Downed == nil {
				d.st.Downed = map[string]bool{}
			}
			d.st.Downed[id] = true
			n++
		}
	}
	if n > 0 {
		d.log.Printf("the last daemon didn't record the stop of %d sessions registered with it; they count as stopped at %s, when it last wrote its state",
			n, written.Format(time.DateTime))
	}
	d.st.Registered = nil
}

// liveLocked says whether session id is connected: the CLI always is, a session in its grace or
// stopped is not. Caller holds mu.
func (d *Daemon) liveLocked(id string) bool {
	if id == proto.CLISession {
		return true
	}
	s := d.sessions[id]
	return s != nil && s.dropped.IsZero()
}

func (d *Daemon) handle(ctx context.Context, req proto.Request) proto.Response {
	sess := req.Session
	if sess == "" {
		sess = proto.CLISession
	}
	d.mu.Lock()
	now := time.Now()
	d.lastActive = now
	registered := d.liveLocked(sess)
	// Whatever a session's MCP server asks is the session at work (#21). The CLI's calls are a
	// human's, and hooks, which reach here as the CLI, mark the sessions they speak for themselves.
	if s := d.sessions[sess]; registered && s != nil {
		d.touchLocked(s, now)
		if toolOps[req.Op] && s.toolCalled.IsZero() {
			s.toolCalled = now
			// Other devices learn that its hook isn't running from presence, which is written when
			// something in it changes; the end of the grace changes nothing else, so it asks.
			time.AfterFunc(hookGrace+time.Second, d.kickPresence)
		}
	}
	d.mu.Unlock()
	if r, ok := d.invisibleAnswer(req); ok {
		return r
	}
	if !registered && req.Op != proto.OpNotices && req.Op != proto.OpTurnEnd && req.Op != proto.OpPing {
		return fail("this session isn't registered with the daemon (it may have just restarted); try again in a few seconds")
	}
	from := bus.Addr(d.device, sess)
	if req.Op == proto.OpStatus || req.Op == proto.OpSend || req.Op == proto.OpTags || req.Op == proto.OpMute {
		if why := d.childRefusal(sess); why != "" {
			return fail(why)
		}
	}

	switch req.Op {
	case proto.OpPing:
		return proto.Response{OK: true, Self: from, Protocol: proto.Protocol}

	case proto.OpWho:
		filter, err := tags.ParseFilter(req.Tags)
		if err != nil {
			return fail(err.Error())
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		resp := proto.Response{OK: true, Self: from, Devices: d.whoLocked(sess, filter), Filter: filter.String()}
		if s := d.sessions[sess]; s != nil {
			resp.Name, resp.NamePending = s.name, s.name == ""
		}
		return resp

	case proto.OpStatus:
		doing := bus.OneLine(req.Doing, 120)
		if err := scrub.Check(doing); err != nil {
			return fail(err.Error())
		}
		d.mu.Lock()
		s := d.sessions[sess]
		if s != nil {
			s.doing = doing
		}
		d.mu.Unlock()
		if s == nil {
			return proto.Response{OK: true, Self: from, Note: "the CLI has no status line"}
		}
		d.kickPresence()
		return proto.Response{OK: true, Self: from, Note: "status set"}

	case proto.OpSend:
		return d.send(ctx, from, req)

	case proto.OpTags:
		if len(req.For) > 0 {
			return d.addToGroups(ctx, sess, req)
		}
		return d.setOwnTags(sess, req)

	case proto.OpMute:
		req.Name = strings.TrimSpace(req.Name)
		return d.mute(sess, req)

	case proto.OpInbox:
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.clearChildInboxLocked(sess) {
			return proto.Response{OK: true, Self: from} // a delegate's (#188)
		}
		// A message on its way into the session as a turn is left to arrive as one.
		var read, left []*inMsg
		for _, m := range d.st.Inbox[sess] {
			if m.Pushing {
				left = append(left, m)
			} else {
				read = append(read, m)
			}
		}
		msgs := d.messageViewsLocked(read, true)
		d.explainViewsLocked(msgs, func(int) []string { return []string{sess} })
		d.markAllLocked(sess, read, bus.MarkRead)
		if len(left) == 0 {
			delete(d.st.Inbox, sess)
		} else {
			d.st.Inbox[sess] = left
		}
		d.saveLocked()
		return proto.Response{OK: true, Self: from, Messages: msgs}

	case proto.OpRoute:
		if req.Route == nil {
			return fail("route needs a route")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if s := d.sessions[sess]; s != nil {
			d.setRouteLocked(s, *req.Route)
		}
		return proto.Response{OK: true, Self: from}

	case proto.OpNotices:
		return d.notices(req)

	case proto.OpTurnEnd:
		return d.turnEnd(req)

	case proto.OpToolStart:
		return d.toolStart(ctx, req)

	case proto.OpToolEnd:
		return d.toolEnd(ctx, req)

	case proto.OpLookup:
		return d.lookup(sess, req)

	case proto.OpLog:
		limit := req.Limit
		if limit <= 0 {
			limit = 30
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		events := d.recent
		if len(events) > limit {
			events = events[len(events)-limit:]
		}
		return proto.Response{OK: true, Self: from, Events: append([]proto.EventView(nil), events...)}

	case proto.OpRotate:
		note, err := d.rotateRequest(ctx, req.Collaborators, req.Force)
		if err != nil {
			return fail(err.Error())
		}
		return proto.Response{OK: true, Note: note}

	case proto.OpNick:
		return d.nick(ctx, req)

	case proto.OpPageKey:
		// Another clone's daemon asks, to send its page's tab here (#348). It has the sessions'
		// token, from this clone's daemon.json, so it could do more than this already.
		page := d.page.Load()
		if page == nil {
			return fail("this daemon isn't serving the page")
		}
		// Under mu, as the restart onto an update decides, so no key is made after it has looked
		// for keys still out: the next daemon's page wouldn't know it (Sol's review, 2026-11-09).
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.updating {
			return fail(proto.ErrRestarting)
		}
		return proto.Response{OK: true, PageKey: page.PageKey()}

	case proto.OpStop:
		d.log.Printf("stop requested")
		d.stop()
		return proto.Response{OK: true, Note: "daemon stopping"}
	}
	return fail("unknown op " + req.Op)
}

// notices returns the messages of a session it hasn't been told about yet, without bodies, and
// marks them told, with how many it has unread when that is news. The hook calls it after a prompt
// or a tool call, so the session is at work (see touchHookLocked); the hook knows only Claude
// Code's session ID, and its process: with them, the sessions of that process follow the hook to
// the ID it reports (see hookSessionsLocked), and a hook that matches nothing gets nothing. In Pi
// it knows the session's file instead (nativeSessionsLocked).
//
// A message that would have gone in as a turn, but for the session being at work, is Held: it goes
// in as one when the turn ends if it is still unread (#202). The count is told with each new
// message and again only when it changes, so it isn't repeated after every tool call.
//
// A session the page sets to be given messages whole while busy (#204) gets each new one with its
// body instead, which the hook shows as its turn would read: it is read then, and leaves the
// inbox and the count. So does any session, for a message with no body (#355). Only a hook that
// says it prints them (req.Whole) gets them so.
func (d *Daemon) notices(req proto.Request) proto.Response {
	wake := d.wakeSnapshot() // setMu is taken alone, before mu
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	if hookReq(req) {
		sessions := d.touchHookLocked(req)
		d.recordIssuesLocked(sessions, req.Issues)
		for _, s := range sessions {
			ids = append(ids, s.id)
			if validTranscript(req.Transcript) && !req.Subagent && s.transcript != req.Transcript {
				// A /clear or a /resume is a new conversation, with a title of its own.
				s.transcript = req.Transcript
				if s.title != "" {
					s.title = ""
					d.kickPresence()
				}
			}
		}
	} else {
		ids = []string{req.Session}
	}
	if hookReq(req) && !slices.ContainsFunc(ids, d.liveLocked) {
		d.holdPromptLocked(req) // its session may be on its way back (#658)
	}
	if d.hidden.Load() {
		return proto.Response{OK: true} // invisible: the hook is seen to run, and told nothing
	}
	var self string
	var fresh []*inMsg
	var whole []delivery
	var told []*session
	unread := 0
	room := newWholeRoom()
	for _, id := range ids {
		// A session in its grace, or stopped, is told of its notices once it is back.
		if id == "" || !d.liveLocked(id) {
			continue
		}
		if self == "" {
			self = bus.Addr(d.device, id)
		}
		if d.clearChildInboxLocked(id) {
			continue // a delegate's (#188)
		}
		if req.Subagent {
			continue // the notice would go to the subagent (#188)
		}
		if d.readOnPromptLocked(id, req.Prompt) {
			d.saveLocked() // a turn handed over has started (#611)
		} else {
			d.missedPromptLocked(id, req.Prompt)
		}
		if s := d.sessions[id]; s != nil && s.sending != "" && req.Prompt != "" &&
			whichTurn(req.Prompt, []awaitingRead{{Turn: s.sending}}) == 0 {
			s.sendingSeen = true // the turn a send still running hands it has begun (#677)
		}
		if s := d.sessions[id]; s != nil {
			told = append(told, s)
		}
		// Only the hook shows a message whole: a request that names no client's session isn't one,
		// and a hook that doesn't say it prints Read (req.Whole) would lose it (#229).
		printsWhole := hookReq(req) && req.Whole
		busyWhole := wake.busyChoice(d.settingsNameLocked(id)) == web.BusyWhole
		for _, m := range d.st.Inbox[id] {
			if m.Pushing || d.heldLocked(id, m) {
				// Do not disturb (#198): what it holds stays queued in the inbox, with no notice, no
				// whole message and no hold for the turn's end, and doesn't count as unread for the
				// hook. Its own person's messages go through (#777).
				continue
			}
			if !m.Noticed && !d.queuedForPushLocked(id, m, wake) {
				// One handed to T3 or the socket before may be there already: it gets the
				// subject only, as after any handoff that failed, not a second whole telling.
				//
				// A message with no body is given whole whatever the setting (#355): its notice
				// would show all of it, and the agent, having it, wouldn't call inbox, so it
				// would stay unread and come a second time when the turn ends.
				//
				// The reply has room for so much (wholeRoom): one that doesn't fit whole is told
				// of as any other, and one whose notice doesn't fit either isn't told of yet, and
				// waits, counted as unread, for the hook's next call.
				if printsWhole && (busyWhole || subjectOnly(m)) && !m.Attempted && !m.PushFailed && room.take(m) {
					whole = append(whole, delivery{id, m})
					continue
				}
				if room.takeNotice(m) {
					m.Held = d.hookBusyLocked(id) && d.holdableLocked(id, m, wake, d.turnStartLocked(id))
					m.Noticed, m.NoticedAt = true, time.Now()
					d.markLocked(id, m.Event, bus.MarkDelivered)
					fresh = append(fresh, m)
				}
			}
			unread++
		}
	}
	if self == "" {
		return proto.Response{OK: true}
	}
	// A message for everyone is in each inbox the hook speaks for, and is shown once.
	var wholeMsgs []*inMsg
	shown := map[string]bool{}
	for _, w := range whole {
		if !shown[w.msg.Event.ID] {
			shown[w.msg.Event.ID] = true
			wholeMsgs = append(wholeMsgs, w.msg)
		}
		d.outOfInboxLocked(w.id, w.msg, "gave", "whole through the hook")
	}
	var read []proto.MessageView
	if len(whole) > 0 {
		read = d.wholeViewsLocked(wholeMsgs, whole) // before the save: it records who was told what
	}
	if len(fresh) > 0 || len(whole) > 0 {
		d.saveLocked()
	}
	var freshMsgs []*inMsg
	for _, m := range fresh {
		if !shown["notice "+m.Event.ID] {
			shown["notice "+m.Event.ID] = true
			freshMsgs = append(freshMsgs, m)
		}
	}
	d.health.noticed(len(freshMsgs))
	resp := proto.Response{OK: true, Self: self, Messages: d.messageViewsLocked(freshMsgs, false), Read: read}
	changed := false
	for _, s := range told {
		changed = changed || s.toldUnread != unread
		s.toldUnread = unread
	}
	if unread > 0 && (len(fresh) > 0 || len(whole) > 0 || changed) {
		resp.Unread = unread
	}
	return resp
}

// subjectOnly says message m has no body and no files, as its reader sees it (view.Turn): its
// subject is all of it, and so is the hook's notice of it.
func subjectOnly(m *inMsg) bool {
	return len(m.Event.Files) == 0 && strings.TrimSpace(bus.Text(m.Event.Body, bus.MaxBody)) == ""
}

// hookReq says req is from the hook, which names its session by its client's ID for it: Claude
// Code's, or another agent's own (Native, Pi's from Murmur's extension). A request with neither
// names the session it is from (Session), as the MCP server's do.
func hookReq(req proto.Request) bool { return req.ClaudeSession != "" || req.Native != "" }

// turnEnd is the hook at the end of a turn (Claude Code's Stop). The messages the sessions it
// speaks for were told of while at work and haven't read, and those that came during the turn and
// weren't told, however long it took, would go in as a turn now (#202). Where the hook can keep the
// turn going (req.Continues), they are its reply instead, read, and the turn goes on with them
// (Tyler, 2026-11-08). A session with none is idle. While the device is invisible the turn ends
// all the same, but nothing keeps it going: what waits is handed over once the device is back.
func (d *Daemon) turnEnd(req proto.Request) proto.Response {
	wake := d.wakeSnapshot() // setMu is taken alone, before mu
	d.mu.Lock()
	defer d.mu.Unlock()
	if !hookReq(req) || req.Subagent {
		return proto.Response{OK: true}
	}
	var given []delivery
	held := false
	room := newWholeRoom()
	for _, s := range d.touchHookLocked(req) {
		first := req.Continues && !s.stopContinues
		s.stopSeen, s.stopContinues = true, req.Continues
		if first {
			// What it was told of before its Stop was known to keep a turn going wasn't held, as
			// there may have been no way in: it is now.
			for _, m := range d.st.Inbox[s.id] {
				if m.Noticed && !m.Held && d.holdableLocked(s.id, m, wake, s.turnStart) {
					m.Held, held = true, true
				}
			}
		}
		d.endTurnLocked(s, wake)
		if !req.Continues || d.hidden.Load() || !d.liveLocked(s.id) || d.clearChildInboxLocked(s.id) {
			continue
		}
		for _, m := range d.continueTurnLocked(s, wake, room) {
			given = append(given, delivery{s.id, m})
		}
	}
	d.kickPush()
	if len(given) == 0 {
		if held {
			d.saveLocked()
		}
		return proto.Response{OK: true}
	}
	// A message for everyone is in each inbox the hook speaks for, and is given once.
	var msgs []*inMsg
	shown := map[string]bool{}
	for _, g := range given {
		if !shown[g.msg.Event.ID] {
			shown[g.msg.Event.ID] = true
			msgs = append(msgs, g.msg)
		}
		d.outOfInboxLocked(g.id, g.msg, "gave", "whole through the Stop hook, keeping its turn going")
	}
	read := d.wholeViewsLocked(msgs, given) // before the save: it records who was told what
	d.saveLocked()
	return proto.Response{OK: true, Read: read}
}

func (d *Daemon) send(ctx context.Context, from string, req proto.Request) proto.Response {
	return d.sendWith(ctx, from, req, nil)
}

// sendWith is send with files that aren't paths for the daemon to read (req.Files): the page's
// person's, which the browser uploaded.
func (d *Daemon) sendWith(ctx context.Context, from string, req proto.Request, uploads []sendFile) proto.Response {
	to := strings.TrimSpace(req.To)
	// A device's group is the device (#463): the message goes on the bus to the device, as one sent
	// to its ID does, so every daemon delivers it, older ones too, and only to that device's
	// sessions. It isn't read as a name after, as a device ID can be shaped like one.
	toDevice := false
	if devs := tags.DevicesOfGroup(to); devs != nil {
		// Two devices can share a group's tag, device-x and x; the message is then sent to neither,
		// rather than to whichever comes first, and the sender is told to use the device's ID.
		var found []string
		d.mu.Lock()
		for _, dev := range devs {
			if _, known := d.peers[dev]; known || dev == d.device {
				found = append(found, dev)
			}
		}
		d.mu.Unlock()
		switch len(found) {
		case 0:
			return fail(fmt.Sprintf("not sent: no device in presence has the group %s. who lists each device's group.%s", tags.Normalize(to), tags.OwnGroupHint(to)))
		case 1:
			to, toDevice = found[0], true
		default:
			return fail(fmt.Sprintf("not sent: devices %s and %s both have the group %s. Send to the one you mean by its ID", found[0], found[1], tags.Normalize(to)))
		}
	} else if g := tags.Normalize(to); bus.IsGroup(g) {
		to = g
	}
	subject := bus.OneLine(req.Subject, 120)
	// Each refusal says whether it is the recipient's alone (#541), so that a send to a list goes on
	// to the rest only past those. One for the recipient quotes it as it was given, which is what an
	// MCP server from before Refused looks for; only a mute or an offline session at an address that
	// is forwarded (queuedAtLocked) names the session it is forwarded to instead, and a device with
	// no key to encrypt to (encryptEventLocked) is named by its device.
	switch valid := bus.ValidTo(to); {
	case !valid && strings.HasPrefix(tags.Normalize(to), tags.GroupPrefix):
		return refuse(proto.RefusedRecipient, fmt.Sprintf("not sent: %q isn't a group. A group's tag is %s and a name of lowercase letters, digits and . _ : -, such as %sui", to, tags.GroupPrefix, tags.GroupPrefix))
	case subject == "":
		return refuse(proto.RefusedSend, "a message needs a recipient (a session's name, device/session, a device, a group such as group:ui, or * for everyone; see who) and a subject")
	case !valid && to != "":
		why := fmt.Sprintf("not sent: %q isn't a recipient. A recipient is a session's name (see who), device/session, a device, a group such as group:ui, or * for everyone", to)
		if l := strings.ToLower(to); bus.ValidTo(l) {
			// Suggested only if it is someone: a session's name, a person's, a device or an address
			// this device knows.
			d.mu.Lock()
			_, isName := d.resolveNameLocked(l)
			_, isPerson := d.resolvePersonLocked(l)
			known := names.Valid(l) && isName || bus.ValidPerson(l) && isPerson || d.addressKnownLocked(bus.SplitAddr(l))
			d.mu.Unlock()
			if known {
				why += fmt.Sprintf("; recipients are lowercase, so did you mean %s?", l)
			}
		}
		return refuse(proto.RefusedRecipient, why)
	case !valid:
		return refuse(proto.RefusedRecipient, "a message needs a recipient (a session's name, device/session, a device, a group such as group:ui, or * for everyone; see who)")
	}
	if len(req.Body) > maxBody {
		return refuse(proto.RefusedSend, fmt.Sprintf("not sent: the body is over %d KB, and the bus keeps everything forever", maxBody>>10))
	}
	// A sender may ask to be told how far its message got (#276), but not of one to everyone: a
	// receipt from every session on the mesh is what this is kept off by default to avoid.
	tellMe := strings.TrimSpace(req.TellMe)
	switch {
	case tellMe == "" || bus.IsPerson(from):
		tellMe = "" // a person's message asks for receipts anyway, and the page shows them
	case !ValidTell(tellMe):
		return fail(fmt.Sprintf("not sent: tell_me is %q or %q, not %q", bus.MarkDelivered, bus.MarkRead, tellMe))
	case to == "*":
		return fail("not sent: tell_me can't be asked of a message to everyone. Send it without tell_me, or to a group or to sessions by name")
	}
	// The body goes on the bus as readers will show it, so what the sender sent is what everyone
	// reads, and it is cleaned before scrub looks: a key with a zero-width space in it is still a
	// key once a reader drops the space. What was sent is checked too: dropping a format character
	// can join a key to the next word, and AKIA…EXAMPLE<U+200B>note then ends in no word boundary.
	body := bus.Text(req.Body, maxBody)
	if err := scrub.Check(subject, body, req.Subject, req.Body); err != nil {
		return refuse(proto.RefusedSend, err.Error())
	}
	// A name is an address: it is turned into the session's device/session here, and that is what
	// goes on the bus, so a daemon that doesn't know names queues the message all the same.
	var toName, byName, renamed string
	if toDevice {
		// Sent to the device, as above.
	} else if names.Valid(to) {
		d.mu.Lock()
		addr, ok := d.resolveNameLocked(to)
		_, isDevice := d.peers[to]
		isDevice = isDevice || to == d.device // a device may have an ID shaped like a name
		var now string
		if ok {
			now, _, _ = d.sessionInfoLocked(d.queuedAtNamedLocked(addr, to))
		}
		d.mu.Unlock()
		switch {
		case ok:
			// The registry keeps a name for good, and the session it was first given to survives a
			// /clear or a /resume under a new name, so a name nobody holds now can still be that
			// session's address. The message goes there, and the sender is told what to call it.
			if now != "" && now != to {
				renamed = fmt.Sprintf("%s was this session's name before it moved to another conversation; it is called %s now. The message was sent to it.", to, now)
			}
			to, toName, byName = addr, to, to
		case !isDevice:
			d.mu.Lock()
			why := d.noSuchNameLocked(to)
			d.mu.Unlock()
			return refuse(proto.RefusedRecipient, why)
		}
	} else if bus.ValidPerson(to) {
		// A person's name is an address too. A device with that ID stands first: it was there before
		// the name, which takes no device's ID when it is chosen. A device's nickname has the same
		// form and is no address: the message would go to a device with that ID, which there isn't,
		// so it is refused, naming the device's ID (#542).
		d.mu.Lock()
		addr, ok := d.resolvePersonLocked(to)
		_, isDevice := d.peers[to]
		isDevice = isDevice || to == d.device
		called := d.calledLocked(to)
		d.mu.Unlock()
		switch {
		case ok && !isDevice:
			to, toName = addr, to
		case len(called) > 0:
			dev := "a device's ID"
			if len(called) == 1 {
				dev = called[0]
			}
			return refuse(proto.RefusedRecipient, fmt.Sprintf("not sent: %s Send to a session's name from who, or to %s for every session on the device.", nickNoAddress(to, called), dev))
		}
	}
	var note string
	if bus.IsGroup(to) {
		d.mu.Lock()
		note = d.groupNoteLocked(to, from)
		d.mu.Unlock()
	} else if to != "*" {
		wake := d.wakeSnapshot() // setMu is taken alone, before mu
		d.mu.Lock()
		at := d.queuedAtNamedLocked(to, byName)
		dev, sess := bus.SplitAddr(at)
		known := d.addressKnownLocked(dev, sess)
		_, stopped := d.st.Stopped[sess]
		offline := d.offlineLocked(dev, sess)
		// Do not disturb doesn't hold a message from this device's person to its own sessions (#777).
		dnd := d.peerDNDLocked(dev, sess) && !(dev == d.device && from == d.personAddr())
		hookOff, agent := d.peerHookOffLocked(dev, sess)
		noWake := d.peerNoWakeLocked(dev, sess, wake)
		// A T3 thread T3 stopped while it was idle has no session, but its route is kept and a message
		// wakes it as one does an idle session, if the wake setting lets it (#669).
		sleeping := false
		if r, ok := d.st.T3Routes[sess]; ok && r.Thread != "" && dev == d.device && stopped {
			sleeping, noWake = true, d.noWakeLocked(sess, wake)
		}
		if noWake == bus.NoWakeIdle && bus.IsPerson(from) {
			noWake = "" // a person's message wakes a session however long it has been idle (#242)
		}
		why := ""
		if offline {
			why = d.offlineWhyLocked(dev, sess, nameOr(toName, at))
		}
		// The recipient has muted the sender (#27): its device would drop the message, so it isn't
		// sent, and this is where the sender learns of the mute.
		muted := d.hasMutedLocked(dev, sess, from)
		d.mu.Unlock()
		switch {
		case muted:
			// The words are Tyler's (2026-11-11).
			return refuse(proto.RefusedRecipient, fmt.Sprintf("your message to %[1]s was not delivered. %[1]s has muted you. Nothing you send can reach them until they unmute you.", nameOr(toName, at)))
		case offline:
			return refuse(proto.RefusedRecipient, "not sent: "+why)
		case (known || sleeping) && dnd:
			note = nameOr(toName, at) + " is on do not disturb: the message is queued in its inbox until it reads its inbox or do not disturb comes off, with no notice or turn."
		case known && noWake != "":
			note = noWakeNote(nameOr(toName, at), noWake, hookOff)
		case known && hookOff:
			// Without the hook the daemon hands the message over as a turn where it can (in T3, or
			// Claude Code's socket), at once, which T3 queues if the session is busy.
			if strings.HasSuffix(agent, " in T3") || agent == "claude-code" {
				note = nameOr(toName, at) + "'s hook isn't running, so it won't be told while it works: it gets this as its next turn."
			} else {
				note = nameOr(toName, at) + "'s hook isn't running, so it sees this when it next reads its inbox."
			}
		case known:
		case sleeping && noWake != "":
			note = noWakeNote(nameOr(toName, at), noWake, false)
		case sleeping:
			note = nameOr(toName, at) + " is idle in T3, which stopped its process: the message wakes it."
		case dev == d.device && stopped:
			note = at + " has stopped. The message waits for it, in case it comes back."
		case bus.IsPerson(at):
			note = nameOr(toName, at) + " isn't on " + brand.Product + " right now. They will see this when they next open it."
		default:
			note = to + " isn't in any device's presence right now, so nobody may read this. Check `who` for addresses."
		}
	}
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: from, At: time.Now().UTC(),
		To: to, ToName: byName, Subject: subject, Body: body}
	// Before anything is read (#765, Tyler, 2026-11-16): an agent's files by its device's agents' Send
	// files, the page's by its person's.
	sends := func() bool {
		return (len(req.Files) == 0 || d.agentSend.Load()) && (len(uploads) == 0 || d.pageSend.Load())
	}
	switch {
	case len(req.Files) > 0 && !d.agentSend.Load():
		return refuse(proto.RefusedSend, "not sent: sending files is off for agents in this repo. Your user can turn on Send files, with agents too, in "+
			brand.Product+"'s Settings; or send the text alone.")
	case len(uploads) > 0 && !d.pageSend.Load():
		return refuse(proto.RefusedSend, "not sent: sending files is off for you in this repo. Turn on Send files in Settings, or send the text alone.")
	}
	filesReused := false // the files took a ref an earlier message went out with (putFiles)
	if len(req.Files) > 0 || len(uploads) > 0 {
		// The files go out first, on a ref of the message's own, so a daemon that reads the event
		// finds them there (files.go).
		read, err := d.readSendFiles(req.Session, req.Files)
		if err != nil {
			return refuse(proto.RefusedSend, "not sent: "+err.Error())
		}
		files, at, reused, sent, err := d.putFiles(ctx, append(read, uploads...), sends)
		if err != nil {
			return refuse(proto.RefusedSend, "not sent: "+err.Error())
		}
		// A send of the same files waits for this one's message to be out, or to have failed
		// (putFiles): whichever way this returns.
		defer sent()
		filesReused = reused
		e.Files, e.FilesAt = files, at
		d.mu.Lock()
		note = strings.TrimSpace(note + " " + d.noFilesNoteLocked(e, nameOr(toName, to)))
		d.mu.Unlock()
	}
	if bus.IsPerson(from) {
		// A person's message asks for receipts, which the page shows (#26). It is kept with its
		// recipients before it is written, as one on this device marks it while it is.
		d.mu.Lock()
		e.Person, e.Receipts = d.st.Person, true
		d.recordSentLocked(e)
		d.mu.Unlock()
	}
	var tellNote string
	if tellMe != "" {
		// Kept like a person's, with whom it is for, and told once it gets that far (tell.go). If
		// none of them writes marks there is nothing to wait for, and the sender is told so now.
		d.mu.Lock()
		e.Receipts = true
		d.recordSentLocked(e)
		rec := d.st.Sent[e.ID]
		rec.Tell, rec.Asker, rec.Subject = tellMe, from, subject
		// The sender's own reading of a message to itself tells it nothing, and the notice would go
		// in the inbox that reading is emptying.
		rec.To = slices.DeleteFunc(rec.To, func(a string) bool { return a == from })
		if len(waitedFor(rec)) == 0 {
			delete(d.st.Sent, e.ID)
			e.Receipts, tellMe = false, ""
			tellNote = "You won't be told how far it gets: it reached no session whose " + brand.Product + " sends receipts."
		}
		d.saveLocked()
		d.mu.Unlock()
	}
	var still func() bool
	if len(e.Files) > 0 {
		still = sends
	}
	// unsent lets go of what was made for a message that wasn't sent, unless its push may land yet
	// (unsure): the message would then name files that are gone, and its receipts would have
	// nothing to be kept in. The files go when their time is up, as any message's do.
	unsent := func(unsure, keepFiles bool) {
		if len(e.Files) > 0 && !filesReused && !unsure && !keepFiles {
			d.filesUnsent(e.FilesAt) // nothing names them: off the remote at the next prune
		}
		// A record kept only to tell an agent how far its message got goes either way: the agent
		// is told it wasn't sent, and a notice half an hour on that nobody has read it would be
		// about a message it has likely sent again.
		if e.Receipts && (!unsure || tellMe != "") {
			d.mu.Lock()
			delete(d.st.Sent, e.ID)
			d.saveLocked()
			d.mu.Unlock()
		}
	}
	// A push that ends unsure leaves the message pending, pushed again until it lands, and the
	// sender is told only if it is given up (pending.go, #781).
	//
	// Its files' ref may be taken by a send of the same files at once (filesSentLocked, below), as the
	// files are on the remote whatever becomes of the message: holding that send back until this one
	// settles would hold it past its own time (Sol's review of rc.2). So a pending send given up
	// keeps its ref its full time, as one that may land does, since another message may name it.
	settle := func(landed, absent bool, err error) {
		if landed {
			return
		}
		unsent(!absent, true)
		d.mu.Lock()
		d.givenUpLocked(from, e.Subject, nameOr(toName, to), err)
		d.saveLocked()
		d.mu.Unlock()
	}
	// Not the person's: the page shows a send as sent once it answers, and has nowhere to show a
	// give-up minutes later, so the person is told at once it wasn't sent and can try again, as before
	// #781 (Fable's review of rc.2).
	if bus.IsPerson(from) {
		settle = nil
	}
	err := d.appendEventIf(ctx, e, still, settle)
	pending := errors.As(err, new(pendingError))
	if err != nil && !pending {
		unsent(errors.As(err, new(unsureError)), false)
		// No key for the recipient's device is that recipient's alone. Anything else here, a team
		// key not yet made or a push that failed, would refuse every recipient the same.
		about := proto.RefusedSend
		if errors.As(err, new(noKeyError)) {
			about = proto.RefusedRecipient
		}
		return refuse(about, "not sent: "+err.Error())
	}
	// A pending send answers as one that landed (Tyler, 2026-11-16): it is pushed until it does, and
	// its sender is told only if it is given up.
	if len(e.Files) > 0 {
		d.mu.Lock()
		d.filesSentLocked(e.FilesAt) // a send of the same files may take its ref now
		d.mu.Unlock()
	}
	note = strings.TrimSpace(renamed + " " + note)
	// To everyone, to a device or to a group: it went out, and the sender is told which of the
	// sessions it was for won't get it, as they have muted it (#27), and which won't be woken by it
	// (#545).
	var named []string
	if dev, sess := bus.SplitAddr(to); to == "*" || sess == "" {
		group := bus.IsGroup(to)
		if to == "*" || group {
			dev = ""
		}
		wake := d.wakeSnapshot() // setMu is taken alone, before mu
		d.mu.Lock()
		muters := d.mutersLocked(from, dev)
		var members []string
		if group {
			members, _ = d.groupMembersLocked(to, from)
			muters = slices.DeleteFunc(muters, func(m string) bool { return !slices.Contains(members, m) })
		}
		unwoken := d.unwokenLocked(from, dev, group, members, wake)
		unwoken = slices.DeleteFunc(unwoken, func(m string) bool { return slices.Contains(muters, m) })
		d.mu.Unlock()
		note = sentences(note, mutersNote(muters), unwokenNote(unwoken))
		named = muters
	}
	// Kept a while, so the sender is told if a device it had read no mute of yet drops it (#466).
	if !bus.IsPerson(from) {
		d.mu.Lock()
		d.outgoingLocked(from, nameOr(toName, to), e, named)
		d.saveLocked()
		d.mu.Unlock()
	}
	if tellMe != "" {
		tellNote = fmt.Sprintf("You'll be told, in your inbox, once it is %s, or in %s with where it is then.", tellMe, minutes(d.opt.TellWait))
	}
	note = strings.TrimSpace(note + " " + tellNote)
	return proto.Response{OK: true, Self: from, Note: note, Telling: tellMe != "", TellKnown: req.TellMe != "",
		Messages: []proto.MessageView{{ID: e.ID, From: from, To: to, ToName: toName, Subject: subject, At: e.At}}}
}

func (d *Daemon) addressKnownLocked(dev, sess string) bool {
	if dev == d.device {
		return sess == "" || sess == proto.CLISession || sess == bus.PersonSession && d.st.Person != "" || d.sessions[sess] != nil
	}
	p := d.peers[dev]
	if p == nil || d.staleLocked(p) {
		return false
	}
	// Every daemon queues for its own CLI, which presence doesn't list.
	if sess == "" || sess == proto.CLISession {
		return true
	}
	for _, s := range p.pres.Sessions {
		if s.ID == sess {
			return true
		}
	}
	return false
}

// sessionInfoLocked describes the session at addr, from this device or a peer's presence.
func (d *Daemon) sessionInfoLocked(addr string) (name, agent, branch string) {
	dev, sess := bus.SplitAddr(addr)
	if dev == d.device {
		if s := d.sessions[sess]; s != nil {
			return s.name, s.agent, s.branch
		}
		if sess == bus.PersonSession && d.st.Person != "" {
			return d.st.Person, bus.AgentPerson, ""
		}
		return "", "", ""
	}
	if p := d.peers[dev]; p != nil {
		for _, s := range p.pres.Sessions {
			if s.ID == sess && sess == bus.PersonSession {
				return d.listedPersonNameLocked(addr, s.Name), s.Agent, s.Branch // only a name that reaches them (#350)
			}
			if s.ID == sess {
				return s.Name, s.Agent, s.Branch
			}
		}
	}
	return "", "", ""
}

// ownerOfLocked is the person behind the session at addr, as its device's presence says: the git
// user name there. "" where it isn't known. Caller holds mu.
func (d *Daemon) ownerOfLocked(addr string) string {
	dev, _ := bus.SplitAddr(addr)
	if dev == d.device {
		return d.owner
	}
	if p := d.peers[dev]; p != nil {
		return p.pres.Owner
	}
	return ""
}

func (d *Daemon) messageViewsLocked(msgs []*inMsg, withBody bool) []proto.MessageView {
	views := make([]proto.MessageView, 0, len(msgs))
	var byAddr map[string]string // the names of senders no longer in presence (lastNameLocked)
	for _, m := range msgs {
		e := m.Event
		name, agent, branch := d.sessionInfoLocked(e.From)
		if name == "" {
			if byAddr == nil {
				byAddr = d.namesByAddrLocked()
			}
			name = byAddr[e.From]
		}
		var device string
		if bus.IsPerson(e.From) {
			// A person's device may be gone from presence: the message says who sent it, if that
			// name is theirs.
			var onMany bool
			agent = bus.AgentPerson
			if name, onMany = d.personSenderLocked(e); onMany {
				device, _ = bus.SplitAddr(e.From)
			}
		}
		v := proto.MessageView{ID: e.ID, From: e.From, FromName: name, FromAgent: agent, FromBranch: branch,
			FromOwner: d.ownerOfLocked(e.From), FromDevice: device, To: e.To, Subject: e.Subject, At: e.At}
		v.Files = d.fileViewsLocked(e)
		if d.isTellNotice(e) {
			v.FromName, v.FromOwner, v.Receipt = brand.Product, "", true // from Murmur itself (#276)
		}
		if withBody {
			v.Body = e.Body
			v.Told = m.NoticedAt
		}
		views = append(views, v)
	}
	return views
}

// whoLocked lists every device and session, or with a filter the sessions whose tags match it and
// the devices they are on. The CLI shows only when it is the one asking.
func (d *Daemon) whoLocked(you string, filter tags.Filter) []proto.Device {
	self := proto.Device{ID: d.device, Host: d.host, Owner: d.owner, Nick: d.nickOfLocked(d.device), Self: true, LastSeen: time.Now(),
		DND: d.dnd.Load()}
	for _, s := range d.sortedSessionsLocked() {
		if d.offMeshLocked(s.id) && s.id != you {
			continue // offline (#189)
		}
		ts := tags.WithDevice(d.tagsOfLocked(s), d.device)
		if !filter.Match(ts) {
			continue
		}
		self.Sessions = append(self.Sessions, proto.SessionView{Addr: bus.Addr(d.device, s.id), Name: s.name,
			Pending: s.name == "", Agent: s.agent, Branch: s.branch, Doing: s.shownDoing(), Started: s.started, LastActive: s.lastActive,
			You: s.id == you, Tags: ts, HookOff: d.hookOffLocked(s)})
	}
	if d.st.Person != "" && filter.Match(tags.Person()) {
		self.Sessions = append(self.Sessions, d.personViewLocked())
	}
	if you == proto.CLISession && filter.Empty() {
		self.Sessions = append(self.Sessions, proto.SessionView{Addr: bus.Addr(d.device, proto.CLISession),
			Agent: "cli", Started: d.started, LastActive: time.Now(), You: true})
	}
	devices := []proto.Device{self}
	for _, id := range sortedKeys(d.peers) {
		p := d.peers[id]
		dv := proto.Device{ID: id, Host: p.pres.Host, Owner: p.pres.Owner, Nick: d.nickOfLocked(id), LastSeen: p.lastChange,
			Stale: d.staleLocked(p), DND: p.pres.DND}
		for _, s := range p.pres.Sessions {
			if s.ID == proto.CLISession {
				continue // V1 daemons list their CLI, which only claims needed
			}
			ts := s.Tags
			if s.ID != bus.PersonSession {
				ts = tags.WithDevice(ts, id) // a person isn't in their device's group, as they get none of its messages
			}
			if !filter.Match(ts) {
				continue
			}
			name := s.Name
			if s.ID == bus.PersonSession {
				name = d.listedPersonNameLocked(bus.Addr(id, s.ID), name)
			}
			dv.Sessions = append(dv.Sessions, proto.SessionView{Addr: bus.Addr(id, s.ID), Name: name,
				Agent: s.Agent, Branch: s.Branch, Doing: s.Doing, Started: peerStarted(p, s), LastActive: peerActive(p, s),
				Tags: ts, HookOff: s.HookOff})
		}
		if !filter.Empty() && len(dv.Sessions) == 0 {
			continue // nothing on it matches
		}
		devices = append(devices, dv)
	}
	return devices
}

// newSessionLocked registers a session that isn't registered. One whose stop hasn't had its time
// (stopSpentLocked: within StoppedInbox, or back as a daemon starts) gets its inbox back, with what arrived while it was stopped, and keeps its claim on
// what was sent since it stopped: a catch-up that reads a backlog ref only after the session has
// registered again would otherwise take those messages for older than the session and drop them.
// Past StoppedInbox, pruneInboxes is only late. Caller holds mu.
func (d *Daemon) newSessionLocked(h *proto.Hello, agent, branch, key string) *session {
	// A key that has a name keeps it, whatever this session's ID, and whichever process
	// registered it: the name is waiting here from the first moment, without the registry.
	now := time.Now()
	s := &session{id: h.ID, agent: agent, cwd: h.Cwd, claudeSession: h.ClaudeSession, branch: branch,
		started: now, lastActive: now, reconnect: h.Reconnect, key: key, clientPID: h.ClientPID}
	// Listed as registered in the same write that takes its stop away, and before the session
	// is told it is registered: a daemon killed at any moment leaves the session either stopped
	// or listed (stopKilledLocked), never neither.
	if d.st.Registered == nil {
		d.st.Registered = map[string]bool{}
	}
	d.st.Registered[h.ID] = true
	if at, ok := d.st.Stopped[h.ID]; ok {
		if d.stopSpentLocked(h.ID, at) {
			d.dropStoppedLocked(h.ID)
		} else {
			delete(d.st.Stopped, h.ID)
			delete(d.st.Downed, h.ID)
			s.since = at
		}
	}
	d.saveLocked()
	// After its own inbox is settled, which an expired stop empties: the name may bring more.
	d.setNameLocked(s, d.nameForKeyLocked(s, key))
	d.sessions[h.ID] = s
	d.noteConversationLocked() // a session starts: its agent is about to look around, and may send
	return s
}
