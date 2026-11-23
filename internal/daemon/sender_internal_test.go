package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
)

// A turn is written from what the receiving daemon knows of the sender the moment the handoff
// takes the message, and the handoff starts as soon as the message is queued, while the sync that
// brought it may still be reading the rest of what it fetched (#71). The sender's name landed in
// the registry, and its presence went out, before its message did, so a sync that fetches the
// message fetches them too, and reads them first: the turn has the name and the agent.
//
// Here the peer's device is known from an earlier sync, without the session; then its session takes
// a name, shows in its presence, still unnamed there (as a presence pushed before the name landed
// is), and sends, all before the next sync. The handoff is played at the kick that queuing the
// message gives it, as pushLoop does.
func TestATurnKnowsTheSenderFetchedWithItsMessage(t *testing.T) {
	d, peer, _ := rateRig(t, rateOptions(), false)
	ctx := context.Background()
	d.mu.Lock()
	d.sessions["bbbbbb"].route = proto.Route{MsgSocket: "socket", MsgToken: "token"}
	d.mu.Unlock()
	const name = "dear-syrup"
	if !names.Valid(name) {
		t.Fatalf("%s isn't a name names.Generate could make", name)
	}
	push := func(ref string, v any, subject string, mode bus.PushMode) {
		t.Helper()
		msg, err := bus.Encode(v, subject)
		if err != nil {
			t.Fatal(err)
		}
		sha, err := peer.NewCommit(ctx, "", msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Push(ctx, sha, ref, mode, ""); err != nil {
			t.Fatal(err)
		}
	}
	presence := func(sessions ...bus.SessionInfo) {
		t.Helper()
		pres := bus.Presence{V: bus.Version, Device: "peer-0001", Host: "peer", Owner: "smash", At: time.Now().UTC(),
			Sessions: append([]bus.SessionInfo{}, sessions...)}
		push(brand.PresencePrefix+"peer-0001", pres, "peer-0001: presence", bus.Force)
	}

	presence()
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}

	rec := bus.NameRecord{V: bus.Version, Name: name, Device: "peer-0001", Session: "aaaaaa", At: time.Now().UTC()}
	push(brand.NamesRef, rec, rec.Subject(), bus.Force)
	presence(bus.SessionInfo{ID: "aaaaaa", Agent: "codex", Started: time.Now().UTC()})
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", At: time.Now().UTC(),
		To: bus.Addr(d.device, "bbbbbb"), Subject: "auth middleware?", Body: "Are you changing it?"}
	push(brand.MeshPrefix+"peer-0001", e, e.Line(), bus.FastForward)

	select {
	case <-d.pushKick: // whatever kicked before the message
	default:
	}
	turn := make(chan string, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-d.pushKick:
			}
			wake := d.wakeSnapshot()
			d.mu.Lock()
			job := d.nextPushLocked("bbbbbb", wake)
			d.mu.Unlock()
			if job != nil {
				turn <- job.text
				return
			}
		}
	}()
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var got string
	select {
	case got = <-turn:
	case <-time.After(30 * time.Second):
		t.Fatal("the message was never queued for a handoff")
	}
	want := "Message from " + name + " (codex, on smash's machine) via " + brand.Product + ":\n\n" +
		"> auth middleware?\n\n> Are you changing it?\n\n" +
		"Reply with " + brand.Product + "'s `send` to " + name + " if a reply is needed.\n"
	if got != want {
		t.Errorf("the turn reads:\n%s\nwant:\n%s", got, want)
	}
}

// The registry and presence are read before the logs, but a stall there mustn't cost the logs
// their time (#304's review): the logs were fetched already, and hold the messages. Here the
// registry this daemon read is gone from the listing, so the sync asks the remote for it again, and
// that request never answers. The message fetched in the same sync is still queued, and the sync
// still fails, since it didn't read all it listed.
func TestAStalledRegistryReadLeavesTheLogsTheirTime(t *testing.T) {
	opt := rateOptions()
	opt.ReadFirstLimit = 2 * time.Second
	d, peer, clone := rateRig(t, opt, false)
	ctx := context.Background()
	push := func(ref string, v any, subject string, mode bus.PushMode) {
		t.Helper()
		msg, err := bus.Encode(v, subject)
		if err != nil {
			t.Fatal(err)
		}
		sha, err := peer.NewCommit(ctx, "", msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Push(ctx, sha, ref, mode, ""); err != nil {
			t.Fatal(err)
		}
	}
	rec := bus.NameRecord{V: bus.Version, Name: "dear-syrup", Device: "peer-0001", Session: "aaaaaa", At: time.Now().UTC()}
	push(brand.NamesRef, rec, rec.Subject(), bus.Force)
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	good := d.st.NamesGood
	d.mu.Unlock()
	if good == "" {
		t.Fatal("the registry wasn't read")
	}

	if err := peer.Push(ctx, "", brand.NamesRef, bus.Force, ""); err != nil {
		t.Fatal(err)
	}
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", At: time.Now().UTC(),
		To: bus.Addr(d.device, "bbbbbb"), Subject: "auth middleware?", Body: "Are you changing it?"}
	push(brand.MeshPrefix+"peer-0001", e, e.Line(), bus.FastForward)

	// The clone's third request to the remote from here on (the listing, the fetch, then the
	// registry asked for again) waits until released. The script, not git, stalls, so it is
	// released at the end, for nothing to be left running in the test's directories.
	dir := filepath.ToSlash(t.TempDir())
	script := dir + "/stall.sh"
	body := "#!/bin/sh\n" +
		"echo x >> '" + dir + "/calls'\n" +
		"if [ \"$(wc -l < '" + dir + "/calls')\" -ge 3 ]; then\n" +
		"\ttouch '" + dir + "/stalled'\n" +
		"\twhile [ ! -f '" + dir + "/release' ]; do sleep 0.2; done\n" +
		"\ttouch '" + dir + "/done'\n" +
		"\texit 1\n" +
		"fi\n" +
		"exec git upload-pack \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "config", "remote.origin.uploadpack", "sh '"+script+"'")
	t.Cleanup(func() {
		if _, err := os.Stat(dir + "/stalled"); err != nil {
			return
		}
		if err := os.WriteFile(dir+"/release", nil, 0o644); err != nil {
			t.Error(err)
			return
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if _, err := os.Stat(dir + "/done"); err == nil {
				return
			}
		}
		t.Error("the stalled request never ended")
	})

	start := time.Now()
	err := d.pollOnce(ctx)
	took := time.Since(start)
	if _, serr := os.Stat(dir + "/stalled"); serr != nil {
		t.Fatalf("the registry wasn't asked for again, so this tested nothing (the sync returned %v)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "names registry") {
		t.Errorf("the sync returned %v, want the registry's stall", err)
	}
	if took > 30*time.Second {
		t.Errorf("the sync took %s: the stall used up its minute", took)
	}
	d.mu.Lock()
	inbox := d.st.Inbox["bbbbbb"]
	d.mu.Unlock()
	if len(inbox) != 1 || inbox[0].Event.Body != "Are you changing it?" {
		t.Errorf("the session's inbox holds %d messages, want the one fetched with the stall", len(inbox))
	}
}

// The registry and presence have ReadFirstLimit, and no more than half of what is left of the
// sync, so a listing and fetches that ran late can't leave them the logs' time too, and a short
// sync, like the 10 s one before a daemon accepts sessions, still reads them.
func TestReadFirstContextKeepsTheLogsTime(t *testing.T) {
	const limit = 15 * time.Second
	for _, c := range []struct {
		name string
		left time.Duration // until the sync's deadline; 0 for none
	}{
		{"no deadline", 0},
		{"a whole minute left", time.Minute},
		{"less than twice the limit left", 20 * time.Second},
		{"the sync before sessions", 10 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.left > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.left)
				defer cancel()
			}
			before := time.Now()
			first, cancel := readFirstContext(ctx, limit)
			defer cancel()
			after := time.Now()
			got, ok := first.Deadline()
			if !ok {
				t.Fatal("no deadline")
			}
			if first.Err() != nil {
				t.Fatal("the reads have no time at all")
			}
			// readFirstContext reads the clock between before and after.
			lo, hi := before.Add(limit), after.Add(limit)
			if sync, ok := ctx.Deadline(); ok && 2*limit > c.left {
				lo, hi = before.Add(sync.Sub(before)/2), after.Add(sync.Sub(after)/2)
			}
			if got.Before(lo) || got.After(hi) {
				t.Errorf("the reads end %s from when they began, want between %s and %s",
					got.Sub(before), lo.Sub(before), hi.Sub(before))
			}
		})
	}
}
