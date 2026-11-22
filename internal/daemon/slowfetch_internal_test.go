package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// What a kill can leave goes once nothing has written to it for a while. A file still being written
// stays, and so does everything of git's that isn't temporary.
func TestRemoveStaleTemp(t *testing.T) {
	t.Parallel()
	gitDir := t.TempDir()
	write := func(rel string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(gitDir, "objects", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// Read-only, as git makes them, which on Windows a plain delete refuses.
		if err := os.WriteFile(path, nil, 0o444); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := staleTempAge + time.Minute
	gone := []string{write("pack/tmp_pack_abc", old), write("pack/tmp_idx_abc", old), write("ab/tmp_obj_abc", old)}
	kept := []string{write("pack/tmp_pack_live", 0), write("ab/tmp_obj_live", 0), write("pack/pack-1.pack", old),
		write("ab/cdef", old), write("info/tmp_other", old)}

	removeStaleTemp(gitDir, log.New(io.Discard, "", 0))

	for _, path := range gone {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not removed: %v", path, err)
		}
	}
	for _, path := range kept {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s removed: %v", path, err)
		}
	}
}

// A slow fetch that fails waits longer before each next try, and one that lands leaves the ref to be
// read, even if the ref moved meanwhile. A failure of the old tip's fetch says nothing about the new.
func TestEndSlowFetch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const ref = "refs/murmur/mesh/zzz-0000"
	d := &Daemon{opt: Options{GitDir: t.TempDir()}, log: log.New(io.Discard, "", 0), bgCtx: ctx,
		pollWake: make(chan struct{}, 1), unfetched: map[string]fetchFail{ref: {sha: "one", slow: true}}}
	end := func(sha string, err error) fetchFail {
		t.Helper()
		d.slowRef = ref
		d.endSlowFetch(ref, sha, "got "+sha, err)
		if d.slowRef != "" {
			t.Fatal("slowRef still set")
		}
		select {
		case <-d.pollWake:
		default:
			t.Fatal("no poll asked for")
		}
		return d.unfetched[ref]
	}

	failed := errors.New("no")
	for tries, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute} {
		f := d.unfetched[ref]
		f.tries = tries - 1
		d.unfetched[ref] = f
		f = end("one", failed)
		if f.tries != tries || !f.slow {
			t.Fatalf("after try %d: %+v", tries, f)
		}
		if wait := time.Until(f.at); wait > want || wait < want-time.Minute/2 {
			t.Errorf("after try %d: waits %s, want %s", tries, wait, want)
		}
	}
	f := d.unfetched[ref]
	f.tries = 40
	d.unfetched[ref] = f
	if wait := time.Until(end("one", failed).at); wait > time.Hour || wait < 59*time.Minute {
		t.Errorf("after many tries: waits %s, want an hour", wait)
	}

	d.unfetched[ref] = fetchFail{sha: "two", slow: true}
	if f := end("one", failed); f.sha != "two" || f.tries != 0 || f.landed || !f.at.IsZero() {
		t.Errorf("the old tip's failed fetch changed the new tip's entry: %+v", f)
	}
	if f := end("one", nil); f.sha != "two" || !f.landed {
		t.Errorf("the old tip's fetch landed, and the ref isn't left to be read: %+v", f)
	}
	d.unfetched[ref] = fetchFail{sha: "two", slow: true}
	if f := end("two", nil); !f.landed || f.got != "got two" {
		t.Errorf("a fetch that landed doesn't leave the ref to be read: %+v", f)
	}

	delete(d.unfetched, ref)
	end("two", nil)
	if _, ok := d.unfetched[ref]; ok {
		t.Error("a fetch that landed brought back an entry the ref no longer had")
	}
}
