package daemon_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/tags"
)

func antigravityHello(t *testing.T, d *device, h proto.Hello) (net.Conn, proto.Response) {
	t.Helper()
	h.Agent = "antigravity in T3"
	conn, resp, err := d.c.Hello(context.Background(), h)
	if err != nil {
		t.Fatalf("Antigravity hello: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, resp
}

func TestAntigravityT3RegistrationEnablesDeliveryAndThreadState(t *testing.T) {
	t.Parallel()
	home, _ := t3Fixture(t, map[string]string{"thread-agy": "Fix the redirect"})
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{
		"thread-agy": {LastRun: time.Now(), Model: "gemini-3-pro"},
	}}
	d := newT3Device(t, newRemote(t), threads)
	route := proto.Route{T3CLI: exe, T3Home: home, T3Thread: "thread-agy"}
	_, parent := antigravityHello(t, d, proto.Hello{ID: "aaaaaa", ClientPID: procP, ByThread: true, Route: route})
	parentName := d.named(parent.Self)
	d.waitDoing(proto.CLISession, parent.Self, "Fix the redirect")
	eventually(t, "the derived Antigravity model in who", func() bool {
		for _, peer := range d.do(proto.Request{Op: proto.OpWho}).Devices {
			for _, sess := range peer.Sessions {
				if sess.Addr == parent.Self && tags.Has(sess.Tags, "model:gemini-3-pro") {
					return true
				}
			}
		}
		return false
	})
	d.send(proto.CLISession, parent.Self, "wake Antigravity", "")
	eventually(t, "an idle T3 turn before any hook", func() bool { return len(calls()) == 1 })
	if got := calls()[0]; strings.Join(got.Args, " ") != "thread send thread-agy -" || !strings.Contains(got.Stdin, "wake Antigravity") {
		t.Fatalf("Antigravity handoff = %+v", got)
	}

	route.T3Thread = "thread-child"
	_, child := antigravityHello(t, d, proto.Hello{ID: "bbbbbb", ClientPID: procQ, ByThread: true, Route: route})
	d.named(child.Self)
	threads.set("thread-child", t3db.State{Parent: "thread-agy"})
	eventually(t, "the Antigravity delegate to leave who", func() bool { return !d.listsSession(child.Self) })
	if r := d.trySend("bbbbbb", parent.Self, "from the child"); r.OK || !strings.Contains(r.Error, parentName) {
		t.Errorf("delegate send: %+v", r)
	}

	threads.set("thread-agy", t3db.State{Settled: true})
	eventually(t, "the settled Antigravity thread to leave who", func() bool { return !d.listsSession(parent.Self) })
	if r := d.trySend(proto.CLISession, parent.Self, "after settling"); r.OK || !strings.Contains(r.Error, "settled") {
		t.Errorf("send to the settled thread: %+v", r)
	}
}

func TestAntigravityT3HookFindsTheThreadAndKeepsItsName(t *testing.T) {
	t.Parallel()
	home, db := t3Fixture(t, map[string]string{"thread-agy": "Backport the fix"})
	if _, err := db.Exec(`CREATE TABLE orchestration_v2_projection_provider_threads (thread_id TEXT, payload_json TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO orchestration_v2_projection_provider_threads VALUES (?, ?, ?)`,
		"thread-agy", `{"nativeThreadRef":{"nativeId":"agy-conversation"}}`, "2026-11-20"); err != nil {
		t.Fatal(err)
	}
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-agy": {Busy: true, LastRun: time.Now()}}}
	d := newDeviceWith(t, newRemote(t), func(d *device) {
		d.grace = 300 * time.Millisecond
		d.threadState = threads.read
	})
	route := proto.Route{T3CLI: exe, T3Home: home}
	conn, resp := antigravityHello(t, d, proto.Hello{ID: "aaaaaa", ClientPID: procP, Route: route})
	addr, name := resp.Self, d.named(resp.Self)
	if title, _ := d.doingIn(proto.CLISession, addr); title != "" {
		t.Fatalf("the title was already known before the hook: %q", title)
	}
	hook := proto.Request{Op: proto.OpNotices, Agent: "antigravity", ClaudeSession: "agy-conversation", Ancestors: []int{procP}}
	if r := d.do(hook); r.Self != addr {
		t.Fatalf("the hook reported for %q, want %q", r.Self, addr)
	}
	d.waitDoing(proto.CLISession, addr, "Backport the fix")
	if got := d.nameOf(addr); got != name {
		t.Fatalf("finding the thread changed the name from %q to %q", name, got)
	}
	d.send(proto.CLISession, addr, "after interruption", "")
	holds(t, "no handoff while the Antigravity run is going", 300*time.Millisecond, func() bool { return len(calls()) == 0 })
	// The run ends without a Stop hook, as when the user interrupts it.
	threads.set("thread-agy", t3db.State{LastRun: time.Now()})
	eventually(t, "a handoff after the interrupted run", func() bool { return len(calls()) == 1 })
	if got := calls()[0]; strings.Join(got.Args, " ") != "thread send thread-agy -" || !strings.Contains(got.Stdin, "after interruption") {
		t.Fatalf("Antigravity handoff = %+v", got)
	}

	conn.Close()
	eventually(t, "the old server to stop", func() bool { return busLines(d, addr, "stopped") == 1 })
	route.T3Thread = "thread-agy"
	_, resumed := antigravityHello(t, d, proto.Hello{ID: "cccccc", ClientPID: procQ, ByThread: true, Route: route})
	if resumed.Self != addr || resumed.Name != name {
		t.Fatalf("registration by thread returned %s as %q, want %s as %q", resumed.Self, resumed.Name, addr, name)
	}
}
