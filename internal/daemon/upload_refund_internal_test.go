package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// Which failed uploads are known to have left nothing on the remote (#788): one never sent, and
// one the remote, asked, doesn't list, a refusal included, as a push that landed can read as
// one. One cut off at its time may land yet, and isn't asked about.
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
		{fmt.Errorf("push %w: hook declined", bus.ErrRefused), gone, true},
		{fmt.Errorf("push %w: remote failed to report status", bus.ErrRefused), there, false},
		{bus.ErrRejected, gone, true},
		{bus.ErrRejected, there, false},
		{ErrNoConsent, there, true},
		{ErrInvisible, there, true},
		{notPushedError{context.DeadlineExceeded}, there, true},
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
	// A push whose time had run out before it began says so, and that it was never sent.
	late, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	<-late.Done()
	if _, err := tryFilesRef(t, d, late); !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, new(notPushedError)) {
		t.Errorf("a push out of time ended with %v", err)
	}
}

// The prune takes a push that was cut off, and never landed, off the day's count (#788). It
// leaves one that did land counted, and one cut off too lately to tell for the next prune.
func TestThePruneRefundsAnUploadThatNeverLanded(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{FileKeep: time.Hour}, false)
	gone, there, fresh := bus.NewFilesAt(), pushFilesRef(t, d), bus.NewFilesAt()
	due, then := time.Now().Add(-2*time.Hour), time.Now().Add(-2*uploadSettle)
	d.mu.Lock()
	d.st.FileRefs = map[string]time.Time{gone: due, there: due, fresh: due}
	d.st.Uploads = []fileUpload{{At: then, Bytes: 5000, Ref: gone}, {At: then, Bytes: 7000, Ref: there},
		{At: time.Now(), Bytes: 8000, Ref: fresh}, {At: then, Bytes: 9000}}
	d.mu.Unlock()
	d.pruneFiles(context.Background())
	d.mu.Lock()
	defer d.mu.Unlock()
	if u := d.st.Uploads; len(u) != 3 || u[0].Ref != there || u[1].Ref != fresh || u[2].Bytes != 9000 {
		t.Errorf("the day's uploads after the prune: %+v", u)
	}
	if _, left := d.st.FileRefs[fresh]; len(d.st.FileRefs) != 1 || !left {
		t.Errorf("the files refs left to delete: %v", d.st.FileRefs)
	}
}

// pushFilesRef pushes a files ref from d and returns its name.
func pushFilesRef(t *testing.T, d *Daemon) string {
	t.Helper()
	at, err := tryFilesRef(t, d, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// tryFilesRef pushes a files ref from d within pushCtx, and returns its name and how the push ended.
func tryFilesRef(t *testing.T, d *Daemon, pushCtx context.Context) (string, error) {
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
	return at, d.pushFrom(pushCtx, repo, sha, bus.FilesRef(at), bus.FastForward, "", nil)
}
