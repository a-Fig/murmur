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

func TestSameDir(t *testing.T) {
	if !sameDir(`/work/repo`, `/work/repo/`) || sameDir("", "") || sameDir("/work/a", "/work/b") {
		t.Error("sameDir")
	}
}

func TestCodexThreadMatch(t *testing.T) {
	made := time.UnixMilli(1794558049136)
	thread := "01a1d3fe-bb70-7331-a99e-36fcc2c3db15"
	const dir = "/work/repo"
	at := func(id string, d time.Duration, r proto.Route) *session {
		return &session{id: id, agent: "codex", cwd: dir, serverStarted: made.Add(d), route: r}
	}
	req := proto.Request{Agent: "codex", ClaudeSession: thread, Cwd: dir}
	soon, late := at("a", 300*time.Millisecond, proto.Route{}), at("b", time.Minute, proto.Route{})
	if got, _, _ := codexThreadMatch(req, []*session{late, soon}); got != soon {
		t.Errorf("got %v, want the session whose server started just after the thread", got)
	}
	if got, _, _ := codexThreadMatch(req, []*session{soon, at("c", time.Second, proto.Route{})}); got != nil {
		t.Errorf("two servers started just after the thread: got %v, want none", got.id)
	}
	elsewhere := at("h", 300*time.Millisecond, proto.Route{})
	elsewhere.cwd = "/work/other"
	unsure := at("i", 300*time.Millisecond, proto.Route{})
	unsure.timeUnsure = true
	for name, s := range map[string]*session{
		"started before the thread": at("d", -time.Second, proto.Route{}),
		"knows its thread":          at("e", 300*time.Millisecond, proto.Route{Native: "other"}),
		"is in T3":                  at("f", 300*time.Millisecond, proto.Route{T3CLI: "t3"}),
		"has no start time":         {id: "g", agent: "codex", cwd: dir},
		"is in another directory":   elsewhere,
		"was matched wrong once":    unsure,
	} {
		if got, _, _ := codexThreadMatch(req, []*session{s}); got != nil {
			t.Errorf("a session that %s was matched", name)
		}
	}
	for name, r := range map[string]proto.Request{
		"another agent's hook": {Agent: "grok", ClaudeSession: thread, Cwd: dir},
		"a subagent's hook":    {Agent: "codex", ClaudeSession: thread, Cwd: dir, Subagent: true},
		"a hook with no cwd":   {Agent: "codex", ClaudeSession: thread},
	} {
		if got, _, _ := codexThreadMatch(r, []*session{soon}); got != nil {
			t.Errorf("%s was matched", name)
		}
	}
	if _, _, out := codexThreadMatch(req, []*session{elsewhere}); len(out) != 1 || out[0] != elsewhere {
		t.Errorf("a session in another directory that fits by time wasn't reported: %v", out)
	}
	// A thread made just before another thread's server, whose hook took that server: the second
	// hook finds the first match wrong.
	taken := at("j", 300*time.Millisecond, proto.Route{Native: "01a1d3fe-bb00-7000-8000-000000000000"})
	taken.nativeFromHook = true
	if got, wrong, _ := codexThreadMatch(req, []*session{taken}); got != nil || len(wrong) != 1 || wrong[0] != taken {
		t.Errorf("got %v and wrong %v; want none and the session the other hook took", got, wrong)
	}
}
