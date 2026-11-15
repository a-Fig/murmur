package daemon_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
)

// `t3 thread send` takes no key that would make a repeat harmless, so a handoff that failed after
// `t3` started may have put the message in the thread, and trying again would make a second turn of
// it. Only an attempt that cannot have reached T3 is made twice (#63).

// lateT3 is a `t3` that isn't there yet: exe names a command that doesn't exist, until install puts
// the stand-in in its place. A handoff to it fails before anything starts.
func lateT3(t *testing.T) (exe string, install func(), calls func() []t3Call) {
	t.Helper()
	exe, calls = fakeT3Path(t)
	return exe, func() { installFakeT3(t, exe) }, calls
}

// A `t3` that took the message and then failed, as one does that times out or has its output lost
// after T3 accepted it, is not run again: the message may be in the thread already, and it would
// be there twice. It waits for the hook's notice instead, and is never handed over a second time.
func TestAHandoffThatMayHaveLandedIsNotRepeated(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	setFakeT3(t, exe, fakeT3Fail, "1") // the stand-in records the call, which is T3 having it, and then fails
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.pushRetry = 300 * time.Millisecond })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "might have landed", "body")
	eventually(t, "the attempt", func() bool { return len(calls()) == 1 })
	holds(t, "no second call after the retry wait", 2*time.Second, func() bool { return len(calls()) == 1 })
	var notices []string
	eventually(t, "the notice", func() bool {
		notices = append(notices, b.hook("claude-bob", 0)...)
		return len(notices) > 0
	})
	if notices[0] != "might have landed" {
		t.Errorf("notices: %q", notices)
	}
}

// The same holds over a restart of the daemon (#82): the memory of a handoff that may have landed
// was in the daemon alone, so a daemon that started within the 15 minutes a message stays fit to hand over
// sent the same turn again. The mark is saved before the handoff, so it is there whether the daemon
// stopped after the call failed or in the middle of it, and the message is left to the notice.
func TestAHandoffThatMayHaveLandedIsNotRepeatedAfterARestart(t *testing.T) {
	t.Parallel()
	route := func(exe string) proto.Route { return proto.Route{T3CLI: exe, T3Thread: "thread-bob"} }
	for _, tc := range []struct {
		name    string
		setting string // what makes the stand-in fail, or take its time, the first time
		val     string
		// failed says the daemon is restarted after the call has failed, not while it runs.
		failed bool
	}{
		{"after the call failed", fakeT3Fail, "1", true},
		{"in the middle of the call", fakeT3Sleep, "30s", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := newRemote(t)
			exe, calls := useFakeT3(t)
			setFakeT3(t, exe, tc.setting, tc.val)
			var out syncBuf
			a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
			alice, _ := a.session("aaaaaa", "")
			bob, _ := b.sessionRoute("bbbbbb", "claude-bob", route(exe))
			eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

			a.send("aaaaaa", bob, "might have landed", "body")
			eventually(t, "the attempt", func() bool { return len(calls()) == 1 })
			if tc.failed {
				eventually(t, "the failure", func() bool { return strings.Contains(out.String(), "as a turn through T3 failed") })
			}
			// On disk now, while the call is still running if it is: a daemon that is killed finds the file as it is.
			st, err := os.ReadFile(filepath.Join(b.gitDir, brand.StateDir, "state.json"))
			if err != nil || !regexp.MustCompile(`"attempted":\s*true`).Match(st) {
				t.Errorf("state.json doesn't say the message was handed over (%v):\n%s", err, st)
			}

			setFakeT3(t, exe, tc.setting, "") // T3 would take the message now, if it were sent again
			b.restart()
			b.sessionRoute("bbbbbb", "claude-bob", route(exe)) // the session's MCP server reconnects
			holds(t, "no second call after the restart", 2*time.Second, func() bool { return len(calls()) == 1 })
			var notices []string
			eventually(t, "the notice", func() bool {
				notices = append(notices, b.hook("claude-bob", 0)...)
				return len(notices) > 0
			})
			if notices[0] != "might have landed" {
				t.Errorf("notices: %q", notices)
			}
		})
	}
}

// A message is not handed over unless the mark that it was has been saved: a mark that couldn't be
// would leave a restart nothing to find, and the turn would be sent again. It is left to the notice,
// as after any failed handoff (#82).
func TestAMessageIsNotHandedOverWhenTheMarkCannotBeSaved(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	// state.json is written by way of state.json.tmp: a directory there makes every save fail. A
	// file in it keeps writeJSON's removal of a leftover tmp from taking the directory away.
	blockSaves(t, filepath.Join(b.gitDir, brand.StateDir, "state.json"))

	a.send("aaaaaa", bob, "cannot be marked", "body")
	eventually(t, "the refusal", func() bool { return strings.Contains(out.String(), "can't be saved") })
	holds(t, "no call to t3", time.Second, func() bool { return len(calls()) == 0 })
	var notices []string
	eventually(t, "the notice", func() bool {
		notices = append(notices, b.hook("claude-bob", 0)...)
		return len(notices) > 0
	})
	if notices[0] != "cannot be marked" {
		t.Errorf("notices: %q", notices)
	}
}

// An attempt that never started anything is the one kind that is tried again, once: no `t3` is
// there yet when the first attempt is made, and the retry finds it.
func TestAHandoffThatNeverStartedIsTriedOnceMore(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, install, calls := lateT3(t)
	var out syncBuf
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.logTo, d.pushRetry = &out, 2*time.Second })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "second try", "body")
	eventually(t, "the first attempt to fail before it started", func() bool {
		return strings.Contains(out.String(), "nothing was sent")
	})
	install()
	eventually(t, "the retry", func() bool { return len(calls()) == 1 })
	eventually(t, "the message to be read", func() bool {
		inbox, notices := b.unread("bbbbbb", "claude-bob")
		return len(inbox) == 0 && len(notices) == 0
	})
	holds(t, "one call and no failure logged", time.Second, func() bool {
		return len(calls()) == 1 && !strings.Contains(out.String(), "as a turn through T3 failed")
	})
}

// A retry that fails to start too leaves the message to the notice, after two attempts and no more.
func TestAHandoffThatNeverStartsFallsBackToTheNotice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _, calls := lateT3(t)
	var out syncBuf
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.logTo, d.pushRetry = &out, 300*time.Millisecond })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "no t3", "body")
	eventually(t, "the failure", func() bool { return strings.Contains(out.String(), "as a turn through T3 failed") })
	if n := strings.Count(out.String(), "nothing was sent"); n != 1 {
		t.Errorf("the retry was logged %d times, want once:\n%s", n, out.String())
	}
	eventually(t, "the notice", func() bool { return len(b.hook("claude-bob", 0)) > 0 })
	if n := len(calls()); n != 0 {
		t.Errorf("the stand-in ran %d times with no command there", n)
	}
}

// blockSaves makes every later save of path by writeJSON fail, by putting a directory where it
// writes its tmp file. The daemon may be saving right then (#442): a Mkdir there can find a save's
// tmp file, and a save's removal of a leftover tmp can take an empty directory away before a file
// is put in it. So the directory is made whole, file and all, under another name, and renamed into
// place, tried again while a save's tmp file is in the way: on Unix while it is there at all, on
// Windows while it is open. On Windows the rename can replace a tmp file a save has closed but not
// yet renamed; that save then carries the directory onto path itself, and every later save fails
// at its rename instead of at writing the tmp file. Either way nothing is saved from then on
// (Fable's review found the second, 2026-11-13).
func blockSaves(t *testing.T, path string) {
	t.Helper()
	staged := path + ".blocker"
	if err := os.Mkdir(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if err = os.Rename(staged, path+".tmp"); err == nil {
			return
		}
	}
	t.Fatalf("the blocker never took the tmp file's place: %v", err)
}
