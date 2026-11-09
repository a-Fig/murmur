package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// giveUpRig is a daemon driven one sync at a time, so a test can hold a ref's fetch failing and
// move the clock past catchUpLimit without waiting for it.
type giveUpRig struct {
	t            *testing.T
	ctx          context.Context
	remote, work string
	// author is where a peer's commits are made, so the daemon's clone has only what it fetches.
	author      string
	gitDir, dir string
	d           *Daemon
}

const (
	giveUpPeer = "far-0001"
	giveUpRef  = brand.MeshPrefix + giveUpPeer
)

func newGiveUpRig(t *testing.T) *giveUpRig {
	t.Helper()
	root := t.TempDir()
	r := &giveUpRig{t: t, ctx: context.Background(), remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work"),
		author: filepath.Join(root, "author")}
	r.git(root, "init", "-q", "--bare", "--initial-branch=main", r.remote)
	r.git(root, "init", "-q", "--initial-branch=main", r.work)
	r.git(root, "init", "-q", "--initial-branch=main", r.author)
	r.git(r.work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	r.git(r.work, "push", "-q", r.remote, "HEAD:refs/heads/main")
	r.git(r.work, "remote", "add", "origin", r.remote)
	r.gitDir = filepath.Join(r.work, ".git")
	r.dir = filepath.Join(r.gitDir, brand.StateDir)
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r.open()
	return r
}

func (r *giveUpRig) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// open starts a new daemon on the clone, as a restart does.
func (r *giveUpRig) open() {
	r.t.Helper()
	opt := Options{GitDir: r.gitDir, Log: log.New(io.Discard, "", 0), StoppedInbox: time.Hour}
	opt.defaults()
	d, err := open(r.ctx, opt, r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	r.d = d
}

// away makes the next daemon take what it finds for a backlog to skip, as after a long outage.
func (r *giveUpRig) away() {
	r.t.Helper()
	r.d.mu.Lock()
	r.d.st.LastSync = time.Now().Add(-2 * time.Hour)
	r.d.saveLocked()
	r.d.mu.Unlock()
	r.open()
}

// append writes an event onto the peer's log on the remote, returning the new tip. A parent of ""
// starts a new history, as a rewrite of the log does.
func (r *giveUpRig) append(parent string, e bus.Event) string {
	r.t.Helper()
	sha := r.commit(parent, e)
	r.git(r.author, "push", "-q", "--force", r.remote, sha+":"+giveUpRef)
	return sha
}

// commit makes the commit of an event without putting it on the remote.
func (r *giveUpRig) commit(parent string, e bus.Event) string {
	r.t.Helper()
	msg, err := bus.Encode(e, e.Line())
	if err != nil {
		r.t.Fatal(err)
	}
	tree := r.git(r.author, "hash-object", "-t", "tree", "-w", "--stdin")
	args := []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", tree, "-m", msg}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return r.git(r.author, args...)
}

func (r *giveUpRig) tagsEvent(at time.Time, remove ...string) bus.Event {
	return bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Tags, From: giveUpPeer + "/cli", At: at.UTC(),
		To: r.d.device + "/bbbbbb", Remove: remove}
}

func (r *giveUpRig) message(subject string) bus.Event {
	return bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: giveUpPeer + "/aaaaaa",
		At: time.Now().UTC(), To: "*", Subject: subject}
}

// failFetches holds a lock on the local copy of the peer's log, as another git would. It is fresh,
// so the daemon doesn't clear it. The returned func lets the next sync fetch the ref again.
func (r *giveUpRig) failFetches() (recovered func()) {
	r.t.Helper()
	lock := filepath.Join(r.gitDir, filepath.FromSlash(giveUpRef)+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		r.t.Helper()
		if err := os.Remove(lock); err != nil {
			r.t.Fatal(err)
		}
		r.d.mu.Lock()
		delete(r.d.unfetched, giveUpRef) // no minute's wait before the next try
		r.d.mu.Unlock()
	}
}

func (r *giveUpRig) sync() {
	r.t.Helper()
	if err := r.d.pollOnce(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

// giveUp syncs with the fetch failing, then past catchUpLimit, so the backlog is given up on.
func (r *giveUpRig) giveUp() {
	r.t.Helper()
	r.sync()
	if !r.d.backlog[giveUpRef] {
		r.t.Fatalf("the log isn't in the backlog: %v", r.d.backlog)
	}
	r.d.backlogAt = r.d.backlogAt.Add(-catchUpLimit - time.Minute)
	r.sync()
	if r.d.backlog[giveUpRef] {
		r.t.Fatal("the backlog wasn't given up on")
	}
}

// bobKnown gives this device a session called quiet-otter that has stopped and whose inbox is no
// longer kept, so a skip has nobody waiting for its messages: what a backlog's tags event is for.
func (r *giveUpRig) bobKnown() {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.d.st.SessionNames["bbbbbb"] = "quiet-otter"
	r.d.st.Stopped["bbbbbb"] = time.Now().Add(-2 * r.d.opt.StoppedInbox)
	r.d.saveLocked()
}

// off says a human's mark has taken tag off the session. A mark is only left on a tag the session
// has, so bobHas sets it first.
func (r *giveUpRig) off(tag string) bool {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	st := r.d.st.Tags["quiet-otter"]
	if st == nil {
		return false
	}
	m, ok := st.Human[tag]
	return ok && !m.On
}

func (r *giveUpRig) bobHas(tag ...string) {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	r.d.putTagStateLocked("quiet-otter", &tags.State{Self: tag})
}

// queued is the subjects read as new: in the CLI's inbox, or held for sessions reconnecting, which is
// where a catch-up alone puts them.
func (r *giveUpRig) queued() []string {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	var got []string
	for _, m := range r.d.st.Inbox[proto.CLISession] {
		got = append(got, m.Event.Subject)
	}
	for _, e := range r.d.pending {
		got = append(got, e.Subject)
	}
	slices.Sort(got)
	return slices.Compact(got)
}

// A human takes a tag off a session in a stretch of a log whose fetch then fails for longer than
// catchUpLimit (#237). The daemon gives up on the stretch's messages, not on the tags event: that is
// applied once the fetch lands, and what the log gained since the give-up is read as usual. Whether
// the device has read the log before or not, and across a restart while the fetch still fails.
func TestAGivenUpBacklogStillAppliesItsTagChanges(t *testing.T) {
	for _, c := range []struct {
		name             string
		readBefore       bool
		restartMeanwhile bool
	}{
		{name: "a log read before", readBefore: true},
		{name: "a log new to the device"},
		{name: "a restart while the fetch fails", readBefore: true, restartMeanwhile: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			r.bobKnown()
			r.bobHas("do-not-disturb")
			read := ""
			if c.readBefore {
				read = r.append("", r.message("read before"))
				r.sync()
				if got := r.d.st.Processed[giveUpRef]; got != read {
					t.Fatalf("processed %q, want %q", got, read)
				}
			}
			r.away()

			tip := r.append(read, r.message("given up on"))
			tip = r.append(tip, r.tagsEvent(time.Now(), "do-not-disturb"))
			recovered := r.failFetches()
			r.giveUp()
			if r.off("do-not-disturb") {
				t.Fatal("the change was applied with the log not fetched")
			}
			if c.restartMeanwhile {
				r.open()
				r.sync()
				if from, ok := r.d.st.TagsFrom[giveUpRef]; !ok || from != read {
					t.Fatalf("after the restart the cursor is %q, %v, want %q", from, ok, read)
				}
			}

			r.append(tip, r.message("sent since"))
			recovered()
			r.sync()

			if !r.off("do-not-disturb") {
				t.Error("the tag change in the backlog wasn't applied")
			}
			if got := r.queued(); !slices.Equal(got, []string{"sent since"}) {
				t.Errorf("queued %q, want only what was sent since the give-up", got)
			}
			if _, ok := r.d.st.TagsFrom[giveUpRef]; ok {
				t.Error("the cursor is still there after its tags events were read")
			}
		})
	}
}

// A log rewritten after the give-up has no path from what was read to its new tip. It is treated as
// any rewritten log is: its history isn't replayed, tags included, and it goes on being read.
func TestAGivenUpBacklogRewrittenMeanwhileIsNotReplayed(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	read := r.append("", r.message("read before"))
	r.sync()
	r.away()

	r.append(read, r.message("given up on"))
	recovered := r.failFetches()
	r.giveUp()

	tip := r.append("", r.tagsEvent(time.Now(), "do-not-disturb"))
	recovered()
	r.sync()
	if r.off("do-not-disturb") {
		t.Error("a rewritten log's history was replayed")
	}
	if _, ok := r.d.st.TagsFrom[giveUpRef]; ok {
		t.Error("the cursor is still there")
	}
	r.append(tip, r.message("after the rewrite"))
	r.sync()
	if got := r.queued(); !slices.Equal(got, []string{"after the rewrite"}) {
		t.Errorf("queued %q, want what came after the rewrite", got)
	}
}

// The same when the fetch failed before it brought the tip given up on, which failFetches can't show:
// git downloads the objects before it takes the ref's lock. With the listing at a tip that doesn't
// hold what was given up on, the log was rewritten, so nothing in the new history is replayed, on a
// log new to this device or one sharing history with what it read.
func TestAGivenUpTipThatNeverCameIsARewrite(t *testing.T) {
	for _, c := range []struct {
		name   string
		shared bool
	}{{name: "a log new to the device"}, {name: "a log with history in common", shared: true}} {
		t.Run(c.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			r.bobKnown()
			r.bobHas("do-not-disturb")
			read := ""
			if c.shared {
				read = r.append("", r.message("read before"))
				r.sync()
			}
			gone := r.commit(read, r.message("never fetched")) // on no ref of the remote
			tip := r.append(read, r.tagsEvent(time.Now(), "do-not-disturb"))
			r.d.mu.Lock()
			r.d.st.Processed[giveUpRef], r.d.st.TagsFrom[giveUpRef] = gone, read
			r.d.mu.Unlock()

			r.sync()
			if r.off("do-not-disturb") {
				t.Error("a tag change from the rewritten log's new history was applied")
			}
			if _, ok := r.d.st.TagsFrom[giveUpRef]; ok {
				t.Error("the cursor is still there")
			}
			r.append(tip, r.message("after the rewrite"))
			r.sync()
			if got := r.queued(); !slices.Equal(got, []string{"after the rewrite"}) {
				t.Errorf("queued %q, want what came after the rewrite", got)
			}
		})
	}
}

// A fetch that started before the give-up can land on a tip older than the one given up on, as a slow
// one does. The listing still shows the later tip, so this is no rewrite: the log waits for that tip,
// with nothing moved back, and its tags events are then applied and its messages still passed over.
func TestAGivenUpBacklogWaitsWhenAnOlderFetchLands(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	read := r.append("", r.message("read before"))
	r.sync()
	older := r.append(read, r.tagsEvent(time.Now(), "do-not-disturb"))
	if err := r.d.repo.Fetch(r.ctx, []string{giveUpRef}); err != nil {
		t.Fatal(err)
	}
	tip := r.append(older, r.message("given up on"))
	r.d.mu.Lock()
	r.d.st.Processed[giveUpRef], r.d.st.TagsFrom[giveUpRef] = tip, read
	r.d.mu.Unlock()

	r.d.advMu.Lock()
	r.d.advanceMesh(r.ctx, giveUpRef, older, tip)
	r.d.advMu.Unlock()
	if got := r.d.st.Processed[giveUpRef]; got != tip {
		t.Errorf("processed %s, want the tip given up on, %s", got, tip)
	}
	if _, ok := r.d.st.TagsFrom[giveUpRef]; !ok {
		t.Error("the cursor went before the tip given up on came")
	}

	r.sync()
	if !r.off("do-not-disturb") {
		t.Error("the tag change wasn't applied once the tip came")
	}
	if got := r.queued(); len(got) != 0 {
		t.Errorf("queued %q from what was given up on", got)
	}
}

// A fetch can also land past the listing, when the peer writes between the two. That is no older
// fetch: it holds the listed tip, so the rewrite is settled, and a peer that keeps writing doesn't
// keep its log waiting.
func TestAGivenUpTipThatNeverCameWithAFetchPastTheListing(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb")
	read := r.append("", r.message("read before"))
	r.sync()
	gone := r.commit(read, r.message("never fetched"))
	listed := r.append(read, r.tagsEvent(time.Now(), "do-not-disturb"))
	past := r.append(listed, r.message("written since the listing"))
	if err := r.d.repo.Fetch(r.ctx, []string{giveUpRef}); err != nil {
		t.Fatal(err)
	}
	r.d.mu.Lock()
	r.d.st.Processed[giveUpRef], r.d.st.TagsFrom[giveUpRef] = gone, read
	r.d.mu.Unlock()

	r.d.advMu.Lock()
	r.d.advanceMesh(r.ctx, giveUpRef, past, listed)
	r.d.advMu.Unlock()
	if _, ok := r.d.st.TagsFrom[giveUpRef]; ok {
		t.Error("the log is still waiting for a tip that is gone")
	}
	if got := r.d.st.Processed[giveUpRef]; got != past {
		t.Errorf("processed %s, want the fetched tip, %s", got, past)
	}
	if r.off("do-not-disturb") {
		t.Error("a tag change from the rewritten log's new history was applied")
	}
}

// With no session here and none it knows of, as on a new clone, nothing in the log can be for this
// device, so a give-up keeps no cursor and a log of any length costs nothing later.
func TestAGivenUpBacklogKeepsNoCursorWithNoSession(t *testing.T) {
	r := newGiveUpRig(t)
	r.append("", r.tagsEvent(time.Now(), "do-not-disturb"))
	recovered := r.failFetches()
	r.giveUp()
	if from, ok := r.d.st.TagsFrom[giveUpRef]; ok {
		t.Errorf("a cursor %q was kept with no session to apply it to", from)
	}
	recovered()
	r.sync()
}

// What junk hid when this device first looked is history for tags as for messages (see
// state.Junked), and a give-up doesn't change that: only a change made after that look is applied.
func TestAGivenUpBacklogKeepsWhatJunkHid(t *testing.T) {
	r := newGiveUpRig(t)
	r.bobKnown()
	r.bobHas("do-not-disturb", "release")
	first := time.Now()
	r.d.mu.Lock()
	r.d.st.Junked[giveUpRef] = first
	r.d.saveLocked()
	r.d.mu.Unlock()
	r.away()

	tip := r.append("", r.tagsEvent(first.Add(-time.Hour), "release"))
	r.append(tip, r.tagsEvent(first, "do-not-disturb"))
	recovered := r.failFetches()
	r.giveUp()
	if _, ok := r.d.st.Junked[giveUpRef]; !ok {
		t.Fatal("the give-up forgot what junk hid before its tags events were read")
	}
	recovered()
	r.sync()
	if r.off("release") {
		t.Error("a change from before the first look was applied")
	}
	if !r.off("do-not-disturb") {
		t.Error("a change from after the first look wasn't applied")
	}
	if _, ok := r.d.st.Junked[giveUpRef]; ok {
		t.Error("the log is still taken for junked after it was read")
	}
}
