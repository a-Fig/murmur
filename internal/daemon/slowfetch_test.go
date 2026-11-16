package daemon_test

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// stallFetches makes this clone's fetches of anything large stop partway for a few seconds, as on a
// slow link: its upload-pack passes on the first reads, which hold a listing and a small fetch, then
// waits. dd because it reads no further than it passes on, unlike head on some systems, and a byte
// at a time because with count, BSD dd holds short reads back until it has a block, so a listing
// shorter than that would never reach git.
func stallFetches(t *testing.T, gitDir string, stall string) {
	t.Helper()
	script := filepath.ToSlash(filepath.Join(t.TempDir(), "stall.sh"))
	body := "#!/bin/sh\ngit upload-pack \"$1\" | {\n" +
		"\treads=$(dd bs=1 count=65536 2>&1 >&3 | awk -F'[+ ]' '/records in/ { print $1 + $2 }')\n" +
		"\tif [ \"$reads\" = 65536 ]; then sleep " + stall + "; cat; fi\n" +
		"} 3>&1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, filepath.Dir(gitDir), "config", "remote.origin.uploadpack", "sh '"+script+"'")
}

// pushLarge puts on ref a commit with message msg whose tree holds a large file, from outside Murmur.
func pushLarge(t *testing.T, remote, ref, msg string) {
	t.Helper()
	work := t.TempDir()
	git(t, work, "init", "-q")
	data := make([]byte, 4<<20)
	rand.Read(data) // so compression can't make it small
	file := filepath.Join(work, "large")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	blob := git(t, work, "hash-object", "-w", "large")
	tree := gitIn(t, work, "100644 blob "+blob+"\tlarge\n", "mktree")
	sha := gitIn(t, work, msg, "commit-tree", tree)
	git(t, work, "push", "-q", remote, sha+":"+ref)
}

// dumpLogOnFailure prints what the daemon logged if the test fails: these tests turn on how long git
// took, which a timeout's message doesn't say.
func dumpLogOnFailure(t *testing.T, out *syncBuf) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the daemon's log:\n%s", out.String())
		}
	})
}

// However slow every fetch is, even past the limit each time, and however often a ref moves, what
// a fetch outside the syncs brought gets read: here, a presence that moves faster than a fetch can
// be counted on to finish. Asked to fetch the ref again first, a sync would cut that off as well,
// and on such a link, or such a loaded machine, it would never read anything.
func TestRefsMovingFasterThanTheirFetchesAreStillRead(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	var out syncBuf
	dumpLogOnFailure(t, &out)
	a := newDeviceWith(t, remote, func(d *device) {
		d.logTo = &out
		d.fetchLimit = time.Millisecond // past it every time
		d.stale = time.Minute
	})
	b := newDevice(t, remote, time.Minute)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a sees bob", func() bool { return a.seesSession(bob) })
	if !strings.Contains(out.String(), "took over") {
		t.Errorf("no fetch ran past the limit, so this tested nothing")
	}
}

// A ref too large to fetch within a sync's limit fails neither that sync nor the next, and comes in
// once its own fetch outside the syncs has finished (#15). This is real git, cut off by the limit
// mid-download; a sync used to wait out its minute on such a ref and fail, at every poll.
func TestLargeRefLeavesTheSyncsWorking(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	var out syncBuf
	dumpLogOnFailure(t, &out)
	a := newDeviceWith(t, remote, func(d *device) {
		d.logTo = &out
		d.fetchLimit = 2 * time.Second
		d.stale = time.Minute
		// Longer than a cancelled git's five seconds to exit (see gitx.run), so that a fetch cut off
		// is over before its stall is, on every platform.
		stallFetches(t, d.gitDir, "7")
	})
	b := newDevice(t, remote, time.Minute)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a sees bob", func() bool { return a.seesSession(bob) })
	eventually(t, "b sees alice", func() bool { return b.seesSession(alice) })

	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "zzz-0000/s", At: time.Now().UTC(),
		To: "*", Subject: "through the large object"}
	msg, err := bus.Encode(e, e.Line())
	if err != nil {
		t.Fatal(err)
	}
	pushLarge(t, remote, "refs/murmur/mesh/zzz-0000", msg)
	eventually(t, "the ref set aside", func() bool {
		return strings.Contains(out.String(), "fetching refs/murmur/mesh/zzz-0000 took over")
	})

	// The syncs go on without it: this message comes through a later sync than the one that set the
	// ref aside, while the ref's own fetch is still stalled or just past it.
	if r := b.send("bbbbbb", alice, "past the large object", ""); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	logged := func(s string) bool {
		for _, ev := range a.do(proto.Request{Op: proto.OpLog}).Events {
			if strings.Contains(ev.Line, s) {
				return true
			}
		}
		return false
	}
	eventually(t, "the message past the large object", func() bool { return logged("past the large object") })

	eventually(t, "the large ref fetched", func() bool {
		return strings.Contains(out.String(), "fetched refs/murmur/mesh/zzz-0000")
	})
	eventually(t, "the event on the large ref", func() bool { return logged("through the large object") })
}

// A send answers once its push has landed, however long the sync that is running takes over a
// fetch (#738): it used to wait for that sync, to queue its own event for this device's sessions,
// with every other write to the log held behind it. The sync does that queueing itself now, so a
// message to a session on the same device still arrives.
func TestASendDoesNotWaitForASyncsSlowFetch(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	var out syncBuf
	dumpLogOnFailure(t, &out)
	const limit = 10 * time.Second
	a := newDeviceWith(t, remote, func(d *device) {
		d.logTo = &out
		d.fetchLimit = limit
		d.ownTipWait = 500 * time.Millisecond
		d.stale = 5 * time.Minute
	})
	alice, _ := a.session("aaaaaa", "")
	a.session("cccccc", "")
	stallFetches(t, a.gitDir, "14")

	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "zzz-0000/s", At: time.Now().UTC(),
		To: "*", Subject: "through the large object"}
	msg, err := bus.Encode(e, e.Line())
	if err != nil {
		t.Fatal(err)
	}
	pushLarge(t, remote, "refs/murmur/mesh/zzz-0000", msg)
	time.Sleep(2 * time.Second) // a sync has listed the ref, and is in its fetch

	start := time.Now()
	if r := a.send("cccccc", alice, "sent during the fetch", ""); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	took := time.Since(start)
	// Waiting for the sync, it took what was left of the fetch's limit and then the ref's fetch
	// alone, another limit.
	if took > limit {
		t.Errorf("the send took %s, with each fetch stalled for %s", took, limit)
	}
	eventually(t, "the fetch to run past its limit", func() bool {
		return strings.Contains(out.String(), "zzz-0000 took over")
	})
	eventually(t, "the message to reach the session on the same device", func() bool {
		return a.queuedFor(alice, "sent during the fetch")
	})
	// Until the stalled fetches are over: one left running holds the remote's files open.
	eventually(t, "the large ref fetched", func() bool {
		return strings.Contains(out.String(), "fetched refs/murmur/mesh/zzz-0000")
	})
}
