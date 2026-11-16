package daemon

import (
	"bytes"
	"context"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/web"
)

// An invisible device's prune passes over the remote without a word (#789): each delete was
// refused, a line in the log for every files ref at every prune. It is noted as owed, and
// leaving invisible wakes the files loop for it. The refs of other devices age meanwhile.
func TestAnInvisiblePruneIsQuietAndOwed(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	d := &Daemon{dir: t.TempDir(), log: log.New(&out, "", 0), device: "here-0001", started: time.Now(),
		opt: Options{FileKeep: time.Hour, FilePrune: time.Hour}, fileKick: make(chan struct{}, 1), pushOne: make(chan struct{}, 1),
		sessions: map[string]*session{},
		st: state{FileRefs: map[string]time.Time{"aaaa": time.Now().Add(-2 * time.Hour)},
			OthersFiles: map[string]othersFile{"bbbb": {Device: "peer-0001"}}}}
	d.hidden.Store(true)
	d.pruneFiles(context.Background())
	if out.Len() != 0 {
		t.Errorf("an invisible device's prune logged: %s", out.String())
	}
	d.pruneFiles(context.Background())
	if age := d.st.OthersFiles["bbbb"].Age; age <= 0 {
		t.Errorf("another device's files ref didn't age while this one was invisible: %v", age)
	}
	if _, kept := d.st.FileRefs["aaaa"]; !kept || !d.pruneOwed.Load() {
		t.Errorf("the ref kept: %v, a prune owed: %v", kept, d.pruneOwed.Load())
	}
	d.setMode(web.ModeInvisible, web.ModeOnline, time.Now())
	select {
	case <-d.fileKick:
	default:
		t.Error("going back online didn't wake the files loop")
	}
}
