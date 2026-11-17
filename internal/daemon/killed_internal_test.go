package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
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

// A daemon that last wrote its state more than StoppedInbox ago, and was then killed: a server
// that reconnects as the next daemon starts keeps its inbox (#762), as it did before a stop was
// recorded for such a session. One that isn't back two minutes on has run out of time.
func TestAServerBackAtOnceKeepsItsInboxHoweverOldTheLastWrite(t *testing.T) {
	t.Parallel()
	load := func() *Daemon {
		dir := t.TempDir()
		st := state{Registered: map[string]bool{"aaaaaa": true, "bbbbbb": true},
			Inbox: map[string][]*inMsg{"aaaaaa": {{}}, "bbbbbb": {{}}}}
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		written := time.Now().Add(-60 * time.Hour)
		if err := os.Chtimes(path, written, written); err != nil {
			t.Fatal(err)
		}
		d := &Daemon{dir: dir, log: log.New(io.Discard, "", 0), device: "here-0001", sessions: map[string]*session{},
			opt: Options{StoppedInbox: 24 * time.Hour}, started: time.Now()}
		if err := d.loadState(); err != nil {
			t.Fatal(err)
		}
		return d
	}

	d := load()
	d.pruneInboxes() // as after the first presence push
	d.mu.Lock()
	// Kept is kept for everything that asks: a message is queued for it, and its thread read.
	if !d.receivesLocked("bbbbbb") || !slices.Contains(d.recipientsLocked(), "bbbbbb") || !d.keptInboxLocked() {
		t.Error("a session whose inbox is kept as the daemon starts isn't one a message is queued for")
	}
	// A stop that old with no daemon having gone down on it has run out, young daemon or not.
	d.st.Stopped["cccccc"], d.st.Inbox["cccccc"] = time.Now().Add(-60*time.Hour), []*inMsg{{}}
	d.mu.Unlock()
	d.pruneInboxes()
	d.mu.Lock()
	if _, kept := d.st.Inbox["cccccc"]; kept || d.receivesLocked("cccccc") {
		t.Error("a session that stopped on its own 60 hours ago kept its inbox")
	}
	s := d.newSessionLocked(&proto.Hello{ID: "aaaaaa"}, "claude-code", "", "aaaaaa")
	_, stopped := d.st.Stopped["aaaaaa"]
	if len(d.st.Inbox["aaaaaa"]) != 1 || stopped || d.st.Downed["aaaaaa"] || s.since.IsZero() {
		t.Errorf("back at once: inbox %v, still stopped %v, still waited for %v, since %v", d.st.Inbox["aaaaaa"], stopped, d.st.Downed["aaaaaa"], s.since)
	}
	if len(d.st.Inbox["bbbbbb"]) != 1 {
		t.Error("a session not back yet lost its inbox inside the two minutes")
	}
	d.started = time.Now().Add(-3 * time.Minute)
	d.mu.Unlock()
	d.pruneInboxes()
	d.mu.Lock()
	if _, kept := d.st.Inbox["bbbbbb"]; kept || d.receivesLocked("bbbbbb") || d.st.Downed["bbbbbb"] {
		t.Error("a session not back after two minutes, stopped 60 hours, kept its inbox")
	}
	if len(d.st.Inbox["aaaaaa"]) != 1 {
		t.Error("the session that came back lost its inbox to the sweep")
	}
	d.mu.Unlock()

	// One that comes back after the two minutes, before any sweep, has run out of time too.
	d = load()
	d.started = time.Now().Add(-3 * time.Minute)
	d.mu.Lock()
	d.newSessionLocked(&proto.Hello{ID: "aaaaaa"}, "claude-code", "", "aaaaaa")
	if len(d.st.Inbox["aaaaaa"]) != 0 {
		t.Error("a session back after the two minutes, stopped 60 hours, kept its inbox")
	}
	d.mu.Unlock()
}
