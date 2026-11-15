package bus_test

import (
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
)

// A presence read from the bus is held to what Murmur writes: the branches a session has committed
// to are on one line each, none empty or repeated, at most bus.MaxCommitted (#21).
func TestPresenceCleanHoldsCommittedToWhatMurmurWrites(t *testing.T) {
	many := []string{"a", "b", "c", "d", "e", "f", "g"}
	p := bus.Presence{Sessions: []bus.SessionInfo{
		{ID: "s1", Committed: append([]string{"feature/x\ny", "", "a", "a"}, many...)},
		{ID: "s2", Committed: []string{strings.Repeat("x", 500)}},
		{ID: "s3"},
	}}
	p.Clean()

	got := p.Sessions[0].Committed
	if len(got) != bus.MaxCommitted {
		t.Fatalf("kept %d branches, want %d: %q", len(got), bus.MaxCommitted, got)
	}
	if got[0] != "feature/x y" || got[1] != "a" || got[2] != "b" {
		t.Errorf("not one line, or empty or repeated ones kept: %q", got)
	}
	if n := len([]rune(p.Sessions[1].Committed[0])); n != 100 {
		t.Errorf("a branch name of %d runes, want it cut to 100", n)
	}
	if p.Sessions[2].Committed != nil {
		t.Errorf("a session with none got %q", p.Sessions[2].Committed)
	}
}

// A daemon from before #21 reads the presence a newer one writes: the new fields are ones it has
// never heard of, and the rest still decodes. Its own presence, in turn, has neither field, which
// reads as a session with no commits and no known activity.
func TestPresenceWithNewFieldsDecodesInAnOlderReader(t *testing.T) {
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	newer := bus.Presence{V: bus.Version, Device: "dev-1", At: at, Sessions: []bus.SessionInfo{{
		ID: "s1", Agent: "claude", Branch: "main", Started: at,
		LastActive: at, Committed: []string{"main", "feature"},
	}}}
	msg, err := bus.Encode(newer, "presence")
	if err != nil {
		t.Fatal(err)
	}

	// The shape a presence had before #21: the same record without the two fields.
	type oldSession struct {
		ID      string    `json:"id"`
		Agent   string    `json:"agent"`
		Branch  string    `json:"branch,omitempty"`
		Started time.Time `json:"started"`
	}
	var old struct {
		Device   string       `json:"device"`
		Sessions []oldSession `json:"sessions"`
	}
	if err := bus.Decode(msg, &old); err != nil {
		t.Fatalf("an older reader fails on the new presence: %v", err)
	}
	if len(old.Sessions) != 1 || old.Sessions[0].Branch != "main" || old.Device != "dev-1" {
		t.Errorf("an older reader got %+v", old)
	}

	// And the other way: a presence without the fields decodes here as one with none.
	oldMsg, err := bus.Encode(old, "presence")
	if err != nil {
		t.Fatal(err)
	}
	var back bus.Presence
	if err := bus.Decode(oldMsg, &back); err != nil {
		t.Fatal(err)
	}
	if s := back.Sessions[0]; !s.LastActive.IsZero() || s.Committed != nil {
		t.Errorf("an old presence read as %+v", s)
	}
	if strings.Contains(oldMsg, "lastActive") || strings.Contains(oldMsg, "committed") {
		t.Errorf("an unset field is written anyway:\n%s", oldMsg)
	}

	// A session with neither writes neither: a presence that doesn't change stays byte-identical.
	b, _ := json.Marshal(bus.SessionInfo{ID: "s1", Started: at})
	if strings.Contains(string(b), "lastActive") || strings.Contains(string(b), "committed") {
		t.Errorf("unset fields are written: %s", b)
	}
}

// The sessions a session has muted (#27) are names: none that isn't one, none repeated, at most
// bus.MaxMuted.
func TestPresenceCleanHoldsMutedToNames(t *testing.T) {
	var many []string
	for len(many) < bus.MaxMuted+10 {
		name, err := names.Generate(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		many = append(many, name)
	}
	p := bus.Presence{Sessions: []bus.SessionInfo{
		{ID: "s1", Muted: []string{many[0], "not a name", "dev-0001/abc", many[0], many[1]}},
		{ID: "s2", Muted: many},
	}}
	p.Clean()
	if got := p.Sessions[0].Muted; len(got) != 2 || got[0] != many[0] || got[1] != many[1] {
		t.Errorf("kept %q, want the two names once each", got)
	}
	if n := len(p.Sessions[1].Muted); n > bus.MaxMuted {
		t.Errorf("kept %d mutes, want at most %d", n, bus.MaxMuted)
	}
}
