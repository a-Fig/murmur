package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A turn's handoff records what the session was told against the conversation it went to (#345):
// by its name, if that lands while the handoff is under way, and never against another
// conversation the session has /cleared or /resumed into meanwhile, named or not.
func TestAHandoffRecordsAgainstItsOwnConversation(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.st.NameKeys = map[string]string{}
	d.st.T3Routes = map[string]t3Route{}
	d.mu.Lock()
	defer d.mu.Unlock()
	ada := "dev-0001/" + bus.PersonSession
	s := d.sessions["aaaaaa"]
	job := func(id string) *pushJob {
		return &pushJob{id: id, key: d.keyOfLocked(id), conv: d.conversationLocked(id), explainKey: d.explainKeyLocked(id)}
	}
	finish := func(j *pushJob) { d.noteExplainedAtLocked(d.explainKeyOfPushLocked(j), ada) }

	// Its name lands while the handoff is under way.
	s.name = ""
	j := job(s.id)
	if j.explainKey != "conv:t3:thread-aaaaaa" {
		t.Fatalf("a pending session is kept by %q", j.explainKey)
	}
	d.st.NameKeys[s.key] = "quiet-otter"
	d.setNameLocked(s, "quiet-otter")
	finish(j)
	if !d.explainedLocked(s.id, ada) || len(d.st.Explained) != 1 {
		t.Errorf("after the name landed: %v", d.st.Explained)
	}

	// It moves to another conversation, with another name, while the handoff is under way.
	d.st.Explained = nil
	j = job(s.id)
	s.key, s.name = "t3:thread-other", "brave-heron"
	finish(j)
	if d.explainedLocked(s.id, ada) || !slices.Contains(d.st.Explained["quiet-otter"], ada) {
		t.Errorf("after a move to a named conversation: %v", d.st.Explained)
	}

	// Its name lands, and then it moves to a conversation whose name is pending, all while the
	// handoff is under way: the new conversation mustn't take what the old one was told.
	d.st.Explained = nil
	b := d.sessions["bbbbbb"]
	b.name = ""
	j = job(b.id)
	d.st.NameKeys[b.key] = "calm-finch"
	d.setNameLocked(b, "calm-finch")
	b.key, b.name = "t3:thread-next", ""
	finish(j)
	if d.explainedLocked(b.id, ada) || !slices.Contains(d.st.Explained["calm-finch"], ada) {
		t.Errorf("after the name landed and a move to a pending conversation: %v", d.st.Explained)
	}
	d.st.NameKeys[b.key] = "late-wren"
	d.setNameLocked(b, "late-wren")
	if d.explainedLocked(b.id, ada) {
		t.Errorf("the new conversation took the old one's record when its name landed: %v", d.st.Explained)
	}

	// A session of T3 that had stopped, with no name yet, comes back in the same thread while the
	// handoff is under way.
	d.st.Explained = nil
	delete(d.sessions, "cccccc")
	d.st.T3Routes["cccccc"] = t3Route{CLI: "/t3", Thread: "thread-cccccc"}
	j = job("cccccc")
	if j.explainKey != "conv:t3:thread-cccccc" {
		t.Fatalf("a stopped session of T3 is kept by %q", j.explainKey)
	}
	inT3(d, "cccccc", 0)
	c := d.sessions["cccccc"]
	c.key, c.name = "t3:thread-cccccc", ""
	d.st.NameKeys[c.key] = "bold-lark"
	d.setNameLocked(c, "bold-lark")
	finish(j)
	if !d.explainedLocked(c.id, ada) || len(d.st.Explained) != 1 {
		t.Errorf("after the session came back and its name landed: %v", d.st.Explained)
	}
}

// explainRig is a device in dir with session bob in T3 thread thread-bob and idle, so a message
// for it is its next turn. name, if set, is the name bob's conversation has. A second rig in the
// same dir, with no name, is the daemon restarted: it reads the first's state.json, and bob
// registers again.
func explainRig(t *testing.T, dir, name string) *Daemon {
	t.Helper()
	d := threadsDaemon(t)
	d.dir = dir
	d.introduced, d.pushing, d.pushWarned = map[string]map[string]bool{}, map[string]bool{}, map[string]bool{}
	if err := d.loadState(); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if name != "" {
		d.st.NameKeys["t3:thread-bob"] = name
	}
	s := d.newSessionLocked(&proto.Hello{ID: "bbbbbb"}, "claude-code", "", "t3:thread-bob")
	s.route = proto.Route{T3CLI: "/t3", T3Thread: "thread-bob"}
	return d
}

// The first turn from ada explains, the second doesn't, grace's first does, and what bob has been
// told outlasts its daemon restarting.
//
// Kept from the three-device test it replaces (#1010): that the long line, naming the person, goes
// with a person's first turn to a session and not their next, that it is kept per person, and that
// it and the session's name are kept in state.json across a restart. A fake is enough, as all of
// that is the handoff's text and what state records: the turns are made and finished as the
// handoff loop makes them (nextPushLocked, finishPush), but for running t3; the people are known
// as a first sight of each makes them known (st.People); and the restart reads the state file a
// real one reads. What it takes a person's message to cross the bus to a turn is
// TestAPersonsGroupMessageLeavesThemToBeExplained's, end to end, and the hook's and inbox's
// TestTheHookAndTheInboxExplainAPersonOnce's. The three devices and a restart started about 790
// git: 22 s on a laptop and 74 s on a Windows runner (#1088's baseline).
func TestAPersonIsExplainedOnlyInTheirFirstTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := explainRig(t, dir, "brave-heron")
	ada, grace := "a-0001/"+bus.PersonSession, "c-0003/"+bus.PersonSession
	people := map[string]string{ada: "ada", grace: "grace"}
	d.mu.Lock()
	d.notePersonLocked("ada", ada)
	d.notePersonLocked("grace", grace)
	d.mu.Unlock()
	turn := func(d *Daemon, from, subject string) string {
		t.Helper()
		wake := d.wakeSnapshot()
		d.mu.Lock()
		d.queueInboxLocked("bbbbbb", bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
			From: from, Person: people[from], To: bus.Addr(d.device, "bbbbbb"), Subject: subject})
		j := d.nextPushLocked("bbbbbb", wake)
		d.mu.Unlock()
		if j == nil || j.via != viaT3 || len(j.msgs) != 1 || j.msgs[0].Event.Subject != subject {
			t.Fatalf("the turn for %q: %+v", subject, j)
		}
		d.finishPush(j, nil)
		return j.text
	}
	check := func(got, who string, long bool) {
		t.Helper()
		longLine := who + " sees only what you `send`, not this conversation. Answer with " + brand.Product + "'s `send` to " + who + "."
		shortLine := "Reply with " + brand.Product + "'s `send` to " + who + " if a reply is needed."
		if strings.Contains(got, longLine) != long || strings.Contains(got, shortLine) == long {
			t.Errorf("want the long line %v, got:\n%s", long, got)
		}
	}
	check(turn(d, ada, "first from ada"), "ada", true)
	check(turn(d, ada, "second from ada"), "ada", false)
	check(turn(d, grace, "first from grace"), "grace", true)

	d = explainRig(t, dir, "")
	if got := d.sessions["bbbbbb"].name; got != "brave-heron" {
		t.Fatalf("bob came back as %q", got)
	}
	check(turn(d, ada, "third from ada"), "ada", false)
}

// Only a session's first turn from a sender says what agent the sender is and whose machine it is
// on; later ones name it alone (#216). Another sender is introduced in its own first turn.
//
// Kept from the two-device test it replaces (#1010): what the first, second and another sender's
// first turn say of who sent them, in the text T3 is handed. A fake is enough, as that text is made
// from what the daemon holds: the sender's name, agent and owner as its device's presence gave them
// (peers), and which senders this conversation has had a turn from. The turns are made and
// finished as the handoff loop makes them (nextPushLocked, finishPush), but for running t3. That a
// first turn's description crosses the bus from a real presence to a real t3 call is
// TestMessageForAT3SessionIsHandedOverAsATurn's, and to a messaging socket
// TestMessageForAClaudeCodeSessionGoesThroughItsMessagingSocket's; that the turns after it name
// the sender alone, with the daemon's loops running between them, is
// TestMessagesForOneSessionAreHandedOverInOrder's. The two devices started about 310 git: 10 s on
// a laptop and 57 s on a Windows runner (main's run 45142751635).
func TestASenderIsDescribedOnlyInItsFirstTurn(t *testing.T) {
	t.Parallel()
	d := explainRig(t, t.TempDir(), "")
	alice, carol := "a-0001/aaaaaa", "a-0001/cccccc"
	d.mu.Lock()
	d.peers["a-0001"] = &peer{lastChange: time.Now(), pres: bus.Presence{V: bus.Version, Device: "a-0001", Owner: "Ada L",
		Sessions: []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "test"}, {ID: "cccccc", Name: "calm-finch", Agent: "test"}}}}
	d.mu.Unlock()
	for _, step := range []struct{ from, subject, want string }{
		{alice, "one", "Message from quiet-otter (test, on Ada L's machine) via " + brand.Product + ":\n\n> one\n"},
		{alice, "two", "Message from quiet-otter via " + brand.Product + ":\n\n> two\n"},
		{carol, "three", "Message from calm-finch (test, on Ada L's machine) via " + brand.Product + ":\n\n> three\n"},
	} {
		wake := d.wakeSnapshot()
		d.mu.Lock()
		d.queueInboxLocked("bbbbbb", bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
			From: step.from, To: bus.Addr(d.device, "bbbbbb"), Subject: step.subject})
		j := d.nextPushLocked("bbbbbb", wake)
		d.mu.Unlock()
		if j == nil || j.via != viaT3 || len(j.msgs) != 1 || j.msgs[0].Event.Subject != step.subject {
			t.Fatalf("the turn for %q: %+v", step.subject, j)
		}
		d.finishPush(j, nil)
		if !strings.HasPrefix(j.text, step.want) {
			t.Errorf("the turn for %q was %q, want it to start %q", step.subject, j.text, step.want)
		}
	}
}
