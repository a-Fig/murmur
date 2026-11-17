package daemon

import (
	"cmp"
	"fmt"
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// A mute holds against a message to everyone or to the device from the first moment, and against
// one sent to the muting session itself once MuteGrace has passed: until then the sender's daemon
// may not have read of the mute, and would take its message for delivered (#466).
func TestAMuteHoldsAgainstDirectMessagesAfterItsGrace(t *testing.T) {
	const me, muter, sender = "here-0001", "quiet-otter", "brave-heron"
	from := bus.Addr("there-0002", "aaaaaa")
	daemonAt := func(muted time.Duration) *Daemon {
		return &Daemon{device: me, opt: Options{MuteGrace: 30 * time.Second},
			sessions: map[string]*session{"bbbbbb": {id: "bbbbbb", name: muter}},
			st: state{Used: map[string]string{sender: from},
				Mutes: map[string]map[string]time.Time{muter: {sender: time.Now().Add(-muted)}}}}
	}
	direct := bus.Event{From: from, To: bus.Addr(me, "bbbbbb")}
	for _, c := range []struct {
		what  string
		ago   time.Duration
		e     bus.Event
		muted bool
	}{
		{"a direct message, just after the mute", time.Second, direct, false},
		{"a direct message, after the grace", time.Minute, direct, true},
		{"a message to everyone, just after the mute", time.Second, bus.Event{From: from, To: "*"}, true},
		{"a message to the device, just after the mute", time.Second, bus.Event{From: from, To: me}, true},
		{"a direct message from someone else, after the grace", time.Minute, bus.Event{From: bus.Addr("there-0002", "cccccc"), To: bus.Addr(me, "bbbbbb")}, false},
	} {
		if got := daemonAt(c.ago).mutedHereLocked("bbbbbb", c.e); got != c.muted {
			t.Errorf("%s: dropped %v, want %v", c.what, got, c.muted)
		}
	}
	// With no grace, as the other tests run, a direct message is dropped at once.
	d := daemonAt(time.Second)
	d.opt.MuteGrace = -1
	if !d.mutedHereLocked("bbbbbb", direct) {
		t.Error("with no grace, a direct message got through")
	}
}

// An address that has held two names, as after a Claude Code /clear, and isn't in presence: a
// mute of either name holds against it, every time, whichever the map gives first (#483).
func TestAMuteHoldsForAnyNameASendersAddressHasHeld(t *testing.T) {
	const me, muter = "here-0001", "quiet-otter"
	from := bus.Addr("there-0002", "aaaaaa")
	e := bus.Event{From: from, To: "*"}
	for _, mutedName := range []string{"brave-heron", "calm-zebra"} {
		d := &Daemon{device: me, opt: Options{MuteGrace: -1},
			sessions: map[string]*session{"bbbbbb": {id: "bbbbbb", name: muter}},
			st: state{Used: map[string]string{"brave-heron": from, "calm-zebra": from, "other-name": bus.Addr("there-0002", "cccccc")},
				Mutes: map[string]map[string]time.Time{muter: {mutedName: time.Now()}}}}
		for i := 0; i < 50; i++ {
			if !d.mutedHereLocked("bbbbbb", e) {
				t.Fatalf("with %s muted, try %d let the message through", mutedName, i)
			}
		}
		if got := d.nameOfLocked(from); got != "brave-heron" {
			t.Errorf("the name of the address is %q; want the first in order, every time", got)
		}
	}
}

// Mutes made while a session's name was pending are added to those its name already has, from
// before a resume, and not dropped (#483).
func TestMutesMadeBeforeANameLandsAreKept(t *testing.T) {
	earlier, later := time.Now().Add(-time.Hour), time.Now()
	s := &session{id: "bbbbbb"}
	d := &Daemon{device: "here-0001", sessions: map[string]*session{"bbbbbb": s}, savesHeld: true,
		st: state{Mutes: map[string]map[string]time.Time{
			"quiet-otter":          {"brave-heron": earlier},
			tagKeyOf("", "bbbbbb"): {"brave-heron": later, "calm-zebra": later},
		}}}
	s.name = "quiet-otter"
	d.adoptMutesLocked(s)
	got := d.st.Mutes["quiet-otter"]
	if len(got) != 2 || !got["brave-heron"].Equal(earlier) || !got["calm-zebra"].Equal(later) {
		t.Errorf("the name's mutes are %v; want its own kept and the pending one added", got)
	}
	if _, left := d.st.Mutes[tagKeyOf("", "bbbbbb")]; left {
		t.Error("the pending mutes are still kept by the ID")
	}
}

// A name that already has as many mutes as a session may have takes no more from the pending
// ones: presence lists at most bus.MaxMuted, and a mute kept here that it left out would refuse
// no sender.
func TestMutesMadeBeforeANameLandsStopAtTheCap(t *testing.T) {
	s := &session{id: "bbbbbb"}
	full := map[string]time.Time{}
	for i := 0; i < bus.MaxMuted; i++ {
		full[fmt.Sprintf("muted-n%d", i)] = time.Now()
	}
	d := &Daemon{device: "here-0001", sessions: map[string]*session{"bbbbbb": s}, savesHeld: true, log: log.New(io.Discard, "", 0),
		st: state{Mutes: map[string]map[string]time.Time{
			"quiet-otter":          full,
			tagKeyOf("", "bbbbbb"): {"calm-zebra": time.Now(), "muted-n0": time.Now()},
		}}}
	s.name = "quiet-otter"
	d.adoptMutesLocked(s)
	got := d.st.Mutes["quiet-otter"]
	if _, added := got["calm-zebra"]; added || len(got) != bus.MaxMuted {
		t.Errorf("the name has %d mutes, calm-zebra among them: %v; want the %d it had", len(got), added, bus.MaxMuted)
	}
}

// The muting session's device applies no group add from a session it has muted, whatever that
// session's daemon knew when it sent the add (#483). An add from anyone else is still applied.
func TestAGroupAddFromAMutedSessionIsNotApplied(t *testing.T) {
	const me, muter = "here-0001", "quiet-otter"
	muted, other := bus.Addr("there-0002", "aaaaaa"), bus.Addr("there-0002", "cccccc")
	d := &Daemon{device: me, opt: Options{MuteGrace: 30 * time.Second}, log: log.New(io.Discard, "", 0), savesHeld: true,
		sessions: map[string]*session{"bbbbbb": {id: "bbbbbb", name: muter}},
		st: state{Used: map[string]string{"brave-heron": muted, "calm-zebra": other},
			Tags: map[string]*tags.State{},
			// Muted a moment ago: a direct message would still get through, and an add doesn't.
			Mutes: map[string]map[string]time.Time{muter: {"brave-heron": time.Now()}}}}
	add := func(from, group string) {
		d.applyGroupAddLocked(bus.Event{Type: bus.GroupAdd, From: from, At: time.Now(), To: bus.Addr(me, "bbbbbb"), Add: []string{group}}, modeNormal)
	}
	add(muted, "group:ui")
	if st := d.st.Tags[muter]; !st.Empty() {
		t.Errorf("an add from a muted session was applied: %v", st.Self)
	}
	add(other, "group:api")
	if st := d.st.Tags[muter]; st == nil || len(st.Self) != 1 || st.Self[0] != "group:api" {
		t.Errorf("an add from a session that isn't muted wasn't applied: %+v", st)
	}
}

// A session that mutes another while its name is pending, sets no tags, and stops before the
// name lands still has the mute while its inbox is kept (#503): its mutes are kept by its ID, and
// so found by it, as its tags are.
func TestAMuteMadeWhileTheNameWasPendingHoldsAfterTheSessionStops(t *testing.T) {
	t.Parallel()
	d := tagsDaemon(t)
	d.opt.MuteGrace = -1
	d.mu.Lock()
	d.sessions["aaaaaa"].name = ""
	d.sessions["bbbbbb"].name = "brave-heron"
	d.mu.Unlock()
	if r := d.mute("aaaaaa", proto.Request{Name: "brave-heron"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, "aaaaaa")
	d.st.Stopped["aaaaaa"] = time.Now()
	if got := d.mutesOfLocked("aaaaaa"); len(got) != 1 || got[0] != "brave-heron" {
		t.Errorf("the stopped session has muted %v; want brave-heron", got)
	}
	d.queueLocked(bus.Event{V: bus.Version, ID: "m1", Type: bus.Message, From: "dev/bbbbbb", To: "dev/aaaaaa", At: time.Now()}, modeNormal)
	if box := d.st.Inbox["aaaaaa"]; len(box) != 0 {
		t.Errorf("the stopped session was given %d message(s) from the session it muted", len(box))
	}
}

// A sender waiting on a new name after /clear is refused by a recipient that muted its old one, as
// the recipient's device would drop the message; one with a name of its own is judged by that name
// alone, as the recipient's device judges it (#602).
func TestASendersMuteCheckUsesTheNamesTheReceiverDoes(t *testing.T) {
	const me, there = "here-0001", "there-0002"
	from := bus.Addr(me, "bbbbbb")
	d := &Daemon{device: me,
		sessions: map[string]*session{"bbbbbb": {id: "bbbbbb"}}, // its new name pending
		st:       state{Used: map[string]string{"brave-heron": from}, SessionNames: map[string]string{"bbbbbb": "calm-zebra"}},
		peers: map[string]*peer{there: {pres: bus.Presence{Device: there,
			Sessions: []bus.SessionInfo{{ID: "rrrrrr", Name: "quiet-otter", Muted: []string{"brave-heron"}}}}}}}
	if !d.hasMutedLocked(there, "rrrrrr", d.muteNamesLocked(from)) {
		t.Error("a sender with no name yet wasn't refused by a recipient that muted a name it held")
	}
	if got := d.mutersLocked(d.muteNamesLocked(from), ""); len(got) != 1 || got[0] != "quiet-otter" {
		t.Errorf("the muters of a sender with no name yet: %v", got)
	}
	// A name its device took last that the registry has given another session since, as a resume
	// does: the receiving device doesn't hold that one against this sender, so neither does it.
	d.st.SessionNames["bbbbbb"] = "brave-heron"
	d.st.Used["brave-heron"] = bus.Addr(me, "yyyyyy")
	d.peers[there].pres.Sessions[0].Muted = []string{"brave-heron"}
	if d.hasMutedLocked(there, "rrrrrr", d.muteNamesLocked(from)) {
		t.Error("a sender was refused for a mute of a name the registry gave another session")
	}
	d.st.Used["brave-heron"] = from
	d.sessions["bbbbbb"].name = "calm-zebra" // it landed
	if d.hasMutedLocked(there, "rrrrrr", d.muteNamesLocked(from)) {
		t.Error("a sender with a name of its own was refused for a mute of its address's old one")
	}
}

// Every device shows another's session that has left presence, or waits on a new name, by the name
// its own device does, the one its presence showed last, not the first of its names by spelling,
// and in every place it is shown (#602).
func TestAnotherDevicesSessionIsShownByItsLatestName(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	const there = "there-0002"
	addr := bus.Addr(there, "aaaaaa")
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: addr, To: "*", Subject: "hello", At: time.Now().UTC()}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Used["brave-heron"], d.st.Used["calm-zebra"] = addr, addr
	all := func(want string) {
		t.Helper()
		for _, s := range shownNames(d, e) {
			if s.name != want {
				t.Errorf("%s shows %q, want %q", s.where, s.name, want)
			}
		}
	}
	all("brave-heron") // nothing seen in presence: the first by spelling
	// Its presence showed it waiting on a new name, by the one it went by last.
	pres := bus.Presence{V: bus.Version, Device: there, Sessions: []bus.SessionInfo{{ID: "aaaaaa", Was: "calm-zebra"}}}
	pres.Clean()
	d.peers[there] = &peer{pres: pres}
	d.notePeerNameLocked(addr, cmp.Or(pres.Sessions[0].Name, pres.Sessions[0].Was))
	all("calm-zebra")
	// It left presence: still calm-zebra.
	d.peers[there] = &peer{pres: bus.Presence{V: bus.Version, Device: there}}
	all("calm-zebra")
	// A presence names Was only while the session has no name.
	named := bus.Presence{V: bus.Version, Device: there, Sessions: []bus.SessionInfo{{ID: "aaaaaa", Name: "brave-heron", Was: "calm-zebra"}}}
	named.Clean()
	if was := named.Sessions[0].Was; was != "" {
		t.Errorf("a named session's presence kept Was %q", was)
	}
}
