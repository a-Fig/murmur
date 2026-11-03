package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// remoteText is every bus commit message on the remote, as anyone who can read the repo sees it.
func remoteText(t *testing.T, remote string) string {
	t.Helper()
	return git(t, remote, "log", "--format=%B", "--glob="+brand.RefRoot+"*", "--")
}

// Once a device turns encryption on, every device encrypts: a message reaches its recipient and
// nobody else, presence still shows teammates' sessions, and neither says anything readable on the
// remote. Turning it off isn't possible: a deleted config ref changes nothing (#156).
func TestEncryptedRepo(t *testing.T) {
	remote := newRemote(t)
	a, b, c := newDevice(t, remote), newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := c.session("cccccc", "")
	eventually(t, "presence", func() bool {
		return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) && c.seesSession(alice) && b.seesSession(carol) && c.seesSession(bob)
	})
	// Before: what is sent is on the remote for anyone to read, which shows the check below sees it.
	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "readable subject"})
	eventually(t, "the readable message", func() bool { return logHas(b, "readable subject") })
	if !strings.Contains(remoteText(t, remote), "readable subject") {
		t.Fatal("the remote's text doesn't show a message sent in the clear")
	}
	if r := a.do(proto.Request{Op: proto.OpEncrypt}); r.Error != "" {
		t.Fatalf("encrypt: %s", r.Error)
	}
	for _, d := range []*device{a, b, c} {
		eventually(t, "every device to know", func() bool { return d.do(proto.Request{Op: proto.OpWho}).Encrypted })
	}

	b.do(proto.Request{Op: proto.OpStatus, Session: "bbbbbb", Doing: "hidden status"})
	a.waitDoing("", bob, "hidden status")
	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "hidden subject", Body: "hidden body"})
	eventually(t, "bob's message", func() bool { return logHas(b, "hidden subject") })
	c.do(proto.Request{Op: proto.OpSend, Session: "cccccc", To: "*", Subject: "hidden broadcast"})
	eventually(t, "the broadcast", func() bool { return logHas(a, "hidden broadcast") && logHas(b, "hidden broadcast") })
	if logHas(c, "hidden subject") {
		t.Error("a message for b was read by c")
	}
	if text := remoteText(t, remote); strings.Contains(text, "hidden") {
		t.Fatalf("the remote carries what is encrypted:\n%s", text)
	}

	git(t, remote, "update-ref", "-d", brand.ConfigRef)
	a.stop()
	a.start()
	if !a.do(proto.Request{Op: proto.OpWho}).Encrypted {
		t.Fatal("encryption went off with the config ref")
	}
	a.session("aaaaaa", "")
	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "hidden after restart"})
	eventually(t, "the message after the restart", func() bool { return logHas(b, "hidden after restart") })
	if text := remoteText(t, remote); strings.Contains(text, "hidden") {
		t.Fatalf("the remote carries what is encrypted:\n%s", text)
	}
}

// Encryption isn't turned on while a device seen lately can't take part, unless forced; and then a
// message to that device is refused rather than sent in the clear.
func TestEncryptRefusesForOlderDevices(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, time.Minute)
	a.session("aaaaaa", "")
	msg, err := bus.Encode(bus.Presence{V: bus.Version, Device: "old-0001", Host: "h", At: time.Now().UTC(),
		Sessions: []bus.SessionInfo{{ID: "x", Agent: "test", Started: time.Now().UTC()}}}, "old")
	if err != nil {
		t.Fatal(err)
	}
	pushMessage(t, remote, brand.PresencePrefix+"old-0001", msg)
	eventually(t, "the older device", func() bool { return a.seesSession("old-0001/x") })

	r := a.do(proto.Request{Op: proto.OpEncrypt})
	if r.Error == "" || !strings.Contains(r.Error, "old-0001") {
		t.Fatalf("encrypt with an older device about: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpEncrypt, Force: true}); r.Error != "" || !strings.Contains(r.Note, "old-0001") {
		t.Fatalf("encrypt --force: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "old-0001/x", Subject: "not in the clear"}); r.Error == "" {
		t.Fatal("a message to a device with no box key was sent")
	}
	if strings.Contains(remoteText(t, remote), "not in the clear") {
		t.Fatal("it went out in the clear")
	}
}

// A device that published a box key and then stopped, as one rolled back to a Murmur that only
// signs does, can't take part: mur encrypt goes by its latest presence, not the key it once had.
func TestEncryptGoesByTheLatestPresence(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, time.Minute)
	a.session("aaaaaa", "")
	key := bus.NewKey()
	ref := brand.PresencePrefix + "rolled-0001"
	publish := func(boxKey, doing string) {
		p := bus.Presence{V: bus.Version, Device: "rolled-0001", Host: "h", At: time.Now().UTC(), BoxKey: boxKey,
			Sessions: []bus.SessionInfo{{ID: "x", Agent: "test", Doing: doing, Started: time.Now().UTC()}}}
		msg, err := bus.EncodeSigned(p, "p", key, "rolled-0001", ref, "")
		if err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		git(t, work, "init", "-q")
		sha := gitIn(t, work, msg, "commit-tree", git(t, work, "hash-object", "-t", "tree", "--stdin"))
		git(t, work, "push", "-q", "--force", remote, sha+":"+ref)
	}
	publish(bus.BoxPublic(bus.NewBoxKey()), "with a key")
	a.waitDoing("", "rolled-0001/x", "with a key")
	publish("", "rolled back")
	a.waitDoing("", "rolled-0001/x", "rolled back")
	if r := a.do(proto.Request{Op: proto.OpEncrypt}); r.Error == "" || !strings.Contains(r.Error, "rolled-0001") {
		t.Fatalf("encrypt with a device that rolled back: %+v", r)
	}
}
