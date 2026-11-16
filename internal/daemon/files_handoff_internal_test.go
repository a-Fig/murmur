package daemon

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// A message that waited for its files goes, once the fetch ends, to the session that holds its
// recipient's name then: one that took the name during a slow fetch, as a process that resumed the
// conversation does, took only what was in the inbox, and the message would otherwise land in the
// old session's, which nobody reads (#703).
func TestFilesThatArriveAfterAHandoffGoToTheNewHolder(t *testing.T) {
	t.Parallel()
	const nameX = "quiet-otter"
	for _, handoff := range []bool{false, true} {
		d := &Daemon{opt: Options{StoppedInbox: time.Hour}, dir: t.TempDir(), log: log.New(io.Discard, "", 0),
			device: "here-0001", started: time.Now(), sessions: map[string]*session{},
			st: state{Inbox: map[string][]*inMsg{}, Stopped: map[string]time.Time{"bbbbbb": time.Now().Add(-time.Minute)},
				NameKeys:    map[string]string{nameKey("", "x", "", "bbbbbb"): nameX},
				NameHolders: map[string]string{nameX: "bbbbbb"}, SessionNames: map[string]string{"bbbbbb": nameX},
				FilesWait: map[string]*fileWait{}}}
		e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", To: "here-0001/bbbbbb",
			Subject: "a file", At: time.Now(), Files: []bus.File{{Name: "a.txt"}}}
		d.mu.Lock()
		d.st.FilesWait[e.ID] = &fileWait{Event: e, Since: time.Now(), For: []string{"bbbbbb"}}
		if handoff {
			// Meanwhile another process resumes the conversation and takes the name.
			d.newSessionLocked(&proto.Hello{ID: "cccccc", ClaudeSession: "x"}, "claude-code", "", nameKey("", "x", "", "cccccc"))
		}
		d.filesFetchedLocked(e.ID)
		want, other := "bbbbbb", "cccccc"
		if handoff {
			want, other = other, want
		}
		if len(d.st.Inbox[want]) != 1 || len(d.st.Inbox[other]) != 0 || len(d.st.FilesWait) != 0 {
			t.Errorf("handoff %v: %s has %d, %s has %d, %d still waiting; want the message in %s's inbox",
				handoff, want, len(d.st.Inbox[want]), other, len(d.st.Inbox[other]), len(d.st.FilesWait), want)
		}
		d.mu.Unlock()
	}
}

// A session that takes another's T3 thread, rather than its name, is a recipient of what waits for
// its files for the other: it took what the other had unread, and nothing would forward to it.
func TestFilesThatArriveAfterARouteTakeoverGoToTheNewSession(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	d.st.Inbox, d.st.FilesWait = map[string][]*inMsg{}, map[string]*fileWait{}
	d.st.Stopped["aaaaaa"] = time.Now().Add(-time.Minute)
	d.st.T3Routes["aaaaaa"] = t3Route{CLI: "/t3", Thread: "thread-x"}
	inT3(d, "bbbbbb", 0)
	b := d.sessions["bbbbbb"]
	b.route.T3Thread = "thread-x"
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/cccccc", To: "dev/aaaaaa",
		Subject: "a file", At: time.Now(), Files: []bus.File{{Name: "a.txt"}}}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.FilesWait[e.ID] = &fileWait{Event: e, Since: time.Now(), For: []string{"aaaaaa"}}
	d.retireRoutesLocked(b, t3Route{CLI: "/t3", Thread: "thread-x"})
	d.filesFetchedLocked(e.ID)
	if len(d.st.Inbox["bbbbbb"]) != 1 || len(d.st.Inbox["aaaaaa"]) != 0 {
		t.Errorf("bbbbbb has %d, aaaaaa has %d; want the message with bbbbbb, which took the thread",
			len(d.st.Inbox["bbbbbb"]), len(d.st.Inbox["aaaaaa"]))
	}
}

// A recipient whose T3 thread settled while its files came is offline, and isn't given the message,
// as one that came for it then would be refused.
func TestFilesThatArriveForASettledThreadAreNotQueued(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	d.st.Inbox, d.st.FilesWait = map[string][]*inMsg{}, map[string]*fileWait{}
	inT3(d, "bbbbbb", 0)
	d.threads[threadKey{"", "thread-bbbbbb"}] = t3db.State{Settled: true}
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/cccccc", To: "dev/bbbbbb",
		Subject: "a file", At: time.Now(), Files: []bus.File{{Name: "a.txt"}}}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.FilesWait[e.ID] = &fileWait{Event: e, Since: time.Now(), For: []string{"bbbbbb"}}
	d.filesFetchedLocked(e.ID)
	if n := len(d.st.Inbox["bbbbbb"]); n != 0 || len(d.st.FilesWait) != 0 {
		t.Errorf("a settled thread was given %d, %d still waiting; want none and none", n, len(d.st.FilesWait))
	}
}
