package daemon_test

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of the commits a session is credited with, of when it was last active, and of lookup
// (#21). A tool call is bracketed by the Claude Code hook: the daemon snapshots the worktree at
// the call's start and credits the commits that appeared by its end.

// tree is the clone's own working directory.
func (d *device) tree() string { return filepath.Dir(d.gitDir) }

// linkedTree adds a worktree of the clone on a new branch, in a folder called name.
func (d *device) linkedTree(name, branch string) string {
	d.t.Helper()
	dir := filepath.Join(d.t.TempDir(), name)
	git(d.t, d.tree(), "worktree", "add", "-q", "-b", branch, dir)
	return dir
}

// claudeSession registers the session of an MCP server whose parent is Claude Code process pid,
// working in cwd, in conversation claude. It returns the session's address and its connection.
func (d *device) claudeSession(id, claude string, pid int, cwd string) (string, net.Conn) {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(),
		proto.Hello{ID: id, Agent: "claude-code", Cwd: cwd, ClaudeSession: claude, ClientPID: pid})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn
}

// callStart and callEnd are what the hook sends the daemon before and after a tool call that ran
// in cwd.
func (d *device) callStart(claude string, pid int, toolUse, cwd string) {
	d.t.Helper()
	d.callEvent(proto.OpToolStart, claude, pid, toolUse, cwd, d.gitDir)
}

func (d *device) callEnd(claude string, pid int, toolUse, cwd string) {
	d.t.Helper()
	d.callEvent(proto.OpToolEnd, claude, pid, toolUse, cwd, d.gitDir)
}

func (d *device) callEvent(op, claude string, pid int, toolUse, cwd, gitDir string) {
	d.t.Helper()
	r := d.do(proto.Request{Op: op, ClaudeSession: claude, ClientPID: pid, ToolUseID: toolUse, Cwd: cwd, GitDir: gitDir})
	if !r.OK {
		d.t.Fatalf("%s: %s", op, r.Error)
	}
}

// commitFile commits a new file in dir, on whatever it has checked out, and returns the SHA.
func commitFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", name)
	gitIn(t, dir, "", "commit", "-q", "-m", "add "+name)
	return git(t, dir, "rev-parse", "HEAD")
}

// look is lookup, asked by the session as (an ID on d, or "" for the CLI) of name.
func (d *device) look(as, name string) *proto.SessionDetail {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpLookup, Session: as, Name: name})
	if !r.OK || r.Lookup == nil {
		d.t.Fatalf("lookup %q: %q", name, r.Error)
	}
	return r.Lookup
}

func commitSHAs(det *proto.SessionDetail) []string {
	var out []string
	for _, c := range det.Commits {
		out = append(out, c.SHA)
	}
	return out
}

// allBus is the text of every commit under refs/murmur/ on the remote: all that the bus carries.
func allBus(t *testing.T, remote string) string {
	t.Helper()
	work := t.TempDir()
	git(t, work, "init", "-q")
	git(t, work, "fetch", "-q", remote, "+refs/murmur/*:refs/murmur/*")
	return git(t, work, "log", "--format=%B", "--all")
}

// noneOnBus fails if any of the texts is on the bus.
func noneOnBus(t *testing.T, remote string, texts ...string) {
	t.Helper()
	bus := allBus(t, remote)
	if !strings.Contains(bus, "refs") && !strings.Contains(bus, "sessions") {
		t.Fatalf("the bus text looks empty, so this proves nothing:\n%s", bus)
	}
	for _, s := range texts {
		if strings.Contains(bus, s) {
			t.Errorf("%q is on the bus; it should stay on the device", s)
		}
	}
}

// lookFor is look for a wait: ok is false while d can't resolve name yet, as a session of another
// device is known only once its presence or the registry has reached d.
func (d *device) lookFor(as, name string) (det *proto.SessionDetail, ok bool) {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpLookup, Session: as, Name: name})
	return r.Lookup, r.OK && r.Lookup != nil
}

func presenceTip(t *testing.T, d *device, remote string) string {
	t.Helper()
	return strings.Fields(git(t, d.tree(), "ls-remote", remote, "refs/murmur/presence/"+d.self()))[0]
}

// presenceSettles waits for d's presence on the remote to stop changing, the same tip for two
// seconds in a row, and returns that tip. A session's start and its naming each ask for a write,
// and on a busy machine one can land seconds after the last name shows.
func presenceSettles(t *testing.T, d *device, remote string) string {
	t.Helper()
	tip, since := presenceTip(t, d, remote), time.Now()
	eventually(t, "presence to stop changing", func() bool {
		if now := presenceTip(t, d, remote); now != tip {
			tip, since = now, time.Now()
		}
		return time.Since(since) >= 2*time.Second
	})
	return tip
}

// A tool call that commits is credited to the session, with the branch, in lookup on its own
// device and in the presence another device reads. The commit's SHA, the worktree and the
// client's session ID go no further than the device.
func TestToolCallThatCommitsIsCredited(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	wt := a.linkedTree("wt-secret-folder", "feat-1")
	alice, _ := a.claudeSession("aaaaaa", "claude-secret-id", procP, wt)
	bob, _ := b.session("bbbbbb", "")
	name := a.named(alice)
	eventually(t, "presence", func() bool { return b.seesSession(alice) && a.seesSession(bob) })

	a.callStart("claude-secret-id", procP, "toolu_1", wt)
	sha := commitFile(t, wt, "one.txt")
	a.callEnd("claude-secret-id", procP, "toolu_1", wt)

	det := a.look("aaaaaa", name)
	if !slices.Equal(commitSHAs(det), []string{sha}) || det.Commits[0].Branch != "feat-1" {
		t.Fatalf("credited commits %+v, want %s on feat-1", det.Commits, sha)
	}
	if det.Commits[0].At.IsZero() || time.Since(det.Commits[0].At) > time.Minute {
		t.Errorf("the credit's time is %v", det.Commits[0].At)
	}
	if !slices.Equal(det.Committed, []string{"feat-1"}) {
		t.Errorf("committed to %v, want [feat-1]", det.Committed)
	}
	if !det.Local || det.Worktree != "wt-secret-folder" || det.ClientSession != "claude-secret-id" {
		t.Errorf("a session of this device should show its worktree folder and client ID: %+v", det)
	}

	// The next call is on another branch: the list is most recent first.
	git(t, wt, "switch", "-q", "-c", "feat-2")
	a.callStart("claude-secret-id", procP, "toolu_2", wt)
	commitFile(t, wt, "two.txt")
	a.callEnd("claude-secret-id", procP, "toolu_2", wt)
	if got := a.look("aaaaaa", name).Committed; !slices.Equal(got, []string{"feat-2", "feat-1"}) {
		t.Errorf("committed to %v, want [feat-2 feat-1]", got)
	}

	// Another device reads it from presence, and has nothing of what stays local. b saw alice
	// above, but maybe not yet by her name, which comes with a later write.
	var far *proto.SessionDetail
	eventually(t, "the branches in presence on b", func() bool {
		det, ok := b.lookFor("bbbbbb", name)
		far = det
		return ok && slices.Equal(det.Committed, []string{"feat-2", "feat-1"})
	})
	if far.Local || far.Worktree != "" || far.ClientSession != "" || len(far.Commits) != 0 {
		t.Errorf("a session of another device shows what stays local: %+v", far)
	}
	noneOnBus(t, remote, "wt-secret-folder", "claude-secret-id", sha)

	// The page shows them too, and when the session was last active.
	var st web.State
	a.page().get("/api/state", &st)
	if _, s, ok := findSession(st, name); !ok || !slices.Equal(s.Committed, []string{"feat-2", "feat-1"}) || s.LastActive.IsZero() {
		t.Errorf("a's page shows %+v for the session, want its committed branches and last-active time", s)
	}
	eventually(t, "the committed branches on b's page", func() bool {
		var st web.State
		b.page().get("/api/state", &st)
		_, s, ok := findSession(st, name)
		return ok && slices.Equal(s.Committed, []string{"feat-2", "feat-1"}) && !s.LastActive.IsZero()
	})
}

// A commit on a detached HEAD is credited and kept, and says so, but has no branch to add to the
// list the others see.
func TestDetachedCommitIsKeptWithoutABranch(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	wt := a.linkedTree("wt-detached", "feat-d")
	alice, _ := a.claudeSession("aaaaaa", "claude-d", procP, wt)
	name := a.named(alice)

	git(t, wt, "switch", "-q", "--detach")
	a.callStart("claude-d", procP, "toolu_d", wt)
	sha := commitFile(t, wt, "detached.txt")
	a.callEnd("claude-d", procP, "toolu_d", wt)

	det := a.look("aaaaaa", name)
	if !slices.Equal(commitSHAs(det), []string{sha}) || det.Commits[0].Branch != "" {
		t.Fatalf("credited commits %+v, want %s with no branch", det.Commits, sha)
	}
	if len(det.Committed) != 0 {
		t.Errorf("committed to %v; a detached commit names no branch", det.Committed)
	}
	if out := view.Lookup(proto.Response{Lookup: det}); !strings.Contains(out, "detached HEAD") {
		t.Errorf("lookup doesn't say the commit was made on a detached HEAD:\n%s", out)
	}
}

// Only a commit that appears during a session's own tool call is its: not one made before the
// call, or between two calls, and not one that session Y's call overlapped when it was in another
// worktree.
func TestOnlyCommitsDuringTheSessionsCallsAreCredited(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	wtX := a.linkedTree("wt-x", "feat-x")
	wtY := a.linkedTree("wt-y", "feat-y")
	x, _ := a.claudeSession("aaaaaa", "claude-x", procP, wtX)
	y, _ := a.claudeSession("bbbbbb", "claude-y", procQ, wtY)
	nx, ny := a.named(x), a.named(y)

	// Outside any call: before the first, and between two.
	commitFile(t, wtX, "before.txt")
	a.callStart("claude-x", procP, "toolu_1", wtX)
	a.callEnd("claude-x", procP, "toolu_1", wtX)
	// The end is answered before its after-snapshot is taken. A lookup waits for that snapshot,
	// so the next commit is made after it, as one made between calls is.
	if got := a.look("aaaaaa", nx).Commits; len(got) != 0 {
		t.Fatalf("commits made before the first call were credited: %+v", got)
	}
	commitFile(t, wtX, "between.txt")
	a.callStart("claude-x", procP, "toolu_2", wtX)
	a.callEnd("claude-x", procP, "toolu_2", wtX)
	if got := a.look("aaaaaa", nx).Commits; len(got) != 0 {
		t.Fatalf("commits made outside a call were credited: %+v", got)
	}

	// X commits during a call of its own that Y's call overlaps, Y being in another worktree.
	a.callStart("claude-y", procQ, "toolu_y", wtY)
	a.callStart("claude-x", procP, "toolu_3", wtX)
	sha := commitFile(t, wtX, "during.txt")
	a.callEnd("claude-x", procP, "toolu_3", wtX)
	a.callEnd("claude-y", procQ, "toolu_y", wtY)

	if got := commitSHAs(a.look("aaaaaa", nx)); !slices.Equal(got, []string{sha}) {
		t.Errorf("X is credited with %v, want only %s", got, sha)
	}
	if got := a.look("bbbbbb", ny).Commits; len(got) != 0 {
		t.Errorf("Y was credited with X's commit: %+v", got)
	}
}

// Calls run in parallel, each with its own tool_use_id: one commit seen by two of them is credited
// once. An end with no start, and a start whose end never comes, credit nothing, and a call that
// never ends doesn't make the next one claim what came before it.
func TestParallelCallsAndACallThatNeverEnds(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	wt := a.linkedTree("wt-p", "feat-p")
	alice, _ := a.claudeSession("aaaaaa", "claude-p", procP, wt)
	name := a.named(alice)

	a.callStart("claude-p", procP, "toolu_p1", wt)
	a.callStart("claude-p", procP, "toolu_p2", wt)
	sha := commitFile(t, wt, "p.txt")
	a.callEnd("claude-p", procP, "toolu_p2", wt)
	a.callEnd("claude-p", procP, "toolu_p1", wt)
	if got := commitSHAs(a.look("aaaaaa", name)); !slices.Equal(got, []string{sha}) {
		t.Fatalf("two parallel calls saw one commit; credited %v, want it once", got)
	}

	// A commit made in one call that a second began after: the first call's end still finds it,
	// as the two are paired by their ids and not by the session.
	a.callStart("claude-p", procP, "toolu_q1", wt)
	early := commitFile(t, wt, "early.txt")
	a.callStart("claude-p", procP, "toolu_q2", wt)
	a.callEnd("claude-p", procP, "toolu_q2", wt)
	a.callEnd("claude-p", procP, "toolu_q1", wt)
	if got := commitSHAs(a.look("aaaaaa", name)); !slices.Equal(got, []string{sha, early}) {
		t.Fatalf("a commit made before the second of two overlapping calls began: credited %v, want %s then %s", got, sha, early)
	}

	// The same id from another conversation is another call: the key has the conversation in it. An
	// end that comes again for a call already closed finds nothing to close, and credits nothing.
	other, _ := a.claudeSession("cccccc", "claude-other", procQ, wt)
	a.callStart("claude-other", procQ, "toolu_p1", wt)
	commitFile(t, wt, "other.txt")
	a.callEnd("claude-p", procP, "toolu_p1", wt) // already closed; must not close the other's
	a.callEnd("claude-other", procQ, "toolu_p1", wt)
	if got := a.look("cccccc", a.named(other)).Commits; len(got) != 1 {
		t.Errorf("the other conversation's call was closed by a stranger's end: %+v", got)
	}
	if got := a.look("aaaaaa", name).Commits; len(got) != 2 {
		t.Errorf("the other conversation's commit was credited to this one too: %+v", got)
	}

	// An end whose start was never seen credits nothing.
	commitFile(t, wt, "no-start.txt")
	a.callEnd("claude-p", procP, "toolu_unknown", wt)
	// A start with no end: the commit made while it is open is nobody's yet, and a later call that
	// starts after it doesn't claim it.
	a.callStart("claude-p", procP, "toolu_never", wt)
	commitFile(t, wt, "while-open.txt")
	a.callStart("claude-p", procP, "toolu_later", wt)
	a.callEnd("claude-p", procP, "toolu_later", wt)
	if got := a.look("aaaaaa", name).Commits; len(got) != 2 {
		t.Errorf("credited %+v; only the first two commits were made during calls that ended", got)
	}
}

// A call that ends with the hook in another worktree of the clone is judged in both: where it
// started, and where it ended. The start's tips are the clone's, so the second worktree's history
// is kept out, and only what the call made there is credited. A third worktree it never went to
// is nobody's.
func TestEndInAnotherWorktreeIsJudgedInBoth(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	wt1 := a.linkedTree("wt-one", "feat-one")
	wt2 := a.linkedTree("wt-two", "feat-two")
	wt3 := a.linkedTree("wt-three", "feat-three")
	alice, _ := a.claudeSession("aaaaaa", "claude-w", procP, wt1)
	name := a.named(alice)

	a.callStart("claude-w", procP, "toolu_w", wt1)
	one := commitFile(t, wt1, "one.txt")
	two := commitFile(t, wt2, "two.txt")
	commitFile(t, wt3, "three.txt")
	a.callEnd("claude-w", procP, "toolu_w", wt2)
	det := a.look("aaaaaa", name)
	if got := commitSHAs(det); !slices.Equal(got, []string{one, two}) {
		t.Errorf("credited %v, want the commit in the worktree it started in (%s) and in the one it ended in (%s)", got, one, two)
	}
	if !slices.Equal(det.Committed, []string{"feat-two", "feat-one"}) {
		t.Errorf("committed to %v, want [feat-two feat-one]", det.Committed)
	}
}

// A call that ends in another worktree, detached there, isn't credited with a commit made on that
// HEAD just before it started, which no ref reaches: every worktree's HEAD is among the start's
// tips (#60). The commit must fall within the 2 s attrib allows before the start for this to show
// anything, which it does unless the machine is very slow.
func TestDetachedCommitInTheWorktreeACallEndsInBeforeItIsNotCredited(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	wt1 := a.linkedTree("wt-start", "feat-start")
	wt2 := a.linkedTree("wt-end", "feat-end")
	alice, _ := a.claudeSession("aaaaaa", "claude-x", procP, wt1)
	name := a.named(alice)

	git(t, wt2, "switch", "-q", "--detach")
	commitFile(t, wt2, "before.txt")
	a.callStart("claude-x", procP, "toolu_x", wt1)
	a.callEnd("claude-x", procP, "toolu_x", wt2)
	if got := a.look("aaaaaa", name).Commits; len(got) != 0 {
		t.Errorf("credited %+v; the call made no commit", got)
	}
}

// A call in another repository, or in none, is not this daemon's to watch.
func TestCallOutsideTheCloneIsSkipped(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	alice, _ := a.claudeSession("aaaaaa", "claude-o", procP, a.tree())
	name := a.named(alice)

	other := t.TempDir()
	git(t, other, "init", "-q", "--initial-branch=main")
	gitIn(t, other, "", "commit", "-q", "--allow-empty", "-m", "init")
	a.callEvent(proto.OpToolStart, "claude-o", procP, "toolu_o", other, filepath.Join(other, ".git"))
	commitFile(t, other, "other.txt")
	a.callEvent(proto.OpToolEnd, "claude-o", procP, "toolu_o", other, filepath.Join(other, ".git"))

	bare := t.TempDir() // not a repository: the hook found no clone
	a.callEvent(proto.OpToolStart, "claude-o", procP, "toolu_n", bare, "")
	a.callEvent(proto.OpToolEnd, "claude-o", procP, "toolu_n", bare, "")

	if got := a.look("aaaaaa", name).Commits; len(got) != 0 {
		t.Errorf("a call outside the clone was credited: %+v", got)
	}
}

// Last active moves with each hook request and each request of the session's MCP server, not with
// the CLI's or a stranger's. It reaches another device in the next presence write, which these
// requests don't cause.
//
// Serial: it shows that no write came, and a write the daemon was already making can land late
// enough to look like one. Waiting for the names and a still tip rules out what can be waited
// for, but a push that failed is retried 15 s later, and the parallel tests' load makes that and a
// slow publish likelier (#369).
func TestLastActiveMovesAndTravelsWithoutWritingPresence(t *testing.T) {
	remote := newRemote(t)
	slow := func(d *device) { d.beat, d.stale = time.Hour, time.Minute }
	a, b := newDeviceWith(t, remote, slow), newDeviceWith(t, remote, slow)
	wt := a.linkedTree("wt-la", "feat-la")
	alice, _ := a.claudeSession("aaaaaa", "claude-la", procP, wt)
	helper, _ := a.session("cccccc", "claude-helper")
	bob, _ := b.session("bbbbbb", "")
	name, helperName := a.named(alice), a.named(helper)
	b.named(bob)
	// The writes the starts and the names caused have landed once b has both names and a's
	// presence has stopped changing.
	b.nameIs(alice, name)
	b.nameIs(helper, helperName)
	tip := presenceSettles(t, a, remote)

	active := func() time.Time { return a.look("cccccc", name).LastActive }
	registered := active()
	if registered.IsZero() {
		t.Fatal("a registered session has no last-active time")
	}
	time.Sleep(5 * time.Second) // so the activity below is far from the registration

	last := registered
	moves := func(what string, do func()) {
		t.Helper()
		time.Sleep(20 * time.Millisecond)
		do()
		now := active()
		if !now.After(last) {
			t.Errorf("%s didn't move last active (%v, was %v)", what, now, last)
		}
		last = now
	}
	moves("a hook asking for notices", func() { a.hook("claude-la", procP) })
	moves("the start of a tool call", func() { a.callStart("claude-la", procP, "toolu_la", wt) })
	moves("the end of a tool call", func() { a.callEnd("claude-la", procP, "toolu_la", wt) })
	moves("an MCP request", func() { a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}) })

	// Requests that aren't the session's leave it alone.
	time.Sleep(20 * time.Millisecond)
	a.do(proto.Request{Op: proto.OpWho})                    // the CLI
	a.hook("claude-nobody", 9999)                           // a hook of no session here
	a.do(proto.Request{Op: proto.OpWho, Session: "cccccc"}) // another session's
	a.callStart("claude-nobody", 9999, "toolu_x", a.tree()) // likewise
	if got := active(); !got.Equal(last) {
		t.Errorf("last active moved to %v on requests that weren't the session's (was %v)", got, last)
	}

	// None of that wrote presence.
	time.Sleep(time.Second)
	if now := presenceTip(t, a, remote); now != tip {
		t.Fatalf("presence was written (%s, was %s) by requests that should only move last active", now, tip)
	}

	// The next write carries it: a status line is what asks for one here. What b makes of it is
	// the time of the last activity plus however long the write took to arrive, which is seconds on
	// a busy machine, so the bound is loose; TestPeerActiveIsTheWritersGapOnThisClock has the exact
	// arithmetic.
	a.do(proto.Request{Op: proto.OpStatus, Session: "cccccc", Doing: "something else"})
	eventually(t, "last active on b", func() bool {
		got := b.look("bbbbbb", name).LastActive
		return !got.IsZero() && got.After(last.Add(-time.Second)) && got.Before(last.Add(30*time.Second))
	})
}

// slowHook gives the hook the time git needs on a machine busy with other test runs, which the
// real limits (a few seconds, so a tool is never held up for long) don't.
func slowHook(t *testing.T) {
	t.Helper()
	notice, bracket := hook.NoticeTimeout, hook.BracketTimeout
	hook.NoticeTimeout, hook.BracketTimeout = time.Minute, 90*time.Second
	t.Cleanup(func() { hook.NoticeTimeout, hook.BracketTimeout = notice, bracket })
}

// The hook's own program, driven with the payloads Claude Code sends, credits a commit end to end:
// the start prints nothing, a tool that can't run a command isn't bracketed, a failing call is
// closed by PostToolUseFailure, and the notices still arrive after a tool call that is bracketed.
func TestHookBracketsToolCalls(t *testing.T) {
	slowHook(t)
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	wt := a.linkedTree("wt-hook", "feat-hook")
	alice, _ := a.claudeSession("aaaaaa", "claude-hook", procP, wt)
	bob, _ := b.session("bbbbbb", "")
	name := a.named(alice)
	eventually(t, "presence", func() bool { return b.seesSession(alice) && a.seesSession(bob) })

	payload := func(event, tool, toolUse string) string {
		out, _ := json.Marshal(map[string]string{"session_id": "claude-hook", "hook_event_name": event, "cwd": wt,
			"tool_name": tool, "tool_use_id": toolUse})
		return string(out)
	}
	run := func(event, tool, toolUse string) string {
		t.Helper()
		t.Setenv("CLAUDE_PID", strconv.Itoa(procP))
		var out bytes.Buffer
		hook.Run(strings.NewReader(payload(event, tool, toolUse)), &out)
		return out.String()
	}

	// A message waiting: the start prints nothing, and the end gives the notice.
	b.nameIs(alice, name)
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: name, Subject: "a question"}); !r.OK {
		t.Fatalf("send: %s", r.Error)
	}
	eventually(t, "the message to reach a", func() bool { return logHas(a, "a question") })
	if out := run("PreToolUse", "Bash", "toolu_h1"); out != "" {
		t.Fatalf("PreToolUse printed %q", out)
	}
	sha := commitFile(t, wt, "hook.txt")
	if out := run("PostToolUse", "Bash", "toolu_h1"); !strings.Contains(out, "a question") || !strings.Contains(out, `"PostToolUse"`) {
		t.Errorf("PostToolUse printed %q; the notice is missing", out)
	}
	if got := commitSHAs(a.look("aaaaaa", name)); !slices.Equal(got, []string{sha}) {
		t.Fatalf("credited %v, want %s", got, sha)
	}

	// Read can't run a command: neither end is sent, so a commit made meanwhile isn't found.
	run("PreToolUse", "Read", "toolu_h2")
	commitFile(t, wt, "during-read.txt")
	run("PostToolUse", "Read", "toolu_h2")
	if got := a.look("aaaaaa", name).Commits; len(got) != 1 {
		t.Errorf("a call of a tool that can't commit was bracketed: %+v", got)
	}

	// A failing call fires PostToolUseFailure: the commit before the failure counts.
	run("PreToolUse", "Bash", "toolu_h3")
	sha3 := commitFile(t, wt, "then-failed.txt")
	if out := run("PostToolUseFailure", "Bash", "toolu_h3"); out != "" {
		t.Errorf("PostToolUseFailure printed %q", out)
	}
	if got := commitSHAs(a.look("aaaaaa", name)); !slices.Equal(got, []string{sha, sha3}) {
		t.Errorf("credited %v, want %s then %s", got, sha, sha3)
	}

	// Garbage and a payload with no tool call do nothing, quietly.
	noCall, _ := json.Marshal(map[string]string{"session_id": "claude-hook", "hook_event_name": "PreToolUse", "cwd": wt})
	for _, in := range []string{"", "not json", string(noCall)} {
		var out bytes.Buffer
		hook.Run(strings.NewReader(in), &out)
		if out.Len() != 0 {
			t.Errorf("hook printed %q for input %q", out.String(), in)
		}
	}
}

// With no daemon running, PreToolUse prints nothing and returns promptly: a tool is never held up
// by Murmur being down, and nothing makes the hook exit with an error (2 would block the tool).
func TestPreToolUseWithTheDaemonDownIsSilent(t *testing.T) {
	slowHook(t)
	remote := newRemote(t)
	a := newDevice(t, remote)
	wt := a.linkedTree("wt-down", "feat-down")
	alice, _ := a.claudeSession("aaaaaa", "claude-down", procP, wt)
	name := a.named(alice)

	// With the daemon up the start does reach it, so the silence below is the daemon's absence.
	t.Setenv("CLAUDE_PID", strconv.Itoa(procP))
	before := a.look("aaaaaa", name).LastActive
	time.Sleep(20 * time.Millisecond)
	up, _ := json.Marshal(map[string]string{"session_id": "claude-down", "hook_event_name": "PreToolUse", "cwd": wt,
		"tool_name": "Bash", "tool_use_id": "toolu_up"})
	var upOut bytes.Buffer
	hook.Run(bytes.NewReader(up), &upOut)
	if upOut.Len() != 0 || !a.look("aaaaaa", name).LastActive.After(before) {
		t.Fatalf("PreToolUse with the daemon up printed %q and didn't reach it", upOut.String())
	}
	a.stop()

	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		in, _ := json.Marshal(map[string]string{"session_id": "claude-down", "hook_event_name": event, "cwd": wt,
			"tool_name": "Bash", "tool_use_id": "toolu_down"})
		var out bytes.Buffer
		start := time.Now()
		hook.Run(bytes.NewReader(in), &out)
		if out.Len() != 0 {
			t.Errorf("%s printed %q with no daemon", event, out.String())
		}
		if took := time.Since(start); took > 8*time.Second {
			t.Errorf("%s took %v with no daemon", event, took)
		}
	}
}

// lookup answers for a live session here, one on another device, one the registry knows that is
// not running (from its own device and from another), and errors for a name nobody has.
func TestLookup(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep = 300*time.Millisecond, time.Hour })
	b := newDevice(t, remote)
	wt := a.linkedTree("wt-lookup", "feat-l")
	alice, _ := a.claudeSession("aaaaaa", "claude-lookup", procP, wt)
	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "writing the lookup"})
	bob, _ := b.session("bbbbbb", "claude-bob")
	b.do(proto.Request{Op: proto.OpStatus, Session: "bbbbbb", Doing: "reviewing"})
	carol, carolConn := a.session("cccccc", "claude-carol")
	nameA, nameB, nameC := a.named(alice), b.named(bob), a.named(carol)
	eventually(t, "presence", func() bool {
		return a.nameOf(bob) == nameB && b.nameOf(alice) == nameA && b.nameOf(carol) == nameC
	})

	// Three messages between alice and bob, and one to alice from carol. Each is read by a before
	// the next is sent, so the order a read them in is the order they were sent in.
	send := func(from *device, sess, to, subject string) {
		t.Helper()
		from.do(proto.Request{Op: proto.OpSend, Session: sess, To: to, Subject: subject})
		eventually(t, subject+" to reach a", func() bool {
			for _, m := range a.look("aaaaaa", nameA).Messages {
				if m.Subject == subject {
					return true
				}
			}
			return false
		})
	}
	send(a, "aaaaaa", nameB, "first")
	send(a, "aaaaaa", nameB, "second")
	send(b, "bbbbbb", nameA, "third")
	send(a, "cccccc", nameA, "someone else's")

	// A live session on this device, asked by itself, by name and by address.
	for _, who := range []string{nameA, alice} {
		det := a.look("aaaaaa", who)
		if det.Name != nameA || det.Addr != alice || det.Device != a.self() || det.Host == "" || det.Agent != "claude-code" {
			t.Errorf("lookup %s: identity %+v", who, det)
		}
		if det.Branch != "feat-l" || det.Doing != "writing the lookup" || det.Started.IsZero() || det.LastActive.IsZero() {
			t.Errorf("lookup %s: activity %+v", who, det)
		}
		if !det.Running || !det.Reachable || !det.You || !det.Local {
			t.Errorf("lookup %s: flags %+v", who, det)
		}
		if det.Worktree != "wt-lookup" || det.ClientSession != "claude-lookup" {
			t.Errorf("lookup %s: local fields %+v", who, det)
		}
	}
	det := a.look("aaaaaa", nameA)
	var subj []string
	for _, m := range det.Messages {
		subj = append(subj, m.Subject)
	}
	if !slices.Equal(subj, []string{"first", "second", "third", "someone else's"}) {
		t.Errorf("alice's messages %v, want the three with bob and carol's to her, oldest first", subj)
	}
	if m := det.Messages[0]; m.FromName != nameA || m.ToName != nameB {
		t.Errorf("a message shows %q to %q, want the names %s to %s", m.FromName, m.ToName, nameA, nameB)
	}
	if got := a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: nameA, Limit: 2}).Lookup.Messages; len(got) != 2 ||
		got[0].Subject != "third" || got[1].Subject != "someone else's" {
		t.Errorf("a limit of 2 gave %+v, want the latest two", got)
	}
	if out := view.Lookup(proto.Response{Lookup: det}); !strings.Contains(out, nameA) || !strings.Contains(out, "feat-l") ||
		!strings.Contains(out, "this device only") {
		t.Errorf("the text of lookup is missing parts:\n%s", out)
	}

	// A session on another device: what presence has, and none of what stays local.
	var far *proto.SessionDetail
	eventually(t, "bob's last-active time and status on a", func() bool {
		far = a.look("aaaaaa", nameB)
		return !far.LastActive.IsZero() && far.Doing == "reviewing"
	})
	if got := len(far.Messages); got != 3 {
		t.Errorf("bob's messages: %d, want the three with alice (not carol's)", got)
	}
	if far.Name != nameB || far.Addr != bob || far.Device != b.self() || far.Agent != "test" || far.Doing != "reviewing" {
		t.Errorf("lookup of bob: %+v", far)
	}
	if !far.Running || !far.Reachable || far.Local || far.You || far.Worktree != "" || far.ClientSession != "" || len(far.Commits) != 0 {
		t.Errorf("lookup of bob shows what isn't his to share, or flags wrong: %+v", far)
	}
	if far := a.look("aaaaaa", bob); far.Name != nameB {
		t.Errorf("lookup of bob by address: %+v", far)
	}

	// A stopped session the registry knows: on its own device, and on another that never saw it stop.
	carolConn.Close()
	eventually(t, "carol stopped", func() bool { return !a.look("aaaaaa", nameC).Running })
	gone := a.look("aaaaaa", nameC)
	if gone.Name != nameC || gone.Reachable || gone.StoppedAt.IsZero() || !gone.Local {
		t.Errorf("lookup of a stopped session: %+v", gone)
	}
	if out := view.Lookup(proto.Response{Lookup: gone}); !strings.Contains(out, "NOT RUNNING") {
		t.Errorf("lookup doesn't say a stopped session isn't running:\n%s", out)
	}
	eventually(t, "carol gone from b's presence", func() bool { return !b.look("bbbbbb", nameC).Running })
	away := b.look("bbbbbb", nameC)
	if away.Name != nameC || away.Device != a.self() || away.Reachable || away.Local || away.Worktree != "" {
		t.Errorf("lookup of a session gone from its device: %+v", away)
	}
	if out := view.Lookup(proto.Response{Lookup: away}); !strings.Contains(out, "NOT RUNNING") {
		t.Errorf("lookup doesn't say a session no longer in presence isn't running:\n%s", out)
	}

	// Nobody by that name, nothing at that address, nothing sensible given.
	unused := func() string {
		for {
			n, err := names.Generate(crand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if n != nameA && n != nameB && n != nameC {
				return n
			}
		}
	}()
	for _, bad := range []string{unused, "no-such-session", a.self() + "/ffffff", "ghost-device-0000/aaaaaa", "", "not a name!", a.self() + "/cli"} {
		r := a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: bad})
		if r.OK || r.Error == "" || r.Lookup != nil {
			t.Errorf("lookup %q should be an error, got %+v", bad, r)
		}
	}
	if r := a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: unused}); !strings.Contains(r.Error, unused) {
		t.Errorf("the error doesn't name what wasn't found: %q", r.Error)
	}

	noneOnBus(t, remote, "wt-lookup", "claude-lookup", "claude-bob", "claude-carol")
}

// A session's credited commits are kept after it stops and across the daemon restarting, and a
// session that comes back within the time its inbox is kept gets them back.
func TestCreditedCommitsSurviveAStopAndARestart(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newTimedDevice(t, remote, 200*time.Millisecond, time.Hour)
	wt := a.linkedTree("wt-keep", "feat-keep")
	alice, conn := a.claudeSession("aaaaaa", "claude-keep", procP, wt)
	name := a.named(alice)

	a.callStart("claude-keep", procP, "toolu_k", wt)
	sha := commitFile(t, wt, "keep.txt")
	a.callEnd("claude-keep", procP, "toolu_k", wt)
	conn.Close()
	eventually(t, "alice stopped", func() bool { return busLines(a, alice, "stopped") == 1 })

	if det := a.look("", name); det.Running || !slices.Equal(commitSHAs(det), []string{sha}) || !slices.Equal(det.Committed, []string{"feat-keep"}) {
		t.Fatalf("after the stop: %+v", det)
	}
	a.restart()
	if det := a.look("", name); det.Running || !slices.Equal(commitSHAs(det), []string{sha}) || !slices.Equal(det.Committed, []string{"feat-keep"}) {
		t.Fatalf("after the daemon restarted: %+v", det)
	}

	// Back, with the same ID: the same session, and its commits with it.
	a.helloAgain("aaaaaa", false)
	if det := a.look("aaaaaa", name); !det.Running || !slices.Equal(commitSHAs(det), []string{sha}) {
		t.Fatalf("after the session returned: %+v", det)
	}
}
