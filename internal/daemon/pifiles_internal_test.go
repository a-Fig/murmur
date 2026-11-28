package daemon

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A Pi file's recorded ID (PiFiles, #1145) is forgotten sessionNameKeep after its last use, once no
// session runs or has its inbox kept at that ID, nor at the ID the file makes, which a session
// that left the file may still have. While either does, it is kept: forgotten, the file's server
// would register at the ID the file makes, the session that left it.
func TestAPiFilesRecordIsForgottenOnceNeitherIDIsInUse(t *testing.T) {
	t.Parallel()
	now := time.Now()
	long := now.Add(-sessionNameKeep - time.Hour)
	const (
		gone, recent = "/s/gone.jsonl", "/s/recent.jsonl"
		runs, kept   = "/s/runs.jsonl", "/s/kept.jsonl"
		leftRuns     = "/s/left-runs.jsonl"
		leftKept     = "/s/left-kept.jsonl"
	)
	d := &Daemon{device: "here-0001",
		sessions: map[string]*session{"runs01": {id: "runs01"}, proto.NativeID(leftRuns): {id: proto.NativeID(leftRuns)}},
		st: state{
			PiFiles: map[string]piFile{
				gone:     {ID: "gone01", At: long},
				recent:   {ID: "recnt1", At: now.Add(-time.Hour)},
				runs:     {ID: "runs01", At: long},
				kept:     {ID: "kept01", At: long},
				leftRuns: {ID: "fresh1", At: long},
				leftKept: {ID: "fresh2", At: long},
			},
			Stopped: map[string]time.Time{"kept01": now, proto.NativeID(leftKept): now},
		}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pruneSessionNamesLocked(now) {
		t.Fatal("nothing was forgotten")
	}
	for file, want := range map[string]bool{gone: false, recent: true, runs: true, kept: true, leftRuns: true, leftKept: true} {
		if _, ok := d.st.PiFiles[file]; ok != want {
			t.Errorf("%s kept: %v, want %v", file, ok, want)
		}
	}
	delete(d.sessions, "runs01")
	delete(d.sessions, proto.NativeID(leftRuns))
	d.st.Stopped = nil
	if !d.pruneSessionNamesLocked(now) || len(d.st.PiFiles) != 1 {
		t.Errorf("with nothing at either ID, left: %v", d.st.PiFiles)
	}
}

// #1187: When PiFiles has no entry for a file (or its recorded session is live in another Pi),
// piIDLocked gives the hello a fresh ID whenever the file's own derived ID is already held by
// another conversation's session, stopped name, registry name, or PiFiles record, while leaving
// untouched files (including a Pi session in its grace on the same file or in T3) at their ID.
func TestPiIDLockedAvoidsMisdeliveryWithoutPiFiles(t *testing.T) {
	t.Parallel()
	const (
		dev   = "here-0001"
		file1 = "/s/f1.jsonl"
		file2 = "/s/f2.jsonl"
	)
	id1 := proto.NativeID(file1)
	addr1 := bus.Addr(dev, id1)
	hello := func(file string, pid int) *proto.Hello {
		return &proto.Hello{ID: proto.NativeID(file), Agent: "pi", ClientPID: pid, TakesSelf: true,
			Route: proto.Route{Native: file}}
	}
	newD := func(sessions map[string]*session, st state) *Daemon {
		if sessions == nil {
			sessions = map[string]*session{}
		}
		return &Daemon{device: dev, dir: t.TempDir(), log: log.Default(), sessions: sessions, st: st}
	}

	t.Run("session on another file even if unnamed", func(t *testing.T) {
		d := newD(map[string]*session{
			id1: {id: id1, dropped: time.Now(), route: proto.Route{Native: file2}},
		}, state{})
		d.mu.Lock()
		got, moved := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if moved || got == id1 || got == "" {
			t.Fatalf("got (%q, %v), want fresh ID != %q", got, moved, id1)
		}
		if d.st.PiFiles[file1].ID != got {
			t.Fatalf("PiFiles[%s] = %q, want %q", file1, d.st.PiFiles[file1].ID, got)
		}
	})

	t.Run("stopped session whose last name differs (e.g. after resume)", func(t *testing.T) {
		d := newD(nil, state{
			NameKeys:     map[string]string{nameKey("", "", file1, id1): "calm-otter"},
			SessionNames: map[string]string{id1: "brisk-finch"},
			Used:         map[string]string{"calm-otter": addr1, "brisk-finch": bus.Addr(dev, "other1")},
		})
		d.mu.Lock()
		got, _ := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if got == id1 || got == "" {
			t.Fatalf("got %q, want fresh ID != %q", got, id1)
		}
	})

	t.Run("registry name in Used even after SessionNames was pruned", func(t *testing.T) {
		d := newD(nil, state{
			NameKeys: map[string]string{nameKey("", "", file1, id1): "calm-otter"},
			Used:     map[string]string{"calm-otter": addr1, "brisk-finch": addr1},
		})
		d.mu.Lock()
		got, _ := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if got == id1 || got == "" {
			t.Fatalf("got %q, want fresh ID != %q", got, id1)
		}
	})

	t.Run("another file in PiFiles still points to id1", func(t *testing.T) {
		d := newD(nil, state{
			PiFiles: map[string]piFile{file2: {ID: id1, At: time.Now()}},
		})
		d.mu.Lock()
		got, _ := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if got == id1 || got == "" {
			t.Fatalf("got %q, want fresh ID != %q", got, id1)
		}
	})

	t.Run("recorded PiFiles session live in another Pi does not fall back to moved ID", func(t *testing.T) {
		d := newD(map[string]*session{
			"live01": {id: "live01", clientPID: 100, route: proto.Route{Native: file1}},
		}, state{
			PiFiles:      map[string]piFile{file1: {ID: "live01", At: time.Now()}},
			NameKeys:     map[string]string{nameKey("", "", file1, id1): "calm-otter"},
			SessionNames: map[string]string{id1: "brisk-finch"},
			Used:         map[string]string{"calm-otter": addr1, "brisk-finch": addr1},
		})
		d.mu.Lock()
		got, _ := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if got == id1 || got == "live01" || got == "" {
			t.Fatalf("got %q, want fresh ID != %q and != live01", got, id1)
		}
		if d.st.PiFiles[file1].ID != "live01" {
			t.Fatalf("PiFiles[%s] overwritten to %q, want live01", file1, d.st.PiFiles[file1].ID)
		}
	})

	t.Run("unmoved file keeps its derived ID", func(t *testing.T) {
		d := newD(nil, state{
			NameKeys:     map[string]string{nameKey("", "", file1, id1): "calm-otter"},
			SessionNames: map[string]string{id1: "calm-otter"},
			Used:         map[string]string{"calm-otter": addr1},
		})
		d.mu.Lock()
		got, _ := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if got != id1 {
			t.Fatalf("got %q, want %q", got, id1)
		}
	})

	t.Run("same file in its grace keeps its derived ID", func(t *testing.T) {
		d := newD(map[string]*session{
			id1: {id: id1, clientPID: 100, dropped: time.Now(), route: proto.Route{Native: file1}},
		}, state{
			NameKeys: map[string]string{nameKey("", "", file1, id1): "calm-otter"},
			Used:     map[string]string{"calm-otter": addr1, "brisk-finch": addr1},
		})
		d.mu.Lock()
		got, _ := d.piIDLocked(hello(file1, 200))
		d.mu.Unlock()
		if got != id1 {
			t.Fatalf("got %q, want %q", got, id1)
		}
	})

	// #1260: a T3 thread's Pi on file1, in its grace or stopped with its route kept, is another
	// conversation than a Pi outside T3 on file1, or one in another thread.
	inT3 := func(dropped bool) map[string]*session {
		s := &session{id: id1, agent: "pi in T3", clientPID: 100, route: proto.Route{Native: file1, T3CLI: "t3", T3Thread: "th-1"}}
		if dropped {
			s.dropped = time.Now()
		}
		return map[string]*session{id1: s}
	}
	stoppedT3 := state{T3Routes: map[string]t3Route{id1: {CLI: "t3", Thread: "th-1"}}}
	outside := map[string]*session{id1: {id: id1, agent: "pi", clientPID: 100, dropped: time.Now(), route: proto.Route{Native: file1}}}
	// A T3 thread's Pi has its thread in its first hello when T3's database already maps the file
	// to it (lookupThread), as on a restart; a new thread's row may not be written yet, and then it
	// has none ("").
	inThread := func(thread string) *proto.Hello {
		h := hello(file1, 200)
		h.Agent, h.T3CLI, h.T3Thread = "pi in T3", "t3", thread
		return h
	}
	for _, c := range []struct {
		what     string
		sessions map[string]*session
		st       state
		h        *proto.Hello
		fresh    bool
	}{
		{"T3 session in its grace, Pi outside T3", inT3(true), state{}, hello(file1, 200), true},
		{"T3 session live, Pi outside T3", inT3(false), state{}, hello(file1, 200), true},
		{"T3 route kept, Pi outside T3", nil, stoppedT3, hello(file1, 200), true},
		{"T3 session in its grace, Pi in another thread", inT3(true), state{}, inThread("th-2"), true},
		{"T3 route kept, Pi in another thread", nil, stoppedT3, inThread("th-2"), true},
		{"T3 session in its grace, its thread's Pi", inT3(true), state{}, inThread("th-1"), false},
		{"T3 session in its grace, a Pi in T3 without its thread yet", inT3(true), state{}, inThread(""), false},
		{"session outside T3 in its grace, a T3 thread's Pi", outside, state{}, inThread("th-1"), true},
		{"session outside T3 in its grace, a Pi in T3 without its thread yet", outside, state{}, inThread(""), true},
	} {
		t.Run(c.what, func(t *testing.T) {
			d := newD(c.sessions, c.st)
			d.mu.Lock()
			got, _ := d.piIDLocked(c.h)
			d.mu.Unlock()
			if c.fresh && (got == id1 || got == "") || !c.fresh && got != id1 {
				t.Fatalf("got %q, want fresh ID: %v", got, c.fresh)
			}
		})
	}

	t.Run("file's recorded ID a T3 thread's, Pi outside T3", func(t *testing.T) {
		for _, c := range []struct {
			what     string
			sessions map[string]*session
			st       state
		}{
			{"in its grace", map[string]*session{"rec001": {id: "rec001", clientPID: 100, dropped: time.Now(),
				route: proto.Route{Native: file1, T3CLI: "t3", T3Thread: "th-1"}}}, state{}},
			{"stopped, route kept", nil, state{T3Routes: map[string]t3Route{"rec001": {CLI: "t3", Thread: "th-1"}}}},
		} {
			c.st.PiFiles = map[string]piFile{file1: {ID: "rec001", At: time.Now()}}
			d := newD(c.sessions, c.st)
			d.mu.Lock()
			got, _ := d.piIDLocked(hello(file1, 200))
			d.mu.Unlock()
			if got != id1 {
				t.Errorf("%s: got %q, want the file's own ID %q", c.what, got, id1)
			}
			if d.st.PiFiles[file1].ID != "rec001" {
				t.Errorf("%s: PiFiles[%s] = %q, want rec001 kept", c.what, file1, d.st.PiFiles[file1].ID)
			}
		}
	})

	t.Run("file's recorded ID a Pi outside T3's, in its grace, T3 thread's Pi", func(t *testing.T) {
		for _, thread := range []string{"th-1", ""} {
			d := newD(map[string]*session{"rec001": {id: "rec001", agent: "pi", clientPID: 100, dropped: time.Now(),
				route: proto.Route{Native: file1}}}, state{PiFiles: map[string]piFile{file1: {ID: "rec001", At: time.Now()}}})
			d.mu.Lock()
			got, _ := d.piIDLocked(inThread(thread))
			d.mu.Unlock()
			if got != id1 {
				t.Errorf("thread %q: got %q, want the file's own ID %q", thread, got, id1)
			}
			if d.st.PiFiles[file1].ID != "rec001" {
				t.Errorf("thread %q: PiFiles[%s] = %q, want rec001 kept", thread, file1, d.st.PiFiles[file1].ID)
			}
		}
	})

	t.Run("Pi in T3 restarting without thread keeps its derived ID", func(t *testing.T) {
		d := newD(nil, state{
			NameKeys:     map[string]string{"t3:th-1": "calm-otter"},
			SessionNames: map[string]string{id1: "calm-otter"},
			Used:         map[string]string{"calm-otter": addr1},
			T3Routes:     map[string]t3Route{id1: {CLI: "t3", Thread: "th-1"}},
		})
		h := &proto.Hello{ID: id1, Agent: "pi in T3", ClientPID: 200, TakesSelf: true,
			Route: proto.Route{Native: file1, T3CLI: "t3"}}
		d.mu.Lock()
		got, _ := d.piIDLocked(h)
		d.mu.Unlock()
		if got != id1 {
			t.Fatalf("got %q, want %q", got, id1)
		}
	})
}

// A Pi session that moved to a new file asks for that file's name, and may stop before the name
// lands (an empty pool, a slow remote). The name is then still its last, as if it had landed in
// time: kept under the name of the file it left, what it had was for that file's next session, and
// once PiFiles was lost a Pi on that file took its ID again (Opus's review of cfa13f91). Not for a
// session that moved on to another file before it stopped (Sol's and Opus's review of c9639dca).
func TestANameLandingAfterAPiSessionStoppedIsItsLast(t *testing.T) {
	t.Parallel()
	const (
		file1 = "/s/f1.jsonl"
		file2 = "/s/f2.jsonl"
		file3 = "/s/f3.jsonl"
		alpha = "calm-otter"
	)
	for _, movedOn := range []bool{false, true} {
		what := "stopped on the key it asked for"
		if movedOn {
			what = "moved on to a nameless key, then stopped"
		}
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			d, _ := namedDaemon(t)
			id := proto.NativeID(file1)
			key := nameKey("", "", file2, id)
			stoppedOn := key
			if movedOn {
				stoppedOn = nameKey("", "", file3, id)
			}
			d.mu.Lock()
			d.st.NameKeys[nameKey("", "", file1, id)] = alpha
			if d.st.SessionNames == nil {
				d.st.SessionNames = map[string]string{}
			}
			d.st.SessionNames[id] = alpha
			d.st.Used[alpha] = bus.Addr(d.device, id)
			d.st.Stopped[id] = time.Now()
			d.stoppedKeys = map[string]string{id: stoppedOn} // its grace ran out while the push was out
			d.mu.Unlock()

			w := nameWork{wants: []wanted{{id: id, key: key, agent: "pi"}}}
			if err := d.pushNames(context.Background(), w); err != nil {
				t.Fatal(err)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			beta := d.st.NameKeys[key]
			if beta == "" {
				t.Fatalf("no name landed for %s", key)
			}
			if d.stoppedKeys != nil {
				t.Errorf("stoppedKeys outlived the push: %v", d.stoppedKeys)
			}
			want := beta
			if movedOn {
				want = alpha
			}
			if got := d.st.SessionNames[id]; got != want {
				t.Fatalf("the stopped session's last name is %q, want %q", got, want)
			}
			if movedOn {
				return
			}
			if d.st.NameHolders[beta] != id {
				t.Errorf("%s's holder is %q, want %q", beta, d.st.NameHolders[beta], id)
			}
			// With PiFiles lost, a Pi on the file the session left doesn't take its ID.
			got, _ := d.piIDLocked(&proto.Hello{ID: id, Agent: "pi", ClientPID: 200, TakesSelf: true,
				Route: proto.Route{Native: file1}})
			if got == id || got == "" {
				t.Fatalf("Pi on the file left got %q, want a fresh ID", got)
			}
		})
	}
}

// A Pi session that asked for a new file's name, then went back to its first file, which has a
// name, and stopped there before the name landed, last had the first file's conversation. The name
// landing isn't its last: what it kept stays with the first file, and a Pi on that file keeps its
// ID (Sol's and Opus's review of c9639dca).
func TestANameLandingAfterAPiSessionMovedBackIsNotItsLast(t *testing.T) {
	const (
		file1 = "/s/f1.jsonl"
		file2 = "/s/f2.jsonl"
		alpha = "calm-otter"
	)
	d, _ := namedDaemon(t)
	id := proto.NativeID(file1)
	d.mu.Lock()
	d.st.NameKeys[nameKey("", "", file1, id)] = alpha
	if d.st.SessionNames == nil {
		d.st.SessionNames = map[string]string{}
	}
	if d.st.NameHolders == nil {
		d.st.NameHolders = map[string]string{}
	}
	d.st.SessionNames[id] = alpha
	d.st.NameHolders[alpha] = id
	d.st.Used[alpha] = bus.Addr(d.device, id)
	d.st.Stopped[id] = time.Now()
	d.mu.Unlock()

	key := nameKey("", "", file2, id)
	w := nameWork{wants: []wanted{{id: id, key: key, agent: "pi"}}}
	if err := d.pushNames(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	beta := d.st.NameKeys[key]
	if got := d.st.SessionNames[id]; got != alpha {
		t.Errorf("the session last on file1 has %q (file2's name, beta=%q) as its last, want %q", got, beta, alpha)
	}
	if got := d.keptForLocked(id); got != alpha {
		t.Errorf("its kept inbox is for %q, want %q", got, alpha)
	}
	got, _ := d.piIDLocked(&proto.Hello{ID: id, Agent: "pi", ClientPID: 200, TakesSelf: true,
		Route: proto.Route{Native: file1}})
	if got != id {
		t.Errorf("Pi on file1 got %q, want its own ID %q", got, id)
	}
}
