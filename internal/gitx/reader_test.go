package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// readerRepo is a bare repository with a commit in it, and a count of the gits started through
// this package since.
func readerRepo(t *testing.T) (g Git, commit string, starts *atomic.Int32) {
	t.Helper()
	g = Git{Dir: t.TempDir()}
	git(t, g, "", "init", "-q", "--bare", g.Dir)
	commit = commitOn(t, g, "", "first\n\nbody\n")
	starts = &atomic.Int32{}
	was := Observe
	Observe = func(string, time.Time, time.Duration) { starts.Add(1) }
	t.Cleanup(func() { Observe = was })
	return g, commit, starts
}

func git(t *testing.T, g Git, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + g.Dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func commitOn(t *testing.T, g Git, parent, message string) string {
	t.Helper()
	tree := git(t, g, "", "hash-object", "-t", "tree", "-w", "--stdin")
	args := []string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return git(t, g, message, append(args, "-F", "-")...)
}

func keep(t *testing.T, g Git, linger time.Duration) *Reader {
	t.Helper()
	r := g.NewReader(linger)
	t.Cleanup(r.Close) // before the directory goes, which a git still running would hold
	return r
}

// A Reader says what an object is and how large, reads it byte for byte, and says so when nothing
// has the name. However many questions come, one git answers them.
func TestReaderAnswersManyQuestionsWithOneGit(t *testing.T) {
	g, commit, starts := readerRepo(t)
	tree := git(t, g, "", "hash-object", "-t", "tree", "-w", "--stdin")
	absent := strings.Repeat("0", len(commit)-1) + "1"
	r := keep(t, g, time.Minute)
	ctx := context.Background()

	objs, err := r.Info(ctx, []string{commit, absent, tree})
	if err != nil {
		t.Fatal(err)
	}
	want := git(t, g, "", "cat-file", "commit", commit) + "\n"
	if o := objs[0]; o.Name != commit || o.Type != "commit" || o.Size != int64(len(want)) || o.Missing || o.Data != nil {
		t.Errorf("the commit: %+v, want a commit of %d bytes", o, len(want))
	}
	if o := objs[1]; o.Name != absent || !o.Missing || o.Type != "" {
		t.Errorf("a name nothing has: %+v", o)
	}
	if o := objs[2]; o.Type != "tree" || o.Size != 0 || o.Missing {
		t.Errorf("the empty tree: %+v", o)
	}

	for range 20 {
		objs, err = r.Contents(ctx, []string{commit, tree}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if string(objs[0].Data) != want || objs[0].Type != "commit" || len(objs[1].Data) != 0 {
			t.Fatalf("read %q and %q, want %q and nothing", objs[0].Data, objs[1].Data, want)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Errorf("%d gits started, want 1", n)
	}
	// "Missing" from a git that has run a while is put to a new one before it is believed.
	if objs, err = r.Info(ctx, []string{absent, commit}); err != nil || !objs[0].Missing || objs[1].Type != "commit" {
		t.Errorf("a name nothing has, asked later: %+v, %v", objs, err)
	}
	if n := starts.Load(); n != 2 {
		t.Errorf("%d gits started, want 2: one to confirm what was missing", n)
	}
}

// Many names in one question are more than the pipes to git hold: writing them all before reading
// any answer would stop both sides.
func TestReaderTakesMoreNamesThanAPipeHolds(t *testing.T) {
	g, commit, _ := readerRepo(t)
	r := keep(t, g, time.Minute)
	names := make([]string, 5000)
	for i := range names {
		names[i] = commit
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	objs, err := r.Contents(ctx, names, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != len(names) || !strings.HasSuffix(string(objs[len(objs)-1].Data), "first\n\nbody\n") {
		t.Errorf("read %d objects, want %d", len(objs), len(names))
	}
}

// What arrives after the git started is there for it: a loose object, and one in a pack that wasn't
// there. A git that didn't look again would call each missing, and a caller would build over it.
func TestReaderSeesWhatArrivesAfterItStarted(t *testing.T) {
	g, commit, starts := readerRepo(t)
	r := keep(t, g, time.Minute)
	ctx := context.Background()
	if _, err := r.Info(ctx, []string{commit}); err != nil {
		t.Fatal(err)
	}

	loose := commitOn(t, g, commit, "second\n")
	if objs, err := r.Info(ctx, []string{loose}); err != nil || objs[0].Type != "commit" {
		t.Fatalf("a commit written since: %+v, %v", objs, err)
	}

	packed := commitOn(t, g, loose, "third\n")
	git(t, g, "", "update-ref", "refs/heads/main", packed)
	git(t, g, "", "repack", "-a", "-d", "-q") // every object into one new pack, and no loose ones
	git(t, g, "", "prune-packed")
	if objs, err := r.Contents(ctx, []string{packed, commit}, 1<<20); err != nil || objs[0].Type != "commit" || objs[1].Type != "commit" {
		t.Fatalf("commits packed since: %+v, %v", objs, err)
	}
	if n := starts.Load(); n != 1 {
		t.Errorf("%d gits started, want the one: nothing was called missing", n)
	}
}

// A name that could be read as anything but an object's is never sent: git reads a line as a
// command.
func TestReaderTakesOnlyObjectNames(t *testing.T) {
	g, commit, starts := readerRepo(t)
	r := keep(t, g, time.Minute)
	for _, name := range []string{"HEAD", "", commit + "\ninfo " + commit, commit[:10], strings.ToUpper(commit), commit + " "} {
		if _, err := r.Info(context.Background(), []string{commit, name}); !errors.Is(err, ErrNoReader) {
			t.Errorf("%q: %v, want ErrNoReader", name, err)
		}
	}
	if n := starts.Load(); n != 0 {
		t.Errorf("%d gits started, want none", n)
	}
}

// An object over the limit isn't read into memory, and the Reader goes on answering.
func TestReaderRefusesAnObjectOverTheLimit(t *testing.T) {
	g, commit, _ := readerRepo(t)
	big := commitOn(t, g, commit, strings.Repeat("x", 100<<10))
	r := keep(t, g, time.Minute)
	ctx := context.Background()
	if objs, err := r.Contents(ctx, []string{commit, big}, 4<<10); err == nil {
		t.Fatalf("read %d objects, want an error", len(objs))
	}
	if objs, err := r.Contents(ctx, []string{commit}, 4<<10); err != nil || objs[0].Type != "commit" {
		t.Errorf("after refusing one: %+v, %v", objs, err)
	}
}

// A question whose context has ended gets that as its answer, and the next question is answered.
func TestReaderStopsWhenItsContextEnds(t *testing.T) {
	g, commit, _ := readerRepo(t)
	r := keep(t, g, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Info(ctx, []string{commit}); !errors.Is(err, context.Canceled) {
		t.Errorf("with its context ended: %v", err)
	}
	if objs, err := r.Info(context.Background(), []string{commit}); err != nil || objs[0].Type != "commit" {
		t.Errorf("the question after: %+v, %v", objs, err)
	}
}

// A git that died between questions is replaced, and the question is answered.
func TestReaderReplacesAGitThatDied(t *testing.T) {
	g, commit, starts := readerRepo(t)
	r := keep(t, g, time.Minute)
	ctx := context.Background()
	if _, err := r.Info(ctx, []string{commit}); err != nil {
		t.Fatal(err)
	}
	r.turn <- struct{}{}
	p := r.p
	<-r.turn
	p.cancel()
	<-p.done
	if objs, err := r.Info(ctx, []string{commit}); err != nil || objs[0].Type != "commit" {
		t.Errorf("after its git died: %+v, %v", objs, err)
	}
	if n := starts.Load(); n != 2 {
		t.Errorf("%d gits started, want 2", n)
	}
}

// The git goes once no question has come for the linger, and the next question starts another.
// Close ends it at once: the repository can be deleted as soon as it returns, which on Windows a
// process with a file of it open would stop.
func TestReaderEndsItsGitWhenIdleAndWhenClosed(t *testing.T) {
	g, commit, starts := readerRepo(t)
	r := keep(t, g, 50*time.Millisecond)
	ctx := context.Background()
	if _, err := r.Info(ctx, []string{commit}); err != nil {
		t.Fatal(err)
	}
	for began := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		r.turn <- struct{}{}
		kept := r.p != nil
		<-r.turn
		if !kept {
			break
		}
		if time.Since(began) > 30*time.Second {
			t.Fatal("the git was still kept long after the linger")
		}
	}
	if _, err := r.Info(ctx, []string{commit}); err != nil {
		t.Fatal(err)
	}
	if n := starts.Load(); n != 2 {
		t.Errorf("%d gits started, want 2", n)
	}

	// Packed, so that the git holds a file open.
	git(t, g, "", "update-ref", "refs/heads/main", commit)
	git(t, g, "", "repack", "-a", "-d", "-q")
	long := keep(t, g, time.Hour)
	if _, err := long.Contents(ctx, []string{commit}, 1<<20); err != nil {
		t.Fatal(err)
	}
	r.Close()
	long.Close()
	if err := os.RemoveAll(filepath.Join(g.Dir, "objects")); err != nil {
		t.Errorf("deleting the repository after Close: %v", err)
	}
	if _, err := long.Info(ctx, []string{commit}); !errors.Is(err, ErrNoReader) {
		t.Errorf("a closed Reader answered %v, want ErrNoReader", err)
	}
}

// A git from before 2.36 has no --batch-command: it prints its usage and exits. The Reader says so
// once and for good, without starting a git for every question after.
func TestReaderOnAGitWithoutBatchCommand(t *testing.T) {
	g, commit, starts := readerRepo(t)
	was := readerArgs
	readerArgs = []string{"cat-file", "--batch-command-of-a-later-git"}
	t.Cleanup(func() { readerArgs = was })
	r := keep(t, g, time.Minute)
	for range 3 {
		if _, err := r.Info(context.Background(), []string{commit}); !errors.Is(err, ErrNoReader) {
			t.Fatalf("%v, want ErrNoReader", err)
		}
	}
	if n := starts.Load(); n != 1 {
		t.Errorf("%d gits started, want 1", n)
	}
}

// A repository that isn't there is an error, not a git that lacks the command: the caller mustn't
// take it for "read another way" and then for "not a commit".
func TestReaderOnABrokenRepository(t *testing.T) {
	g, commit, _ := readerRepo(t)
	r := keep(t, Git{Dir: filepath.Join(g.Dir, "nowhere")}, time.Minute)
	_, err := r.Info(context.Background(), []string{commit})
	var gerr *Error
	if err == nil || errors.Is(err, ErrNoReader) || !errors.As(err, &gerr) {
		t.Errorf("%v, want git's own error", err)
	}
}

// A question waits its turn only as long as its own context lasts: one with a short deadline isn't
// held behind another's stalled git.
func TestReaderQuestionGivesUpItsTurnWhenItsContextEnds(t *testing.T) {
	g, commit, _ := readerRepo(t)
	r := keep(t, g, time.Minute)
	r.turn <- struct{}{} // another question's turn, which doesn't end
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Info(ctx, []string{commit})
	<-r.turn
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%v, want its deadline", err)
	}
	if objs, err := r.Info(context.Background(), []string{commit}); err != nil || objs[0].Type != "commit" {
		t.Errorf("the question after: %+v, %v", objs, err)
	}
}

// Close waits for a git that was already told to go, by the linger or to confirm a missing object,
// as well as the one kept: neither may outlive it.
func TestReaderCloseWaitsForAGitAlreadyToldToGo(t *testing.T) {
	g, commit, _ := readerRepo(t)
	r := keep(t, g, time.Minute)
	ctx := context.Background()
	if _, err := r.Info(ctx, []string{commit}); err != nil {
		t.Fatal(err)
	}
	r.turn <- struct{}{}
	first := r.p
	<-r.turn
	// Asking for what isn't here retires the first git and starts a second.
	if _, err := r.Info(ctx, []string{strings.Repeat("0", len(commit)-1) + "1"}); err != nil {
		t.Fatal(err)
	}
	r.turn <- struct{}{}
	second := r.p
	<-r.turn
	if second == nil || second == first {
		t.Fatal("no second git was started to confirm what was missing")
	}
	r.Close()
	for i, p := range []*readerProc{first, second} {
		select {
		case <-p.done:
		default:
			t.Errorf("git %d was still running when Close returned", i+1)
		}
	}
}
