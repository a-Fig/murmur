package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// Session tags (#198), as the session's own device decides them.

func tagsDaemon(t *testing.T) *Daemon {
	d := threadsDaemon(t)
	d.opt.StaleAfter = 6 * time.Minute
	d.st.Tags = map[string]*tags.State{}
	d.st.Inbox = map[string][]*inMsg{}
	d.st.Stopped = map[string]time.Time{}
	d.started = time.Now().Add(-time.Hour) // past the catch-up window, so delivery isn't held
	for _, id := range []string{"aaaaaa", "bbbbbb"} {
		inT3(d, id, time.Minute)
		d.sessions[id].key = "t3:thread-" + id
	}
	return d
}

func tagNames(ts []tags.Tag) string { return strings.Join(tags.Names(ts), " ") }

func TestOwnTagsAndTheDerivedModel(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{Model: "claude-opus-5-5"}

	resp := d.setOwnTags("aaaaaa", proto.Request{Add: []string{"Reviewing"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if got := tagNames(resp.Tags); got != "model:opus-5.5 reviewing" {
		t.Fatalf("tags after setting = %q", got)
	}
	if src := resp.Tags[0].Src; src != tags.SrcDerived {
		t.Errorf("the model's source is %q, want derived", src)
	}
	if resp := d.setOwnTags("aaaaaa", proto.Request{Add: []string{"no spaces"}}); resp.Error == "" {
		t.Error("a tag with a space was taken")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// The session's own model stands over T3's.
	d.st.Tags["name-aaaaaa"].SetSelf([]string{"model:sonnet-5.5"}, nil)
	if got := tagNames(d.tagsOfLocked(d.sessions["aaaaaa"])); got != "model:sonnet-5.5 reviewing" {
		t.Errorf("tags = %q, want the session's model in place of T3's", got)
	}

	// who lists by tags.
	f, _ := tags.ParseFilter([]string{"reviewing"})
	got := d.whoLocked("bbbbbb", f)[0].Sessions
	if len(got) != 1 || got[0].Name != "name-aaaaaa" {
		t.Errorf("who tagged reviewing = %+v", got)
	}
	f, _ = tags.ParseFilter([]string{"-reviewing"})
	if got := d.whoLocked("bbbbbb", f)[0].Sessions; len(got) != 1 || got[0].Name != "name-bbbbbb" {
		t.Errorf("who without reviewing = %+v", got)
	}
}

func TestTagsChangeNothingAboutDelivery(t *testing.T) {
	t.Parallel()
	// No tag carries behaviour: one called unavailable is a label like any other.
	d := tagsDaemon(t)
	if resp := d.setOwnTags("aaaaaa", proto.Request{Add: []string{"unavailable"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	d.queueLocked(bus.Event{ID: "m1", Type: bus.Message, From: "other/xxxxxx", To: "*", At: now}, modeNormal)
	d.queueLocked(bus.Event{ID: "m2", Type: bus.Message, From: "other/xxxxxx", To: "dev/aaaaaa", At: now}, modeNormal)
	if len(d.st.Inbox["aaaaaa"]) != 2 || len(d.st.Inbox["bbbbbb"]) != 1 {
		t.Fatalf("inboxes: tagged %d, untagged %d", len(d.st.Inbox["aaaaaa"]), len(d.st.Inbox["bbbbbb"]))
	}
}

func TestAHumansTagsStandOverTheSessions(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.setOwnTags("aaaaaa", proto.Request{Add: []string{"reviewing"}})
	d.mu.Lock()
	d.peers["far"] = &peer{pres: bus.Presence{Owner: "Maya"}}
	// From another device's page: release on, reviewing off.
	e := bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.Tags, From: "far/cli", To: "dev/aaaaaa", At: time.Now(),
		Add: []string{"release"}, Remove: []string{"reviewing"}}
	if !e.Valid() {
		t.Fatal("the tags event isn't valid")
	}
	d.applyTagsEventLocked(e)
	// One for a session elsewhere does nothing here.
	d.applyTagsEventLocked(bus.Event{Type: bus.Tags, From: "far/cli", To: "other/aaaaaa", Add: []string{"x"}})
	got := d.tagsOfLocked(d.sessions["aaaaaa"])
	d.mu.Unlock()
	if want := []tags.Tag{{Name: "release", Src: tags.SrcHuman}}; !slices.Equal(got, want) {
		t.Fatalf("tags = %+v, want %+v", got, want)
	}
	if m := d.st.Tags["name-aaaaaa"].Human["release"]; m.By != "Maya on far" {
		t.Errorf("the mark is by %q", m.By)
	}

	// The session can't undo the human's choice, and is told so.
	resp := d.setOwnTags("aaaaaa", proto.Request{Add: []string{"reviewing"}, Remove: []string{"release"}})
	if !strings.Contains(resp.Note, "a human") || tagNames(resp.Tags) != "release" {
		t.Errorf("after the session's change: %q, %q", resp.Note, tagNames(resp.Tags))
	}

	// The tags are kept by the session's name, so a new ID for the same conversation has them,
	// and so does the session once its key changes (it found its T3 thread).
	d.mu.Lock()
	defer d.mu.Unlock()
	inT3(d, "dddddd", 0)
	d.sessions["dddddd"].name = "name-aaaaaa"
	d.sessions["dddddd"].key = "claude:other"
	if !tags.Has(d.tagsOfLocked(d.sessions["dddddd"]), "release") {
		t.Error("the conversation's tags didn't follow it to its new session ID and key")
	}
}

func TestTagsEventValid(t *testing.T) {
	t.Parallel()
	base := bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.Tags, From: "far/cli", To: "dev/aaaaaa", Add: []string{"ok"}}
	if !base.Valid() {
		t.Fatal("a good tags event isn't valid")
	}
	for name, mod := range map[string]func(*bus.Event){
		"to a device":      func(e *bus.Event) { e.To = "dev" },
		"to everyone":      func(e *bus.Event) { e.To = "*" },
		"nothing changed":  func(e *bus.Event) { e.Add = nil },
		"a bad tag":        func(e *bus.Event) { e.Add = []string{"Not A Tag"} },
		"too many at once": func(e *bus.Event) { e.Add = slices.Repeat([]string{"x"}, tags.MaxPerSession+1) },
	} {
		e := base
		mod(&e)
		if e.Valid() {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestAChangeForASessionThatIsntRegisteredWaitsForIt(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	// Its key was rekeyed once, so two keys have its name (see rekeyLocked); the name is what counts.
	d.st.NameKeys = map[string]string{"claude:c": "name-aaaaaa", "t3:thread-aaaaaa": "name-aaaaaa"}
	d.st.SessionNames["aaaaaa"] = "name-aaaaaa"
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, "aaaaaa") // its daemon has restarted, and it hasn't registered again yet
	d.applyTagsEventLocked(bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.Tags, From: "far/cli",
		To: "dev/aaaaaa", At: time.Now(), Add: []string{"release"}})
	inT3(d, "aaaaaa", 0)
	d.sessions["aaaaaa"].key = "t3:thread-aaaaaa"
	if !tags.Has(d.tagsOfLocked(d.sessions["aaaaaa"]), "release") {
		t.Fatal("the change made while the session was away was lost")
	}
}

func TestTagsSetWhileTheNameIsPendingFollowItsName(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.st.NameHolders = map[string]string{}
	d.mu.Lock()
	s := d.sessions["aaaaaa"]
	s.name = ""
	d.mu.Unlock()
	if resp := d.setOwnTags("aaaaaa", proto.Request{Add: []string{"reviewing"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setNameLocked(s, "quiet-otter")
	if !tags.Has(d.tagsOfLocked(s), "reviewing") || d.st.Tags["id:aaaaaa"] != nil {
		t.Fatalf("after the name landed: tags %v, kept %v", d.tagsOfLocked(s), d.st.Tags)
	}
}

func TestDoNotDisturbHoldsMessagesUntilTheTagComesOff(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	if resp := d.setOwnTags("aaaaaa", proto.Request{Add: []string{"do-not-disturb"}}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	always := pageSettings{Wake: web.Wake{When: web.WakeAlways}}
	d.mu.Lock()
	d.queueLocked(bus.Event{ID: "m1", Type: bus.Message, From: "other/xxxxxx", To: "dev/aaaaaa", At: time.Now()}, modeNormal)
	d.queueLocked(bus.Event{ID: "m2", Type: bus.Message, From: "other/xxxxxx", To: "*", At: time.Now()}, modeNormal)
	box := d.st.Inbox["aaaaaa"]
	if len(box) != 2 {
		d.mu.Unlock()
		t.Fatalf("inbox holds %d, want both messages kept", len(box))
	}
	if !d.heldLocked("aaaaaa", box[0]) || d.heldLocked("bbbbbb", box[0]) || !d.mayWakeLocked("bbbbbb", always) {
		d.mu.Unlock()
		t.Fatal("a session on do not disturb doesn't hold the message, or one that isn't does, or may not be woken")
	}
	// The messages waited longer than a turn's limit.
	for _, m := range box {
		m.Arrived = time.Now().Add(-time.Hour)
	}
	d.mu.Unlock()

	// No notice through the hook either.
	if resp := d.notices(proto.Request{Session: "aaaaaa"}); len(resp.Messages) != 0 {
		t.Fatalf("the hook was told of %d messages", len(resp.Messages))
	}

	// The tag comes off: what waited may go in as a turn, however long it waited.
	d.setOwnTags("aaaaaa", proto.Request{Remove: []string{"do-not-disturb"}})
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.heldLocked("aaaaaa", d.st.Inbox["aaaaaa"][0]) || !d.mayWakeLocked("aaaaaa", always) {
		t.Fatal("still held, or not woken, after the tag came off")
	}
	for _, m := range d.st.Inbox["aaaaaa"] {
		if !pushable(m) {
			t.Errorf("message %s can't go in as a turn after the tag came off", m.Event.ID)
		}
	}
}

func TestDoNotDisturbSetByAHumanForASessionAway(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.st.SessionNames["aaaaaa"] = "name-aaaaaa"
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, "aaaaaa")
	d.applyTagsEventLocked(bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.Tags, From: "far/cli",
		To: "dev/aaaaaa", At: time.Now(), Add: []string{"do-not-disturb"}})
	if !d.dndLocked("aaaaaa") {
		t.Fatal("a session away isn't on do not disturb by the tags kept for its name")
	}
	// Another device's session, by its presence.
	d.peers["far"] = &peer{lastChange: time.Now(), pres: bus.Presence{Sessions: []bus.SessionInfo{
		{ID: "cccccc", Tags: []tags.Tag{{Name: tags.DoNotDisturb}}}}}}
	if !d.peerDNDLocked("far", "cccccc") || d.peerDNDLocked("far", "dddddd") {
		t.Fatal("peerDNDLocked is wrong")
	}
}

func TestDoNotDisturbOfASessionThatStoppedBeforeItsNameLanded(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.mu.Lock()
	d.sessions["aaaaaa"].name = ""
	d.mu.Unlock()
	d.setOwnTags("aaaaaa", proto.Request{Add: []string{"do-not-disturb"}})
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, "aaaaaa")
	if !d.dndLocked("aaaaaa") {
		t.Fatal("a session that stopped with its name pending lost its do not disturb")
	}
}

func TestReleaseRetriesAMessageWhosePushFailedUnsent(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.setOwnTags("aaaaaa", proto.Request{Add: []string{"do-not-disturb"}})
	d.mu.Lock()
	d.queueLocked(bus.Event{ID: "m1", Type: bus.Message, From: "other/xxxxxx", To: "dev/aaaaaa", At: time.Now()}, modeNormal)
	d.st.Inbox["aaaaaa"][0].PushFailed = true // an earlier delivery that never reached the session
	d.mu.Unlock()
	d.setOwnTags("aaaaaa", proto.Request{Remove: []string{"do-not-disturb"}})
	d.mu.Lock()
	defer d.mu.Unlock()
	if m := d.st.Inbox["aaaaaa"][0]; !pushable(m) {
		t.Fatalf("after the tag came off the message still can't go in: %+v", m)
	}
}

func TestTheDaemonStaysUpForAThreadOnDoNotDisturb(t *testing.T) {
	t.Parallel()
	d := idleDaemon(map[string]time.Duration{"aaaaaa": time.Minute})
	d.st.Tags = map[string]*tags.State{"name-aaaaaa": {Self: []string{tags.DoNotDisturb}}}
	d.threads = map[threadKey]t3db.State{}
	always := pageSettings{Wake: web.Wake{When: web.WakeAlways}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.dndLocked("aaaaaa") {
		t.Fatal("the thread isn't on do not disturb")
	}
	if !d.holdsRouteLocked(always) {
		t.Fatal("the daemon would exit, and never read the tag's removal or hand over what waited")
	}
}

// A session that stopped before its name landed, having set no tags and muted nobody, is still
// this device's while its inbox is kept: a person's change and an agent's add made meanwhile are
// kept by its ID for it, as for one that had tags already, and it has them if it comes back.
func TestAChangeForAStoppedSessionWhoseNameNeverLandedIsKept(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, "aaaaaa")
	d.st.Stopped["aaaaaa"] = time.Now()
	d.applyTagsEventLocked(bus.Event{V: bus.Version, ID: "abcdef12", Type: bus.Tags, From: "far/cli",
		To: "dev/aaaaaa", At: time.Now(), Add: []string{"release"}})
	d.applyGroupAddLocked(bus.Event{V: bus.Version, ID: "abcdef13", Type: bus.GroupAdd, From: "far/xxxxxx",
		To: "dev/aaaaaa", At: time.Now(), Add: []string{"group:ui"}}, modeNormal)
	if !d.hasTagLocked("aaaaaa", "release") || !d.hasTagLocked("aaaaaa", "group:ui") {
		t.Fatalf("the stopped session's tags are %+v; want the person's change and the add kept", d.st.Tags)
	}
	inT3(d, "aaaaaa", 0) // back, its name still pending
	s := d.sessions["aaaaaa"]
	s.name = ""
	if got := tagNames(d.tagsOfLocked(s)); got != "group:ui release" {
		t.Errorf("back, it has %q", got)
	}
}

// After a restart, a session whose name is pending and that kept nothing here hasn't registered
// again yet when the daemon reads a person's change to its tags, an agent's add of it to a group,
// and a message to that group (#524). They wait for it, as the message does, and it has all three
// once it is back.
func TestAChangeForASessionNotBackAfterARestartWaitsForIt(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.started = time.Now() // just restarted: within the catch-up window
	d.caughtUp = map[string]bool{}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, "aaaaaa")
	for _, id := range []string{"aaaaaa", "cccccc"} { // cccccc never comes back
		d.applyTagsEventLocked(bus.Event{V: bus.Version, ID: "abcdef1" + id[:1], Type: bus.Tags, From: "far/cli",
			To: "dev/" + id, At: time.Now(), Add: []string{"release"}})
		d.applyGroupAddLocked(bus.Event{V: bus.Version, ID: "abcdef2" + id[:1], Type: bus.GroupAdd, From: "far/xxxxxx",
			To: "dev/" + id, At: time.Now(), Add: []string{"group:ui"}}, modeCatchUp)
	}
	d.queueLocked(bus.Event{V: bus.Version, ID: "m1", Type: bus.Message, From: "far/xxxxxx", To: "group:ui", At: time.Now()}, modeCatchUp)

	inT3(d, "aaaaaa", 0)
	s := d.sessions["aaaaaa"]
	s.name, s.reconnect = "", true
	d.catchUpLocked("aaaaaa")
	if got := tagNames(d.tagsOfLocked(s)); got != "group:ui release" {
		t.Errorf("back, it has %q; want the person's change and the add", got)
	}
	if box := d.st.Inbox["aaaaaa"]; len(box) != 1 || box[0].Event.ID != "m1" {
		t.Errorf("back, its inbox holds %d message(s); want the group's", len(box))
	}
	if d.st.Tags[tagKeyOf("", "cccccc")] != nil {
		t.Errorf("a session that isn't back was given tags: %+v", d.st.Tags[tagKeyOf("", "cccccc")])
	}
}

// What a session whose name never landed kept by its ID goes when its inbox does, as nothing else
// would ever drop it; one stopped more lately keeps its own.
func TestWhatAStoppedSessionKeptByItsIDGoesWithItsInbox(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.opt.StoppedInbox = time.Hour
	d.mu.Lock()
	d.st.Mutes = map[string]map[string]time.Time{}
	for _, id := range []string{"aaaaaa", "bbbbbb"} {
		delete(d.sessions, id)
		d.st.Stopped[id] = time.Now()
		st := &tags.State{}
		if err := st.SetSelf([]string{"release"}, nil); err != nil {
			t.Fatal(err)
		}
		d.st.Tags[tagKeyOf("", id)] = st
		d.st.Mutes[tagKeyOf("", id)] = map[string]time.Time{"brave-heron": time.Now()}
	}
	d.st.Stopped["aaaaaa"] = time.Now().Add(-2 * time.Hour)
	d.mu.Unlock()
	d.pruneInboxes()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st.Tags[tagKeyOf("", "aaaaaa")] != nil || d.st.Mutes[tagKeyOf("", "aaaaaa")] != nil {
		t.Errorf("kept for a session stopped past StoppedInbox: tags %+v, mutes %v",
			d.st.Tags[tagKeyOf("", "aaaaaa")], d.st.Mutes[tagKeyOf("", "aaaaaa")])
	}
	if d.st.Tags[tagKeyOf("", "bbbbbb")] == nil || d.st.Mutes[tagKeyOf("", "bbbbbb")] == nil {
		t.Errorf("a session stopped within StoppedInbox lost what it kept: tags %+v, mutes %v",
			d.st.Tags[tagKeyOf("", "bbbbbb")], d.st.Mutes[tagKeyOf("", "bbbbbb")])
	}
}

// keepByID gives session id, as one whose name never landed, a tag and a mute kept by its ID.
// Caller holds mu.
func keepByID(t *testing.T, d *Daemon, id string) {
	t.Helper()
	st := &tags.State{}
	if err := st.SetSelf([]string{"release"}, nil); err != nil {
		t.Fatal(err)
	}
	d.st.Tags[tagKeyOf("", id)] = st
	if d.st.Mutes == nil {
		d.st.Mutes = map[string]map[string]time.Time{}
	}
	d.st.Mutes[tagKeyOf("", id)] = map[string]time.Time{"brave-heron": time.Now()}
}

// A session back after its stop ran out, before pruneInboxes got to it, gets none of what it kept
// by its ID, as it gets no inbox: the same as had the prune come first.
func TestASessionBackAfterItsStopRanOutGetsNoneOfWhatItKeptByItsID(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.opt.StoppedInbox = time.Hour
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range []string{"aaaaaa", "bbbbbb"} {
		delete(d.sessions, id)
		keepByID(t, d, id)
		d.st.Inbox[id] = []*inMsg{{}}
	}
	d.st.Stopped["aaaaaa"] = time.Now().Add(-2 * time.Hour)
	d.st.Stopped["bbbbbb"] = time.Now()
	d.newSessionLocked(&proto.Hello{ID: "aaaaaa"}, "claude-code", "", "aaaaaa")
	d.newSessionLocked(&proto.Hello{ID: "bbbbbb"}, "claude-code", "", "bbbbbb")
	if d.st.Inbox["aaaaaa"] != nil || d.st.Tags[tagKeyOf("", "aaaaaa")] != nil || d.st.Mutes[tagKeyOf("", "aaaaaa")] != nil {
		t.Errorf("back past StoppedInbox, it kept: inbox %d, tags %+v, mutes %v", len(d.st.Inbox["aaaaaa"]),
			d.st.Tags[tagKeyOf("", "aaaaaa")], d.st.Mutes[tagKeyOf("", "aaaaaa")])
	}
	if d.st.Inbox["bbbbbb"] == nil || d.st.Tags[tagKeyOf("", "bbbbbb")] == nil || d.st.Mutes[tagKeyOf("", "bbbbbb")] == nil {
		t.Errorf("back within StoppedInbox, it lost: inbox %d, tags %+v, mutes %v", len(d.st.Inbox["bbbbbb"]),
			d.st.Tags[tagKeyOf("", "bbbbbb")], d.st.Mutes[tagKeyOf("", "bbbbbb")])
	}
}

// A session whose name never landed and that went down with its daemon is never recorded as
// stopped. Two minutes after the start, when its inbox is taken for gone, so is what it kept by
// its ID; a session that is back, or stopped within StoppedInbox, keeps it.
func TestWhatASessionNotBackAfterARestartKeptByItsIDGoesWithItsInbox(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.opt.StoppedInbox = time.Hour
	d.mu.Lock()
	d.started = time.Now()
	for _, id := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd"} {
		keepByID(t, d, id)
	}
	d.sessions["aaaaaa"].name = "" // back, its name still pending
	delete(d.sessions, "bbbbbb")   // never back
	d.st.Stopped["cccccc"] = time.Now()
	delete(d.st.Tags, tagKeyOf("", "dddddd")) // never back, with only a mute
	d.mu.Unlock()

	kept := func(id string) bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Tags[tagKeyOf("", id)] != nil || d.st.Mutes[tagKeyOf("", id)] != nil
	}
	d.pruneInboxes()
	if !kept("bbbbbb") || !kept("dddddd") {
		t.Fatal("dropped within two minutes of the start, while the session may still reconnect")
	}
	d.mu.Lock()
	d.started = time.Now().Add(-3 * time.Minute)
	d.mu.Unlock()
	d.pruneInboxes()
	for _, id := range []string{"bbbbbb", "dddddd"} {
		if kept(id) {
			t.Errorf("%s, not back after the restart, still has what it kept by its ID", id)
		}
	}
	for _, id := range []string{"aaaaaa", "cccccc"} {
		d.mu.Lock()
		tagged, muted := d.st.Tags[tagKeyOf("", id)] != nil, d.st.Mutes[tagKeyOf("", id)] != nil
		d.mu.Unlock()
		if !tagged || !muted {
			t.Errorf("%s lost what it kept by its ID: tags %v, mutes %v", id, tagged, muted)
		}
	}
}
