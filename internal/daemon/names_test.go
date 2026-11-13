package daemon_test

import (
	"encoding/binary"
	"io"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// Tests of session names (#20): a name is taken by compare-and-swap on refs/murmur/names, follows
// the client's session ID, and is the session's address.

// seededNames is a reproducible stream of random bytes: devices given streams with the same seed
// pick the same names in the same order.
func seededNames(seed uint64) io.Reader {
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:], seed)
	return rand.NewChaCha8(key)
}

// nthName is the name the nth pick (from 1) of a stream with this seed makes, when nothing is
// taken.
func nthName(t *testing.T, seed uint64, n int) string {
	t.Helper()
	src := seededNames(seed)
	var name string
	for range n {
		var err error
		if name, err = names.Generate(src); err != nil {
			t.Fatal(err)
		}
	}
	return name
}

// gate holds the first pick of every device that arrives at it until all n have, so that all of
// them choose a name before any of them has pushed one.
type gate struct {
	mu   sync.Mutex
	need int
	open chan struct{}
}

func newGate(n int) *gate { return &gate{need: n, open: make(chan struct{})} }

func (g *gate) arrive() {
	g.mu.Lock()
	if g.need--; g.need == 0 {
		close(g.open)
	}
	g.mu.Unlock()
	select {
	case <-g.open:
	case <-time.After(20 * time.Second): // the test fails on what follows, not by hanging
	}
}

// countedReader is a name source that counts the bytes drawn from it, and holds its first draw at
// a gate if it has one.
type countedReader struct {
	r    io.Reader
	gate *gate
	once sync.Once
	n    atomic.Int64
}

func (c *countedReader) Read(p []byte) (int, error) {
	if c.gate != nil {
		c.once.Do(c.gate.arrive)
	}
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// newNamedDevice is newDevice with a source for the names its sessions pick.
func newNamedDevice(t *testing.T, remote string, src io.Reader, stale ...time.Duration) *device {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	d := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond,
		names: src}
	if len(stale) > 0 {
		d.stale = stale[0]
	}
	d.start()
	return d
}

// nameOf is the name d's who shows for the session at addr, "" if it shows none.
func (d *device) nameOf(addr string) string {
	for _, dv := range d.do(proto.Request{Op: proto.OpWho}).Devices {
		for _, s := range dv.Sessions {
			if s.Addr == addr {
				return s.Name
			}
		}
	}
	return ""
}

// named waits for the session at addr to have a name on d, and returns it.
func (d *device) named(addr string) string {
	d.t.Helper()
	var name string
	eventually(d.t, "a name for "+addr, func() bool { name = d.nameOf(addr); return name != "" })
	if !names.Valid(name) {
		d.t.Fatalf("%s is named %q, which names.Generate couldn't make", addr, name)
	}
	return name
}

// inboxWant is inboxOf for several messages that arrive on their own schedules: it waits for n,
// then for a moment more, in case there are others.
func inboxWant(d *device, sess string, n int) []string {
	var got []string
	read := func() {
		for _, m := range d.do(proto.Request{Op: proto.OpInbox, Session: sess}).Messages {
			got = append(got, m.Subject)
		}
	}
	eventually(d.t, "messages for "+sess, func() bool { read(); return len(got) >= n })
	time.Sleep(500 * time.Millisecond)
	read()
	return got
}

// registry is refs/murmur/names on the remote as read from outside Murmur.
type registry struct {
	tip      string
	isCommit bool
	records  []bus.NameRecord // every commit in the tip's history, oldest first
	text     string           // their messages
	roots    int              // commits in that history with no parent
}

func readRegistry(t *testing.T, remote string) registry {
	t.Helper()
	work := t.TempDir()
	git(t, work, "init", "-q")
	var r registry
	out := git(t, work, "ls-remote", remote, "refs/murmur/names")
	if out == "" {
		return r
	}
	// The tip is what was fetched, not what ls-remote showed: a daemon may write in between.
	git(t, work, "fetch", "-q", remote, "refs/murmur/names")
	r.tip = git(t, work, "rev-parse", "FETCH_HEAD")
	if git(t, work, "cat-file", "-t", r.tip) != "commit" {
		return r
	}
	r.isCommit = true
	r.text = git(t, work, "log", "--reverse", "--format=%B%x1e", r.tip)
	for _, msg := range strings.Split(r.text, "\x1e") {
		if strings.TrimSpace(msg) == "" {
			continue
		}
		var rec bus.NameRecord
		if err := bus.Decode(strings.TrimSpace(msg), &rec); err != nil {
			t.Fatalf("registry commit %q: %v", msg, err)
		}
		r.records = append(r.records, rec)
	}
	r.roots = len(strings.Fields(git(t, work, "rev-list", "--max-parents=0", r.tip)))
	return r
}

// holdsNames says whether the registry, as a daemon joining now would read it, has every name.
func (r registry) holdsNames(want ...string) bool {
	have := map[string]bool{}
	for _, rec := range r.records {
		for name := range rec.Claims() {
			have[name] = true
		}
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return len(r.records) > 0
}

// assigned lists the names the registry's commits assign (not snapshots), oldest first.
func (r registry) assigned() []string {
	var got []string
	for _, rec := range r.records {
		if rec.Name != "" {
			got = append(got, rec.Name)
		}
	}
	return got
}

// busText is every commit message of device dev's log on the remote.
func busText(t *testing.T, remote string, d *device) string {
	t.Helper()
	return openedLog(t, remote, d)
}

// Two devices that pick the same name at the same moment can't both have it: the leased push of
// one lands, the other is refused, reads what it missed and picks again.
func TestSimultaneousAssignmentsCannotShareAName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newGate(2)
	ra := &countedReader{r: seededNames(1), gate: g}
	rb := &countedReader{r: seededNames(1), gate: g} // the same stream, so the same first pick
	a, b := newNamedDevice(t, remote, ra), newNamedDevice(t, remote, rb)
	alice, _ := a.session("aaaaaa", "claude-alice")
	bob, _ := b.session("bbbbbb", "claude-bob")

	na, nb := a.named(alice), b.named(bob)
	if na == nb {
		t.Fatalf("both sessions are named %s", na)
	}
	if first := nthName(t, 1, 1); na != first && nb != first {
		t.Fatalf("neither got the name both picked first, %s: %s and %s", first, na, nb)
	}
	if drawn := ra.n.Load() + rb.n.Load(); drawn <= 16 {
		t.Fatalf("%d bytes drawn for two names; the one that lost should have picked again", drawn)
	}
	eventually(t, "each device to show the other's name", func() bool {
		return a.nameOf(bob) == nb && b.nameOf(alice) == na
	})

	reg := readRegistry(t, remote)
	got := reg.assigned()
	sort.Strings(got)
	want := []string{na, nb}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the registry assigns %q, want %q", got, want)
	}
	if reg.roots != 1 {
		t.Fatalf("the registry has %d roots; a refused push should not rewrite it", reg.roots)
	}
}

// A daemon joining later reads the whole history, not only the tip: a name taken in any commit is
// taken, and what it picks next is a name nobody has.
func TestLateJoinerSkipsEveryUsedName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newNamedDevice(t, remote, seededNames(3))
	var used []string
	for i, claude := range []string{"claude-1", "claude-2", "claude-3"} {
		addr, _ := a.session("aaaaa"+string(rune('1'+i)), claude)
		used = append(used, a.named(addr))
	}
	want := []string{nthName(t, 3, 1), nthName(t, 3, 2), nthName(t, 3, 3)}
	if strings.Join(used, ",") != strings.Join(want, ",") {
		t.Fatalf("a's names %q, want the stream's first three, %q", used, want)
	}

	b := newNamedDevice(t, remote, seededNames(3)) // would pick the same three first
	bob, _ := b.session("bbbbbb", "claude-bob")
	if got := b.named(bob); got != nthName(t, 3, 4) {
		t.Fatalf("b is named %s, want the first name nobody had: %s", got, nthName(t, 3, 4))
	}
}

// Different keys get different names, and a session with no client session ID is named by its
// own ID.
func TestDifferentKeysGetDifferentNames(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	var addrs []string
	for _, s := range []struct {
		d          *device
		id, claude string
	}{{a, "aaaaa1", "claude-1"}, {a, "aaaaa2", "claude-2"}, {b, "bbbbb1", "claude-3"}, {b, "bbbbb2", ""}, {a, "aaaaa3", ""}} {
		addr, _ := s.d.session(s.id, s.claude)
		addrs = append(addrs, addr)
	}
	seen := map[string]string{}
	for _, addr := range addrs {
		name := a.named(addr)
		if other, dup := seen[name]; dup {
			t.Fatalf("%s and %s are both named %s", other, addr, name)
		}
		seen[name] = addr
	}
}

// The same key keeps its name: across its MCP server restarting, across the ID Murmur gives it
// changing, and across the daemon restarting, which needs no registry to remember it. Nothing of
// the client's own session ID goes on the bus.
func TestSameKeyKeepsItsName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, conn := a.session("aaaaaa", "claude-alice-session-0001")
	name := a.named(alice)
	// Her start is on a's log on the remote before anything else happens. The test ends by reading
	// that log, and a start still unpushed when a goes offline below can be lost: a write that
	// fails in the background is logged, not sent again (appendSoon). Presence would still give b
	// her name, with a log that never reaches the remote (#410, Sol's review of #433).
	mesh := "refs/murmur/mesh/" + a.self()
	eventually(t, "her start on a's log", func() bool {
		return git(t, remote, "for-each-ref", mesh) != "" && strings.Contains(busText(t, remote, a), " started (")
	})

	conn.Close()
	// The daemon may not have seen the drop yet, and answers "in use" until it has: the MCP server
	// tries again, and so does the test.
	conn2, err := a.restartedMCP("aaaaaa", "claude-alice-session-0001")
	for end := time.Now().Add(10 * time.Second); err != nil && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
		conn2, err = a.restartedMCP("aaaaaa", "claude-alice-session-0001")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := a.nameOf(alice); got != name {
		t.Fatalf("after the MCP server restarted she is %q, was %q", got, name)
	}

	// The same client session under another ID, once the first is gone: the name follows the key.
	conn2.Close()
	eventually(t, "the daemon to see the connection drop", func() bool {
		return a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-alice-session-0001"}).Self == "" // only a live session gets its notices
	})
	other, conn3 := a.session("aaaaab", "claude-alice-session-0001")
	if got := a.nameOf(other); got != name {
		t.Fatalf("under another ID she is %q, was %q", got, name)
	}
	conn3.Close()

	// The daemon restarts and can't reach the remote: the name is there at once.
	a.offline(remote, true)
	a.restart()
	back, _ := a.session("aaaaaa", "claude-alice-session-0001")
	if got := a.nameOf(back); got != name {
		t.Fatalf("after the daemon restarted she is %q, was %q", got, name)
	}
	if resp := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}); resp.NamePending || resp.Name != name {
		t.Fatalf("who answers %q, pending %v", resp.Name, resp.NamePending)
	}
	a.offline(remote, false)
	eventually(t, "b to show her name", func() bool { return b.nameOf(back) == name })

	if reg := readRegistry(t, remote); strings.Contains(reg.text, "claude-alice") || len(reg.assigned()) != 1 {
		t.Fatalf("the registry should hold her one assignment and not her client session ID:\n%s", reg.text)
	}
	if strings.Contains(busText(t, remote, a), "claude-alice") {
		t.Fatal("the client's session ID is on the bus")
	}
}

// A session that stops and comes back is the same session with the same name, and a message sent
// to the name while it was away waits for it.
func TestNameSurvivesAStop(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, time.Second, 0)
	alice, _ := a.session("aaaaaa", "claude-alice")
	bob, conn := b.session("bbbbbb", "claude-bob")
	bn := b.named(bob)
	eventually(t, "a to show bob's name", func() bool { return a.nameOf(bob) == bn })

	conn.Close()
	eventually(t, "bob gone from a", func() bool { return !a.listsSession(bob) })
	// Not in presence, but his name still says who he is.
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bn, Subject: "while you were away"})
	if r.Error != "" || r.Messages[0].To != bob {
		t.Fatalf("send to a stopped session's name: %+v", r)
	}
	if !strings.Contains(r.Note, "presence") {
		t.Fatalf("the note should say nobody may read it: %q", r.Note)
	}

	b.session("bbbbbb", "claude-bob")
	if got := b.nameOf(bob); got != bn {
		t.Fatalf("back as %q, was %q", got, bn)
	}
	if got := inboxOf(b, "bbbbbb"); len(got) != 1 || got[0] != "while you were away" {
		t.Fatalf("bob's inbox: %q", got)
	}
	_ = alice
}

// A registry that is overwritten with junk, deleted, or rewritten frees no name: the daemons that
// read it keep every name they have seen, and write a new root with a snapshot of all of them, so
// that a daemon joining later, which would have picked the first name again, learns it is taken.
func TestDamagedRegistryFreesNoName(t *testing.T) {
	t.Parallel()
	damage := map[string]func(t *testing.T, remote string){
		"junk over it": func(t *testing.T, remote string) {
			work, tree := junkTree(t)
			git(t, work, "push", "-q", "--force", remote, tree+":refs/murmur/names")
		},
		"deleted": func(t *testing.T, remote string) {
			git(t, t.TempDir(), "init", "-q") // a scratch repo to push from
			work, _ := junkTree(t)
			git(t, work, "push", "-q", remote, ":refs/murmur/names")
		},
		"rewritten": func(t *testing.T, remote string) {
			rec := bus.NameRecord{V: bus.Version, Name: nthName(t, 99, 1), Device: "evil-0001", Session: "x", At: time.Now().UTC()}
			msg, err := bus.Encode(rec, rec.Subject())
			if err != nil {
				t.Fatal(err)
			}
			work, tree := junkTree(t)
			sha := gitIn(t, work, msg, "commit-tree", tree)
			git(t, work, "push", "-q", "--force", remote, sha+":refs/murmur/names")
		},
	}
	for _, kind := range []string{"junk over it", "deleted", "rewritten"} {
		t.Run(kind, func(t *testing.T) {
			remote := newRemote(t)
			a := newNamedDevice(t, remote, seededNames(7))
			alice, _ := a.session("aaaaaa", "claude-alice")
			first := a.named(alice)
			if first != nthName(t, 7, 1) {
				t.Fatalf("a is named %s, want the stream's first, %s", first, nthName(t, 7, 1))
			}
			if !readRegistry(t, remote).holdsNames(first) {
				t.Fatal("the registry doesn't hold her name")
			}

			damage[kind](t, remote)
			eventually(t, "a to put the registry right", func() bool {
				r := readRegistry(t, remote)
				return r.isCommit && r.holdsNames(first)
			})
			reg := readRegistry(t, remote)
			if kind == "rewritten" && !reg.holdsNames(nthName(t, 99, 1)) {
				t.Fatalf("the name in the rewritten registry was dropped:\n%s", reg.text)
			}

			b := newNamedDevice(t, remote, seededNames(7)) // would pick `first` again
			bob, _ := b.session("bbbbbb", "claude-bob")
			if got := b.named(bob); got == first {
				t.Fatalf("b took %s, which a had", got)
			}
			// And a, which saw it all happen, hasn't forgotten: a new session on it gets a new name too.
			carol, _ := a.session("aaaaab", "claude-carol")
			if got := a.named(carol); got == first || got == b.nameOf(bob) {
				t.Fatalf("a's new session took %s, which was in use", got)
			}
		})
	}
}

// Junk at the registry's tip before anyone has a name is built over, not stopped on: the first
// name is a new root, leased on the junk.
func TestFirstNameOverJunkRegistry(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	work, tree := junkTree(t)
	git(t, work, "push", "-q", remote, tree+":refs/murmur/names")
	a := newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-alice")
	name := a.named(alice)
	if reg := readRegistry(t, remote); !reg.isCommit || len(reg.assigned()) != 1 || reg.assigned()[0] != name {
		t.Fatalf("the registry: %+v", reg)
	}
}

// A ref under the registry's name keeps the registry off the remote, as one under a device's name
// does its presence. Nothing Murmur writes is there, so daemons delete it.
func TestRefUnderTheRegistryIsDeleted(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	work, tree := junkTree(t)
	git(t, work, "push", "-q", remote, tree+":refs/murmur/names/x")
	a := newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-alice")
	name := a.named(alice)
	eventually(t, "the registry on the remote", func() bool { return readRegistry(t, remote).holdsNames(name) })
}

// A name seen in another device's presence is in use, whether or not the registry shows it.
func TestNamesInPresenceAreUsed(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	taken := nthName(t, 5, 1)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	pushMessage(t, remote, "refs/murmur/presence/peer-0001", "peer-0001: 1 sessions\n\n"+
		`{"v":1,"device":"peer-0001","host":"peer","at":"`+at+`","sessions":[`+
		`{"id":"x","name":"`+taken+`","agent":"test","started":"`+at+`"}]}`+"\n")
	a := newNamedDevice(t, remote, seededNames(5), time.Minute) // would pick `taken` first
	alice, _ := a.session("aaaaaa", "claude-alice")
	if got := a.named(alice); got == taken {
		t.Fatalf("a took %s, which is in a peer's presence", got)
	}
	eventually(t, "the peer's name in who", func() bool { return a.nameOf("peer-0001/x") == taken })
}

// Without a reachable remote a session works, addressed as device/session, with its name pending;
// the name is taken in the background when the remote is back.
func TestNameIsPendingUntilItLands(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	a.offline(remote, true)
	alice, _ := a.session("aaaaaa", "claude-alice")
	resp := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})
	if resp.Name != "" || !resp.NamePending || resp.Self != alice {
		t.Fatalf("who answers name %q, pending %v, self %q", resp.Name, resp.NamePending, resp.Self)
	}
	text := view.Who(resp)
	if !strings.Contains(text, "name pending") || !strings.Contains(text, alice) {
		t.Fatalf("who shows:\n%s", text)
	}
	if r := a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "working without a name"}); r.Error != "" {
		t.Fatalf("status: %s", r.Error)
	}
	holds(t, "alice without a name while the remote is out of reach", time.Second, func() bool {
		return a.nameOf(alice) == ""
	})

	a.offline(remote, false)
	name := a.named(alice)
	text = view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}))
	if strings.Contains(text, "pending") || !strings.Contains(text, "You are "+name+" ("+alice+")") {
		t.Fatalf("who shows:\n%s", text)
	}
	eventually(t, "the registry", func() bool { return readRegistry(t, remote).holdsNames(name) })
}

// send takes a name. The message is addressed to the session's device/session on the bus, so a
// daemon that doesn't know names queues it all the same; and the recipient sees who sent it by
// name.
func TestSendByName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-alice")
	bob, _ := b.session("bbbbbb", "claude-bob")
	cleo, _ := b.session("cccccc", "claude-cleo")
	an, bn, cn := a.named(alice), b.named(bob), b.named(cleo)
	a.nameIs(bob, bn)
	a.nameIs(cleo, cn)
	// b names alice's agent in its notice, which is in a's encrypted presence: b reads it once it
	// holds the team key, and a name alone, which the registry gives in the clear, comes sooner.
	eventually(t, "b to read alice's presence", func() bool { return b.seesSession(alice) })

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bn, Subject: "by name", Body: "the body"})
	if r.Error != "" || r.Note != "" || len(r.Messages) != 1 || r.Messages[0].To != bob || r.Messages[0].ToName != bn {
		t.Fatalf("send: %+v", r)
	}
	if text := view.Sent(r); !strings.Contains(text, bn+" ("+bob+")") {
		t.Fatalf("sent view: %q", text)
	}

	var notices []proto.MessageView
	eventually(t, "a notice", func() bool {
		notices = b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"}).Messages
		return len(notices) > 0
	})
	if n := notices[0]; n.From != alice || n.FromName != an || n.To != bob || n.Subject != "by name" {
		t.Fatalf("notice: %+v", n)
	}
	if text := view.Notices(notices, 0); !strings.Contains(text, "[Murmur] "+an+" ("+alice+", test)") {
		t.Fatalf("notice text: %q", text)
	}
	inbox := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"})
	text := view.Inbox(inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].Body != "the body" ||
		!strings.Contains(text, "message from "+an+" ("+alice+")") || !strings.Contains(text, "(Reply to "+an+".)") {
		t.Fatalf("inbox:\n%s", text)
	}
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(got) != 0 {
		t.Fatalf("the other session on that device got %+v", got)
	}

	// On the bus the message is addressed, not named.
	bus := busText(t, remote, a)
	if !strings.Contains(bus, `"to":"`+bob+`"`) || strings.Contains(bus, `"to":"`+bn) {
		t.Fatalf("the message on the bus:\n%s", bus)
	}

	// The reply goes back by name.
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: an, Subject: "reply"}); r.Error != "" || r.Messages[0].To != alice {
		t.Fatalf("reply: %+v", r)
	}
	if got := inboxOf(a, "aaaaaa"); len(got) != 1 || got[0] != "reply" {
		t.Fatalf("alice's inbox: %q", got)
	}
	// who marks the caller.
	who := view.Who(b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"}))
	for _, line := range strings.Split(who, "\n") {
		if strings.HasPrefix(line, "  ") && strings.Contains(line, bob) != strings.Contains(line, "(you)") {
			t.Fatalf("who marks (you) on the wrong line %q:\n%s", line, who)
		}
	}
	if !strings.Contains(who, "You are "+bn+" ("+bob+").") || !strings.Contains(who, an+"  "+alice) {
		t.Fatalf("who:\n%s", who)
	}
}

// A name nobody has is an error that says so, and every other form of address still works.
func TestUnknownNameErrorsAndOtherAddressesWork(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-alice")
	bob, _ := b.session("bbbbbb", "claude-bob")
	a.named(alice)
	bn := b.named(bob)
	a.nameIs(bob, bn)

	unknown := nthName(t, 424242, 1)
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: unknown, Subject: "to nobody"})
	if !strings.Contains(r.Error, "no session is named") || !strings.Contains(r.Error, unknown) {
		t.Fatalf("send to an unknown name: %+v", r)
	}
	for _, bad := range []string{"quiet", "Quiet Otter", "quiet-otter-extra"} {
		if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bad, Subject: "x"}); r.Error == "" && !strings.Contains(r.Note, "presence") {
			t.Fatalf("send to %q: %+v", bad, r)
		}
	}

	for to, subject := range map[string]string{bob: "address", b.self(): "device", "*": "everyone", b.self() + "/cli": "cli"} {
		if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: to, Subject: subject}); r.Error != "" || r.Note != "" {
			t.Fatalf("send to %s: %+v", to, r)
		}
	}
	got := inboxWant(b, "bbbbbb", 3)
	sort.Strings(got)
	if strings.Join(got, ",") != "address,device,everyone" {
		t.Fatalf("bob's inbox: %q", got)
	}
	got = inboxWant(b, "", 3)
	sort.Strings(got)
	if strings.Join(got, ",") != "cli,device,everyone" {
		t.Fatalf("b's CLI inbox: %q", got)
	}
}

// A daemon without names publishes none and doesn't read the registry. Its sessions show without
// a name, and are reached and heard from at device/session as before.
func TestV1PeerWithoutNamesStillWorks(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote, time.Minute)
	alice, _ := a.session("aaaaaa", "claude-alice")
	an := a.named(alice)

	at := time.Now().UTC().Format(time.RFC3339Nano)
	pushMessage(t, remote, "refs/murmur/presence/old-0001", "old-0001: 1 sessions\n\n"+
		`{"v":1,"device":"old-0001","host":"old","at":"`+at+`","sessions":[`+
		`{"id":"x","agent":"claude-code","started":"`+at+`"}]}`+"\n")
	eventually(t, "the old device's session", func() bool { return a.seesSession("old-0001/x") })
	for _, dv := range a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}).Devices {
		for _, s := range dv.Sessions {
			if dv.ID == "old-0001" && (s.Name != "" || s.Pending) {
				t.Fatalf("the old session shows as %+v", s)
			}
		}
	}

	// It can't read what this device writes, which is encrypted (#156), so a message to it is
	// refused rather than sent where it can't be read.
	for _, to := range []string{"old-0001/x", "old-0001"} {
		r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: to, Subject: "hello old"})
		if !strings.Contains(r.Error, "before the bus was encrypted") {
			t.Fatalf("send to %s: %+v", to, r)
		}
	}
	// Its log is there once its session's start is written, which waits for the first team key.
	eventually(t, "a's log on the remote", func() bool {
		return strings.Contains(git(t, remote, "for-each-ref", "refs/murmur/mesh/"), a.self())
	})
	if text := busText(t, remote, a); strings.Contains(text, "hello old") {
		t.Fatalf("the message went out:\n%s", text)
	}

	pushEvent(t, remote, "refs/murmur/mesh/old-0001", bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: "old-0001/x", At: time.Now().UTC(), To: alice, Subject: "hello new"})
	var msgs []proto.MessageView
	eventually(t, "the old session's message", func() bool {
		msgs = a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-alice"}).Messages
		return len(msgs) > 0
	})
	if m := msgs[0]; m.From != "old-0001/x" || m.FromName != "" || m.FromAgent != "claude-code" {
		t.Fatalf("notice: %+v", m)
	}
	if text := view.Notices(msgs, 0); !strings.Contains(text, `[Murmur] old-0001/x (claude-code): "hello new"`) {
		t.Fatalf("notice text: %q", text)
	}
	_ = an
}
