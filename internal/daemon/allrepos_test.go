package daemon_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/a-Fig/murmur/internal/web"
)

// settingsOf is the settings a device's page reads.
func settingsOf(t *testing.T, p *page) web.Settings {
	t.Helper()
	code, body := p.call("GET", "/api/settings", p.token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/settings: %d %s", code, body)
	}
	var s web.Settings
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("settings %s: %v", body, err)
	}
	return s
}

// A change on one page's All repos tab reaches every clone on the machine: the clone whose page made
// it at once, another clone's running daemon within its look, and a clone that starts later. A
// repo's own change after it stands until All repos changes that same setting again (#816).
func TestAllReposSettingsReachEveryClone(t *testing.T) {
	t.Parallel()
	clonesDir := t.TempDir()
	a := newDeviceWith(t, newRemote(t), func(d *device) { d.clones = clonesDir })
	b := newDeviceWith(t, newRemote(t), func(d *device) { d.clones = clonesDir })
	pa, pb := a.page(), b.page()

	if code, body := pa.call("GET", "/api/settings/all", pa.token, ""); code != http.StatusOK || body != "{}\n" && body != "{}" {
		t.Fatalf("settings for all repos before any: %d %q, want none", code, body)
	}
	if code, body := pa.call("PUT", "/api/settings/all", pa.token, `{"wake":{"when":"sometimes"}}`); code != http.StatusBadRequest {
		t.Errorf("a wake the page wouldn't save: %d %s, want 400", code, body)
	}
	if code, body := pa.call("PUT", "/api/settings/all", pa.token, `{"wake":{"when":"never"},"sendFiles":true}`); code != http.StatusOK {
		t.Fatalf("PUT /api/settings/all: %d %s", code, body)
	}
	if s := settingsOf(t, pa); s.Wake.When != web.WakeNever || !s.SendFiles {
		t.Errorf("the clone whose page set them: %+v, want wake never and sending on at once", s)
	}
	eventually(t, "the other clone to take the settings for all repos", func() bool {
		s := settingsOf(t, pb)
		return s.Wake.When == web.WakeNever && s.SendFiles
	})

	// A page that loaded b's settings before it took them saves a change of the busy choice alone,
	// with the old wake beside it: the wake taken from All repos stays.
	if code, body := pb.call("PUT", "/api/settings", pb.token, `{"wake":{"when":"within"},"busy":"whole","set":["busy"]}`); code != http.StatusOK {
		t.Fatalf("a save naming busy: %d %s", code, body)
	}
	if s := settingsOf(t, pb); s.Wake.When != web.WakeNever || s.Busy != web.BusyWhole {
		t.Errorf("after a save naming busy alone: %+v, want the wake from All repos (never) kept and busy whole", s)
	}

	// A Do not disturb save names neither, and keeps both.
	if code, body := pb.call("PUT", "/api/settings", pb.token, `{"wake":{"when":"within"},"busy":"subject","mode":"dnd","set":[]}`); code != http.StatusOK {
		t.Fatalf("a Do not disturb save: %d %s", code, body)
	}
	if s := settingsOf(t, pb); s.Wake.When != web.WakeNever || s.Busy != web.BusyWhole || s.Mode != web.ModeDND {
		t.Errorf("after a Do not disturb save: %+v, want wake never and busy whole kept, and the mode dnd", s)
	}

	// b's own page changes the wake and the busy choice there.
	if code, body := pb.call("PUT", "/api/settings", pb.token, `{"wake":{"when":"always"},"busy":"whole","sendFiles":true}`); code != http.StatusOK {
		t.Fatalf("b's own save: %d %s", code, body)
	}
	// All repos changes the busy choice only: b's wake stands.
	if code, body := pa.call("PUT", "/api/settings/all", pa.token, `{"busy":"subject"}`); code != http.StatusOK {
		t.Fatalf("PUT /api/settings/all: %d %s", code, body)
	}
	eventually(t, "the other clone to take the new busy choice", func() bool {
		return settingsOf(t, pb).Busy == web.BusySubject
	})
	if s := settingsOf(t, pb); s.Wake.When != web.WakeAlways || !s.SendFiles {
		t.Errorf("b after a change of the busy choice for all repos: %+v, want its own wake (always) kept", s)
	}

	// A clone that starts later takes everything the record holds.
	c := newDeviceWith(t, newRemote(t), func(d *device) { d.clones = clonesDir })
	pc := c.page()
	eventually(t, "a clone started later to take the settings for all repos", func() bool {
		s := settingsOf(t, pc)
		return s.Wake.When == web.WakeNever && s.Busy == web.BusySubject && s.SendFiles
	})
	if code, body := pc.call("GET", "/api/settings/all", pc.token, ""); code != http.StatusOK || body == "{}\n" {
		t.Errorf("the later clone's page reads the settings for all repos: %d %s", code, body)
	}
}
