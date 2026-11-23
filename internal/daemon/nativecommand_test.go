package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// nativeCommandHello registers a session as `mur tool` does for Murmur's Hermes plugin (#975): a
// command's hello keyed by the session's own ID, with no T3 thread.
func nativeCommandHello(t *testing.T, d *device, id, native string) proto.Response {
	t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "hermes", Command: true, Route: proto.Route{Native: native}})
	if err != nil {
		t.Fatalf("command hello %s: %v", id, err)
	}
	conn.Close()
	return resp
}

// A session `mur tool` registers by its own ID stays past its connection and the grace, a second
// call is the same session, it ends once idle for the hold's time, and the next call brings it back
// at the same address with the same name.
func TestANativeCommandsSessionIsHeldWhileInUse(t *testing.T) {
	defer daemon.SetCommandHold(time.Hour, 50*time.Millisecond)()
	remote := newRemote(t)
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 200 * time.Millisecond })

	first := nativeCommandHello(t, b, "a1b2c3", "hermes:local-1")
	name := b.named(first.Self)
	time.Sleep(time.Second) // five graces
	if !registered(b, "a1b2c3") {
		t.Fatal("the command's session stopped with its connection")
	}
	if again := nativeCommandHello(t, b, "a1b2c3", "hermes:local-1"); again.Self != first.Self || again.Name != name {
		t.Errorf("a second call registered %s (%s), want %s (%s)", again.Self, again.Name, first.Self, name)
	}
	if logHas(b, "a1b2c3 reconnected") {
		t.Error("the second call took the session over, where it should have acted as it")
	}

	daemon.SetCommandHold(300*time.Millisecond, 50*time.Millisecond)
	eventually(t, "the session to stop once idle", func() bool { return logHas(b, "/a1b2c3 stopped") })
	back := nativeCommandHello(t, b, "a1b2c3", "hermes:local-1")
	if back.Self != first.Self {
		t.Errorf("back at %s, want %s", back.Self, first.Self)
	}
	if got := b.named(back.Self); got != name {
		t.Errorf("back as %s, want %s", got, name)
	}
}

// A command's hello that names neither a T3 thread nor a session of its own is refused, as is one
// that names both a native ID and a thread without T3's command line.
func TestACommandHelloWithNothingToKeyItIsRefused(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	for _, r := range []proto.Route{{}, {Native: "hermes:x", T3Thread: "thread-x"}} {
		_, _, err := b.c.Hello(context.Background(), proto.Hello{ID: "d4d4d4", Agent: "hermes", Command: true, Route: r})
		if err == nil || !strings.Contains(err.Error(), "names no thread") {
			t.Errorf("route %+v: %v", r, err)
		}
	}
}

// A held session keyed by its own ID isn't taken over by a hello for another at the same ID, as two
// Hermes sessions whose IDs hash alike would be.
func TestANativeCommandsHeldSessionIsntTakenByAnother(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	nativeCommandHello(t, b, "e5e5e5", "hermes:local-one")
	_, _, err := b.c.Hello(context.Background(), proto.Hello{ID: "e5e5e5", Agent: "hermes", Command: true, Route: proto.Route{Native: "hermes:local-two"}})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("another session's hello: %v", err)
	}
	conn, _, err := b.c.Hello(context.Background(), proto.Hello{ID: "e5e5e5", Agent: "claude-code"})
	if err == nil {
		conn.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("a server's hello: %v", err)
	}
}
