package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/web"
)

// The page's own wake and busy choices for a session follow it to the name it gets after its
// clone lost its keys (#836): they are kept by name, and left with the old one a session set
// never to be woken came back with the device's choice. While the new name is pending, and until
// the choices are moved, the session is looked up by its old name; the move waits for the next
// read of the settings, which have a lock of their own.
func TestASessionsSettingsFollowItToItsNewName(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	never := web.Wake{When: web.WakeNever}
	d.settings = defaultSettings()
	d.settings.SessionWake = map[string]web.Wake{"quiet-otter": never, "calm-finch": never, "brisk-wren": {When: web.WakeAlways}}
	d.settings.SessionBusy = map[string]string{"quiet-otter": web.BusyWhole}
	d.mu.Lock()
	s := d.sessions["aaaaaa"]
	s.key = "k1"
	o := d.sessions["bbbbbb"]
	o.key = "k2"
	d.st.NamesLeft = map[string]string{"k1": "quiet-otter", "k2": "calm-finch"}
	d.setNameLocked(s, "")
	if got := d.settingsNameLocked(s.id); got != "quiet-otter" {
		t.Errorf("with its name pending, its settings are %q's", got)
	}
	d.setNameLocked(s, "bold-lark")
	d.setNameLocked(o, "brisk-wren") // which has a choice of its own
	if got := d.settingsNameLocked(s.id); got != "quiet-otter" || len(d.st.SettingsMoves) != 2 {
		t.Errorf("its name landed and nothing moved yet: its settings are %q's, moves %v", got, d.st.SettingsMoves)
	}
	d.mu.Unlock()

	snap := d.wakeSnapshot()
	if snap.choice("bold-lark") != never || snap.busyChoice("bold-lark") != web.BusyWhole {
		t.Errorf("after the move: wake %+v, busy %q", snap.choice("bold-lark"), snap.busyChoice("bold-lark"))
	}
	if w := snap.choice("brisk-wren"); w.When != web.WakeAlways {
		t.Errorf("a name's own choice gave way to the old name's: %+v", w)
	}
	for _, old := range []string{"quiet-otter", "calm-finch"} {
		if _, ok := snap.SessionWake[old]; ok {
			t.Errorf("%s still has a choice", old)
		}
	}
	d.mu.Lock()
	if got := d.settingsNameLocked(s.id); got != "bold-lark" || len(d.st.SettingsMoves) != 0 || d.settingsMove.Load() {
		t.Errorf("after the move: its settings are %q's, moves %v", got, d.st.SettingsMoves)
	}
	d.mu.Unlock()
	// And it is on disk, for the next daemon.
	b, err := os.ReadFile(d.settingsPath())
	var saved pageSettings
	if err == nil {
		err = json.Unmarshal(b, &saved)
	}
	if err != nil || saved.SessionWake["bold-lark"] != never {
		t.Errorf("settings.json after the move: %s (%v)", b, err)
	}
}

// A name with nothing kept but a choice on the page is still one to hand on.
func TestDropOldNamesKeepsANameWithSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"nameKeys":{"k1":"quiet-otter","k2":"calm-finch","k3":"plain-wren"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	set := `{"wake":{"when":"always"},"sessionWake":{"quiet-otter":{"when":"never"}},"sessionBusy":{"calm-finch":"whole"}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(set), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dropOldNames(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var st struct {
		NamesLeft map[string]string `json:"namesLeft"`
	}
	if err := json.Unmarshal(b, &st); err != nil || len(st.NamesLeft) != 2 || st.NamesLeft["k1"] != "quiet-otter" || st.NamesLeft["k2"] != "calm-finch" {
		t.Errorf("state.json after: %s (%v)", b, err)
	}
}
