package daemon_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of what the page's Recent view is told sessions did, other than send messages (#448).

// activity reads the activity after cursor.
func (p *page) activity(cursor string) (acts []web.Activity, next string) {
	p.t.Helper()
	var r struct {
		Activity []web.Activity `json:"activity"`
		Cursor   string         `json:"cursor"`
	}
	p.get("/api/activity?cursor="+url.QueryEscape(cursor), &r)
	return r.Activity, r.Cursor
}

// has says acts has one of kind by the session at addr that ok accepts.
func hasAct(acts []web.Activity, kind, addr string, ok func(web.Activity) bool) bool {
	for _, a := range acts {
		if a.Kind == kind && a.Addr == addr && (ok == nil || ok(a)) {
			return true
		}
	}
	return false
}

// A session's start, its status changing and its stop each show, on its own device's page and on
// another's, under the session's name. The status it starts with isn't a change.
func TestThePageIsToldWhatSessionsDid(t *testing.T) {
	remote := newRemote(t)
	// A short grace, so the session whose connection closes is stopped within the test.
	a, b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour), newDevice(t, remote)
	alice, conn := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	aliceName := a.named(alice)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	pa, pb := a.page(), b.page()

	started := func(x web.Activity) bool { return x.Agent == "test" && x.Who == aliceName }
	eventually(t, "the start on both pages", func() bool {
		mine, _ := pa.activity("")
		theirs, _ := pb.activity("")
		return hasAct(mine, web.ActStarted, alice, started) && hasAct(theirs, web.ActStarted, alice, started)
	})

	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "writing the lookup"})
	now := func(x web.Activity) bool { return x.Doing == "writing the lookup" && x.Who == aliceName }
	eventually(t, "the status on both pages", func() bool {
		mine, _ := pa.activity("")
		theirs, _ := pb.activity("")
		return hasAct(mine, web.ActStatus, alice, now) && hasAct(theirs, web.ActStatus, alice, now)
	})
	// Setting it to what it is changes nothing.
	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "writing the lookup"})

	_, cursor := pb.activity("")
	conn.Close()
	eventually(t, "the stop, after the cursor", func() bool {
		more, _ := pb.activity(cursor)
		return hasAct(more, web.ActStopped, alice, nil)
	})
	all, _ := pb.activity("")
	statuses := 0
	for _, x := range all {
		if x.Kind == web.ActStatus && x.Addr == alice {
			statuses++
		}
		if x.ID == "" || x.At.IsZero() {
			t.Errorf("an activity without an ID or a time: %+v", x)
		}
	}
	if statuses != 1 {
		t.Errorf("%d status changes shown for one change, want 1: %+v", statuses, all)
	}
	if more, _ := pb.activity(cursor); hasAct(more, web.ActStarted, alice, nil) {
		t.Errorf("what came before the cursor came again: %+v", more)
	}
}

// A tag a session takes or drops shows as a change of its tags.
func TestThePageIsToldOfTagChanges(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	pb := b.page()

	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"reviewing"}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	eventually(t, "the tag on the other device's page", func() bool {
		acts, _ := pb.activity("")
		return hasAct(acts, web.ActTags, alice, func(x web.Activity) bool {
			return len(x.Added) == 1 && x.Added[0] == "reviewing" && len(x.Removed) == 0
		})
	})
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Remove: []string{"reviewing"}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	eventually(t, "the tag coming off", func() bool {
		acts, _ := pb.activity("")
		return hasAct(acts, web.ActTags, alice, func(x web.Activity) bool {
			return len(x.Removed) == 1 && x.Removed[0] == "reviewing" && len(x.Added) == 0
		})
	})
}

// The commits one tool call made show as one activity on the session's own device, with their
// count and the last one's subject; another device learns only the branch, from presence.
func TestThePageIsToldOfCommits(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	wt := a.linkedTree("wt-feed", "feat-feed")
	alice, _ := a.claudeSession("aaaaaa", "claude-feed-id", procP, wt)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return b.seesSession(alice) && a.seesSession(bob) })

	a.callStart("claude-feed-id", procP, "toolu_1", wt)
	commitFile(t, wt, "one.txt")
	commitFile(t, wt, "two.txt")
	a.callEnd("claude-feed-id", procP, "toolu_1", wt)

	pa, pb := a.page(), b.page()
	eventually(t, "the commits on the session's own page", func() bool {
		acts, _ := pa.activity("")
		return hasAct(acts, web.ActCommits, alice, func(x web.Activity) bool {
			return x.Commits == 2 && x.Branch == "feat-feed" && x.Subject == "add two.txt"
		})
	})
	eventually(t, "the branch on the other device's page", func() bool {
		acts, _ := pb.activity("")
		return hasAct(acts, web.ActCommits, alice, func(x web.Activity) bool { return x.Commits == 0 && x.Branch == "feat-feed" })
	})
	// The same commits, seen again by a later call, aren't shown twice.
	a.callStart("claude-feed-id", procP, "toolu_2", wt)
	a.callEnd("claude-feed-id", procP, "toolu_2", wt)
	acts, _ := pa.activity("")
	n := 0
	for _, x := range acts {
		if x.Kind == web.ActCommits {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d commit activities on the session's own page, want 1: %+v", n, acts)
	}
}
