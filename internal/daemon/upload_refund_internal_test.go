package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// Which failed uploads are known to have left nothing on the remote (#788): one refused, one
// never sent, and one the remote, asked, doesn't list. One cut off at its time may land yet, and
// isn't asked about.
func TestWhichFailedUploadsAreLost(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	ctx := context.Background()
	gone, there := bus.NewFilesAt(), pushFilesRef(t, d)
	for _, c := range []struct {
		err  error
		at   string
		lost bool
	}{
		{fmt.Errorf("push %w: hook declined", bus.ErrRefused), there, true},
		{bus.ErrRejected, there, true},
		{ErrNoConsent, there, true},
		{ErrInvisible, there, true},
		{fmt.Errorf("%w: signal: killed", context.DeadlineExceeded), gone, false},
		{context.Canceled, gone, false},
		{errors.New("the connection dropped"), gone, true},
		{errors.New("the connection dropped"), there, false},
	} {
		if got := d.uploadLost(ctx, c.at, c.err); got != c.lost {
			t.Errorf("%v: lost %v, want %v", c.err, got, c.lost)
		}
	}
	stopped, stop := context.WithCancel(ctx)
	stop()
	if d.uploadLost(stopped, gone, errors.New("killed")) {
		t.Error("a push the daemon's stop cut off counts as lost")
	}
}

// The prune takes a push that was cut off, and never landed, off the day's count (#788), and
// leaves one that did land counted.
func TestThePruneRefundsAnUploadThatNeverLanded(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{FileKeep: time.Hour}, false)
	gone, there := bus.NewFilesAt(), pushFilesRef(t, d)
	due := time.Now().Add(-2 * time.Hour)
	d.mu.Lock()
	d.st.FileRefs = map[string]time.Time{gone: due, there: due}
	d.st.Uploads = []fileUpload{{At: time.Now(), Bytes: 5000, Ref: gone}, {At: time.Now(), Bytes: 7000, Ref: there}, {At: time.Now(), Bytes: 9000}}
	d.mu.Unlock()
	d.pruneFiles(context.Background())
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.st.Uploads) != 2 || d.st.Uploads[0].Ref != there || d.st.Uploads[1].Bytes != 9000 {
		t.Errorf("the day's uploads after the prune: %+v", d.st.Uploads)
	}
	if len(d.st.FileRefs) != 0 {
		t.Errorf("the files refs left to delete: %v", d.st.FileRefs)
	}
}

// pushFilesRef pushes a files ref from d and returns its name.
func pushFilesRef(t *testing.T, d *Daemon) string {
	t.Helper()
	ctx := context.Background()
	pack, err := bus.PackFiles([][]byte{[]byte("sealed")})
	if err != nil {
		t.Fatal(err)
	}
	repo, done, err := d.scratch()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	sha, err := repo.NewFilesCommit(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	at := bus.NewFilesAt()
	if err := d.pushFrom(ctx, repo, sha, bus.FilesRef(at), bus.FastForward, ""); err != nil {
		t.Fatal(err)
	}
	return at
}
