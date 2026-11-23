package daemon

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// A Pi file's recorded ID (PiFiles, #1145) is forgotten sessionNameKeep after its last use, once no
// session runs or has its inbox kept at that ID, nor at the ID the file makes, which a session
// that left the file may still have. While either does, it is kept: forgotten, the file's server
// would register at the ID the file makes, the session that left it.
func TestAPiFilesRecordIsForgottenOnceNeitherIDIsInUse(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-sessionNameKeep - time.Hour)
	const (
		gone, recent = "/s/gone.jsonl", "/s/recent.jsonl"
		runs, kept   = "/s/runs.jsonl", "/s/kept.jsonl"
		leftRuns     = "/s/left-runs.jsonl"
		leftKept     = "/s/left-kept.jsonl"
	)
	d := &Daemon{device: "here-0001",
		sessions: map[string]*session{"runs01": {id: "runs01"}, proto.NativeID(leftRuns): {id: proto.NativeID(leftRuns)}},
		st: state{
			PiFiles: map[string]piFile{
				gone:     {ID: "gone01", At: long},
				recent:   {ID: "recnt1", At: now.Add(-time.Hour)},
				runs:     {ID: "runs01", At: long},
				kept:     {ID: "kept01", At: long},
				leftRuns: {ID: "fresh1", At: long},
				leftKept: {ID: "fresh2", At: long},
			},
			Stopped: map[string]time.Time{"kept01": now, proto.NativeID(leftKept): now},
		}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pruneSessionNamesLocked(now) {
		t.Fatal("nothing was forgotten")
	}
	for file, want := range map[string]bool{gone: false, recent: true, runs: true, kept: true, leftRuns: true, leftKept: true} {
		if _, ok := d.st.PiFiles[file]; ok != want {
			t.Errorf("%s kept: %v, want %v", file, ok, want)
		}
	}
	delete(d.sessions, "runs01")
	delete(d.sessions, proto.NativeID(leftRuns))
	d.st.Stopped = nil
	if !d.pruneSessionNamesLocked(now) || len(d.st.PiFiles) != 1 {
		t.Errorf("with nothing at either ID, left: %v", d.st.PiFiles)
	}
}
