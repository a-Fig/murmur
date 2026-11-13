package daemon

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

func TestUUIDV7Time(t *testing.T) {
	// A thread ID Codex 0.164 made, at 1794558049136 ms (2026-11-12).
	if got, ok := uuidV7Time("01a1d3fe-bb70-7331-a99e-36fcc2c3db15"); !ok || got.UnixMilli() != 1794558049136 {
		t.Errorf("got %v, %v", got.UnixMilli(), ok)
	}
	for _, id := range []string{"", "th-main", "01a1d3fe-bb70-4331-a99e-36fcc2c3db15", "zz a1d3fe-bb70-7331-a99e-36fcc2c3db15"} {
		if _, ok := uuidV7Time(id); ok {
			t.Errorf("%q was taken for a UUIDv7", id)
		}
	}
}

func TestCodexThreadSessionLocked(t *testing.T) {
	made := time.UnixMilli(1794558049136)
	thread := "01a1d3fe-bb70-7331-a99e-36fcc2c3db15"
	at := func(id string, d time.Duration, r proto.Route) *session {
		return &session{id: id, agent: "codex", serverStarted: made.Add(d), route: r}
	}
	req := proto.Request{Agent: "codex", ClaudeSession: thread}
	soon, late := at("a", 300*time.Millisecond, proto.Route{}), at("b", time.Minute, proto.Route{})
	if got := codexThreadSessionLocked(req, []*session{late, soon}); got != soon {
		t.Errorf("got %v, want the session whose server started just after the thread", got)
	}
	if got := codexThreadSessionLocked(req, []*session{soon, at("c", time.Second, proto.Route{})}); got != nil {
		t.Errorf("two servers started just after the thread: got %v, want none", got.id)
	}
	for name, s := range map[string]*session{
		"started before the thread": at("d", -time.Second, proto.Route{}),
		"knows its thread":          at("e", 300*time.Millisecond, proto.Route{Native: "other"}),
		"in T3":                     at("f", 300*time.Millisecond, proto.Route{T3CLI: "t3"}),
		"no start time":             {id: "g", agent: "codex"},
	} {
		if got := codexThreadSessionLocked(req, []*session{s}); got != nil {
			t.Errorf("a session that %s was matched", name)
		}
	}
	if got := codexThreadSessionLocked(proto.Request{Agent: "grok", ClaudeSession: thread}, []*session{soon}); got != nil {
		t.Error("another agent's hook was matched by Codex's rule")
	}
}
