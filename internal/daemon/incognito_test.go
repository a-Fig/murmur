package daemon_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// incognito turns the page's incognito setting on or off.
func (p *page) incognito(on bool) {
	p.t.Helper()
	body := `{"wake":{"when":"within"},"busy":"subject","incognito":false}`
	if on {
		body = strings.Replace(body, "false", "true", 1)
	}
	if code, b := p.call("PUT", "/api/settings", p.token, body); code != http.StatusOK {
		p.t.Fatalf("incognito %v: %d %s", on, code, b)
	}
}

// Incognito takes a device off Murmur in a repo as if it were offline (#681): teammates drop it,
// its agents can see and send nothing, the hook gives nothing, and the page shows none of its
// agents and sends nothing. A message that came before waits in the inbox, and turning incognito
// off brings it and the device back as they were.
func TestIncognitoTakesTheDeviceOffAndBringsItBack(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-a")
	bob, _ := b.session("bbbbbb", "claude-b")
	eventually(t, "b to see alice", func() bool { _, ok := b.whoEntry(alice); return ok })
	eventually(t, "a to see bob", func() bool { _, ok := a.whoEntry(bob); return ok })
	b.send("bbbbbb", alice, "before the switch", "")
	eventually(t, "the message to reach a", func() bool { return a.queuedFor(alice, "before the switch") })

	p := a.page()
	p.incognito(true)
	eventually(t, "b to drop a", func() bool { return !b.lists(a.self()) })

	for _, req := range []proto.Request{
		{Op: proto.OpWho, Session: "aaaaaa"},
		{Op: proto.OpLookup, Session: "aaaaaa", Name: bob},
		{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "hidden"},
		{Op: proto.OpInbox, Session: "aaaaaa"},
		{Op: proto.OpWho}, // `mur who` too
	} {
		if r := a.do(req); !strings.Contains(r.Error, "incognito") || len(r.Messages) > 0 || len(r.Devices) > 0 {
			t.Errorf("%s while incognito: %+v", req.Op, r)
		}
	}
	if r := a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-a", Whole: true}); r.Error != "" || len(r.Messages) > 0 || r.Unread > 0 {
		t.Errorf("the hook's notices while incognito: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-a", Continues: true}); len(r.Read) > 0 {
		t.Errorf("a turn's end while incognito gave %+v", r.Read)
	}
	st := p.state()
	if !st.Incognito || len(st.Devices) == 0 || len(st.Devices[0].Sessions) != 0 {
		t.Errorf("the page's state while incognito: %+v", st)
	}
	if code, body := p.call("POST", "/api/send", p.token, `{"to":"`+bob+`","text":"hi"}`); code == http.StatusOK || !strings.Contains(body, "incognito") {
		t.Errorf("the page sent while incognito: %d %s", code, body)
	}
	if b.lists(a.self()) {
		t.Error("b sees a again while it is incognito")
	}

	p.incognito(false)
	eventually(t, "b to see alice again", func() bool { _, ok := b.whoEntry(alice); return ok })
	if inbox, _ := a.unread("aaaaaa", ""); !slices.Contains(inbox, "before the switch") {
		t.Errorf("the message from before the switch is gone: %q", inbox)
	}
	if r := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}); r.Error != "" {
		t.Errorf("who after incognito: %s", r.Error)
	}
}
