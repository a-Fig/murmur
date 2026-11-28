package daemon

import (
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
)

// keptRig is a daemon with a message in an inbox that came while its files were kept, with
// Receive files on for agents and the mesh owner's policy keeping files.
func keptRig(t *testing.T) (*Daemon, bus.Event) {
	d := &Daemon{repoFiles: &fileSwitch{}, dir: t.TempDir(), device: "dev-0001", savesHeld: true}
	d.agentRecv.on.Store(true)
	d.st.TeamOwnerChecked = true
	d.st.TeamOwner = &meshOwner{Policy: bus.RepoPolicy{AllowFiles: false}}
	d.st.FilesWait = map[string]*fileWait{}
	e := bus.Event{V: bus.Version, ID: "m1", Type: bus.Message, From: "peer-0001/aaaaaa", To: "dev-0001/bbbbbb",
		FilesAt: "files-1", Files: []bus.File{{Name: "build.log", Size: 7, Sum: "00", Key: "k"}}}
	d.st.Inbox = map[string][]*inMsg{"bbbbbb": {{Event: e}}}
	d.refreshRepoFilesLocked()
	if len(d.st.FilesWait) != 0 {
		t.Fatalf("fetching while the policy keeps files: %v", d.st.FilesWait)
	}
	return d, e
}

// The mesh owner's policy letting files in again fetches the files of the messages that came while
// it kept them, as Receive files turned on does, and until they are here a session is told where
// they will be saved (#1316).
func TestTheOwnerLettingFilesInFetchesWhatCameWithoutThem(t *testing.T) {
	d, e := keptRig(t)
	d.st.TeamOwner.Policy.AllowFiles = true
	d.refreshRepoFilesLocked()
	if d.st.FilesWait[e.ID] == nil {
		t.Fatal("the message's files weren't sent for")
	}
	views := d.fileViewsLocked(e)
	want := filepath.Join(d.filesDir(e), "build.log")
	if len(views) != 1 || views[0].Path != want || !views[0].Fetching {
		t.Errorf("the file on its way: %+v, want it on its way to %s", views, want)
	}
}
