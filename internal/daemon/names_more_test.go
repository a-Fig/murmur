package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// More tests of session names (#20), written to probe what names_test.go leaves open: names a
// source keeps offering after they are taken, a registry rewound or overwritten while a daemon
// can't push, and everything the bus holds checked for the client's own session ID.

// newDeviceWith is newDevice with fields set before its daemon starts.
func newDeviceWith(t *testing.T, remote string, set func(*device)) *device {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	d := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: 3 * time.Second, poll: 100 * time.Millisecond}
	if set != nil {
		set(d)
	}
	d.start()
	return d
}

// draw is one name a stream picks and the bytes the pick took from it.
type draw struct {
	name string
	raw  []byte
}

// draws are the first n picks of a stream with this seed.
func draws(t *testing.T, seed uint64, n int) []draw {
	t.Helper()
	src := seededNames(seed)
	var out []draw
	for range n {
		var buf bytes.Buffer
		name, err := names.Generate(io.TeeReader(src, &buf))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, draw{name, buf.Bytes()})
	}
	return out
}

// offering is a name source that picks the given names in order, then whatever rest picks: a
// source that offers a name again after it is taken shows whether the daemon remembers that it is.
func offering(rest io.Reader, picks ...draw) io.Reader {
	var rs []io.Reader
	for _, p := range picks {
		rs = append(rs, bytes.NewReader(p.raw))
	}
	return io.MultiReader(append(rs, rest)...)
}

// pushOffline cuts d's pushes to the remote, or restores them, leaving its fetches alone.
func (d *device) pushOffline(remote string, off bool) {
	dir := filepath.Dir(d.gitDir)
	if off {
		git(d.t, dir, "config", "remote.origin.pushurl", filepath.Join(filepath.Dir(remote), "nowhere.git"))
		return
	}
	git(d.t, dir, "config", "--unset", "remote.origin.pushurl")
}

// namesState is the part of d's state.json that holds names.
type namesState struct {
	Used   map[string]string `json:"usedNames"`
	Tip    string            `json:"namesTip"`
	Repair bool              `json:"namesRepair"`
}

// namesState reads it; ok is false while the file is being replaced.
func (d *device) namesState() (st namesState, ok bool) {
	b, err := os.ReadFile(filepath.Join(d.gitDir, brand.StateDir, "state.json"))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	return st, true
}

// registryCommits lists the registry's commits on the remote, oldest first, and a scratch repo
// that has them.
func registryCommits(t *testing.T, remote string) (work string, shas []string) {
	t.Helper()
	work = t.TempDir()
	git(t, work, "init", "-q")
	git(t, work, "fetch", "-q", remote, "refs/murmur/names")
	return work, strings.Fields(git(t, work, "rev-list", "--reverse", "FETCH_HEAD"))
}

// tipIsSnapshot says whether the registry's tip is a root commit whose record holds every name in
// want: what a daemon writes when the registry has lost names.
func tipIsSnapshot(t *testing.T, remote string, want ...string) bool {
	t.Helper()
	work := t.TempDir()
	git(t, work, "init", "-q")
	if git(t, work, "ls-remote", remote, "refs/murmur/names") == "" {
		return false
	}
	git(t, work, "fetch", "-q", remote, "refs/murmur/names")
	if git(t, work, "cat-file", "-t", "FETCH_HEAD") != "commit" {
		return false
	}
	if parents := strings.Fields(git(t, work, "rev-list", "--parents", "-n", "1", "FETCH_HEAD")); len(parents) != 1 {
		return false
	}
	var rec bus.NameRecord
	if bus.Decode(git(t, work, "log", "-1", "--format=%B", "FETCH_HEAD"), &rec) != nil {
		return false
	}
	claims := rec.Claims()
	for _, w := range want {
		if _, ok := claims[w]; !ok {
			return false
		}
	}
	return true
}

// noNameTwice fails the test if the registry assigns any name more than once.
func noNameTwice(t *testing.T, reg registry) {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range reg.assigned() {
		if seen[name] {
			t.Fatalf("the registry assigns %s twice:\n%s", name, reg.text)
		}
		seen[name] = true
	}
}

// Three devices all pick the same name at once, and their sources keep offering it after they
// lose: exactly one gets it, the others get names of their own, and the registry assigns no name
// twice. A loser that didn't take in what it fetched before trying again would take it a second
// time, on top of the winner's commit.
func TestThreeDevicesRaceForOneName(t *testing.T) {
	remote := newRemote(t)
	x := draws(t, 21, 1)[0]
	g := newGate(3)
	var devs []*device
	for i := range 3 {
		src := &countedReader{r: offering(seededNames(uint64(100+i)), x, x, x), gate: g}
		devs = append(devs, newDeviceWith(t, remote, func(d *device) { d.names = src }))
	}
	var addrs, got []string
	for i, d := range devs {
		addr, _ := d.session("aaaaa"+string(rune('1'+i)), "claude-race-"+string(rune('1'+i)))
		addrs = append(addrs, addr)
	}
	for i, d := range devs {
		got = append(got, d.named(addrs[i]))
	}
	winners := 0
	for _, n := range got {
		if n == x.name {
			winners++
		}
	}
	if winners != 1 || got[0] == got[1] || got[1] == got[2] || got[0] == got[2] {
		t.Fatalf("names %q; want three different names, one of them %s", got, x.name)
	}
	reg := readRegistry(t, remote)
	noNameTwice(t, reg)
	assigned := reg.assigned()
	sort.Strings(assigned)
	want := append([]string(nil), got...)
	sort.Strings(want)
	if strings.Join(assigned, ",") != strings.Join(want, ",") {
		t.Fatalf("the registry assigns %q, want %q", assigned, want)
	}
	eventually(t, "every device to show every name", func() bool {
		for _, d := range devs {
			for i, addr := range addrs {
				if d.nameOf(addr) != got[i] {
					return false
				}
			}
		}
		return true
	})
}

// A session keeps its name however it comes back: its MCP server reconnecting inside the grace,
// the session stopped and started again, under another ID with the same client session, and
// across a daemon restart. None of it takes a second name from the registry.
func TestNameFollowsTheKeyThroughEveryReturn(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep = 2*time.Second, time.Hour })
	b := newDevice(t, remote)
	const key = "claude-alice-0001"
	alice, conn := a.session("aaaaaa", key)
	name := a.named(alice)
	live := func() bool { return a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: key}).Self != "" }

	// Inside the grace.
	conn.Close()
	eventually(t, "the drop", func() bool { return !live() })
	conn, err := a.restartedMCP("aaaaaa", key)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.nameOf(alice); got != name {
		t.Fatalf("reconnected in the grace as %q, was %q", got, name)
	}

	// After the grace: stopped, then back.
	conn.Close()
	eventually(t, "alice stopped", func() bool { return busLines(a, alice, "stopped") == 1 })
	if conn, err = a.restartedMCP("aaaaaa", key); err != nil {
		t.Fatal(err)
	}
	if got := a.nameOf(alice); got != name {
		t.Fatalf("back after a stop as %q, was %q", got, name)
	}

	// Stopped again, and back under another ID.
	conn.Close()
	eventually(t, "alice stopped again", func() bool { return busLines(a, alice, "stopped") == 2 })
	other, conn := a.session("aaaaab", key)
	if got := a.nameOf(other); got != name {
		t.Fatalf("back under another ID as %q, was %q", got, name)
	}

	// The daemon restarts; the name is there from the hello on, not pending.
	conn.Close()
	a.restart()
	a.session("aaaaab", key)
	if r := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaab"}); r.Name != name || r.NamePending {
		t.Fatalf("after a daemon restart who answers %q, pending %v; was %q", r.Name, r.NamePending, name)
	}
	eventually(t, "b to show the name at the new address", func() bool { return b.nameOf(other) == name })
	if reg := readRegistry(t, remote); len(reg.assigned()) != 1 {
		t.Fatalf("the registry has %d assignments for one key:\n%s", len(reg.assigned()), reg.text)
	}
}

// Different keys get different names, and so do two MCP servers live at once in one client
// session: they share a key but not an address. Each keeps its own name when it reconnects.
func TestTwoServersOfOneSessionDoNotShareAName(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	const key = "claude-shared-0001"
	first, c1 := a.session("aaaaaa", key)
	second, c2 := a.session("bbbbbb", key)
	third, _ := a.session("cccccc", "claude-other-0002")
	fourth, _ := a.session("dddddd", "")
	fifth, _ := a.session("eeeeee", "")
	addrs := []string{first, second, third, fourth, fifth}
	seen := map[string]string{}
	for _, addr := range addrs {
		n := a.named(addr)
		if o, dup := seen[n]; dup {
			t.Fatalf("%s and %s are both named %s", o, addr, n)
		}
		seen[n] = addr
	}
	n1, n2 := a.nameOf(first), a.nameOf(second)

	live := func(id string) bool {
		for _, dv := range a.do(proto.Request{Op: proto.OpWho}).Devices {
			for _, s := range dv.Sessions {
				if s.Addr == a.self()+"/"+id {
					return true
				}
			}
		}
		return false
	}
	c2.Close()
	if !live("bbbbbb") {
		t.Fatal("the second server's session left presence at once; the grace should keep it")
	}
	eventually(t, "the second server's drop", func() bool {
		_, err := a.restartedMCP("bbbbbb", key)
		return err == nil
	})
	if got := a.nameOf(second); got != n2 {
		t.Fatalf("the second server reconnected as %q, was %q", got, n2)
	}
	c1.Close()
	eventually(t, "the first server's drop", func() bool {
		_, err := a.restartedMCP("aaaaaa", key)
		return err == nil
	})
	if got := a.nameOf(first); got != n1 {
		t.Fatalf("the first server reconnected as %q, was %q", got, n1)
	}
	noNameTwice(t, readRegistry(t, remote))
}

// The registry rewound by a force-push to an earlier commit frees none of the names it dropped:
// the daemon that read them writes a root commit holding every name, and a daemon that joins later
// and would pick them first learns they are taken.
func TestRewoundRegistryFreesNoName(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.names, d.grace = seededNames(31), 300*time.Millisecond })
	var taken []string
	var conns []io.Closer
	for i := range 3 {
		addr, conn := a.session("aaaaa"+string(rune('1'+i)), "claude-rewind-"+string(rune('1'+i)))
		taken = append(taken, a.named(addr))
		conns = append(conns, conn)
	}
	// The second session stops, so its name lives on only in history.
	conns[1].Close()
	eventually(t, "the second session stopped", func() bool { return !a.seesSession(a.self() + "/aaaaa2") })

	work, shas := registryCommits(t, remote)
	if len(shas) != 3 {
		t.Fatalf("the registry has %d commits, want 3", len(shas))
	}
	git(t, work, "push", "-q", "--force", remote, shas[0]+":refs/murmur/names")

	eventually(t, "a root snapshot of every name", func() bool { return tipIsSnapshot(t, remote, taken...) })
	b := newDeviceWith(t, remote, func(d *device) { d.names = seededNames(31) })
	bob, _ := b.session("bbbbbb", "claude-bob")
	if got := b.named(bob); got != nthName(t, 31, 4) {
		t.Fatalf("b is named %s; want the first name nobody had, %s (taken: %q)", got, nthName(t, 31, 4), taken)
	}
}

// A registry rewound while its daemon can't push, and then added to by another writer before the
// daemon is back, still gets the snapshot: the daemon must not take a plain new commit on the
// damaged history for the repair it was waiting to make. Otherwise a name it saw is gone from the
// registry for good, and a daemon joining later takes it.
func TestRepairSurvivesACommitBeforeIt(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.names, d.grace = seededNames(41), 300*time.Millisecond })
	first, _ := a.session("aaaaa1", "claude-first")
	n1 := a.named(first)
	second, conn := a.session("aaaaa2", "claude-second")
	n2 := a.named(second)
	conn.Close()
	eventually(t, "the second session stopped", func() bool { return !a.seesSession(second) })

	a.pushOffline(remote, true)
	work, shas := registryCommits(t, remote)
	git(t, work, "push", "-q", "--force", remote, shas[0]+":refs/murmur/names")
	eventually(t, "a to see that the registry lost a name", func() bool {
		st, ok := a.namesState()
		return ok && st.Repair && st.Tip == shas[0]
	})

	// Another writer, which never knew n2, adds a name on the rewound registry.
	outsider := nthName(t, 999, 1)
	rec := bus.NameRecord{V: bus.Version, Name: outsider, Device: "peer-0001", Session: "x", At: time.Now().UTC()}
	msg, err := bus.Encode(rec, rec.Subject())
	if err != nil {
		t.Fatal(err)
	}
	pushMessage(t, remote, "refs/murmur/names", msg)
	_, after := registryCommits(t, remote)
	eventually(t, "a to read the new commit", func() bool {
		st, ok := a.namesState()
		return ok && st.Tip == after[len(after)-1]
	})

	a.pushOffline(remote, false)
	eventually(t, "a root snapshot of every name", func() bool { return tipIsSnapshot(t, remote, n1, n2, outsider) })
	b := newDeviceWith(t, remote, func(d *device) { d.names = seededNames(41) }) // would pick n1, then n2
	bob, _ := b.session("bbbbbb", "claude-bob")
	if got := b.named(bob); got == n1 || got == n2 {
		t.Fatalf("b took %s, which a had", got)
	}
}

// What isn't a Murmur registry at the tip, a blob or an unrelated commit, frees no name, including
// one seen only in another device's presence: the next write is a root commit holding every name,
// and a daemon that joins once that presence is gone learns them from it.
func TestGarbageRegistryTip(t *testing.T) {
	garbage := map[string]func(t *testing.T, remote string){
		"blob": func(t *testing.T, remote string) {
			work, _ := junkTree(t)
			blob := gitIn(t, work, "not a registry\n", "hash-object", "-w", "--stdin")
			git(t, work, "push", "-q", "--force", remote, blob+":refs/murmur/names")
		},
		"unrelated commit": func(t *testing.T, remote string) {
			work, tree := junkTree(t)
			sha := gitIn(t, work, "names: none\n\n{not json\n", "commit-tree", tree)
			git(t, work, "push", "-q", "--force", remote, sha+":refs/murmur/names")
		},
	}
	for _, kind := range []string{"blob", "unrelated commit"} {
		t.Run(kind, func(t *testing.T) {
			remote := newRemote(t)
			p := draws(t, 52, 1)[0]
			at := time.Now().UTC().Format(time.RFC3339Nano)
			pushMessage(t, remote, "refs/murmur/presence/peer-0001", "peer-0001: 1 sessions\n\n"+
				`{"v":1,"device":"peer-0001","host":"peer","at":"`+at+`","sessions":[`+
				`{"id":"x","name":"`+p.name+`","agent":"test","started":"`+at+`"}]}`+"\n")
			a := newDeviceWith(t, remote, func(d *device) { d.names, d.stale = seededNames(51), time.Minute })
			eventually(t, "the peer's name on a", func() bool { return a.nameOf("peer-0001/x") == p.name })
			alice, _ := a.session("aaaaaa", "claude-alice")
			n1 := a.named(alice)

			garbage[kind](t, remote)
			eventually(t, "a root snapshot of every name", func() bool { return tipIsSnapshot(t, remote, n1, p.name) })

			work, _ := junkTree(t)
			git(t, work, "push", "-q", remote, ":refs/murmur/presence/peer-0001")
			eventually(t, "the peer gone from a", func() bool { return !a.seesSession("peer-0001/x") })
			b := newDeviceWith(t, remote, func(d *device) {
				d.names = offering(seededNames(51), p) // would pick the peer's, then a's
			})
			bob, _ := b.session("bbbbbb", "claude-bob")
			if got := b.named(bob); got == n1 || got == p.name {
				t.Fatalf("b took %s, which was in use", got)
			}
			noNameTwice(t, readRegistry(t, remote))
		})
	}
}

// A name a real peer's presence shows, and that the registry doesn't hold (here it was overwritten
// while that peer couldn't push), is used: a daemon joining then, whose source keeps offering that
// name, takes another, and writes it into the registry it rebuilds.
func TestNameOnlyInPresenceIsUsed(t *testing.T) {
	remote := newRemote(t)
	pick := draws(t, 61, 1)[0]
	b := newDeviceWith(t, remote, func(d *device) { d.names = seededNames(61) })
	bob, _ := b.session("bbbbbb", "claude-bob")
	if got := b.named(bob); got != pick.name {
		t.Fatalf("b is named %s, want %s", got, pick.name)
	}
	eventually(t, "b's presence with the name", func() bool {
		// A device pushes one ref at a time, so b's presence can land after its name does.
		if git(t, remote, "for-each-ref", "refs/murmur/presence/"+b.self()) == "" {
			return false
		}
		work := t.TempDir()
		git(t, work, "init", "-q")
		git(t, work, "fetch", "-q", remote, "refs/murmur/presence/"+b.self())
		return strings.Contains(git(t, work, "log", "-1", "--format=%B", "FETCH_HEAD"), pick.name)
	})
	b.pushOffline(remote, true)
	work, tree := junkTree(t)
	git(t, work, "push", "-q", "--force", remote, tree+":refs/murmur/names")

	a := newDeviceWith(t, remote, func(d *device) {
		d.names, d.stale = offering(seededNames(62), pick, pick, pick), time.Minute
	})
	eventually(t, "b's name on a", func() bool { return a.nameOf(bob) == pick.name })
	alice, _ := a.session("aaaaaa", "claude-alice")
	an := a.named(alice)
	if an == pick.name {
		t.Fatalf("a took %s, which b's presence shows", an)
	}
	eventually(t, "the registry to hold both", func() bool { return readRegistry(t, remote).holdsNames(an, pick.name) })
}

// Without the remote a session works, its name pending, and it gets one soon after the remote is
// back, with no hello to prompt it: not only when a retry delay that grew while the remote was out
// of reach runs out.
func TestPendingNameLandsSoonAfterTheRemoteIsBack(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.retry = 15 * time.Second })
	b := newDevice(t, remote)
	eventually(t, "a and b to see each other", func() bool { return a.seesDevices(2) && b.seesDevices(2) })
	a.offline(remote, true)
	alice, _ := a.session("aaaaaa", "claude-alice")
	r := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})
	if r.Name != "" || !r.NamePending || r.Self != alice || !strings.Contains(view.Who(r), "name pending") {
		t.Fatalf("who while offline: name %q, pending %v, self %q:\n%s", r.Name, r.NamePending, r.Self, view.Who(r))
	}
	if r := a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "nameless"}); r.Error != "" {
		t.Fatalf("status while offline: %s", r.Error)
	}
	if r := a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}); r.Error != "" {
		t.Fatalf("inbox while offline: %s", r.Error)
	}
	holds(t, "no name while the remote is out of reach", 1500*time.Millisecond, func() bool { return a.nameOf(alice) == "" })

	a.offline(remote, false)
	back := time.Now()
	name := a.named(alice)
	if took := time.Since(back); took > 6*time.Second {
		t.Fatalf("the name landed %s after the remote was back", took.Round(100*time.Millisecond))
	}
	eventually(t, "b to show alice's name", func() bool { return b.nameOf(alice) == name })
}

// send by name reaches the named session and no other, on another device, on this one, and after
// the session came back under another ID. Every other form of address still works, an unknown name
// is an error that names it, and on the bus a message is only ever addressed.
func TestSendByNameAcrossDevices(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDevice(t, remote)
	c := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep = 300*time.Millisecond, time.Hour })
	alice, _ := a.session("aaaaa1", "claude-alice")
	anna, _ := a.session("aaaaa2", "claude-anna")
	bob, _ := b.session("bbbbbb", "claude-bob")
	cleo, cleoConn := c.session("cccccc", "claude-cleo")
	carl, _ := c.session("cccccd", "claude-carl")
	_, annaN, bobN, cleoN, carlN := a.named(alice), a.named(anna), b.named(bob), c.named(cleo), c.named(carl)
	eventually(t, "a to show every name", func() bool {
		return a.nameOf(bob) == bobN && a.nameOf(cleo) == cleoN && a.nameOf(carl) == carlN
	})

	send := func(to, subject string) proto.Response {
		t.Helper()
		r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaa1", To: to, Subject: subject})
		if r.Error != "" || r.Note != "" {
			t.Fatalf("send to %s: %+v", to, r)
		}
		return r
	}
	if r := send(cleoN, "to cleo"); r.Messages[0].To != cleo {
		t.Fatalf("sent to %s, want %s", r.Messages[0].To, cleo)
	}
	if r := send(annaN, "to anna"); r.Messages[0].To != anna {
		t.Fatalf("sent to %s, want %s", r.Messages[0].To, anna)
	}
	if got := inboxOf(c, "cccccc"); len(got) != 1 || got[0] != "to cleo" {
		t.Fatalf("cleo's inbox: %q", got)
	}
	if got := inboxOf(a, "aaaaa2"); len(got) != 1 || got[0] != "to anna" {
		t.Fatalf("anna's inbox: %q", got)
	}
	for _, s := range []struct {
		d  *device
		id string
	}{{c, "cccccd"}, {b, "bbbbbb"}} {
		if got := s.d.do(proto.Request{Op: proto.OpInbox, Session: s.id}).Messages; len(got) != 0 {
			t.Fatalf("%s got %+v", s.id, got)
		}
	}

	unknown := nthName(t, 777, 1)
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaa1", To: unknown, Subject: "x"}); !strings.Contains(r.Error, unknown) ||
		!strings.Contains(r.Error, "no session is named") {
		t.Fatalf("send to an unknown name: %+v", r)
	}

	send(bob, "address")
	send(c.self(), "device")
	send("*", "everyone")
	if got := inboxWant(b, "bbbbbb", 2); strings.Join(sorted(got), ",") != "address,everyone" {
		t.Fatalf("bob's inbox: %q", got)
	}
	if got := inboxWant(c, "cccccd", 2); strings.Join(sorted(got), ",") != "device,everyone" {
		t.Fatalf("carl's inbox: %q", got)
	}

	// Cleo stops and comes back under another ID; her name follows her there, and so does what she
	// hadn't read (#37): the device's and everyone's messages from above.
	cleoConn.Close()
	eventually(t, "cleo stopped", func() bool { return !a.seesSession(cleo) })
	cleo2, _ := c.session("ccccce", "claude-cleo")
	if got := c.nameOf(cleo2); got != cleoN {
		t.Fatalf("cleo is back as %q, was %q", got, cleoN)
	}
	eventually(t, "a to show cleo at her new address", func() bool { return a.nameOf(cleo2) == cleoN })
	if r := send(cleoN, "to cleo again"); r.Messages[0].To != cleo2 {
		t.Fatalf("sent to %s, want %s", r.Messages[0].To, cleo2)
	}
	if got := inboxWant(c, "ccccce", 3); strings.Join(sorted(got), ",") != "device,everyone,to cleo again" {
		t.Fatalf("cleo's new inbox: %q", got)
	}

	// On the bus, every message is addressed: To is never a name.
	for _, line := range strings.Split(busText(t, remote, a.self()), "\n") {
		var e bus.Event
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &e) != nil || e.Type != bus.Message {
			continue
		}
		if names.Valid(e.To) || !bus.ValidTo(e.To) {
			t.Fatalf("a message on the bus is to %q", e.To)
		}
	}
}

func sorted(s []string) []string {
	s = append([]string(nil), s...)
	sort.Strings(s)
	return s
}

// The client's own session ID goes nowhere on the bus: not in the registry, presence, any event
// or any object a fetch of refs/murmur/* brings, whatever the session does, including a second
// server in the same client session, a stop and return, a daemon restart and a registry repair.
func TestClientSessionIDIsNeverOnTheBus(t *testing.T) {
	const secret = "zq-secret-claude-session-0000-1111"
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep = 300*time.Millisecond, time.Hour })
	b := newDevice(t, remote)
	alice, conn := a.session("aaaaaa", secret)
	twin, _ := a.session("aaaaab", secret)
	bob, _ := b.session("bbbbbb", "claude-bob")
	an, _, bn := a.named(alice), a.named(twin), b.named(bob)
	eventually(t, "names across devices", func() bool { return a.nameOf(bob) == bn && b.nameOf(alice) == an })

	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "working"})
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bn, Subject: "hi bob"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: an, Subject: "hi alice"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	inboxOf(a, "aaaaaa")
	conn.Close()
	eventually(t, "alice stopped", func() bool { return busLines(a, alice, "stopped") == 1 })
	a.restart()
	a.session("aaaaaa", secret)
	work, tree := junkTree(t)
	git(t, work, "push", "-q", "--force", remote, tree+":refs/murmur/names")
	eventually(t, "the registry repaired", func() bool { return tipIsSnapshot(t, remote, an, bn) })
	eventually(t, "alice back on b", func() bool { return b.nameOf(alice) == an })

	dump := t.TempDir()
	git(t, dump, "init", "-q")
	git(t, dump, "fetch", "-q", remote, "+refs/murmur/*:refs/murmur/*")
	everything := git(t, dump, "cat-file", "--batch-all-objects", "--batch") + git(t, dump, "for-each-ref")
	for _, bad := range []string{secret, "secret-claude", "claude:"} {
		if strings.Contains(everything, bad) {
			t.Fatalf("%q is on the bus", bad)
		}
	}
}

// A session whose key comes back under another ID while the old one is still in its grace takes
// the name with it, and a peer sending to the name reaches the live session, not the one about to
// stop, whose inbox nobody would read.
func TestNameMovesToTheLiveSessionInTheGrace(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote) // the default grace, 60 s: the old session outlasts the test
	b := newDevice(t, remote)
	const key = "claude-alice-0001"
	old, conn := a.session("aaaaaa", key)
	name := a.named(old)
	conn.Close()
	eventually(t, "the drop", func() bool { return a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: key}).Self == "" })
	newer, _ := a.session("aaaaab", key)
	if got := a.nameOf(newer); got != name {
		t.Fatalf("the session under its new ID is %q, was %q", got, name)
	}
	eventually(t, "b to show the name on the live session only", func() bool {
		return b.nameOf(newer) == name && b.seesSession(old) && b.nameOf(old) == ""
	})
	r := b.do(proto.Request{Op: proto.OpSend, To: name, Subject: "by name"})
	if r.Error != "" || r.Messages[0].To != newer {
		t.Fatalf("send by name went to %+v, want %s", r, newer)
	}
	if got := inboxOf(a, "aaaaab"); len(got) != 1 || got[0] != "by name" {
		t.Fatalf("the live session's inbox: %q", got)
	}
}

// A registry rewound while its daemon was stopped is repaired when it starts, even when that is the
// first start since the daemon kept people's claims (#268), which reads the registry again from its
// start: that read mustn't hide the damage.
func TestAnUpgradedDaemonStillRepairsARewoundRegistry(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.names, d.grace = seededNames(53), 300*time.Millisecond })
	var taken []string
	for i := range 3 {
		addr, _ := a.session("aaaaa"+string(rune('1'+i)), "claude-upgrade-"+string(rune('1'+i)))
		taken = append(taken, a.named(addr))
	}
	a.stop()
	work, shas := registryCommits(t, remote)
	if len(shas) != 3 {
		t.Fatalf("the registry has %d commits, want 3", len(shas))
	}
	git(t, work, "push", "-q", "--force", remote, shas[0]+":refs/murmur/names")
	setState(t, a, "namesPeople", false) // as a daemon from before left it
	a.start()
	eventually(t, "a root snapshot of every name", func() bool { return tipIsSnapshot(t, remote, taken...) })
}
