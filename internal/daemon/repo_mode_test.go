package daemon_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

func TestRepoModeChangesAcrossDevices(t *testing.T) {
	remote := newRemote(t)
	clone := filepath.Join(t.TempDir(), "owner")
	git(t, filepath.Dir(clone), "clone", "-q", remote, clone)
	if _, err := setup.RefsOnly(t.Context(), clone); err != nil {
		t.Fatal(err)
	}
	a := &device{t: t, gitDir: filepath.Join(clone, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	a.start()
	b, c := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := c.session("cccccc", "")
	eventually(t, "owner knows both teammates", func() bool {
		return a.seesSession(bob) && a.seesSession(carol)
	})
	if r := b.do(proto.Request{Op: proto.OpRepoMode, EncryptionMode: bus.EncryptionPrivate}); !strings.Contains(r.Error, "only the mesh owner") {
		t.Fatalf("teammate changed the mode: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpRepoMode, EncryptionMode: bus.EncryptionOff}); !strings.Contains(r.Error, "explicit confirmation") {
		t.Fatalf("turned encryption off without confirmation: %+v", r)
	}
	mode := func(want string) {
		t.Helper()
		note := "Repo encryption: " + want + "."
		if r := a.do(proto.Request{Op: proto.OpRepoMode, EncryptionMode: want, AllowPlaintext: want == bus.EncryptionOff}); r.Error != "" || r.Note != note {
			t.Fatalf("switching to %s: %+v", want, r)
		}
		eventually(t, "teammates see "+want, func() bool {
			return b.do(proto.Request{Op: proto.OpRepoMode}).Note == note && c.do(proto.Request{Op: proto.OpRepoMode}).Note == note
		})
	}
	send := func(subject, body string) bus.Event {
		t.Helper()
		if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: subject, Body: body}); r.Error != "" {
			t.Fatalf("send %s: %+v", subject, r)
		}
		eventually(t, "recipient reads "+subject, func() bool {
			return slices.ContainsFunc(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages, func(m proto.MessageView) bool {
				return m.Subject == subject && m.Body == body
			})
		})
		return lastEventOn(t, remote, a.self())
	}
	initial := teamIDs(t, remote)

	mode(bus.EncryptionPrivate)
	private := send("private draft", "private message scope fixture")
	recipients := []string{a.self(), b.self()}
	slices.Sort(recipients)
	if private.Box == nil || !slices.Equal(private.Box.Recipients(), recipients) {
		t.Fatalf("private event recipients: %+v", private)
	}
	if strings.Contains(remoteObjects(t, remote), "private message scope fixture") {
		t.Fatal("the private draft is readable in the remote objects")
	}

	mode(bus.EncryptionOff)
	clear := send("plaintext draft", "plaintext scope fixture")
	if clear.Box != nil || clear.Body != "plaintext scope fixture" {
		t.Fatalf("Off wrote an encrypted event: %+v", clear)
	}

	mode(bus.EncryptionTeam)
	epochs := teamIDs(t, remote)
	if len(epochs) != len(initial)+1 || slices.Contains(initial, epochs[len(epochs)-1]) {
		t.Fatalf("returning to Team reused an earlier key: before %v, after %v", initial, epochs)
	}
	team := send("team draft", "fresh team key scope fixture")
	if team.Box == nil || !slices.Contains(team.Box.Recipients(), epochs[len(epochs)-1]) {
		t.Fatalf("Team didn't write for the new key: %+v", team)
	}
}
