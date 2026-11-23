package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// Through the reading of a presence from git, as a sync reads it: its colour is dated by its
// commit, but no earlier than this daemon's last good sync before the presence was first listed.
//   - A daemon that slept 10 days, and whose first sync after it failed to fetch a peer's final
//     presence (committed 8 days ago), reads it a sync later: the colour is gone, as it is for the
//     teammates who stayed awake (#1165 item 3, through a failed fetch).
//   - A daemon that has kept syncing reads a live peer whose clock runs 8 days behind: the colour
//     is held (#1169).
func TestAPresenceReadFromGitIsDatedForItsColourFromBeforeAGap(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name      string
		lastGood  time.Duration // before now: this daemon's last good sync when the presence was listed
		readLater bool          // its first fetch failed, and a later sync reads it
		held      bool
	}{
		{"asleep 10 days, read a sync late", 10 * day, true, false},
		{"awake, a writer 8 days behind", 5 * time.Second, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tagsDaemon(t)
			d.maxRecord = bus.MaxRecord
			d.opt.StaleAfter = 6 * time.Minute
			dir := t.TempDir()
			runGit(t, dir, "init", "-q", dir)
			now := time.Now()
			d.repo = &bus.Repo{Git: gitx.Git{Dir: filepath.Join(dir, ".git"),
				Env: []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", now.Add(-8*day).Unix())}},
				Name: "peer", Email: "peer@murmur.invalid"}
			pres := bus.Presence{V: bus.Version, Device: "peerdev", Host: "p", At: now.Add(-8 * day).UTC(),
				Color: 3, ColorAt: now.Add(-30 * day).UTC()}
			msg, err := bus.Encode(pres, "peerdev: presence")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			sha, err := d.repo.NewCommit(ctx, "", msg)
			if err != nil {
				t.Fatal(err)
			}
			ref := brand.PresencePrefix + "peerdev"
			d.mu.Lock()
			// This daemon already knew the peer, by an older presence it read before.
			d.peers["peerdev"] = &peer{sha: "old", listed: "old", writtenOf: "old", lastChange: now.Add(-tc.lastGood),
				pres: bus.Presence{V: bus.Version, Device: "peerdev", Color: 3, ColorAt: now.Add(-30 * day).UTC()}}
			d.lastPollOK = now.Add(-tc.lastGood)
			d.notePresencesListedLocked(map[string]string{ref: sha})
			d.mu.Unlock()
			if tc.readLater {
				d.sawPresence("peerdev", sha, false) // the fetch failed
				d.mu.Lock()
				d.lastPollOK = time.Now() // and that sync ended well
				d.notePresencesListedLocked(map[string]string{ref: sha})
				d.mu.Unlock()
			}
			d.updatePeer(ctx, ref, sha)
			d.mu.Lock()
			defer d.mu.Unlock()
			d.lastPollOK = time.Now()
			if p := d.peers["peerdev"]; p == nil || p.sha != sha {
				t.Fatalf("the presence wasn't read: %+v", p)
			}
			if got := d.colorsLocked()["peerdev"]; got.Picked != tc.held {
				t.Errorf("the peer's colour is %+v, want it picked: %v", got, tc.held)
			}
		})
	}
}
