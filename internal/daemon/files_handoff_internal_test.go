package daemon

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
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
