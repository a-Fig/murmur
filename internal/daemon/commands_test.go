package daemon_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// commandHello registers a session as a shell command does (#913): a hello that says it is a
// command's, whose connection closes as soon as it is answered.
func commandHello(t *testing.T, d *device, id, cli, thread string) string {
	t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "agent in T3", ByThread: true, Command: true,
		Route: proto.Route{T3CLI: cli, T3Thread: thread}})
	if err != nil {
		t.Fatalf("command hello %s: %v", id, err)
	}
	conn.Close()
	return resp.Self
}

// registered says whether session id is registered with d.
func registered(d *device, id string) bool {
	return !strings.Contains(d.do(proto.Request{Op: proto.OpInbox, Session: id}).Error, "isn't registered")
}

// A command's session stays registered after its connection closes, well past the grace, a second
// command is the same session, and a message to it is handed to its thread through T3, saying to
// answer with the command (#913).
func TestACommandsSessionStaysAfterItsHello(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-c1": {LastRun: time.Now()}}}
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 200 * time.Millisecond; d.threadState = threads.read })

	self := commandHello(t, b, "c1c1c1", exe, "thread-c1")
	name := b.named(self)
	time.Sleep(time.Second) // five graces
	if !registered(b, "c1c1c1") {
		t.Fatal("the command's session stopped with its connection")
	}
	if again := commandHello(t, b, "c1c1c1", exe, "thread-c1"); again != self {
		t.Errorf("a second command registered %s, want %s", again, self)
	}

	a.session("aaaaaa", "")
	eventually(t, "presence", func() bool { return a.seesSession(self) })
	a.nameIs(self, name)
	before := len(calls())
	a.send("aaaaaa", name, "for the command's session", "hello")
	eventually(t, "the message handed to T3", func() bool { return len(calls()) > before })
	c := calls()[len(calls())-1]
	if !slices.Contains(c.Args, "thread-c1") || !strings.Contains(c.Stdin, "for the command's session") {
		t.Errorf("T3 was asked %q with %q", c.Args, c.Stdin)
	}
	if want := "Reply with `" + brand.Command + " send`"; !strings.Contains(c.Stdin, want) {
		t.Errorf("the turn doesn't say to answer with the command:\n%s", c.Stdin)
	}
}

// A server of the thread's own takes over the session a command holds, without an "in use", a
// command run beside it is that session, and once the server stops, so does the session.
func TestAThreadsServerTakesOverItsCommandsSession(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-c2": {LastRun: time.Now()}}}
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 200 * time.Millisecond; d.threadState = threads.read })

	self := commandHello(t, b, "c2c2c2", exe, "thread-c2")
	name := b.named(self)
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "c2c2c2", Agent: "claude-code in T3", ByThread: true,
		Route: proto.Route{T3CLI: exe, T3Thread: "thread-c2"}})
	if err != nil {
		t.Fatalf("the thread's server: %v", err)
	}
	if resp.Self != self || resp.Name != name {
		t.Errorf("the server registered %s (%s), want %s (%s)", resp.Self, resp.Name, self, name)
	}
	if beside := commandHello(t, b, "ffffff", exe, "thread-c2"); beside != self {
		t.Errorf("a command beside the server is %s, want %s", beside, self)
	}
	conn.Close()
	eventually(t, "the session to stop with its server", func() bool { return !registered(b, "c2c2c2") })
}

// A command's session ends when T3 shows its thread settled, or once its thread has been idle for
// the hold's time.
func TestACommandsSessionEndsWithItsThread(t *testing.T) {
	defer daemon.SetCommandHold(time.Hour, 50*time.Millisecond)()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-c3": {LastRun: time.Now()}, "thread-c4": {LastRun: time.Now().Add(-2 * time.Hour)}}}
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 200 * time.Millisecond; d.threadState = threads.read })

	commandHello(t, b, "c3c3c3", exe, "thread-c3")
	time.Sleep(500 * time.Millisecond)
	if !registered(b, "c3c3c3") {
		t.Fatal("the session stopped while its thread was in use")
	}
	threads.set("thread-c3", t3db.State{Settled: true, LastRun: time.Now()})
	eventually(t, "the session to stop with its thread settled", func() bool { return logHas(b, "/c3c3c3 stopped") })

	daemon.SetCommandHold(300*time.Millisecond, 50*time.Millisecond)
	commandHello(t, b, "c4c4c4", exe, "thread-c4")
	// Watched in the log: a request as the session would be the session at work.
	eventually(t, "the session to stop with its thread idle", func() bool { return logHas(b, "/c4c4c4 stopped") })
}
