package daemon

import (
	"context"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// namedDaemon is a daemon in a clone of a bare remote that has just taken its first session name,
// so its registry is on the remote and recorded as read.
func namedDaemon(t *testing.T) (d *Daemon, remote string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	runGit(t, root, "init", "-q", "--bare", remote)
	clone := filepath.Join(root, "clone")
	runGit(t, root, "clone", "-q", "--no-local", remote, clone)
	opt := Options{GitDir: filepath.Join(clone, ".git"), Log: log.New(io.Discard, "", 0), NamePool: -1}
	opt.defaults()
	d, err := open(context.Background(), opt, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := nameWork{wants: []wanted{{id: "aaaaaa", key: nameKey("", "", "", "aaaaaa")}}}
	if err := d.pushNames(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if d.st.NamesGood == "" || d.st.NamesGood != d.st.NamesTip {
		t.Fatalf("after the first name the registry's tip is %q and the last read in full %q", d.st.NamesTip, d.st.NamesGood)
	}
	return d, remote
}

// A sync lists the remote before it takes advMu, and this daemon's own first name can be pushed
// and recorded in between: the registry is on the remote and read, and the listing says it isn't
// there. That is a registry that hasn't been made yet, not one that was deleted, and must not set
// the registry to be repaired, which would replace its history with a snapshot (#80).
func TestListingFromBeforeTheFirstNameIsNotALostRegistry(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	ctx := context.Background()
	tip := d.st.NamesTip

	d.advMu.Lock()
	d.processNames(ctx, "") // what pollOnce does with a listing that has no registry
	d.advMu.Unlock()

	if d.st.NamesRepair {
		t.Error("a listing from before the registry was made set it to be repaired")
	}
	if d.st.NamesTip != tip || d.st.NamesGood != tip {
		t.Errorf("the registry's tip became %q, last read in full %q; want both %q", d.st.NamesTip, d.st.NamesGood, tip)
	}
	if w := d.nextNames(); !w.empty() {
		t.Errorf("the assigner has work to do: %+v", w)
	}
	if got, err := d.repo.RemoteRef(ctx, brand.NamesRef); err != nil || got != tip {
		t.Errorf("the registry on the remote is %q (%v); it should still be %q", got, err, tip)
	}
}

// A registry that is gone from the remote after having been read is lost, however the daemon
// found out: the check above must not hide a real deletion.
func TestRegistryDeletedFromTheRemoteIsStillALostRegistry(t *testing.T) {
	t.Parallel()
	d, remote := namedDaemon(t)
	runGit(t, t.TempDir(), "--git-dir", remote, "update-ref", "-d", brand.NamesRef)

	d.advMu.Lock()
	d.processNames(context.Background(), "")
	d.advMu.Unlock()

	if !d.st.NamesRepair {
		t.Error("a registry deleted from the remote was not set to be repaired")
	}
	if w := d.nextNames(); !w.repair || len(w.wants)+len(w.binds)+w.reserve != 0 {
		t.Errorf("the assigner has %+v; want a repair and nothing else", w)
	}
}

// claimsAt gathers what the registry's history up to sha holds, and the reservations in it.
func claimsAt(t *testing.T, d *Daemon, sha string) (claims, aside map[string]string) {
	t.Helper()
	var commits []bus.Commit
	err := d.repo.Log(context.Background(), "", sha, bus.MaxSnapshot, func(page []bus.Commit) error {
		commits = append(commits, page...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, aside = map[string]string{}, map[string]string{}
	for _, c := range commits {
		var rec bus.NameRecord
		if bus.Decode(c.Message, &rec) != nil {
			continue
		}
		for name, addr := range rec.Claims() {
			if claims[name] == "" {
				claims[name] = addr
			}
		}
		if name, dev, ok := rec.Reservation(); ok {
			aside[name] = dev
		}
	}
	return claims, aside
}

// A sync can find the registry rewound after the assigner took its work and before it pushes.
// The push is built on the tip it reads, so it must make the repair that tip needs, whatever the
// work said: building on a rewound tip without the snapshot would lose names for good (Codex's
// review of #144).
func TestRepairFoundAfterTheWorkWasTakenIsMade(t *testing.T) {
	t.Parallel()
	d, remote := namedDaemon(t)
	ctx := context.Background()
	first := d.st.NamesTip
	key := nameKey("", "", "", "aaaaab")
	if err := d.pushNames(ctx, nameWork{wants: []wanted{{id: "aaaaab", key: key}}}); err != nil {
		t.Fatal(err)
	}
	lost := d.st.NameKeys[key]
	w := nameWork{reserve: 1} // taken before the rewind is read

	runGit(t, t.TempDir(), "--git-dir", remote, "update-ref", brand.NamesRef, first)
	d.advMu.Lock()
	d.processNames(ctx, first)
	d.advMu.Unlock()
	if !d.st.NamesRepair {
		t.Fatal("the rewind wasn't noticed")
	}
	if err := d.pushNames(ctx, w); err != nil {
		t.Fatal(err)
	}
	tip, err := d.repo.RemoteRef(ctx, brand.NamesRef)
	if err != nil {
		t.Fatal(err)
	}
	if claims, _ := claimsAt(t, d, tip); claims[lost] == "" {
		t.Fatalf("the registry lost %s: %v", lost, claims)
	}
}

// A session that takes a name from the pool is found by it at once, even after it moves to another
// conversation before the registry has it down for the name (Codex's review of #144).
func TestNameFromThePoolIsTheSessionsAddressAtOnce(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.NamePool = []string{"quiet-otter"}
	s := &session{id: "bbbbbb", key: "claude:one"}
	d.sessions[s.id] = s
	d.setNameLocked(s, d.nameForKeyLocked(s, s.key))
	if s.name != "quiet-otter" {
		t.Fatalf("the session took %q from the pool", s.name)
	}
	s.key, d.st.NameKeys["claude:two"] = "claude:two", "limber-geode"
	d.setNameLocked(s, "limber-geode") // it followed Claude Code to a conversation with a name
	if addr, ok := d.resolveNameLocked("quiet-otter"); !ok || addr != bus.Addr(d.device, "bbbbbb") {
		t.Fatalf("quiet-otter resolves to %q, %v", addr, ok)
	}
}

// A repaired registry keeps the names set aside as reservations, with their devices, so a daemon
// that joins later knows whose they are (Codex's review of #144).
func TestRepairKeepsNamesSetAside(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	ctx := context.Background()
	d.st.Used["quiet-otter"], d.st.Reserved["quiet-otter"] = "", "other-0001"
	d.st.NamesRepair = true
	if err := d.pushNames(ctx, nameWork{repair: true}); err != nil {
		t.Fatal(err)
	}
	claims, aside := claimsAt(t, d, d.st.NamesTip)
	if aside["quiet-otter"] != "other-0001" {
		t.Fatalf("the repaired registry sets aside %v", aside)
	}
	for name := range d.st.Used {
		if _, ok := claims[name]; !ok {
			t.Fatalf("the repaired registry lost %s", name)
		}
	}
}

// A second reservation of a name already set aside, read in an incremental sync, is ignored as a
// reader of the whole history ignores it, so the two agree on who may assign the name (Codex's
// review of #144).
func TestReservationOfANameAlreadySetAsideIsIgnored(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	ctx := context.Background()
	if err := d.pushNames(ctx, nameWork{reserve: 1}); err != nil {
		t.Fatal(err)
	}
	name := d.st.NamePool[0]
	forge := func(parent string, r bus.NameRecord) string {
		msg, err := bus.Encode(r, r.Subject())
		if err != nil {
			t.Fatal(err)
		}
		sha, err := d.repo.NewCommit(ctx, parent, msg)
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}
	tip := forge(d.st.NamesTip, bus.NameRecord{V: bus.Version, Name: name, Device: "intruder-0001"})
	tip = forge(tip, bus.NameRecord{V: bus.Version, Name: name, Device: "intruder-0001", Session: "s1"})
	d.advMu.Lock()
	d.processNames(ctx, tip)
	d.advMu.Unlock()
	if got := d.st.Used[name]; got != "" {
		t.Fatalf("%s, set aside for this device, is taken as %s's", name, got)
	}
	if got := d.st.Reserved[name]; got != d.device {
		t.Fatalf("%s is set aside for %q", name, got)
	}
}

// A name a daemon knows with no session and no device, from a snapshot that kept no devices, gains
// its device from the first reservation it reads, whether it reads only what is new or the whole
// history again, as a reader starting fresh does; and an assignment from another device is then
// refused (Codex's second review of #144).
func TestKnownNameWithNoDeviceGainsItsReservation(t *testing.T) {
	t.Parallel()
	for _, whole := range []bool{false, true} {
		d, _ := namedDaemon(t)
		ctx := context.Background()
		const name = "quiet-otter"
		d.st.Used[name] = "" // read before, from a snapshot that kept no devices
		forge := func(parent string, r bus.NameRecord) string {
			msg, err := bus.Encode(r, r.Subject())
			if err != nil {
				t.Fatal(err)
			}
			sha, err := d.repo.NewCommit(ctx, parent, msg)
			if err != nil {
				t.Fatal(err)
			}
			return sha
		}
		parent := d.st.NamesTip
		reservation := bus.NameRecord{V: bus.Version, Name: name, Device: "owner-0001"}
		if whole {
			// Another daemon's repair: a new root, the reservation first, the rest in its snapshot.
			parent = ""
			reservation.Used = map[string]string{}
			for n, addr := range d.st.Used {
				if n != name {
					reservation.Used[n] = addr
				}
			}
		}
		tip := forge(parent, reservation)
		tip = forge(tip, bus.NameRecord{V: bus.Version, Name: name, Device: "intruder-0001", Session: "s1"})
		d.advMu.Lock()
		d.processNames(ctx, tip)
		d.advMu.Unlock()
		if got := d.st.Reserved[name]; got != "owner-0001" {
			t.Errorf("whole history %v: %s is set aside for %q", whole, name, got)
		}
		if got := d.st.Used[name]; got != "" {
			t.Errorf("whole history %v: %s is taken as %s's", whole, name, got)
		}
	}
}

// shownName is the name one place shows for a sender.
type shownName struct{ where, name string }

// shownNames is the name each place shows for the sender of e, a session that isn't in presence:
// the inbox, activity, lookup's detail and its list of messages, and the name a mute check goes by.
// The order is fixed, so a failing test reports the same place every run.
func shownNames(d *Daemon, e bus.Event) []shownName {
	d.msgs = []pageMsg{{seq: 1, e: e}}
	det := d.detailLocked(e.From, "", 10)
	return []shownName{
		{"inbox", d.messageViewsLocked([]*inMsg{{Event: e}}, false)[0].FromName},
		{"activity", d.labelerLocked()(e.From)},
		{"lookup", det.Name},
		{"lookup's messages", det.Messages[0].FromName},
		{"nameOfLocked", d.nameOfLocked(e.From)},
	}
}

// An address that has held many names, as after Claude Code's /clear, and has left presence is
// shown by one of them, the same every time and in every place: the first by spelling, as
// nameOfLocked gives it. A map's order chose it before (#502).
func TestADepartedSenderIsShownByOneNameEverywhere(t *testing.T) {
	from := bus.Addr("there-0002", "aaaaaa")
	used := map[string]string{"other-name": bus.Addr("there-0002", "cccccc")}
	for _, n := range []string{"brave-heron", "calm-zebra", "dusty-crane", "eager-lynx", "fond-otter",
		"glad-raven", "hazy-moose", "icy-finch", "jolly-newt", "keen-quail", "lucky-shrew", "mild-stoat"} {
		used[n] = from
	}
	d := &Daemon{device: "here-0001", st: state{Used: used}}
	e := bus.Event{ID: "m1", From: from, To: "*", Subject: "hi"}
	for i := 0; i < 50; i++ {
		for _, got := range shownNames(d, e) {
			if got.name != "brave-heron" {
				t.Fatalf("try %d: %s shows the sender as %q; want brave-heron, the first by spelling, every time", i, got.where, got.name)
			}
		}
	}
}

// One of this device's sessions that has stopped is shown by the name it took last, which this
// device knows, wherever that falls in spelling among the names its address has held (#502).
func TestADepartedLocalSessionIsShownByTheNameItTookLast(t *testing.T) {
	const me = "here-0001"
	from := bus.Addr(me, "aaaaaa")
	used := map[string]string{}
	for _, n := range []string{"brave-heron", "calm-zebra", "dusty-crane", "eager-lynx", "fond-otter"} {
		used[n] = from
	}
	d := &Daemon{device: me, st: state{Used: used, SessionNames: map[string]string{"aaaaaa": "dusty-crane"},
		Stopped: map[string]time.Time{"aaaaaa": time.Now()}}}
	e := bus.Event{ID: "m1", From: from, To: me, Subject: "hi"}
	for i := 0; i < 50; i++ {
		for _, got := range shownNames(d, e) {
			if got.name != "dusty-crane" {
				t.Fatalf("try %d: %s shows the sender as %q; want dusty-crane, the name it took last", i, got.where, got.name)
			}
		}
	}
}

// The name shown for a departed sender is one a mute holds against: here a session of this device
// that took the name of a conversation it resumed, which the registry has down for another session
// ID (#502).
func TestAMuteOfADepartedLocalSendersShownNameHolds(t *testing.T) {
	const me, muter = "here-0001", "quiet-otter"
	from := bus.Addr(me, "aaaaaa")
	d := &Daemon{device: me, opt: Options{MuteGrace: -1},
		sessions: map[string]*session{"bbbbbb": {id: "bbbbbb", name: muter}},
		st: state{Used: map[string]string{"brave-heron": bus.Addr(me, "zzzzzz")},
			SessionNames: map[string]string{"aaaaaa": "brave-heron", "zzzzzz": "brave-heron"},
			Mutes:        map[string]map[string]time.Time{muter: {"brave-heron": time.Now()}}}}
	e := bus.Event{From: from, To: me}
	if got := d.nameOfLocked(from); got != "brave-heron" {
		t.Errorf("the sender is shown as %q; want brave-heron", got)
	}
	if !d.mutedHereLocked("bbbbbb", e) {
		t.Error("a mute of the name the sender is shown by let its message through")
	}
}

// A T3 session that finds its thread while a push that also refills the pool is in flight takes the
// pushed name on both keys and leaves every reserved pool name untouched (#874).
func TestRekeyDuringPushRefillingPoolTakesNoSecondName(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	d.opt.NamePool = 3
	d.mu.Lock()
	s := &session{id: "xxxxxx", agent: "claude-code in T3", claudeSession: "conv-pool", key: "claude:conv-pool", clientPID: 800}
	d.sessions[s.id] = s
	d.mu.Unlock()

	w := d.nextNames()
	if len(w.wants) != 1 || w.reserve != 3 {
		t.Fatalf("nextNames: wants=%+v reserve=%d; want 1 want and 3 reserve", w.wants, w.reserve)
	}
	d.mu.Lock()
	d.setRouteLocked(s, proto.Route{T3Thread: "thread-pool", T3CLI: "/opt/t3/bin/t3"})
	d.mu.Unlock()

	if err := d.pushNames(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.name == "" {
		t.Fatal("session has no name after push landed")
	}
	if got := d.st.NameKeys["claude:conv-pool"]; got != s.name {
		t.Errorf("claude key has %q, session has %q", got, s.name)
	}
	if got := d.st.NameKeys["t3:thread-pool"]; got != s.name {
		t.Errorf("t3 key has %q, session has %q", got, s.name)
	}
	if d.st.NamedFromCLI["t3:thread-pool"] {
		t.Error("T3's own name was marked NamedFromCLI")
	}
	if len(d.st.NamePool) != 3 {
		t.Errorf("pool has %d names after landing, want 3 (a second name was taken from the pool)", len(d.st.NamePool))
	}
}

// A T3 session that already knew its thread when nextNames picked it, and stops before the push
// lands, still shares its landed name with its Claude session key (#874).
func TestStoppedT3SessionSharesLandedNameWithClaudeKey(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	d.mu.Lock()
	s := &session{id: "tttttt", agent: "claude-code in T3", claudeSession: "conv-gone",
		key: "t3:thread-gone", route: proto.Route{T3Thread: "thread-gone"}, clientPID: 800}
	d.sessions[s.id] = s
	d.mu.Unlock()

	w := d.nextNames()
	d.mu.Lock()
	delete(d.sessions, s.id)
	d.mu.Unlock()

	if err := d.pushNames(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t3Name := d.st.NameKeys["t3:thread-gone"]
	cliName := d.st.NameKeys["claude:conv-gone"]
	if t3Name == "" || cliName != t3Name {
		t.Errorf("t3 key has %q, claude key has %q; want both equal and non-empty", t3Name, cliName)
	}
}

// When givePoolLocked names a CLI session in its grace before a live T3 session on the same
// conversation, T3 shares that name rather than taking a second one from the pool (#874).
func TestGivePoolSharesNameBetweenDroppedCLIAndT3(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.NamePool = []string{"bold-lark", "keen-swift"}
	cli := &session{id: "aaaaaa", agent: "claude-code", claudeSession: "conv-pool-share",
		key: "claude:conv-pool-share", clientPID: 700, dropped: time.Now()}
	t3 := &session{id: "tttttt", agent: "claude-code in T3", claudeSession: "conv-pool-share",
		key: "t3:thread-pool-share", route: proto.Route{T3Thread: "thread-pool-share"}, clientPID: 800}
	d.sessions[cli.id] = cli
	d.sessions[t3.id] = t3

	d.givePoolLocked()
	if cli.name != "bold-lark" || t3.name != "bold-lark" {
		t.Errorf("cli=%q t3=%q; want both bold-lark", cli.name, t3.name)
	}
	if !d.st.NamedFromCLI["t3:thread-pool-share"] {
		t.Error("name given to CLI session was not marked NamedFromCLI on the T3 thread")
	}
	if len(d.st.NamePool) != 1 {
		t.Errorf("pool has %d names left, want 1", len(d.st.NamePool))
	}
}
