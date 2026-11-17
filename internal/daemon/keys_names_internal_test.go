package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		`"reservedNames":{"brisk-finch":"` + old + `"},"nameKeys":{"k1":"quiet-otter"},"namePool":["brisk-finch"],` +
		`"nameBinds":{"brisk-finch":"bbbbbb"},"nameHolders":{"quiet-otter":"aaaaaa"},"sessionNames":{"aaaaaa":"quiet-otter"},` +
		`"namesSeen":{"aaaaaa":"2026-11-16T00:00:00Z"}}`
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
	for _, k := range []string{"nameKeys", "namePool", "nameBinds", "nameHolders", "sessionNames", "namesSeen"} {
		if _, ok := st[k]; ok {
			t.Errorf("%s is still in state.json", k)
		}
	}
	for _, k := range []string{"pins", "usedNames", "reservedNames"} {
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
