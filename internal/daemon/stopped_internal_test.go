package daemon

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/t3db"
)

// What a session the daemon's stop ended has is kept as a stopped session's is (#736): the next
// daemon, two minutes old, sweeps away what belongs to sessions neither registered nor stopped,
// and this one's inbox, commits and T3 route stay.
func TestWhatASessionEndedByTheDaemonsStopHasOutlivesTheSweep(t *testing.T) {
	t.Parallel()
	d, _ := bracketDaemon(t)
	d.mu.Lock()
	d.st.T3Routes = map[string]t3Route{"aaaaaa": {CLI: "t3", Thread: "th-a"}, "ghost": {CLI: "t3", Thread: "th-g"}}
	d.st.Inbox["aaaaaa"], d.st.Inbox["ghost"] = []*inMsg{{}}, []*inMsg{{}}
	d.st.Commits["aaaaaa"] = []commitRec{{SHA: "c1"}}
	d.stoppedWithDaemonLocked("aaaaaa")
	d.started = time.Now().Add(-3 * time.Minute) // the next daemon, past waiting for anyone
	d.mu.Unlock()

	d.pruneInboxes()
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.st.Stopped["aaaaaa"]; !ok || d.sessions["aaaaaa"] != nil {
		t.Fatalf("the session isn't recorded as stopped")
	}
	if len(d.st.Inbox["aaaaaa"]) != 1 || len(d.st.Commits["aaaaaa"]) != 1 || d.st.T3Routes["aaaaaa"].Thread != "th-a" {
		t.Errorf("kept: inbox %d, commits %d, route %+v", len(d.st.Inbox["aaaaaa"]), len(d.st.Commits["aaaaaa"]), d.st.T3Routes["aaaaaa"])
	}
	if _, ok := d.st.Inbox["ghost"]; ok {
		t.Error("the inbox of a session with no stop recorded was kept")
	}
}

// For its first minute a daemon hands nothing to the thread of a session that isn't registered
// while the thread is unread or T3 shows a run of it going: its server may be about to reconnect,
// and the message would be queued behind the run (#736). After that, or with the thread idle, it
// is a stopped session's thread, which a message may wake.
func TestAThreadWhoseSessionMayReconnectIsLeftAlone(t *testing.T) {
	t.Parallel()
	d, _ := bracketDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.T3Routes = map[string]t3Route{"bbbbbb": {CLI: "t3", Thread: "th-b"}}
	d.st.Stopped["bbbbbb"], d.st.Stopped["dddddd"] = time.Now(), time.Now()
	d.st.T3Routes["dddddd"] = t3Route{CLI: "t3", Thread: "th-d"}
	d.st.Downed = map[string]bool{"bbbbbb": true}
	k := threadKey{"", "th-b"}
	d.threads = map[threadKey]t3db.State{}

	if !d.hookBusyLocked("bbbbbb") {
		t.Error("a thread not yet read was free to take a turn")
	}
	d.threads[k] = t3db.State{Busy: true}
	if !d.hookBusyLocked("bbbbbb") {
		t.Error("a thread with a run going was free to take a turn")
	}
	d.threads[k] = t3db.State{}
	if d.hookBusyLocked("bbbbbb") {
		t.Error("an idle thread, read, was held")
	}
	d.threads[k] = t3db.State{Busy: true}
	d.started = time.Now().Add(-2 * reconnectHold)
	if d.hookBusyLocked("bbbbbb") {
		t.Error("the thread was still held after the daemon's first minute")
	}
	d.started = time.Now()
	if d.hookBusyLocked("cccccc") {
		t.Error("a session with no thread was held")
	}
	if d.hookBusyLocked("dddddd") {
		t.Error("the thread of a session that stopped before the daemon did was held")
	}
}
