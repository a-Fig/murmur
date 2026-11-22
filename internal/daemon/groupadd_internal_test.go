package daemon

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// A group a person took a session out of is left out of an agent's add of it, with a line in the
// log, and the rest are given: kept as the session's own under the person's mark, it would only
// take one of its 12 places (#543).
func TestAnAddLeavesOutAGroupAPersonTookTheSessionOutOf(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	var out strings.Builder
	d.log = log.New(&out, "", 0)
	d.st.Tags["name-bbbbbb"] = &tags.State{Human: map[string]tags.Mark{"group:ui": {On: false, By: "tyler", At: time.Now()}}}
	d.mu.Lock()
	d.applyGroupAddLocked(bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.GroupAdd, From: "far/ffffff",
		To: "dev/bbbbbb", At: time.Now(), Add: []string{"group:ui", "group:bus"}}, modeNormal)
	self := d.st.Tags["name-bbbbbb"].Self
	d.mu.Unlock()
	if !slices.Equal(self, []string{"group:bus"}) {
		t.Errorf("the session's own tags are %q; want group:bus alone", self)
	}
	if want := "far/ffffff added dev/bbbbbb to group:ui, but a person took that session out of it; not applied"; !strings.Contains(out.String(), want) {
		t.Errorf("logged %q; want %q", out.String(), want)
	}
}

// An add for a session on a device whose clock runs an hour or more ahead of this one's is refused
// to the adder: that device would take the add, dated by this clock, as too old to apply (#543). A
// clock a little ahead doesn't matter, and nor does a presence time pinned ahead by a clock that went
// back (nextPresenceAtLocked): its commit, which git dates by the clock now, shows the clock is right.
func TestAnAddForADeviceWhoseClockRunsAnHourAheadIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		signed, written time.Duration // ahead of this clock: the presence's own time, its commit's
		refused         bool
	}{{2 * time.Hour, 2 * time.Hour, true}, {20 * time.Minute, 20 * time.Minute, false}, {2 * time.Hour, 0, false}} {
		d := tagsDaemon(t)
		now := time.Now()
		d.peers["far"] = &peer{lastChange: now, written: now.Add(tc.written), pres: bus.Presence{V: bus.Version, Device: "far",
			At: now.Add(tc.signed), Groups: true, Sessions: []bus.SessionInfo{{ID: "ffffff", Agent: "claude"}}}}
		if got := addRefusedForClock(t, d, "far/ffffff"); got != tc.refused {
			t.Errorf("a presence signed %s and written %s ahead: refused for the clock %v; want %v", tc.signed, tc.written, got, tc.refused)
		}
	}
}

// The same, through the reading of a presence from git: a first read times it by its commit
// (firstSight), and a device whose clock runs ahead dates its commit ahead too.
func TestAPresenceReadFromGitShowsAClockAhead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		signed, written time.Duration
		refused         bool
	}{{2 * time.Hour, 2 * time.Hour, true}, {2 * time.Hour, 0, false}} {
		d := tagsDaemon(t)
		d.maxRecord = bus.MaxRecord
		dir := t.TempDir()
		runGit(t, dir, "init", "-q", dir)
		now := time.Now()
		d.repo = &bus.Repo{Git: gitx.Git{Dir: filepath.Join(dir, ".git"),
			Env: []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", now.Add(tc.written).Unix())}},
			Name: "farclock", Email: "farclock@murmur.invalid"}
		pres := bus.Presence{V: bus.Version, Device: "farclock", Host: "far", At: now.Add(tc.signed).UTC(), Groups: true,
			Sessions: []bus.SessionInfo{{ID: "ffffff", Agent: "claude", Started: now.UTC()}}}
		msg, err := bus.Encode(pres, "farclock: 1 sessions")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		sha, err := d.repo.NewCommit(ctx, "", msg)
		if err != nil {
			t.Fatal(err)
		}
		d.updatePeer(ctx, brand.PresencePrefix+"farclock", sha)
		d.mu.Lock()
		p := d.peers["farclock"]
		d.mu.Unlock()
		if p == nil || p.sha != sha {
			t.Fatalf("the presence wasn't read: %+v", p)
		}
		if got := addRefusedForClock(t, d, "farclock/ffffff"); got != tc.refused {
			t.Errorf("a presence signed %s and written %s ahead: refused for the clock %v; want %v", tc.signed, tc.written, got, tc.refused)
		}
	}
}

// addRefusedForClock adds addr to group:ui and says whether the add was refused for its device's
// clock. No team key here, so an add that passes the checks fails to go out, at once.
func addRefusedForClock(t *testing.T, d *Daemon, addr string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := d.addToGroups(ctx, "aaaaaa", proto.Request{Add: []string{"group:ui"}, For: []string{addr}})
	t.Logf("%s: %s", addr, r.Error)
	return strings.Contains(r.Error, "clock runs at least")
}
