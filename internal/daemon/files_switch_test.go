package daemon_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// lookupShows waits for lookup of name, from session on d, to show want.
func lookupShows(t *testing.T, d *device, session, name, want string) {
	t.Helper()
	var look string
	eventually(t, "lookup of "+name+" to show "+strings.TrimSpace(want), func() bool {
		look = view.Lookup(d.do(proto.Request{Op: proto.OpLookup, Session: session, Name: name}))
		return strings.Contains(look, want)
	})
}

// Send files and Receive files are off until a person turns them on (#765, Tyler, 2026-11-16). With
// sending off, a send with files is refused before anything is read or pushed, saying the user can
// turn it on. who doesn't mention it: most agents never send files, and the refusal says it when it
// matters (#859, Tyler, 2026-11-17). lookup is where an agent checks, of itself or another session.
func TestASendWithFilesIsRefusedWhileSendFilesIsOff(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.filesOff = true })
	me, _ := a.session("aaaaaa", "")
	other, _ := a.session("cccccc", "")
	if who := view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})); strings.Contains(strings.ToLower(who), "files") {
		t.Errorf("who mentions files:\n%s", who)
	}
	const off = "  files: sends none (Send files is off for agents here), takes none from other devices (Receive files is off for agents here)\n"
	for _, addr := range []string{me, other} {
		if look := view.Lookup(a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: a.named(addr)})); !strings.Contains(look, off) {
			t.Errorf("lookup of %s, want %q:\n%s", addr, off, look)
		}
	}
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "a log",
		Files: []string{writeFile(t, "build.log", []byte("a line\n"))}})
	if !strings.Contains(r.Error, "not sent: sending files is off for agents in this repo") || !strings.Contains(r.Error, "Send files") {
		t.Errorf("send: %q, want it refused, saying Send files is off", r.Error)
	}
	if refs := gitOK(remote, "for-each-ref", "refs/murmur/files"); refs != "" {
		t.Errorf("a refused send left files on the remote: %s", refs)
	}
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "the text alone"}); r.Error != "" {
		t.Errorf("a send with no files: %s", r.Error)
	}
}

// A device with Receive files off takes a message's text, and neither fetches nor shows its files.
// Its presence says so, so the sender is told when it sends files, and lookup shows it; who doesn't
// (#859).
func TestReceiveFilesOffTakesTheTextAlone(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.filesOff = true })
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see bob", func() bool { return a.seesSession(bob) })
	name := a.named(bob)
	if who := view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})); strings.Contains(strings.ToLower(who), "files") {
		t.Errorf("who mentions files:\n%s", who)
	}
	lookupShows(t, a, "aaaaaa", name, "  files: takes none (Receive files is off for agents on its device)\n")
	if look, want := view.Lookup(a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: a.named(bus.Addr(a.self(), "aaaaaa"))})),
		"  files: sends them, takes them\n"; !strings.Contains(look, want) {
		t.Errorf("lookup of itself, files on, want %q:\n%s", want, look)
	}
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "the failing page",
		Files: []string{writeFile(t, "shot.png", []byte("not really a picture"))}})
	if r.Error != "" || !strings.Contains(r.Note, "takes no files: Receive files is off for the agents on its device") {
		t.Errorf("send: %+v, want it sent, its note saying bob takes no files", r)
	}
	m := inboxMessage(t, b, "bbbbbb")
	if m.Subject != "the failing page" || len(m.Files) != 0 {
		t.Errorf("bob got %q with files %+v; want the text alone", m.Subject, m.Files)
	}
	// It says a file was kept from it, and why, so bob can say so (#860).
	turn := view.Turn(m)
	if want := "It came with a file, which you don't have: your user turned off receiving files.\n"; m.FilesKept != 1 || !strings.Contains(turn, want) {
		t.Errorf("bob's turn, FilesKept %d, lacks %q:\n%s", m.FilesKept, want, turn)
	}
	t.Logf("bob's turn:\n%s", turn)
	if got, _ := filepath.Glob(filepath.Join(b.gitDir, brand.StateDir, "files", "*")); len(got) != 0 {
		t.Errorf("b fetched files it doesn't take: %v", got)
	}
}

// The person's switches and the agents' are their own (Tyler, 2026-11-16): with the person's on and
// the agents' off, the page sends and shows files while an agent's send with files is refused and
// its inbox gets the text alone; and a sender to the person is told when the person takes none.
func TestThePersonsFileSwitchesAreApartFromTheAgents(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) {
		d.files = `{"sendFiles":true,"receiveFiles":true,"agentsSendOff":true,"agentsReceiveOff":true}`
	})
	b := newDevice(t, remote)
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada-lovelace")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(bus.Addr(a.self(), bus.PersonSession)) })

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bobName, Subject: "a log",
		Files: []string{writeFile(t, "build.log", []byte("a line\n"))}})
	if !strings.Contains(r.Error, "sending files is off for agents") {
		t.Errorf("an agent's send with files, its agents' Send files off: %q", r.Error)
	}
	if look, want := view.Lookup(a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: "ada-lovelace"})),
		"  files: sends them, takes them\n"; !strings.Contains(look, want) {
		t.Errorf("lookup of the person here, want %q:\n%s", want, look)
	}
	if code, body := pa.sendFiles(bobName, "from me", web.Upload{Name: "notes.txt", Data: []byte("mine\n")}); code != 200 {
		t.Errorf("the person's send with files, their Send files on: %d %s", code, body)
	}

	// From bob to a's agent and to a's person: the agent gets the text alone, the person the file.
	lookupShows(t, b, "bbbbbb", bus.Addr(a.self(), "aaaaaa"), "  files: takes none (Receive files is off for agents on its device)\n")
	r = b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: bus.Addr(a.self(), "aaaaaa"), Subject: "for the agent",
		Files: []string{writeFile(t, "shot.png", []byte("a picture"))}})
	if r.Error != "" || !strings.Contains(r.Note, "Receive files is off for the agents") {
		t.Errorf("to a's agent: %+v, want a note that it takes no files", r)
	}
	if m := inboxMessage(t, a, "aaaaaa"); len(m.Files) != 0 {
		t.Errorf("a's agent got files %+v; want the text alone", m.Files)
	}
	lookupShows(t, b, "bbbbbb", "ada-lovelace", "  files: takes them\n")
	r = b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: "ada-lovelace", Subject: "for the person",
		Files: []string{writeFile(t, "chart.png", []byte("a chart"))}})
	if r.Error != "" || strings.Contains(r.Note, "text alone") {
		t.Errorf("to a's person: %+v, want it sent with no note", r)
	}
	var m web.Message
	eventually(t, "the page to list the person's message", func() (ok bool) {
		m, ok = pa.messageWith("for the person")
		return ok
	})
	if len(m.Files) != 1 {
		t.Fatalf("the page's message: %+v, want its file", m)
	}
	if code, body := pa.call("GET", "/api/file?id="+m.ID+"&i=0", pa.token, ""); code != 200 || body != "a chart" {
		t.Errorf("the person's file, asked for: %d %q", code, body)
	}

	// A save names the switches it sets; one from a page that hadn't seen them change leaves them.
	if code, body := pa.call("PUT", "/api/settings", pa.token, `{"wake":{"when":"within"},"sendFiles":false,"receiveFiles":false}`); code != 200 {
		t.Fatalf("a save naming no switch: %d %s", code, body)
	}
	if _, body := pa.call("GET", "/api/settings", pa.token, ""); !strings.Contains(body, `"sendFiles":true`) || !strings.Contains(body, `"receiveFiles":true`) {
		t.Errorf("a save naming no switch changed them: %s", body)
	}
	if code, body := pa.call("PUT", "/api/settings", pa.token, `{"wake":{"when":"within"},"receiveFiles":false,"filesSet":["receiveFiles"]}`); code != 200 {
		t.Fatalf("a save naming one switch: %d %s", code, body)
	}
	if _, body := pa.call("GET", "/api/settings", pa.token, ""); !strings.Contains(body, `"sendFiles":true`) || !strings.Contains(body, `"receiveFiles":false`) {
		t.Errorf("a save naming receiveFiles: %s", body)
	}
}

// A sender to a person who turned Receive files off is told so when it sends files, and lookup
// shows it; who doesn't (#859).
func TestASenderIsToldWhenAPersonTakesNoFiles(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.files = `{"sendFiles":true}` })
	b := newDevice(t, remote)
	b.session("bbbbbb", "")
	pa := a.page()
	pa.choose("ada-lovelace")
	eventually(t, "b to see ada", func() bool {
		return strings.Contains(view.Who(b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"})), "ada-lovelace")
	})
	if who := view.Who(b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"})); strings.Contains(strings.ToLower(who), "files") {
		t.Errorf("who mentions files:\n%s", who)
	}
	lookupShows(t, b, "bbbbbb", "ada-lovelace", "  files: takes none (Receive files is off)\n")
	r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: "ada-lovelace", Subject: "the chart",
		Files: []string{writeFile(t, "chart.png", []byte("a chart"))}})
	if r.Error != "" || !strings.Contains(r.Note, "ada-lovelace takes no files: Receive files is off, so the text alone gets there.") {
		t.Errorf("to ada: %+v, want a note that she takes no files", r)
	}
}

// A device that has files republishes its presence every heartbeat, 500 ms here, and the reader
// fetches and reads each newer one in turn. lookup never says its files are unknown meanwhile: the
// last one read stands (#859). The presences counted are the ones a fetched, from its own copy of
// the ref, so each was one a had to read.
func TestADeviceWithFilesIsNeverUnknownWhileItRepublishes(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.filesOff = true })
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see bob", func() bool { return a.seesSession(bob) })
	name := a.named(bob)
	const want = "  files: takes none (Receive files is off for agents on its device)\n"
	lookupShows(t, a, "aaaaaa", name, want)
	ref := brand.PresencePrefix + b.self()
	shas := map[string]bool{}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) || len(shas) < 3; time.Sleep(50 * time.Millisecond) {
		if look := view.Lookup(a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: name})); !strings.Contains(look, want) {
			t.Fatalf("lookup of bob as it republishes:\n%s", look)
		}
		if sha := gitOK(a.gitDir, "rev-parse", ref); sha != "" {
			shas[sha] = true
		}
		if len(shas) < 3 && time.Now().After(end.Add(55*time.Second)) {
			t.Fatalf("a fetched %d of bob's presences in a minute, so this tested little", len(shas))
		}
	}
}
