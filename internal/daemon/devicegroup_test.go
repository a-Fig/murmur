package daemon_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// A device is a group (#463): every agent's session on it has group:device-<id>, which nobody sets
// or takes off, who lists it and searches by it, and a message to it is a message to the device.
func TestADeviceIsAGroup(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := b.session("cccccc", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })
	devA, devB := a.self(), b.self()

	// Each device's group holds its sessions, here and as the other device sees them.
	for _, c := range []struct {
		on        *device
		as, addr  string
		group     string
		want      bool
		described string
	}{
		{a, "aaaaaa", alice, tags.DeviceGroup(devA), true, "alice in a's group, on a"},
		{a, "aaaaaa", bob, tags.DeviceGroup(devB), true, "bob in b's group, on a"},
		{a, "aaaaaa", carol, tags.DeviceGroup(devB), true, "carol in b's group, on a"},
		{b, "bbbbbb", alice, tags.DeviceGroup(devA), true, "alice in a's group, on b"},
		{a, "aaaaaa", bob, tags.DeviceGroup(devA), false, "bob in a's group, on a"},
		{b, "bbbbbb", alice, tags.DeviceGroup(devB), false, "alice in b's group, on b"},
	} {
		if got := c.on.inGroup(c.as, c.addr, c.group); got != c.want {
			t.Errorf("%s: %v, want %v", c.described, got, c.want)
		}
	}
	if det := a.look("aaaaaa", bob); det == nil || !tags.Has(det.Tags, tags.DeviceGroup(devB)) {
		t.Errorf("bob's lookup doesn't show b's group: %+v", det)
	}

	// Nobody sets it, or takes it off.
	for _, req := range []proto.Request{
		{Op: proto.OpTags, Session: "aaaaaa", Add: []string{tags.DeviceGroup(devB)}},
		{Op: proto.OpTags, Session: "aaaaaa", Remove: []string{tags.DeviceGroup(devA)}},
		{Op: proto.OpTags, Session: "aaaaaa", Add: []string{tags.DeviceGroup(devA)}, For: []string{bob}},
	} {
		if r := a.do(req); r.Error == "" || !strings.Contains(r.Error, "nobody sets or removes it") {
			t.Errorf("%+v: %q, want it refused", req, r.Error)
		}
	}
	if !a.inGroup("aaaaaa", alice, tags.DeviceGroup(devA)) {
		t.Error("alice left her device's group")
	}

	// A message to b's group reaches b's sessions, and goes on the bus to the device.
	r := a.send("aaaaaa", tags.DeviceGroup(devB), "to b's sessions", "")
	if r.Error != "" {
		t.Fatalf("sending to b's group: %s", r.Error)
	}
	if len(r.Messages) != 1 || r.Messages[0].To != devB {
		t.Errorf("sent %+v, want one message to the device %s", r.Messages, devB)
	}
	for _, sess := range []string{"bbbbbb", "cccccc"} {
		if got := inboxOf(b, sess); !slices.Equal(got, []string{"to b's sessions"}) {
			t.Errorf("%s's inbox is %q, want the device's message", sess, got)
		}
	}
	holds(t, "nothing for alice", time.Second, func() bool {
		return len(a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages) == 0
	})

	// A device nobody knows has no group to send to.
	r = a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: tags.DeviceGroup("nowhere-0000"), Subject: "lost"})
	if !strings.Contains(r.Error, "no device in presence has the group group:device-nowhere-0000") {
		t.Errorf("sending to an unknown device's group: %q", r.Error)
	}

	// A group a team made under the prefix before devices had groups is no device's, and the
	// sender is told the name to use for one of their own (#705).
	r = a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "group:device-lab", Subject: "to the lab"})
	if !strings.Contains(r.Error, "no device in presence has the group group:device-lab") || !strings.Contains(r.Error, "such as group:lab") {
		t.Errorf("sending to an old group under the device prefix: %q", r.Error)
	}
}
