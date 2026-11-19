package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
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
	// Every place that asks goes by that name: the session isn't to be woken, pending or landed.
	asked := func(when string) {
		t.Helper()
		if d.mayWakeLocked(s.id, d.settings) || d.noWakeLocked(s.id, d.settings) != bus.NoWakeOff || d.personWakesLocked(s.id, d.settings) {
			t.Errorf("%s, a session set never to be woken may be: mayWake %v, noWake %q, personWakes %v", when,
				d.mayWakeLocked(s.id, d.settings), d.noWakeLocked(s.id, d.settings), d.personWakesLocked(s.id, d.settings))
		}
	}
	asked("with its name pending")
	d.setNameLocked(s, "bold-lark")
	d.setNameLocked(o, "brisk-wren") // which has a choice of its own
	asked("with its name landed and its settings not yet moved")
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
	if _, ok := snap.SessionBusy["quiet-otter"]; ok {
		t.Error("quiet-otter still has a busy choice")
	}
	// A page that read the settings before the move saves what it read, with one change: the
	// old names in it are read as the new ones, and the new name's choice isn't lost.
	stale := web.Settings{Wake: web.Wake{When: web.WakeAlways}, Busy: web.BusySubject,
		SessionWake: map[string]web.Wake{"quiet-otter": never, "calm-finch": never, "brisk-wren": {When: web.WakeAlways}},
		SessionBusy: map[string]string{"quiet-otter": web.BusyWhole}}
	got, err := pageSource{d}.SetSettings(context.Background(), stale)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionWake["bold-lark"] != never || got.SessionBusy["bold-lark"] != web.BusyWhole || len(got.SessionWake) != 2 || got.SessionWake["brisk-wren"].When != web.WakeAlways {
		t.Errorf("after a save from a page that read before the move: %+v, busy %v", got.SessionWake, got.SessionBusy)
	}
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
	// k4's name has a choice still to be moved to it, from a loss before this one.
	if err := os.WriteFile(path, []byte(`{"nameKeys":{"k1":"quiet-otter","k2":"calm-finch","k3":"plain-wren","k4":"keen-swift"},`+
		`"settingsMoves":[["quiet-otter","keen-swift"]]}`), 0o600); err != nil {
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
	if err := json.Unmarshal(b, &st); err != nil || len(st.NamesLeft) != 3 || st.NamesLeft["k1"] != "quiet-otter" || st.NamesLeft["k2"] != "calm-finch" || st.NamesLeft["k4"] != "keen-swift" {
		t.Errorf("state.json after: %s (%v)", b, err)
	}
}

// A save that fails leaves the names waiting, and the session looked up by its old name; the
// next read of the settings moves them. A name that lands meanwhile isn't lost with the first.
func TestAFailedSettingsMoveIsTriedAgain(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	never := web.Wake{When: web.WakeNever}
	d.settings = defaultSettings()
	d.settings.SessionWake = map[string]web.Wake{"quiet-otter": never, "calm-finch": never}
	d.st.SettingsMoves = [][2]string{{"quiet-otter", "bold-lark"}}
	d.settingsMove.Store(true)
	// A folder where the file goes: the save fails.
	if err := os.Mkdir(d.settingsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if snap := d.wakeSnapshot(); snap.choice("quiet-otter") != never || len(d.st.SettingsMoves) != 1 || !d.settingsMove.Load() {
		t.Errorf("after a save that failed: %+v, moves %v", snap.SessionWake, d.st.SettingsMoves)
	}
	if err := os.Remove(d.settingsPath()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.SettingsMoves = append(d.st.SettingsMoves, [2]string{"calm-finch", "keen-swift"})
	d.mu.Unlock()
	snap := d.wakeSnapshot()
	if snap.choice("bold-lark") != never || snap.choice("keen-swift") != never || len(snap.SessionWake) != 2 || len(d.st.SettingsMoves) != 0 || d.settingsMove.Load() {
		t.Errorf("after the next read: %+v, moves %v", snap.SessionWake, d.st.SettingsMoves)
	}
}

// A clone that loses its keys twice, with the save of the first move failing between, has its
// session's choices two names back: they are looked up there, pending and landed, and both moves
// are made at the next read.
func TestTwoLossesWithAFailedSaveBetweenKeepNeverWake(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	never := web.Wake{When: web.WakeNever}
	d.settings = defaultSettings()
	d.settings.SessionWake = map[string]web.Wake{"quiet-otter": never}
	// A folder where the file goes: the save fails.
	if err := os.Mkdir(d.settingsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	asked := func(id, when string) {
		t.Helper()
		if d.mayWakeLocked(id, d.settings) || d.noWakeLocked(id, d.settings) != bus.NoWakeOff || d.personWakesLocked(id, d.settings) {
			t.Errorf("%s, a session set never to be woken may be (its settings are %q's)", when, d.settingsNameLocked(id))
		}
	}
	d.mu.Lock()
	s := d.sessions["aaaaaa"]
	s.key = "k1"
	d.st.NamesLeft = map[string]string{"k1": "quiet-otter"}
	d.setNameLocked(s, "")
	d.setNameLocked(s, "bold-lark")
	d.mu.Unlock()
	d.wakeSnapshot() // fails to save
	d.mu.Lock()
	// The second loss: its key's name is the first new one, whose move is still to be made.
	d.st.NamesLeft = map[string]string{"k1": "bold-lark"}
	d.setNameLocked(s, "")
	asked(s.id, "lost twice, with its name pending")
	d.setNameLocked(s, "keen-swift")
	asked(s.id, "lost twice, with its name landed")
	if len(d.st.SettingsMoves) != 2 {
		t.Errorf("moves %v", d.st.SettingsMoves)
	}
	d.mu.Unlock()
	if err := os.Remove(d.settingsPath()); err != nil {
		t.Fatal(err)
	}
	snap := d.wakeSnapshot()
	if snap.choice("keen-swift") != never || len(snap.SessionWake) != 1 || len(d.st.SettingsMoves) != 0 {
		t.Errorf("after the next read: %+v, moves %v", snap.SessionWake, d.st.SettingsMoves)
	}
	d.mu.Lock()
	asked(s.id, "after the move")
	d.mu.Unlock()
}
