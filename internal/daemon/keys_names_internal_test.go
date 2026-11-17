package daemon

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/tags"
)

// A clone whose keys are lost has its names taken out of its state file before it takes its new
// device ID (#617): a start that fails after that, before the daemon saves its state, leaves the
// next one with new keys and nothing to say the ID changed, and it would have kept the old names
// under the new ID. The rest of the file is kept, and a start with the keys in place changes
// nothing.
func TestLostKeysDropTheNamesBeforeTheNewID(t *testing.T) {
	dir := t.TempDir()
	old := "device-0123abcd"
	if err := os.WriteFile(filepath.Join(dir, "device"), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state.json")
	before := `{"pins":{"` + old + `":{"sign":"x"}},"usedNames":{"quiet-otter":"` + old + `/aaaaaa","brisk-finch":""},` +
		`"reservedNames":{"brisk-finch":"` + old + `"},"namePool":["brisk-finch"],` +
		`"nameBinds":{"brisk-finch":"bbbbbb"},"nameHolders":{"quiet-otter":"aaaaaa"},"sessionNames":{"aaaaaa":"quiet-otter"},` +
		`"namesSeen":{"aaaaaa":"2026-11-16T00:00:00Z"},` +
		// What sessions kept by their names (#827): k1's, k2's and k3's names have something, k4's
		// has nothing. k0 never came back after an earlier loss, and still has tags to take.
		`"nameKeys":{"k1":"quiet-otter","k2":"calm-finch","k3":"brave-heron","k4":"plain-wren"},` +
		`"namesLeft":{"k0":"old-crane","k1":"older-name"},` +
		`"tags":{"quiet-otter":{"self":["group:ui"]},"old-crane":{"self":["reviewing"]}},` +
		`"mutes":{"calm-finch":{"loud-jay":"2026-11-16T00:00:00Z"}},"explained":{"brave-heron":["dev-0001/person"]},` +
		`"unwritten":[{"v":1,"id":"e1","type":"session.started","from":"` + old + `/aaaaaa"}]}`
	if err := os.WriteFile(state, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(state, saved, saved); err != nil {
		t.Fatal(err)
	}
	device, _, _, err := loadIdentity(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if device == old {
		t.Fatalf("kept %s with new keys", old)
	}
	read := func() map[string]json.RawMessage {
		b, err := os.ReadFile(state)
		if err != nil {
			t.Fatal(err)
		}
		var st map[string]json.RawMessage
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatalf("state.json: %v\n%s", err, b)
		}
		return st
	}
	st := read()
	// The starts and stops not yet written go too: they are from the old device's addresses.
	for _, k := range []string{"nameKeys", "namePool", "nameBinds", "nameHolders", "sessionNames", "namesSeen", "unwritten"} {
		if _, ok := st[k]; ok {
			t.Errorf("%s is still in state.json", k)
		}
	}
	// Which name each key had is kept, for the names that have something to hand on (#827).
	var left map[string]string
	if err := json.Unmarshal(st["namesLeft"], &left); err != nil {
		t.Errorf("namesLeft: %v", err)
	}
	if want := map[string]string{"k0": "old-crane", "k1": "quiet-otter", "k2": "calm-finch", "k3": "brave-heron"}; !maps.Equal(left, want) {
		t.Errorf("namesLeft is %v, want %v", left, want)
	}
	for _, k := range []string{"pins", "usedNames", "reservedNames", "tags", "mutes", "explained"} {
		if _, ok := st[k]; !ok {
			t.Errorf("%s went from state.json", k)
		}
	}
	// Its time is still when the last daemon saved it, which dates the stops a killed one left.
	if fi, err := os.Stat(state); err != nil {
		t.Error(err)
	} else if !fi.ModTime().Equal(saved) {
		t.Errorf("state.json's time is %v, want %v", fi.ModTime(), saved)
	}
	// The next start, its keys in place, is the same device, and leaves the file alone.
	after, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if again, _, _, err := loadIdentity(dir, nil, nil); err != nil || again != device {
		t.Fatalf("the next start is %s (%v), want %s", again, err, device)
	}
	if b, _ := os.ReadFile(state); string(b) != string(after) {
		t.Errorf("the next start changed state.json:\n%s\nwas\n%s", b, after)
	}
}

// A session that comes back after its clone lost its keys has a new name (#617), and takes to it
// what its key kept under the old one (#827): the tags, the mutes, added to any the new name has,
// and who has been explained to it. A key that left nothing takes nothing, and a name's own tags
// stand over the old one's.
func TestANewNameTakesWhatTheOldOneKept(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	ada := "dev-0001/" + bus.PersonSession
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	s := d.sessions["aaaaaa"]
	s.key = "k1"
	d.st.NamesLeft = map[string]string{"k1": "quiet-otter", "k2": "calm-finch"}
	d.st.Tags = map[string]*tags.State{"quiet-otter": {Self: []string{"group:ui", tags.DoNotDisturb}}, "calm-finch": {Self: []string{"old"}},
		"brisk-wren": {Self: []string{"own"}}}
	d.st.Mutes = map[string]map[string]time.Time{"quiet-otter": {"loud-jay": at, "shrill-owl": at}, "bold-lark": {"shrill-owl": at.Add(time.Hour)}}
	d.st.Explained = map[string][]string{"quiet-otter": {ada}}

	d.setNameLocked(s, "bold-lark")
	if st := d.st.Tags["bold-lark"]; st == nil || !slices.Equal(st.Self, []string{"group:ui", tags.DoNotDisturb}) || d.st.Tags["quiet-otter"] != nil {
		t.Errorf("tags after the new name landed: %v", d.st.Tags)
	}
	if !d.ownDNDLocked(s.id) {
		t.Error("the session is off do not disturb")
	}
	if m := d.st.Mutes["bold-lark"]; len(m) != 2 || !m["loud-jay"].Equal(at) || !m["shrill-owl"].Equal(at.Add(time.Hour)) || d.st.Mutes["quiet-otter"] != nil {
		t.Errorf("mutes after the new name landed: %v", d.st.Mutes)
	}
	if !slices.Contains(d.st.Explained["bold-lark"], ada) || d.st.Explained["quiet-otter"] != nil {
		t.Errorf("explained after the new name landed: %v", d.st.Explained)
	}
	if _, waits := d.st.NamesLeft["k1"]; waits || d.st.NamesLeft["k2"] != "calm-finch" {
		t.Errorf("namesLeft after k1 came back: %v", d.st.NamesLeft)
	}

	// Another key's session comes back to a name that has tags of its own: those stand.
	o := &session{id: "bbbbbb", key: "k2"}
	d.sessions[o.id] = o
	d.setNameLocked(o, "brisk-wren")
	if st := d.st.Tags["brisk-wren"]; st == nil || !slices.Equal(st.Self, []string{"own"}) || d.st.Tags["calm-finch"] != nil || len(d.st.NamesLeft) != 0 {
		t.Errorf("tags after a name with its own took an old one's: %v, left %v", d.st.Tags, d.st.NamesLeft)
	}
}
