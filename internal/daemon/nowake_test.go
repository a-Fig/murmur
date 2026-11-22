package daemon_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of telling a sender that its recipient won't be woken (#545): the recipient's wake setting
// keeps an agent's message out of an idle session, and the message waits for its next turn. The
// sender is told in send's result and in lookup, whether the recipient is on its own device or
// another.

const wontBeWoken = "won't be woken"

// lookupText is lookup of name as session id asks it, rendered as the agent reads it, or the
// refusal: a device that hasn't read the name yet refuses, and a wait asks again.
func (d *device) lookupText(id, name string) string {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpLookup, Session: id, Name: name})
	if r.Error != "" {
		return r.Error
	}
	return view.Lookup(r)
}

// putSettings sets the page's settings on device d.
func (d *device) putSettings(settings string) {
	d.t.Helper()
	pg := d.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token, settings); code != http.StatusOK {
		d.t.Fatalf("PUT %s: %d %s", settings, code, body)
	}
}

// sendNote sends as the page's person, and returns what the page is told.
func (p *page) sendNote(to, text string) string {
	p.t.Helper()
	b, _ := json.Marshal(map[string]string{"to": to, "text": text})
	code, body := p.call("POST", "/api/send", p.token, string(b))
	if code != http.StatusOK {
		p.t.Fatalf("sending to %s: %d %s", to, code, body)
	}
	var sent web.Sent
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		p.t.Fatalf("what a send returned: %q: %v", body, err)
	}
	return sent.Note
}

// quietPair is two devices on remote: a, which takes b for stale only after an hour, and b, whose
// heartbeat is an hour, so that a sees b's presence change only when something b publishes changes.
func quietPair(t *testing.T, remote string, threads *fakeThreads) (a, b *device) {
	a = newDevice(t, remote, time.Hour)
	b = newDeviceWith(t, remote, func(d *device) {
		d.beat = time.Hour
		if threads != nil {
			d.threadState = threads.read
		}
	})
	return a, b
}

func TestSendSaysWakingIsOffForTheRecipient(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := quietPair(t, remote, nil)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	carol, _ := b.session("cccccc", "")
	bobName, carolName := b.named(bob), b.named(carol)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })

	// Bob may be woken, and nothing says otherwise.
	if r := a.send("aaaaaa", bob, "hello", ""); strings.Contains(r.Note, wontBeWoken) {
		t.Errorf("a session that may be woken: %q", r.Note)
	}
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })

	b.putSettings(`{"wake":{"when":"never"}}`)

	// On bob's own device, at once.
	r := b.send("cccccc", bobName, "local", "")
	if !strings.Contains(r.Note, bobName+" "+wontBeWoken) || !strings.Contains(r.Note, "waking is off") ||
		!strings.Contains(r.Note, "next turn") {
		t.Errorf("a local sender was told %q", r.Note)
	}
	if got := b.lookupText("cccccc", bobName); !strings.Contains(got, wontBeWoken+": waking is off") {
		t.Errorf("a local lookup:\n%s", got)
	}

	// On another device, once bob's presence says so: the change is published, as no heartbeat
	// comes in time.
	eventually(t, "alice's lookup to say bob won't be woken", func() bool {
		return strings.Contains(a.lookupText("aaaaaa", bobName), wontBeWoken+": waking is off")
	})
	r = a.send("aaaaaa", bobName, "remote", "")
	if !strings.Contains(r.Note, bobName+" "+wontBeWoken) || !strings.Contains(r.Note, "waking is off") {
		t.Errorf("a remote sender was told %q", r.Note)
	}
	eventually(t, "the message to reach b", func() bool { return logHas(b, "remote") })
	holds(t, "no handoff under never", 500*time.Millisecond, func() bool { return len(calls()) == 1 })

	// A person's message is kept out too, so a person is told the same.
	pg := a.page()
	pg.choose("ada-lovelace")
	if note := pg.sendNote(bobName, "from a person"); !strings.Contains(note, bobName+" "+wontBeWoken) {
		t.Errorf("a person was told %q", note)
	}

	// Waking back on for bob alone: the warning goes, and carol, still under the device's never, is
	// named in a message to her device.
	b.putSettings(`{"wake":{"when":"never"},"sessionWake":{"` + bobName + `":{"when":"always"}}}`)
	eventually(t, "alice's lookup to drop the warning", func() bool {
		return !strings.Contains(a.lookupText("aaaaaa", bobName), wontBeWoken)
	})
	if r := a.send("aaaaaa", bobName, "again", ""); strings.Contains(r.Note, wontBeWoken) {
		t.Errorf("bob may be woken again: %q", r.Note)
	}
	r = a.send("aaaaaa", b.self(), "all of you", "")
	if !strings.Contains(r.Note, carolName+" "+wontBeWoken) || strings.Contains(r.Note, bobName) {
		t.Errorf("a send to the device was told %q", r.Note)
	}
}

// Under the default, within 5 minutes, a thread idle for longer isn't woken by an agent's message,
// and the sender is told. A person's message to it alone wakes it however long it has been idle, so
// a person sending to it isn't told, and once it has, presence says it may be woken again; one to
// its device doesn't, so a person sending there is.
func TestSendSaysTheRecipientHasBeenIdleTooLong(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-bob": {LastRun: time.Now().Add(-10 * time.Minute)}}}
	a, b := quietPair(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	b.session("cccccc", "")
	bobName := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	idle := wontBeWoken + ": it has been idle longer than its wake setting allows"
	eventually(t, "bob's own device to see the thread idle", func() bool {
		return strings.Contains(b.lookupText("cccccc", bobName), idle)
	})
	if r := b.send("cccccc", bobName, "local", ""); !strings.Contains(r.Note, bobName+" "+wontBeWoken) ||
		!strings.Contains(r.Note, "idle longer") {
		t.Errorf("a local sender was told %q", r.Note)
	}
	eventually(t, "alice's lookup to say bob won't be woken", func() bool {
		return strings.Contains(a.lookupText("aaaaaa", bobName), idle)
	})
	if r := a.send("aaaaaa", bobName, "remote", ""); !strings.Contains(r.Note, bobName+" "+wontBeWoken) ||
		!strings.Contains(r.Note, "idle longer") {
		t.Errorf("a remote sender was told %q", r.Note)
	}
	holds(t, "no handoff to a thread idle 10 minutes", 500*time.Millisecond, func() bool { return len(calls()) == 0 })

	// A person's message to bob's device doesn't wake an idle thread (only one to bob alone does), so
	// the person is told.
	pg := a.page()
	pg.choose("ada-lovelace")
	if note := pg.sendNote(b.self(), "to the device from a person"); !strings.Contains(note, bobName+" "+wontBeWoken) {
		t.Errorf("a person's message to the device was told %q", note)
	}
	eventually(t, "the message to the device to reach b", func() bool { return logHas(b, "to the device from a person") })
	holds(t, "no handoff for a person's message to the device", 500*time.Millisecond, func() bool { return len(calls()) == 0 })

	if note := pg.sendNote(bobName, "from a person"); strings.Contains(note, wontBeWoken) {
		t.Errorf("a person's message wakes an idle thread, but the person was told %q", note)
	}
	eventually(t, "the person's message handed over", func() bool { return len(calls()) >= 1 })
	eventually(t, "alice's lookup to drop the warning", func() bool {
		return !strings.Contains(a.lookupText("aaaaaa", bobName), wontBeWoken)
	})
}

// A sender to a group, or to everyone, is told which of the sessions it reaches won't be woken.
func TestSendToManyNamesTheSessionsThatWontBeWoken(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := b.session("cccccc", "")
	bobName, carolName := b.named(bob), b.named(carol)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })
	b.tagSelf("bbbbbb", []string{"group:ui"}, nil)
	b.putSettings(`{"wake":{"when":"always"},"sessionWake":{"` + bobName + `":{"when":"never"}}}`)

	eventually(t, "alice to see bob won't be woken", func() bool {
		return strings.Contains(a.lookupText("aaaaaa", bobName), wontBeWoken)
	})
	eventually(t, "alice to see bob in group:ui", func() bool {
		return strings.Contains(a.lookupText("aaaaaa", bobName), "group:ui")
	})
	if r := a.send("aaaaaa", "group:ui", "to the group", ""); !strings.Contains(r.Note, bobName+" "+wontBeWoken) {
		t.Errorf("the group's sender was told %q", r.Note)
	}
	if r := a.send("aaaaaa", "*", "to everyone", ""); !strings.Contains(r.Note, bobName+" "+wontBeWoken) ||
		strings.Contains(r.Note, carolName) {
		t.Errorf("a sender to everyone was told %q", r.Note)
	}
}
