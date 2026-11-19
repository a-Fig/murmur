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
		// has nothing. k0 never came back after an earlier loss, and still has tags to take; so does
		// k4, whose name since has none. k5's name has nothing either, but k2's session muted it.
		`"nameKeys":{"k1":"quiet-otter","k2":"calm-finch","k3":"brave-heron","k4":"dull-wren","k5":"plain-wren"},` +
		`"namesLeft":{"k0":"old-crane","k1":"older-name","k4":"old-crane"},` +
		`"tags":{"quiet-otter":{"self":["group:ui"]},"old-crane":{"self":["reviewing"]}},` +
		`"mutes":{"calm-finch":{"loud-jay":"2026-11-16T00:00:00Z","plain-wren":"2026-11-16T00:00:00Z"}},"explained":{"brave-heron":["dev-0001/person"]},` +
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
	if want := map[string]string{"k0": "old-crane", "k1": "quiet-otter", "k2": "calm-finch", "k3": "brave-heron", "k4": "old-crane", "k5": "plain-wren"}; !maps.Equal(left, want) {
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

	// One whose new name is pending goes by the old name's meanwhile: it is on do not disturb,
	// a tag it adds is added to them, and the session that muted it still doesn't hear from it.
	// By its key: moved to another conversation, it has none of them, and back, has them again.
	// The name takes the lot when it lands, and the mute of the old name is then of the new one.
	p := &session{id: "cccccc", key: "k3"}
	d.sessions[p.id] = p
	d.st.NamesLeft = map[string]string{"k3": "brave-heron"}
	d.st.Tags["brave-heron"] = &tags.State{Self: []string{tags.DoNotDisturb}}
	d.st.Mutes["bold-lark"]["brave-heron"] = at
	d.st.Mutes["brave-heron"] = map[string]time.Time{"loud-jay": at}
	d.st.Explained["brave-heron"] = []string{ada}
	d.setNameLocked(p, "")
	if !d.ownDNDLocked(p.id) || d.tagKeyLocked(p.id) != "brave-heron" || d.st.NamesLeft["k3"] != "brave-heron" || d.explainKeyLocked(p.id) != "brave-heron" {
		t.Errorf("with its name pending: kept by %q, explained by %q, left %v", d.tagKeyLocked(p.id), d.explainKeyLocked(p.id), d.st.NamesLeft)
	}
	from := bus.Addr(d.device, p.id)
	if _, muted := d.mutedAtLocked(s.id, from); !muted {
		t.Error("with its name pending, the session that muted it hears from it")
	}
	// And it is refused, or told who, when it sends: a message dropped on arrival with its sender
	// told nothing is what a mute never does. Only for a muter here.
	if !d.hasMutedLocked(d.device, s.id, from) || slices.Contains(d.muteNamesLocked(from, false), "brave-heron") ||
		!slices.Equal(d.mutersLocked(from, ""), []string{"bold-lark"}) {
		t.Errorf("with its name pending, a send finds it muted as %v (there %v), by %v", d.muteNamesLocked(from, true), d.muteNamesLocked(from, false), d.mutersLocked(from, ""))
	}
	// Stopped before the name lands, it still goes by the old name's: a T3 thread is found by
	// its route.
	delete(d.sessions, p.id)
	d.st.T3Routes[p.id] = t3Route{Thread: "thread-cccccc"}
	d.st.NamesLeft[nameKey("thread-cccccc", "", "", p.id)] = "brave-heron"
	if d.tagKeyLocked(p.id) != "brave-heron" || !d.ownDNDLocked(p.id) {
		t.Errorf("stopped with its name pending, it is kept by %q", d.tagKeyLocked(p.id))
	}
	delete(d.st.T3Routes, p.id)
	delete(d.st.NamesLeft, nameKey("thread-cccccc", "", "", p.id))
	d.sessions[p.id] = p
	p.key = "k9"
	if d.ownDNDLocked(p.id) || d.tagKeyLocked(p.id) != "id:cccccc" {
		t.Errorf("in another conversation it is kept by %q", d.tagKeyLocked(p.id))
	}
	p.key = "k3"
	d.tagStateLocked(p).Self = append(d.tagStateLocked(p).Self, "reviewing")
	d.setNameLocked(p, "keen-swift")
	if st := d.st.Tags["keen-swift"]; st == nil || !slices.Equal(st.Self, []string{tags.DoNotDisturb, "reviewing"}) || !d.ownDNDLocked(p.id) || d.st.Tags["brave-heron"] != nil {
		t.Errorf("tags once the name landed: %v", d.st.Tags)
	}
	if m := d.st.Mutes["bold-lark"]; !m["keen-swift"].Equal(at) || len(m) != 3 || len(d.st.Mutes["keen-swift"]) != 1 || len(d.st.NamesLeft) != 0 {
		t.Errorf("mutes once the name landed: %v, left %v", d.st.Mutes, d.st.NamesLeft)
	}
	if !slices.Contains(d.st.Explained["keen-swift"], ada) {
		t.Errorf("explained once the name landed: %v", d.st.Explained)
	}
}

// A state file whose namesLeft is null, as a hand might leave it, doesn't stop the daemon.
func TestDropOldNamesReadsANullNamesLeft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"namesLeft":null,"nameKeys":{"k1":"quiet-otter"},"tags":{"quiet-otter":{"self":["a"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dropOldNames(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var st struct {
		NamesLeft map[string]string `json:"namesLeft"`
	}
	if err := json.Unmarshal(b, &st); err != nil || st.NamesLeft["k1"] != "quiet-otter" {
		t.Errorf("state.json after: %s (%v)", b, err)
	}
}
