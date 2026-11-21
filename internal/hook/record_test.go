package hook

import (
	"testing"

	"github.com/a-Fig/murmur/internal/here"
)

func TestOwnRecordMatchesUniqueIDsBeforeAntigravityFallback(t *testing.T) {
	native := here.Session{Agent: Antigravity, Native: "conv-1", GitDir: "native"}
	thread := here.Session{Agent: "antigravity in T3", T3Thread: "thread-1", GitDir: "thread"}
	standalone := here.Session{Agent: Antigravity, GitDir: "standalone"}
	for _, c := range []struct {
		name  string
		recs  []here.Session
		match string
	}{
		{"native before thread", []here.Session{thread, native}, "native"},
		{"one thread among several", []here.Session{standalone, thread}, "thread"},
		{"shared thread", []here.Session{thread, thread}, ""},
		{"shared client without IDs", []here.Session{standalone, standalone}, ""},
		{"conflicting native ID", []here.Session{{Agent: Antigravity, Native: "conv-2", T3Thread: "thread-1"}}, ""},
		{"native ID from another agent", []here.Session{{Agent: Codex, Native: "conv-1"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, ok := ownRecord(Antigravity, "conv-1", "thread-1", c.recs)
			if r.GitDir != c.match || ok != (c.match != "") {
				t.Errorf("got %+v, %v, want %q", r, ok, c.match)
			}
		})
	}
}
