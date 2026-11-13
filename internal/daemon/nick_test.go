package daemon_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of a device's nickname (#190): its user sets it, and teammates see it beside the device's
// ID. It is a label, and no address.

// nickOf is what device d's who says the device id is called.
func (d *device) nickOf(id string) string {
	for _, dv := range d.do(proto.Request{Op: proto.OpWho}).Devices {
		if dv.ID == id {
			return dv.Nick
		}
	}
	return ""
}

func (d *device) setNick(nick string) proto.Response {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpNick, Nick: nick})
	if r.Error != "" {
		d.t.Fatalf("the nickname %s: %s", nick, r.Error)
	}
	return r
}

func TestADevicesNicknameIsSeenBesideItsID(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see b's session", func() bool { _, ok := a.whoEntry(bob); return ok })

	if r := b.do(proto.Request{Op: proto.OpNick}); !strings.Contains(r.Note, "has no nickname.") {
		t.Errorf("before one is set: %+v", r)
	}
	if r := b.setNick("tyler"); !strings.Contains(r.Note, "has the nickname tyler") {
		t.Errorf("setting it: %+v", r)
	}
	if got := b.nickOf(b.self()); got != "tyler" {
		t.Errorf("its own who calls it %q", got)
	}
	eventually(t, "a to see b's nickname", func() bool { return a.nickOf(b.self()) == "tyler" })
	if got := view.Who(a.do(proto.Request{Op: proto.OpWho})); !strings.Contains(got, "\ntyler  "+b.self()+" (") {
		t.Errorf("who doesn't show the nickname beside the ID:\n%s", got)
	}
	l := a.do(proto.Request{Op: proto.OpLookup, Name: bob})
	if l.Error != "" || l.Lookup.Nick != "tyler" {
		t.Fatalf("lookup: %+v", l)
	}
	if got := view.Lookup(l); !strings.Contains(got, "  device tyler  "+b.self()+" (") {
		t.Errorf("lookup doesn't show the nickname:\n%s", got)
	}

	// It is no address: a message goes to a device by its ID, whatever any device is called.
	if r := a.do(proto.Request{Op: proto.OpSend, To: "tyler/bbbbbb", Subject: "by nickname"}); r.Error == "" && r.Messages[0].To != "tyler/bbbbbb" {
		t.Errorf("a send to tyler/bbbbbb went to %s", r.Messages[0].To)
	}
	if l := a.do(proto.Request{Op: proto.OpLookup, Name: "tyler/bbbbbb"}); l.Error == "" {
		t.Errorf("lookup took a nickname for a device: %+v", l.Lookup)
	}

	// Two devices may have one nickname: the ID beside it says which is which.
	a.setNick("tyler")
	if r := a.do(proto.Request{Op: proto.OpNick, Clear: true}); r.Error != "" || a.nickOf(a.self()) != "" {
		t.Errorf("taking it off: %+v, and who says %q", r, a.nickOf(a.self()))
	}
	for _, bad := range []string{"Tyler S", "x", b.self()} {
		if r := a.do(proto.Request{Op: proto.OpNick, Nick: bad}); r.Error == "" {
			t.Errorf("the nickname %q was taken: %+v", bad, r)
		}
	}
}

// A device with no nickname is called by its person's name, which nobody else's device can take.
func TestADeviceIsCalledByItsPersonsName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	a.page().choose("ada-lovelace")
	if got := a.nickOf(a.self()); got != "ada-lovelace" {
		t.Errorf("its own who calls it %q", got)
	}
	eventually(t, "b to call a by its person's name", func() bool {
		_, ok := b.whoEntry(alice)
		return ok && b.nickOf(a.self()) == "ada-lovelace"
	})
	if r := b.do(proto.Request{Op: proto.OpNick, Nick: "ada-lovelace"}); !strings.Contains(r.Error, "the name of the person on "+a.self()) {
		t.Errorf("taking another device's person's name: %+v", r)
	}
	// A nickname of its own stands before the person's name.
	a.setNick("ada-desktop")
	eventually(t, "b to see the nickname", func() bool { return b.nickOf(a.self()) == "ada-desktop" })
}

// The page sets the nickname as `mur nickname` does (#533): the same checks, the same state, and
// its account says what it is.
func TestThePageSetsTheNickname(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	p := a.page()
	nick := func(body string) (int, web.Account, string) {
		t.Helper()
		code, b := p.call("PUT", "/api/nick", p.token, body)
		var acct web.Account
		json.Unmarshal([]byte(b), &acct)
		return code, acct, b
	}
	if code, acct, b := nick(`{"nick":"tyler-desk"}`); code != http.StatusOK || acct.Nick != "tyler-desk" {
		t.Fatalf("setting it: %d %s", code, b)
	}
	if got := a.nickOf(a.self()); got != "tyler-desk" {
		t.Errorf("who calls it %q", got)
	}
	if got := p.state().Account.Nick; got != "tyler-desk" {
		t.Errorf("the page's state has the nickname %q", got)
	}
	for _, bad := range []string{"Tyler S", a.self()} {
		if code, _, b := nick(`{"nick":"` + bad + `"}`); code != http.StatusBadRequest {
			t.Errorf("the nickname %q: %d %s", bad, code, b)
		}
	}
	if code, acct, b := nick(`{"nick":""}`); code != http.StatusOK || acct.Nick != "" || a.nickOf(a.self()) != "" {
		t.Errorf("clearing it: %d %s, and who says %q", code, b, a.nickOf(a.self()))
	}
}
