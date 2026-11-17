package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A daemon that was killed left its sessions registered in the state file, and the next one
// counts them as stopped when that file was last written (#762): what they have then outlives
// the sweep that takes what belongs to sessions neither registered nor stopped. A stop already
// recorded keeps its time, and a session nobody listed is still swept.
func TestSessionsOfAKilledDaemonCountAsStopped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	earlier := time.Now().Add(-20 * time.Hour).Round(time.Second)
	st := state{
		Registered: map[string]bool{"aaaaaa": true, "bbbbbb": true},
		Stopped:    map[string]time.Time{"bbbbbb": earlier},
		Inbox:      map[string][]*inMsg{"aaaaaa": {{}}, "bbbbbb": {{}}, "ghost": {{}}},
		T3Routes:   map[string]t3Route{"aaaaaa": {CLI: "t3", Thread: "th-a"}, "ghost": {CLI: "t3", Thread: "th-g"}},
		Commits:    map[string][]commitRec{"aaaaaa": {{SHA: "c1"}}},
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	written := time.Now().Add(-3 * time.Hour).Round(time.Second)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{dir: dir, log: log.New(io.Discard, "", 0), device: "here-0001", sessions: map[string]*session{},
		opt: Options{StoppedInbox: 24 * time.Hour}}
	if err := d.loadState(); err != nil {
		t.Fatal(err)
	}
	if at := d.st.Stopped["aaaaaa"]; !at.Equal(written) {
		t.Errorf("the session of the killed daemon stopped at %v, want %v, when the state was last written", at, written)
	}
	if at := d.st.Stopped["bbbbbb"]; !at.Equal(earlier) {
		t.Errorf("a stop already recorded moved to %v", at)
	}
	if !d.st.Downed["aaaaaa"] || d.st.Downed["bbbbbb"] || len(d.st.Registered) != 0 {
		t.Errorf("waited for: %v, still registered: %v", d.st.Downed, d.st.Registered)
	}

	d.started = time.Now().Add(-3 * time.Minute)
	d.pruneInboxes()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.st.Inbox["aaaaaa"]) != 1 || d.st.T3Routes["aaaaaa"].Thread != "th-a" || len(d.st.Commits["aaaaaa"]) != 1 {
		t.Errorf("the sweep took what the killed daemon's session had: inbox %v, routes %v, commits %v", d.st.Inbox, d.st.T3Routes, d.st.Commits)
	}
	if _, kept := d.st.Inbox["ghost"]; kept {
		t.Error("the sweep left the inbox of a session nobody listed")
	}
}
