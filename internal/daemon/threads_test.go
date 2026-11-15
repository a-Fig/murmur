package daemon_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// The wake setting's "within N minutes" (#25) and settled T3 threads going offline (#189), end to
// end, with a stand-in for T3's database.

// fakeThreads is the stand-in: each thread's state, as the test sets it.
type fakeThreads struct {
	mu sync.Mutex
	st map[string]t3db.State
}

func (f *fakeThreads) set(thread string, st t3db.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st[thread] = st
}

func (f *fakeThreads) read(_ context.Context, _, _, thread string) (t3db.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.st[thread]
	if !ok {
		return t3db.State{}, t3db.ErrNotFound
	}
	return st, nil
}

// trySend sends as session id, and returns the answer, refusal or not.
func (d *device) trySend(id, to, subject string) proto.Response {
	d.t.Helper()
	return d.do(proto.Request{Op: proto.OpSend, Session: id, To: to, Subject: subject})
}

// newT3Device is newDevice whose T3 database is threads.
func newT3Device(t *testing.T, remote string, threads *fakeThreads) *device {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	d := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond,
		threadState: threads.read}
	d.start()
	return d
}

// Under the default, within 3 minutes, a thread idle longer is not woken: the message waits as a
// notice. Once the thread is at work again, it goes in as the next turn.
func TestWithinLeavesALongIdleThreadAlone(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-bob": {LastRun: time.Now().Add(-10 * time.Minute)}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "not now", "")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "not now") })
	holds(t, "no handoff to a thread idle 10 minutes", time.Second, func() bool { return len(calls()) == 0 })

	threads.set("thread-bob", t3db.State{Busy: true})
	eventually(t, "handoff once the thread is at work", func() bool { return len(calls()) == 1 })
	if !strings.Contains(calls()[0].Stdin, "not now") {
		t.Errorf("the turn handed over: %q", calls()[0].Stdin)
	}

	threads.set("thread-bob", t3db.State{LastRun: time.Now()})
	a.send("aaaaaa", bob, "just now", "")
	eventually(t, "handoff to a thread idle a moment", func() bool { return len(calls()) == 2 })
}

// A settled thread is offline: it leaves the other device's who, a message to it is refused, and
// it is back once unsettled.
func TestSettledThreadIsOfflineAndRefusesMessages(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-bob": {LastRun: time.Now()}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	threads.set("thread-bob", t3db.State{Settled: true, LastRun: time.Now()})
	eventually(t, "bob to go offline", func() bool { return !a.listsSession(bob) })
	resp := a.trySend("aaaaaa", bobName, "anyone there?")
	if resp.OK || !strings.Contains(resp.Error, "settled") {
		t.Errorf("a send to a settled thread: ok %v, error %q", resp.OK, resp.Error)
	}
	resp = a.trySend("aaaaaa", bob, "by address")
	if resp.OK || !strings.Contains(resp.Error, "settled") {
		t.Errorf("a send to a settled thread by address: ok %v, error %q", resp.OK, resp.Error)
	}
	// From its own device too.
	b.session("cccccc", "")
	if resp := b.trySend("cccccc", bobName, "local"); resp.OK || !strings.Contains(resp.Error, "settled") {
		t.Errorf("a local send to a settled thread: ok %v, error %q", resp.OK, resp.Error)
	}
	holds(t, "nothing handed over to a settled thread", 500*time.Millisecond, func() bool { return len(calls()) == 0 })

	threads.set("thread-bob", t3db.State{LastRun: time.Now()})
	eventually(t, "bob to be back", func() bool { return a.seesSession(bob) })
	if resp := a.send("aaaaaa", bobName, "welcome back", ""); !resp.OK {
		t.Fatalf("a send once unsettled: %q", resp.Error)
	}
	eventually(t, "handoff once unsettled", func() bool { return len(calls()) == 1 })
}

// A delegate is off the mesh (#188): another device stops listing it once its thread is read, a
// message to it is refused with its parent named, and its own sends are refused.
func TestDelegateIsReachedThroughItsParent(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-boss": {LastRun: time.Now()}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	boss, _ := b.sessionRoute("bbbbbb", "claude-boss", proto.Route{T3CLI: exe, T3Thread: "thread-boss"})
	helper, _ := b.sessionRoute("cccccc", "claude-helper", proto.Route{T3CLI: exe, T3Thread: "thread-helper"})
	bossName, helperName := b.named(boss), b.named(helper)
	// Both devices know the helper by name before its thread is read, so that its leaving their
	// who is the thread's state arriving: a that never saw it would leave it out already, and
	// would refuse a send to its name as one nobody has (#387).
	a.nameIs(boss, bossName)
	a.nameIs(helper, helperName)
	eventually(t, "presence", func() bool { return b.seesSession(alice) })

	threads.set("thread-helper", t3db.State{Parent: "thread-boss", LastRun: time.Now()})
	eventually(t, "the delegate to leave both devices' who", func() bool {
		return a.seesSession(boss) && !a.listsSession(helper) && !b.listsSession(helper)
	})
	if resp := a.trySend("aaaaaa", helperName, "for the helper"); resp.OK || !strings.Contains(resp.Error, "delegate") {
		t.Errorf("a send to the delegate from another device: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("bbbbbb", helperName, "local"); resp.OK || !strings.Contains(resp.Error, "Message "+bossName+" instead") {
		t.Errorf("a send to the delegate on its device: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("cccccc", alice, "from the helper"); resp.OK || !strings.Contains(resp.Error, bossName) {
		t.Errorf("the delegate's own send: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("bbbbbb", alice, "from the boss"); !resp.OK {
		t.Errorf("the parent's send: %q", resp.Error)
	}
}
