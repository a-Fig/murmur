package daemon

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// colorRig is a daemon with only what the colour rules need: this device's pick of colour 3, made
// an hour ago, and one peer that picked the same colour a month ago and whose presence last
// changed gone ago.
func colorRig(t *testing.T, gone time.Duration) *Daemon {
	t.Helper()
	now := time.Now()
	return &Daemon{device: "me-0001", dir: t.TempDir(), log: log.New(io.Discard, "", 0),
		kick: make(chan struct{}, 1), lastPollOK: now, colorSettled: true,
		st: state{Color: 3, ColorAt: now.Add(-time.Hour).UTC()},
		peers: map[string]*peer{"dead-0001": {lastChange: now.Add(-gone),
			pres: bus.Presence{V: bus.Version, Device: "dead-0001", Color: 3, ColorAt: now.Add(-30 * 24 * time.Hour).UTC()}}}}
}

// A device whose presence hasn't changed for a week no longer holds the colour it picked: a daemon
// that was killed leaves its presence behind for good, and nobody could pick its colour (#581).
// One gone for less, though stale, keeps it.
func TestADeviceGoneAWeekLetsItsColourGo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		gone time.Duration
		held bool
	}{
		{"an hour", time.Hour, true},
		{"six days", 6 * 24 * time.Hour, true},
		{"eight days", 8 * 24 * time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := colorRig(t, c.gone)
			d.mu.Lock()
			defer d.mu.Unlock()
			// As a page asking would see it, with this device's pick not yet made.
			d.st.Color, d.st.ColorAt = 0, time.Time{}
			if got := d.colorsLocked()["dead-0001"]; got.Picked != c.held {
				t.Errorf("the peer's colour is %+v, want it picked: %v", got, c.held)
			}
		})
	}
}

// Picking the colour of a device gone a week is kept: the gone device's earlier pick doesn't take
// it back at the next sync, as a live device's would.
func TestAPickOfAGoneDevicesColourIsKept(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		gone time.Duration
		kept bool
	}{
		{"gone an hour, the earlier pick wins", time.Hour, false},
		{"gone eight days, this device keeps it", 8 * 24 * time.Hour, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := colorRig(t, c.gone)
			d.mu.Lock()
			defer d.mu.Unlock()
			d.settleColorLocked(time.Now())
			if kept := d.st.Color == 3; kept != c.kept {
				t.Errorf("after a sync this device's pick is %d (lost to %q), want it kept: %v", d.st.Color, d.st.ColorLostTo, c.kept)
			}
			if c.kept {
				if got := d.colorsLocked(); !got["me-0001"].Picked || got["me-0001"].Color != 2 || got["dead-0001"].Picked {
					t.Errorf("the colours are %+v, want this device with its pick and the gone one with none", got)
				}
			}
		})
	}
}

// A daemon that slept through the week, as on a laptop closed that long, holds its pick out of its
// presence until a sync has settled it: its teammates let the colour go meanwhile.
func TestADaemonAwayAWeekSettlesItsColourAgain(t *testing.T) {
	t.Parallel()
	d := colorRig(t, time.Hour)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.presenceLanded = time.Now().Add(-time.Hour).Round(0)
	d.colorAwayLocked()
	if color, _ := d.shownColorLocked(); color != 3 {
		t.Errorf("an hour after its last presence landed, the next carries colour %d, want 3", color)
	}
	d.presenceLanded = time.Now().Add(-8 * 24 * time.Hour).Round(0)
	began := time.Now().Add(-time.Second) // a sync that listed the remote before the daemon found it had been away
	d.colorAwayLocked()
	if color, _ := d.shownColorLocked(); color != 0 {
		t.Errorf("eight days after its last presence landed, the next carries colour %d, want none until a sync", color)
	}
	// That sync knows nothing of a pick made meanwhile, so it doesn't settle the colour.
	d.peers = map[string]*peer{}
	d.lastPollOK = time.Now()
	d.settleColorLocked(began)
	if d.colorSettled {
		t.Error("a sync begun before the daemon found it had been away settled its colour")
	}
	// A later sync finds a live peer picked it meanwhile, after this device had: it is given up all
	// the same, as on a restart, where the earlier pick would win between two devices that stayed.
	now := time.Now()
	d.peers = map[string]*peer{"live-0001": {lastChange: now,
		pres: bus.Presence{V: bus.Version, Device: "live-0001", Color: 3, ColorAt: now.Add(-time.Minute).UTC()}}}
	d.lastPollOK = now
	d.settleColorLocked(now.Add(time.Second))
	if d.st.Color != 0 || d.st.ColorLostTo == "" || !d.colorSettled {
		t.Errorf("after the sync: colour %d, lost to %q, settled %v; want the pick given up", d.st.Color, d.st.ColorLostTo, d.colorSettled)
	}
}

// A device back from invisible has its colour settled only by a sync begun after it came back: one
// under way may have listed the remote before a teammate picked the colour (#1084).
func TestBackFromInvisibleTheColourWaitsForAFreshSync(t *testing.T) {
	t.Parallel()
	d := colorRig(t, time.Hour)
	d.peers = map[string]*peer{}
	d.setMode("online", "invisible", time.Time{})
	began := time.Now().Add(-time.Second) // a sync under way as the device comes back
	d.setMode("invisible", "online", time.Now())
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastPollOK = time.Now()
	d.settleColorLocked(began)
	if d.colorSettled {
		t.Error("a sync begun before the device came back settled its colour")
	}
	d.settleColorLocked(time.Now().Add(time.Second))
	if color, _ := d.shownColorLocked(); color != 3 {
		t.Errorf("after a sync begun since, the presence carries colour %d, want its pick, 3", color)
	}
}

// A daemon away a week whose colour nobody took has its pick back after the next sync.
func TestADaemonAwayAWeekKeepsAColourNobodyTook(t *testing.T) {
	t.Parallel()
	d := colorRig(t, time.Hour)
	d.peers = map[string]*peer{}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.presenceLanded = time.Now().Add(-8 * 24 * time.Hour).Round(0)
	d.colorAwayLocked()
	d.lastPollOK = time.Now()
	d.settleColorLocked(time.Now().Add(time.Second))
	if color, _ := d.shownColorLocked(); color != 3 || d.st.ColorLostTo != "" {
		t.Errorf("after the sync the presence carries colour %d (lost to %q), want its pick, 3", color, d.st.ColorLostTo)
	}
}

// A device whose colour waits to be settled doesn't settle it past a live peer whose newest
// presence the sync couldn't fetch: that presence may carry the colour, picked while this device
// was away, and the pick put back would take it from the peer (#1165).
func TestTheColourWaitsForEveryLivePresenceToBeRead(t *testing.T) {
	t.Parallel()
	d := colorRig(t, time.Hour)
	d.opt.StaleAfter = time.Hour
	d.peers = map[string]*peer{}
	d.colorSettled = false                    // just started, or back from invisible
	d.sawPresence("live-0001", "aaaa", false) // listed, its fetch failed
	d.mu.Lock()
	defer d.mu.Unlock()
	d.settleColorLocked(time.Now().Add(-time.Second))
	if d.colorSettled {
		t.Fatal("a sync that couldn't fetch a live peer's presence settled this device's colour")
	}
	// The next sync fetches it: the peer picked the colour while this device was away.
	now := time.Now()
	d.peers["live-0001"] = &peer{sha: "aaaa", listed: "aaaa", lastChange: now,
		pres: bus.Presence{V: bus.Version, Device: "live-0001", Color: 3, ColorAt: now.Add(-time.Minute).UTC()}}
	d.settleColorLocked(now)
	if d.st.Color != 0 || d.st.ColorLostTo == "" || !d.colorSettled {
		t.Errorf("after the sync that read it: colour %d, lost to %q, settled %v; want the pick given up", d.st.Color, d.st.ColorLostTo, d.colorSettled)
	}

	// A presence that was fetched and found unreadable never will be read, so it doesn't hold the
	// colour back.
	d.st.Color, d.st.ColorAt, d.st.ColorLostTo = 3, now.Add(-time.Hour).UTC(), ""
	d.colorSettled = false
	d.peers = map[string]*peer{}
	d.mu.Unlock()
	d.sawPresence("junk-0001", "bbbb", true)
	d.mu.Lock()
	d.settleColorLocked(time.Now())
	if color, _ := d.shownColorLocked(); color != 3 {
		t.Errorf("beside a presence found unreadable, the presence carries colour %d, want its pick, 3", color)
	}
	// Nor does one refused as not its device's, fetched after a fetch of it failed.
	d.colorSettled = false
	d.peers = map[string]*peer{"forged-0001": {sha: "aaaa", listed: "cccc", lastChange: time.Now()}}
	d.refusedSHA = map[string]string{"forged-0001": "cccc"}
	d.settleColorLocked(time.Now())
	if color, _ := d.shownColorLocked(); color != 3 {
		t.Errorf("beside a presence refused, the presence carries colour %d, want its pick, 3", color)
	}
}
