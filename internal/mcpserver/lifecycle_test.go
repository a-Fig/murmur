// Tests of the server's life in a repo: turning on (activation), turning off (deactivation),
// moving to another repo, and registering again after the daemon goes away. activation.go states
// the passive guarantee the first of them checks.

package mcpserver

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/proto"
)

// helloConn is the daemon's end of the i-th hello connection.
func helloConn(f *fakeDaemon, i int) net.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[i]
}

// helloEnded waits for the server to close the i-th hello connection, which is how the daemon
// learns that a session ended.
func helloEnded(t *testing.T, f *fakeDaemon, i int) {
	t.Helper()
	conn := helloConn(f, i)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("hello connection %d is still open: %v", i, err)
	}
}

// helloOpen checks that the i-th hello connection is still open.
func helloOpen(t *testing.T, f *fakeDaemon, i int) {
	t.Helper()
	conn := helloConn(f, i)
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, err := io.Copy(io.Discard, conn)
	conn.SetReadDeadline(time.Time{})
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("hello connection %d ended: %v", i, err)
	}
}

// gitFiles is every path under the clone's git directory.
func gitFiles(t *testing.T, gitDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(gitDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// In a repo this user hasn't joined, the server doesn't so much as connect to a daemon that is
// running there, and leaves the clone as it found it: no Murmur refs, no file added.
func TestPassiveOpensNoConnectionAndWritesNothing(t *testing.T) {
	f := &fakeDaemon{}
	p := repo(t, true, f)
	before := gitFiles(t, p.GitDir)
	cs, _ := open(t, p.Top, 25*time.Millisecond)
	settle()
	if names := toolNamesOf(t, cs); len(names) != 0 {
		t.Errorf("tools listed where Murmur is off: %q", names)
	}
	if n := f.accepted.Load(); n != 0 {
		t.Errorf("the server made %d connections to the daemon where Murmur is off", n)
	}
	out, err := exec.Command("git", "-C", p.Top, "for-each-ref", "refs/murmur").CombinedOutput()
	if err != nil {
		t.Fatalf("git for-each-ref: %v\n%s", err, out)
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Errorf("Murmur refs in a repo the user hasn't joined:\n%s", out)
	}
	if after := gitFiles(t, p.GitDir); !slices.Equal(before, after) {
		t.Errorf("the clone's git directory changed: before %q, after %q", before, after)
	}
}

// Turned on, the server stays on with one registration: each look at the files that finds the user
// still joined leaves the session as it is.
func TestAJoinedServerRegistersOnce(t *testing.T) {
	f := &fakeDaemon{}
	top, _ := joinedRepo(t, f)
	cs, _ := open(t, top, 25*time.Millisecond)
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	settle() // some sixteen more looks
	if n := f.helloCount(); n != 1 {
		t.Errorf("%d hellos while the user stayed joined", n)
	}
	helloOpen(t, f, 0)
	if !offersTools(toolNamesOf(t, cs)) {
		t.Errorf("tools: %q", toolNamesOf(t, cs))
	}
}

// A leave with the daemon still running ends the session there: the server closes its hello
// connection, takes its tools away, says in its record that it is off, and doesn't register again.
func TestALeaveEndsTheRegistration(t *testing.T) {
	hdir := t.TempDir()
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := openWith(t, Options{Agent: "claude-code", Version: "test", Dir: p.Top, Poll: 25 * time.Millisecond, HereDir: hdir})
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	waitFor(t, "the record to say it is on", func() bool { r, _ := mine(t, hdir); return r.On })

	if _, err := p.ForgetConsent(); err != nil {
		t.Fatal(err)
	}
	helloEnded(t, f, 0)
	waitFor(t, "the tools to go", func() bool { return len(toolNamesOf(t, cs)) == 0 })
	waitFor(t, "the record to say it is off", func() bool { r, _ := mine(t, hdir); return !r.On })
	settle()
	if n := f.helloCount(); n != 1 {
		t.Errorf("registered again after the user left: %d hellos", n)
	}
}

// Joining again after a leave turns the server back on as the same session: a new registration
// with the ID it had, and the tools back.
func TestARejoinRegistersTheSameSessionAgain(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "1e1e1e1e-aaaa-bbbb-cccc-ddddeeeeffff")
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, 25*time.Millisecond)
	waitFor(t, "the first registration", func() bool { return f.helloCount() == 1 })
	first := f.lastHello()

	if _, err := p.ForgetConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tools to go", func() bool { return len(toolNamesOf(t, cs)) == 0 })
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second registration", func() bool { return f.helloCount() == 2 })
	if h := f.lastHello(); h.ID != first.ID || h.ID != sessionID("1e1e1e1e-aaaa-bbbb-cccc-ddddeeeeffff") {
		t.Errorf("registered again as %q, first as %q", h.ID, first.ID)
	}
	waitFor(t, "the tools to come back", func() bool { return offersTools(toolNamesOf(t, cs)) })
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"}); err != nil || res.IsError {
		t.Fatalf("who after joining again: %v %+v", err, res)
	}
}

// A move ends the session in the repo it leaves, as a leave does, and the session registers in the
// new one under the same ID.
func TestAMoveEndsTheSessionInTheOldRepo(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "2f2f2f2f-aaaa-bbbb-cccc-ddddeeeeffff")
	hdir := t.TempDir()
	fa, fb := &fakeDaemon{}, &fakeDaemon{}
	topA, _ := joinedRepo(t, fa)
	topB, _ := joinedRepo(t, fb)
	openWith(t, Options{Agent: "claude-code", Version: "test", Dir: topA, Poll: 25 * time.Millisecond, HereDir: hdir})
	waitFor(t, "the session to register in A", func() bool { return fa.helloCount() == 1 })
	waitFor(t, "the record to say it is on", func() bool { r, _ := mine(t, hdir); return r.On })

	rec, _ := mine(t, hdir)
	if err := here.Ask(hdir, rec, topB, time.Now()); err != nil {
		t.Fatal(err)
	}
	helloEnded(t, fa, 0)
	waitFor(t, "the session to register in B", func() bool { return fb.helloCount() == 1 })
	if a, b := fa.lastHello(), fb.lastHello(); a.ID != b.ID || b.Cwd != topB {
		t.Errorf("registered in A as %q, in B as %q with cwd %q", a.ID, b.ID, b.Cwd)
	}
}

// A move to another folder of the clone the session is in changes nothing: the registration stays
// as it was.
func TestAMoveWithinTheSameCloneKeepsTheRegistration(t *testing.T) {
	hdir := t.TempDir()
	f := &fakeDaemon{}
	top, gitDir := joinedRepo(t, f)
	sub := filepath.Join(top, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	cs, _ := openWith(t, Options{Agent: "claude-code", Version: "test", Dir: top, Poll: 25 * time.Millisecond, HereDir: hdir})
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	waitFor(t, "the record to say it is on", func() bool { r, _ := mine(t, hdir); return r.On })

	rec, _ := mine(t, hdir)
	if err := here.Ask(hdir, rec, sub, time.Now()); err != nil {
		t.Fatal(err)
	}
	settle()
	helloOpen(t, f, 0)
	if n := f.helloCount(); n != 1 {
		t.Errorf("%d hellos after a move within the clone", n)
	}
	if r, _ := mine(t, hdir); !r.On || r.GitDir != gitDir {
		t.Errorf("record after a move within the clone: %+v", r)
	}
	if !offersTools(toolNamesOf(t, cs)) {
		t.Errorf("tools after a move within the clone: %q", toolNamesOf(t, cs))
	}
}

// When the daemon goes away and comes back (it restarted, say), the server registers again as the
// same session, saying it is a reconnection, and its client sees no change: the tools stay listed
// and a call goes through.
func TestADaemonRestartKeepsTheSessionOn(t *testing.T) {
	f := &fakeDaemon{}
	top, _ := joinedRepo(t, f)
	cs, changed := open(t, top, 25*time.Millisecond)
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	waitFor(t, "the tools to appear", func() bool { return offersTools(toolNamesOf(t, cs)) })
	settle()
	first, lists := f.lastHello(), changed.Load()

	f.dropConnections()
	waitFor(t, "the session to register again", func() bool { return f.helloCount() == 2 })
	if h := f.lastHello(); h.ID != first.ID || !h.Reconnect {
		t.Errorf("registered again with %+v, first with ID %q", h, first.ID)
	}
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"}); err != nil || res.IsError {
		t.Fatalf("who after the reconnect: %v %+v", err, res)
	}
	if !offersTools(toolNamesOf(t, cs)) || changed.Load() != lists {
		t.Errorf("the tools changed over a reconnect: %q, %d list changes", toolNamesOf(t, cs), changed.Load()-lists)
	}
}

// A session whose ID the daemon keeps saying is in use (a previous server for it not yet seen gone,
// or two sessions that drew the same ID) tries three times, then registers under a new one.
func TestASessionWhoseIDStaysInUseTakesANewOne(t *testing.T) {
	h := helloFor("claude-code", "/work")
	taken := h.ID
	f := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.ID == taken {
			return "session ID " + h.ID + " is in use", ""
		}
		return "", ""
	}}
	startedLink(t, f, nil, h)
	if n := f.helloCount(); n != 4 {
		t.Errorf("%d hellos, want three refused and one more", n)
	}
	if got := f.lastHello().ID; got == taken || len(got) != 6 {
		t.Errorf("registered as %q, the ID in use was %q", got, taken)
	}
}
