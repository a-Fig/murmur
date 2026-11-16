package daemon

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// While the device the registry has a name on is up, a send to the name goes there, whatever
// another device's presence shows. With that device gone or stale, it goes to a device still
// writing its presence before a stale one (#604).
func TestResolveNameAmongPresences(t *testing.T) {
	t.Parallel()
	now := time.Now()
	shows := func(live bool) *peer {
		p := &peer{pres: bus.Presence{Sessions: []bus.SessionInfo{{ID: "x", Name: "quiet-otter"}}}, lastChange: now}
		if !live {
			p.lastChange = now.Add(-time.Hour)
		}
		return p
	}
	for _, c := range []struct {
		what           string
		used, reserved string // the registry: quiet-otter's address, or the device that set it aside
		peers          map[string]*peer
		want           string
	}{
		{"the registry's device over one whose ID sorts first", "peer-0002/x", "",
			map[string]*peer{"peer-0001": shows(true), "peer-0002": shows(true)}, "peer-0002/x"},
		{"the registry's device, whichever of its sessions shows the name (#37)", "peer-0002/earlier", "",
			map[string]*peer{"peer-0001": shows(true), "peer-0002": shows(true)}, "peer-0002/x"},
		{"a live device over the registry's, gone stale: a clone back under a new ID", "peer-0001/x", "",
			map[string]*peer{"peer-0001": shows(false), "peer-0002": shows(true)}, "peer-0002/x"},
		{"the registry's device when every one is stale", "peer-0002/x", "",
			map[string]*peer{"peer-0001": shows(false), "peer-0002": shows(false)}, "peer-0002/x"},
		{"the device that set the name aside", "", "peer-0002",
			map[string]*peer{"peer-0001": shows(true), "peer-0002": shows(true)}, "peer-0002/x"},
		{"the first by device ID when the registry knows neither", "peer-0009/x", "",
			map[string]*peer{"peer-0001": shows(true), "peer-0002": shows(true)}, "peer-0001/x"},
		{"the only one that shows it, whatever the registry says", "peer-0009/x", "",
			map[string]*peer{"peer-0001": {lastChange: now}, "peer-0002": shows(false)}, "peer-0002/x"},
		{"the registry's address when no presence shows it", "peer-0009/x", "",
			map[string]*peer{"peer-0001": {lastChange: now}}, "peer-0009/x"},
		{"the registry's address when its device is up and the session has stopped", "peer-0002/stopped", "",
			map[string]*peer{"peer-0001": shows(true), "peer-0002": {lastChange: now}}, "peer-0002/stopped"},
		{"this device's own stopped session, whatever a presence shows", "here-0001/stopped", "",
			map[string]*peer{"peer-0001": shows(true)}, "here-0001/stopped"},
	} {
		d := &Daemon{device: "here-0001", opt: Options{StaleAfter: 6 * time.Minute}, lastPollOK: now, peers: c.peers,
			st: state{Used: map[string]string{}, Reserved: map[string]string{}}}
		if c.used != "" {
			d.st.Used["quiet-otter"] = c.used
		}
		if c.reserved != "" {
			d.st.Used["quiet-otter"] = "" // in use, by nobody known
			d.st.Reserved["quiet-otter"] = c.reserved
		}
		d.mu.Lock()
		got, ok := d.resolveNameLocked("quiet-otter")
		d.mu.Unlock()
		if !ok || got != c.want {
			t.Errorf("%s: resolved to %q, %v; want %s", c.what, got, ok, c.want)
		}
	}

	// A name a live device has set aside and not yet given to a session is nobody's to send to,
	// though another device's presence shows it.
	d := &Daemon{device: "here-0001", opt: Options{StaleAfter: 6 * time.Minute}, lastPollOK: now,
		peers: map[string]*peer{"peer-0001": shows(true), "peer-0002": {lastChange: now}},
		st:    state{Used: map[string]string{"quiet-otter": ""}, Reserved: map[string]string{"quiet-otter": "peer-0002"}}}
	d.mu.Lock()
	got, ok := d.resolveNameLocked("quiet-otter")
	d.mu.Unlock()
	if ok {
		t.Errorf("a name set aside by a live device resolved to %q", got)
	}
}
