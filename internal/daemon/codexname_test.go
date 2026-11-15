package daemon_test

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of a Codex session's name outside T3 (#36). Codex's MCP server learns its thread ID at its
// first tool call and tells the daemon in a route (Native); the name follows that thread.

// native tells the daemon a session's Codex thread, as its MCP server does at its first tool call.
func (d *device) native(id, thread string) {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpRoute, Session: id, Route: &proto.Route{Native: thread}})
	if r.Error != "" {
		d.t.Fatalf("route: %s", r.Error)
	}
}

// A Codex CLI session whose MCP server restarts (a new Murmur session ID, the same Codex thread)
// has its old name back once the thread ID arrives; a session that has already shown the name it
// was given under its Murmur ID keeps that name when it learns its thread.
func TestCodexNameFollowsItsThreadAcrossRestarts(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, conn := b.session("bbbbbb", "")
	name := b.named(first)
	b.native("bbbbbb", "thread-1")
	b.nameIs(first, name) // the name it had under its own ID stays with the thread
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, _ := b.session("cccccc", "")
	if other := b.named(second); other == name {
		t.Fatalf("a new session took %s before it knew its thread", name)
	}
	b.native("cccccc", "thread-1")
	b.nameIs(second, name)
}

// Another Codex thread is another name, and each thread keeps its own when they come back in turn.
func TestCodexThreadsHaveTheirOwnNames(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, conn := b.session("bbbbbb", "")
	b.native("bbbbbb", "thread-1")
	name := b.named(first)
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, conn := b.session("cccccc", "")
	b.native("cccccc", "thread-2")
	other := b.named(second)
	if other == name {
		t.Fatalf("two Codex threads share the name %s", name)
	}
	conn.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })

	third, _ := b.session("dddddd", "")
	b.native("dddddd", "thread-1")
	b.nameIs(third, name)
}

// Inside T3 the T3 thread wins: two sessions of one thread share a name even when Codex gave them
// different thread IDs of its own, and a session's name does not follow a Codex thread ID that
// another T3 thread's session also has.
func TestT3ThreadWinsOverTheCodexThread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob", Native: "codex-1"})
	name := b.named(first)
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, _ := b.sessionRoute("cccccc", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob", Native: "codex-2"})
	b.nameIs(second, name)

	third, _ := b.sessionRoute("dddddd", "", proto.Route{T3CLI: exe, T3Thread: "thread-carol", Native: "codex-1"})
	if other := b.named(third); other == name {
		t.Errorf("a Codex thread ID joined two T3 threads under the name %s", name)
	}
}
