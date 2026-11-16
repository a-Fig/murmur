package daemon

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// Going back online (#681) releases what arrived since the device left online, and what a
// session's own do not disturb held if that came off meanwhile: X took its tag off while the
// device was on do not disturb, which kept the tag on it, so nothing was released then. What
// arrived before the device left online for a session that wasn't holding it keeps its fate.
// And while the device is on do not disturb, a person taking the tag off a session doesn't take
// it off what who and lookup show, as send still goes by it.
func TestGoingOnlineReleasesWhatAnOwnDoNotDisturbHeld(t *testing.T) {
	t.Parallel()
	d := &Daemon{dir: t.TempDir(), log: log.New(io.Discard, "", 0), device: "here-0001", started: time.Now(),
		sessions: map[string]*session{}, st: state{Inbox: map[string][]*inMsg{}, Tags: map[string]*tags.State{}}}
	d.mu.Lock()
	d.newSessionLocked(&proto.Hello{ID: "xxxxxx"}, "claude-code", "", "xxxxxx")
	d.newSessionLocked(&proto.Hello{ID: "yyyyyy"}, "claude-code", "", "yyyyyy")
	d.mu.Unlock()
	if r := d.setOwnTags("xxxxxx", proto.Request{Add: []string{tags.DoNotDisturb}}); !r.OK {
		t.Fatalf("x's do not disturb: %s", r.Error)
	}
	msg := func(to string) *inMsg {
		return &inMsg{Arrived: time.Now().Add(-time.Hour), Event: bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
			From: "peer-0001/aaaaaa", To: "here-0001/" + to, Subject: "from an hour ago"}}
	}
	held, before := msg("xxxxxx"), msg("yyyyyy")
	d.mu.Lock()
	d.st.Inbox["xxxxxx"], d.st.Inbox["yyyyyy"] = []*inMsg{held}, []*inMsg{before}
	d.mu.Unlock()

	away := time.Now().Add(-time.Minute)
	d.setMode(web.ModeOnline, web.ModeDND, away)
	if r := d.setOwnTags("xxxxxx", proto.Request{Remove: []string{tags.DoNotDisturb}}); !r.OK {
		t.Fatalf("x taking its tag off: %s", r.Error)
	}
	d.mu.Lock()
	if err := d.humanTagsLocked("yyyyyy", nil, []string{tags.DoNotDisturb}, "ada-lovelace", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !d.dndLocked("xxxxxx") {
		t.Error("x is off do not disturb while the device is on it")
	}
	if !tags.Has(d.tagsOfLocked(d.sessions["yyyyyy"]), tags.DoNotDisturb) {
		t.Error("a person's mark took do not disturb off y's tags while the device is on it")
	}
	d.mu.Unlock()

	d.setMode(web.ModeDND, web.ModeOnline, away)
	if !pushable(held) {
		t.Errorf("x's message isn't let go once back online: %+v", held)
	}
	if !before.Released.IsZero() || pushable(before) {
		t.Errorf("y's message from before the device left online was released: %+v", before)
	}
}
