package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// refsRepo is readerRepo with the ref storage named: "files", or "reftable" where this git has it.
// The files kind is asked for through the environment, which a git from before the choice (2.45)
// ignores, where it would refuse --ref-format; a git whose own default is reftable heeds it.
func refsRepo(t *testing.T, format string) (g Git, commit string, starts *atomic.Int32) {
	t.Helper()
	g = Git{Dir: t.TempDir()}
	args := []string{"init", "-q", "--bare"}
	if format != "files" {
		args = append(args, "--ref-format="+format)
	}
	cmd := exec.Command("git", append(args, g.Dir)...)
	if format == "files" {
		cmd.Env = append(os.Environ(), "GIT_DEFAULT_REF_FORMAT=files")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if format != "files" {
			t.Skipf("this git has no %s refs: %s", format, strings.TrimSpace(string(out)))
		}
		t.Fatalf("git init: %v: %s", err, out)
	}
	commit = commitOn(t, g, "", "first\n")
	starts = &atomic.Int32{}
	was := Observe
	Observe = func(string, time.Time, time.Duration) { starts.Add(1) }
	t.Cleanup(func() { Observe = was })
	return g, commit, starts
}

// Refs says what each ref is at, and which aren't there, in the order asked.
func TestReaderRefsSaysWhatARefIsAt(t *testing.T) {
	g, commit, _ := readerRepo(t)
	second := commitOn(t, g, commit, "second\n")
	git(t, g, "", "update-ref", "refs/murmur/fetch-0123456789abcdef/mesh/device-1a2b3c4d", commit)
	git(t, g, "", "update-ref", "refs/murmur/fetch-0123456789abcdef/names", second)
	r := keep(t, g, time.Minute)
	names := []string{"refs/murmur/fetch-0123456789abcdef/names", "refs/murmur/fetch-0123456789abcdef/gone", "refs/murmur/fetch-0123456789abcdef/mesh/device-1a2b3c4d"}
	objs, err := r.Refs(context.Background(), names)
	if err != nil || len(objs) != 3 {
		t.Fatalf("%+v, %v", objs, err)
	}
	for i, want := range []Object{
		{Name: names[0], ID: second, Type: "commit"},
		{Name: names[1], Missing: true},
		{Name: names[2], ID: commit, Type: "commit"},
	} {
		if got := objs[i]; got.Name != want.Name || got.ID != want.ID || got.Type != want.Type || got.Missing != want.Missing {
			t.Errorf("%s: %+v, want %+v", names[i], got, want)
		}
	}
	if objs, err := r.Refs(context.Background(), nil); err != nil || len(objs) != 0 {
		t.Errorf("no names: %+v, %v", objs, err)
	}
}

// A git that has run a while finds a ref made after it started, also one made after the refs were
// packed, without another git being started: bus.Repo.Fetch asks for refs that a fetch has only
// just written. Held to for loose and packed refs, and for reftable where this git has it. A ref
// that has gone is called missing, which a new git confirms.
//
// The moves here are found too, each leaving a loose ref. A ref that lives only in packed-refs and
// moves there needn't be (Reader.Refs says why), which is why a Fetch never asks twice of a name.
func TestReaderSeesRefsMadeAfterItStarted(t *testing.T) {
	for _, format := range []string{"files", "reftable"} {
		t.Run(format, func(t *testing.T) {
			g, first, starts := refsRepo(t, format)
			second := commitOn(t, g, first, "second\n")
			third := commitOn(t, g, second, "third\n")
			r := keep(t, g, time.Minute)
			ctx := context.Background()
			if _, err := r.Info(ctx, []string{first}); err != nil { // the git is running from here on
				t.Fatal(err)
			}
			at := func(when, ref, want string) {
				t.Helper()
				objs, err := r.Refs(ctx, []string{ref})
				if err != nil || len(objs) != 1 || objs[0].Missing || objs[0].ID != want {
					t.Fatalf("%s: %s is %+v, %v; want it at %s", when, ref, objs, err, want)
				}
			}
			const a, b = "refs/murmur/fetch-00000000000000aa/names", "refs/murmur/fetch-00000000000000bb/mesh/device-1a2b3c4d"

			git(t, g, "", "update-ref", a, first)
			at("made since the git started", a, first)
			git(t, g, "", "update-ref", a, second)
			at("moved since", a, second)

			git(t, g, "", "pack-refs", "--all")
			at("packed since", a, second)
			git(t, g, "", "update-ref", b, first)
			at("made after the refs were packed", b, first)
			git(t, g, "", "update-ref", a, third)
			at("moved after it was packed", a, third)
			git(t, g, "", "pack-refs", "--all")
			git(t, g, "", "update-ref", b, third)
			at("moved after a second packing", b, third)

			if n := starts.Load(); n != 1 {
				t.Errorf("%d gits started, want the one that was kept to have answered every time", n)
			}

			git(t, g, "", "update-ref", "-d", a)
			objs, err := r.Refs(ctx, []string{a, b})
			if err != nil || len(objs) != 2 || !objs[0].Missing || objs[1].ID != third {
				t.Fatalf("after one ref was deleted: %+v, %v", objs, err)
			}
			if n := starts.Load(); n != 2 {
				t.Errorf("%d gits started, want 2: a new one confirms what the kept one called missing", n)
			}
		})
	}
}

// Only a full, plain ref name is sent to git: it reads a line as a command, and a name as a
// revision, with a syntax of its own.
func TestReaderRefsTakesOnlyPlainRefNames(t *testing.T) {
	g, commit, starts := readerRepo(t)
	git(t, g, "", "update-ref", "refs/heads/main", commit)
	r := keep(t, g, time.Minute)
	for _, name := range []string{
		"", "HEAD", "main", "heads/main", commit, "refs/", "refs", "refs//main", "refs/heads/", "refs/heads//main",
		"refs/heads/main ", " refs/heads/main", "refs/heads/ma in", "refs/heads/main\ninfo " + commit, "refs/heads/main\r",
		"refs/heads/main\x00", "refs/heads/main\t", "refs/heads/..", "refs/heads/a..b", "refs/heads/.hidden", "refs/heads/-x",
		"refs/heads/main.lock", "refs/heads/main^", "refs/heads/main~1", "refs/heads/main:x", "refs/heads/main@{1}",
		"refs/heads/ma*n", "refs/heads/ma?n", "refs/heads/ma[n", "refs/heads/ma\\n", "refs/heads/maïn", "--batch-all-objects",
		// As git describe's output ends. Git 2.53 answers the first two with the object the hex
		// names; the rest are refused for being of the same shape, which an older git may take.
		"refs/murmur/fetch-0123456789abcdef/mesh/evil-1-g" + commit[:7], "refs/heads/v1-0-gabcd", "refs/heads/x-gABCDEF0", "refs/heads/a-g1",
		"refs/" + strings.Repeat("a", 1100),
	} {
		if IsRefName(name) {
			t.Errorf("IsRefName(%q) is true", name)
		}
		if _, err := r.Refs(context.Background(), []string{"refs/heads/main", name}); !errors.Is(err, ErrNoReader) {
			t.Errorf("%q: %v, want ErrNoReader", name, err)
		}
	}
	if n := starts.Load(); n != 0 {
		t.Errorf("%d gits started, want none", n)
	}
	for _, name := range []string{"refs/heads/main", "refs/murmur/names", "refs/murmur/fetch-0123456789abcdef/mesh/device-1a2b3c4d",
		"refs/murmur/fetch-0123456789abcdef/presence/toms-laptop-3f2a", "refs/a.b_c-d/E9", "refs/heads/big", "refs/heads/a-g", "refs/heads/x-gabc/y",
		"refs/heads/logabc", "refs/heads/a-gx1"} {
		if !IsRefName(name) {
			t.Errorf("IsRefName(%q) is false", name)
		}
	}
}

// Without --batch-command, a git from before 2.36, Refs says so as Info does, and the caller reads
// another way.
func TestReaderRefsOnAGitWithoutBatchCommand(t *testing.T) {
	g, commit, _ := readerRepo(t)
	git(t, g, "", "update-ref", "refs/heads/main", commit)
	was := readerArgs
	readerArgs = []string{"cat-file", "--batch-command-of-a-later-git"}
	t.Cleanup(func() { readerArgs = was })
	r := keep(t, g, time.Minute)
	if _, err := r.Refs(context.Background(), []string{"refs/heads/main"}); !errors.Is(err, ErrNoReader) {
		t.Fatalf("%v, want ErrNoReader", err)
	}
}
