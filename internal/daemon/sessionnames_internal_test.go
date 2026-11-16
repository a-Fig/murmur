package daemon

import (
	"testing"
	"time"
)

// The name a long-gone session took last is forgotten once nothing else points at it (#747): T3
// makes a new session for a thread at every restart of its MCP server, and each left an entry for
// good. A session that runs, one whose inbox is kept, the name's holder and the session the
// registry has the name down for all keep theirs.
func TestTheNameOfALongGoneSessionIsForgotten(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-sessionNameKeep - time.Hour)
	d := &Daemon{device: "here-0001", sessions: map[string]*session{"runnin": {id: "runnin"}},
		st: state{
			SessionNames: map[string]string{"oldold": "quiet-otter", "recent": "quiet-otter", "holder": "quiet-otter",
				"first1": "quiet-otter", "runnin": "brisk-finch", "stoppd": "calm-heron", "undated": "tidy-wren"},
			NamesSeen: map[string]time.Time{"oldold": long, "recent": now.Add(-time.Hour), "holder": long,
				"first1": long, "runnin": long, "stoppd": long, "nobody": long},
			NameHolders: map[string]string{"quiet-otter": "holder"},
			Used:        map[string]string{"quiet-otter": "here-0001/first1"},
			Stopped:     map[string]time.Time{"stoppd": now},
		}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pruneSessionNamesLocked(now) {
		t.Fatal("nothing was forgotten")
	}
	for id, want := range map[string]bool{"oldold": false, "recent": true, "holder": true, "first1": true,
		"runnin": true, "stoppd": true, "undated": true} {
		if _, kept := d.st.SessionNames[id]; kept != want {
			t.Errorf("%s kept: %v, want %v", id, kept, want)
		}
	}
	if _, ok := d.st.NamesSeen["oldold"]; ok {
		t.Error("the forgotten session's time was kept")
	}
	if _, ok := d.st.NamesSeen["nobody"]; ok {
		t.Error("a time with no name was kept")
	}
	// One from before the times were kept counts from now, and goes a keep later.
	if at, ok := d.st.NamesSeen["undated"]; !ok || now.Sub(at) > time.Hour {
		t.Errorf("the undated session's time is %v, %v", at, ok)
	}
	if d.pruneSessionNamesLocked(now) {
		t.Error("a second look changed something")
	}
	if !d.pruneSessionNamesLocked(now.Add(sessionNameKeep+2*time.Hour)) || d.st.SessionNames["undated"] != "" {
		t.Error("the undated session's name outlived its keep")
	}
}
