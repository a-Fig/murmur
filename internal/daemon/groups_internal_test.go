package daemon

import (
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// What waited for a session that isn't back when the catch-up window closes is let go, and logged.
func TestAChangeNobodyCameBackForIsLetGo(t *testing.T) {
	d := tagsDaemon(t)
	d.log = log.New(io.Discard, "", 0)
	d.started = time.Now()
	d.mu.Lock()
	d.applyTagsEventLocked(bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.Tags, From: "far/cli",
		To: "dev/cccccc", At: time.Now(), Add: []string{"release"}})
	held := len(d.heldTags)
	d.started = time.Now().Add(-time.Hour)
	d.mu.Unlock()
	d.pruneInboxes()
	if held != 1 || len(d.heldTags) != 0 {
		t.Errorf("held %d, then %d once the window closed; want 1, then none", held, len(d.heldTags))
	}
}

// An add refused as too old says how old it was by this device's clock, beside its time by its
// sender's, so a clock far off on either side shows (#543). One refused in a skip while younger
// than that says so, rather than "too long ago".
func TestAnAddTooOldToApplySaysHowOld(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	var out strings.Builder
	d.log = log.New(&out, "", 0)
	add := func(at time.Time, mode syncMode) string {
		out.Reset()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.applyGroupAddLocked(bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.GroupAdd, From: "far/xxxxxx",
			To: "dev/aaaaaa", At: at, Add: []string{"group:ui"}}, mode)
		return out.String()
	}
	at := time.Now().Add(-2 * time.Hour)
	got := add(at, modeNormal)
	// The age is rounded to the second, so a slow run may log 2h0m1s.
	for _, want := range []string{"at " + at.Format(time.RFC3339) + " by its clock, 2h0m",
		" ago by this device's, too long ago to apply"} {
		if !strings.Contains(got, want) {
			t.Errorf("logged %q; want it to say %q", got, want)
		}
	}
	// From a sender whose clock runs ahead: sent before the session started, by that clock.
	d.mu.Lock()
	d.sessions["aaaaaa"].started = time.Now().Add(time.Hour)
	d.mu.Unlock()
	got = add(time.Now().Add(20*time.Minute), modeSkip)
	if want := "read after a long gap and not shown to be sent since the session started; not applied"; !strings.Contains(got, want) ||
		strings.Contains(got, "too long ago") {
		t.Errorf("refused in a skip, logged %q; want it to say %q", got, want)
	}
}
