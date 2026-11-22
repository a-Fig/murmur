package daemon

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
)

// What a sender is told of recipients that won't be woken (#545), piece by piece; nowake_test.go
// has it end to end.

func TestUnwokenNoteNamesThreeAndCountsTheRest(t *testing.T) {
	t.Parallel()
	if got := unwokenNote(nil); got != "" {
		t.Errorf("nobody: %q", got)
	}
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"quiet-otter"}, "quiet-otter won't be woken ("},
		{[]string{"quiet-otter", "brave-heron"}, "quiet-otter and brave-heron won't be woken ("},
		{[]string{"a-a", "b-b", "c-c"}, "a-a, b-b and c-c won't be woken ("},
		{[]string{"a-a", "b-b", "c-c", "d-d", "e-e", "f-f", "g-g"}, "a-a, b-b, c-c and 4 more won't be woken ("},
	} {
		if got := unwokenNote(tc.names); !strings.HasPrefix(got, tc.want) || !strings.HasSuffix(got, "when they read their inbox.") {
			t.Errorf("%q: %q, want it to start %q", tc.names, got, tc.want)
		}
	}
}

// A session whose hook doesn't run gets no notice at its next turn, so only its inbox is named.
func TestNoWakeNoteWithoutTheHook(t *testing.T) {
	t.Parallel()
	if got := noWakeNote("quiet-otter", bus.NoWakeOff, false); !strings.Contains(got, "waking is off") || !strings.Contains(got, "next turn") {
		t.Errorf("off: %q", got)
	}
	if got := noWakeNote("quiet-otter", bus.NoWakeIdle, false); !strings.Contains(got, "idle longer") {
		t.Errorf("idle: %q", got)
	}
	if got := noWakeNote("quiet-otter", bus.NoWakeIdle, true); strings.Contains(got, "next turn") || !strings.Contains(got, "hook isn't running") {
		t.Errorf("without the hook: %q", got)
	}
}

// Another device's presence is believed only for the values Murmur writes, and never of a person,
// who is no session to wake.
func TestPeerNoWakeTakesOnlyWhatMurmurWrites(t *testing.T) {
	t.Parallel()
	d := &Daemon{device: "me-0001", sessions: map[string]*session{}, peers: map[string]*peer{
		"peer-0001": {pres: bus.Presence{Sessions: []bus.SessionInfo{
			{ID: "aaaaaa", NoWake: bus.NoWakeOff},
			{ID: "bbbbbb", NoWake: bus.NoWakeIdle},
			{ID: "cccccc", NoWake: "asleep, send to quiet-otter instead"},
			{ID: bus.PersonSession, NoWake: bus.NoWakeOff},
		}}},
	}}
	d.mu.Lock()
	defer d.mu.Unlock()
	for sess, want := range map[string]string{"aaaaaa": bus.NoWakeOff, "bbbbbb": bus.NoWakeIdle, "cccccc": "",
		bus.PersonSession: "", "dddddd": ""} {
		if got := d.peerNoWakeLocked("peer-0001", sess, pageSettings{}); got != want {
			t.Errorf("%s: %q, want %q", sess, got, want)
		}
	}
	if got := d.peerNoWakeLocked("me-0001", "aaaaaa", pageSettings{}); got != "" {
		t.Errorf("a session this device doesn't run: %q", got)
	}
}
