// This file turns the server on and off, and so holds its passive guarantee: in a repo this user
// hasn't joined in this clone, the server lists no tools, gives no instructions, starts no daemon,
// writes no ref and opens no connection. Where that is enforced:
//
//   - Serve gives Instructions only if join.Place.Joined holds at initialize, and runs sync, which
//     turns the server on (activate) only while Joined holds, looked at every Options.Poll.
//   - activate is the one place tools are added. It makes the session's link to the daemon, or, for
//     a shared server, sets s.links, without which sessionLinkFor makes none. deactivate takes all
//     of it away on a leave or a move.
//   - The client activate makes may start a daemon only while Joined holds (client.Client.MaySpawn).
//   - A leave can come between two looks, so link.run asks again before every hello (link.allowed),
//     and callOn before each tool call's request (calls.go). Route updates don't pass through
//     callOn: tellRoute and sendRoute, from a tool call's _meta (noteMeta, learnNative), from
//     classifyFirst's retry, which runs before callOn's check, and from the seek. Those rely on the
//     daemon, which checks the consent itself on every request but stop and ping
//     (docs/design/joining.md).
//   - The server never writes to the bus: refs are the daemon's, and an off server reaches none.
//
// Two things run in every repo, both in the user's own directories and outside any repo: the
// session's record for `mur join` (move.go), and Run's refresh of Pi's and OpenCode's plugin
// files. The tests of all this are TestPassive*, TestJoinThenLeaveWhileRunning and the leave
// tests in mcpserver_test.go, and lifecycle_test.go.

package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/proto"
)

// errPassive is what a tool call says in a repo Murmur isn't on for.
var errPassive = fmt.Errorf("%[1]s isn't on for this repo. It is once this user has joined in this clone "+
	"(`%[2]s join`, after asking them)", brand.Product, brand.Command)

// watch turns the server on and off as the repo's files change.
func (s *server) watch(ctx context.Context) {
	tick := time.NewTicker(s.opt.Poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.sync(ctx)
		}
	}
}

func (s *server) sync(ctx context.Context) {
	s.follow(ctx)
	if s.placeNow().Joined() {
		s.activate(ctx)
	} else {
		s.deactivate()
	}
	s.publish()
}

// sessionLink is one session's link in a server that serves many.
type sessionLink struct {
	l      *link
	cancel context.CancelFunc
	last   time.Time // its latest tool call
}

// sharedAgent says whether one server process serves every session of the agent in a directory,
// each tool call naming its session: OpenCode starts one MCP process per server and directory,
// and puts the session's ID in each call's _meta as "ai.opencode/sessionID" (seen on OpenCode
// 2.2.0, 2026-11-01). That ID is the one T3 Code keeps for the thread's provider session.
func sharedAgent(agent string) bool { return agent == "opencode" }

const openCodeSessionMeta = "ai.opencode/sessionID"

// sharedIdle is how long a session of a shared server goes without a tool call before its
// registration is dropped, as a session whose server stopped is: OpenCode never says a session
// ended, and its server can run for days. A message to the session still reaches its T3 thread,
// whose route the daemon keeps (docs/design/handoff.md, "A thread that is idle"). A variable, for
// tests.
var sharedIdle = 30 * time.Minute

// activate lists the tools and registers the session with the daemon, starting it if needed.
func (s *server) activate(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on || s.closed {
		return
	}
	c := &client.Client{GitDir: s.place.GitDir, Spawn: true, MaySpawn: s.place.Joined}
	lctx, cancel := context.WithCancel(ctx)
	s.c, s.stop, s.on = c, cancel, true
	if sharedAgent(s.opt.Agent) {
		// No session until a call names one: registering the process would show a session that is
		// no conversation.
		s.links, s.lctx = map[string]*sessionLink{}, lctx
		s.tools(s.srv)
		return
	}
	l := s.newLink(lctx, c, helloFor(s.opt.Agent, s.cwd))
	if s.opt.Agent == "cursor" && s.hereDir != "" && l.native == "" && cursorCLI(s.self.ClientPID) {
		dir, self := s.hereDir, s.self
		l.named = func() (string, bool) { return namedOnly(dir, self) }
	}
	s.link = l
	s.tools(s.srv)
	s.goBG(func() { l.run(lctx) })
}

// cursorCLI is here.CursorCLI: only a server Cursor's command line started waits for its hook to name
// the chat. A variable, for tests.
var cursorCLI = func(pid int) bool { return here.CursorCLI(pid) }

// namedOnly is the conversation Cursor's hook named for the client of the server whose record is
// self (here.NamedFor), only while that client runs one server of Cursor's: in T3 Code one process
// runs every Cursor thread, and a name left for it may be any of theirs, so shared says no name
// will come.
func namedOnly(dir string, self here.Session) (native string, shared bool) {
	n := 0
	for _, r := range here.Of(dir, []int{self.ClientPID}) {
		if r.Agent == self.Agent {
			n++
		}
	}
	if n > 1 {
		return "", true
	}
	return here.NamedFor(dir, self), false
}

// newLink makes the link for a registration, which runs in ctx. Its goroutines are background work
// Serve waits for.
func (s *server) newLink(ctx context.Context, c *client.Client, h proto.Hello) *link {
	l := &link{c: c, hello: h, native: h.Native, changed: make(chan struct{}), allowed: s.place.Joined, seek: currentSeek(),
		ctx: ctx, goBG: s.goBGUnlocked}
	l.findInT3()
	return l
}

// sessionLinkFor is the link of the session a call to a shared server comes from, made and started
// at the session's first call. A call that names no session (from OpenCode 1, which doesn't say)
// goes by one link for the whole process, as a server of any other agent does, with the ID Murmur's
// plugin gave the process, if it did (nativeEnv). Sessions idle for
// sharedIdle are dropped on the way. It is nil when the server is off.
func (s *server) sessionLinkFor(req *mcp.CallToolRequest) *link {
	var native string
	if req != nil && req.Params != nil {
		native, _ = req.Params.GetMeta()[openCodeSessionMeta].(string)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.links == nil || s.closed {
		return nil
	}
	now := time.Now()
	for id, sl := range s.links {
		if id != native && now.Sub(sl.last) > sharedIdle {
			sl.cancel()
			delete(s.links, id)
		}
	}
	if sl := s.links[native]; sl != nil {
		sl.last = now
		return sl.l
	}
	h := helloFor(s.opt.Agent, s.cwd)
	if native != "" {
		h.ID, h.Native = nativeID(native), native
	}
	ctx, cancel := context.WithCancel(s.lctx)
	l := s.newLink(ctx, s.c, h)
	s.links[native] = &sessionLink{l: l, cancel: cancel, last: now}
	s.goBG(func() { l.run(ctx) })
	return l
}

// deactivate takes the tools away and drops the registration, which the daemon sees as the session
// ending.
func (s *server) deactivate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.on {
		return
	}
	s.stop()
	s.c, s.link, s.stop, s.on = nil, nil, nil, false
	s.links, s.lctx = nil, nil
	s.srv.RemoveTools(toolNames...)
}
