package daemon_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// A Codex session outside T3 that hasn't called a Murmur tool is found by its hook (#453): the
// thread's ID says when it was made, and the session whose server started just after is its.

// codexThreadAt is a Codex thread ID, a UUIDv7, made at t.
func codexThreadAt(t time.Time, tail string) string {
	ms := t.UnixMilli()
	return fmt.Sprintf("%08x-%04x-7abc-8def-%s", ms>>16, ms&0xffff, tail)
}

// codexStarted registers a Codex session outside T3 of process pid whose server started at started.
func (d *device) codexStarted(id string, pid int, started time.Time, r proto.Route) string {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "codex", ClientPID: pid,
		Started: started.UnixMilli(), Route: r})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self
}

// Two terminal threads of one Codex app server: the hook of the first finds its session by when its
// server started, and a message to it goes through `codex queue` for that thread, before it ever
// called a Murmur tool. A thread whose server started too long after it is matched to none.
func TestACodexHookFindsItsSessionByWhenItsServerStarted(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	made := time.Now().Add(-time.Minute)
	first := codexThreadAt(made, "000000000001")
	second := codexThreadAt(made.Add(30*time.Second), "000000000002")
	one := b.codexStarted("bbbbbb", 400, made.Add(300*time.Millisecond), proto.Route{CodexCLI: exe})
	two := b.codexStarted("cccccc", 400, made.Add(40*time.Second), proto.Route{CodexCLI: exe})
	eventually(t, "presence", func() bool { return a.seesSession(one) && a.seesSession(two) && b.seesSession(alice) })

	b.do(proto.Request{Op: proto.OpNotices, Agent: "codex", ClaudeSession: first, Ancestors: []int{901, 400, 1}})
	b.do(proto.Request{Op: proto.OpNotices, Agent: "codex", ClaudeSession: second, Ancestors: []int{902, 400, 1}})

	a.send("aaaaaa", one, "auth middleware?", "Are you changing it?")
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; len(c.Args) < 3 || strings.Join(c.Args[:3], " ") != "queue --thread "+first {
		t.Fatalf("codex was run as %q", c.Args)
	}
	a.send("aaaaaa", two, "and you?", "")
	holds(t, "no handoff to a session no hook was matched to", 1500*time.Millisecond, func() bool { return len(calls()) == 1 })
}
