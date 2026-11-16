package daemon_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// Send files and Receive files are off until a person turns them on (#765, Tyler, 2026-11-16). With
// sending off, a send with files is refused before anything is read or pushed, saying the user can
// turn it on, and who says so first.
func TestASendWithFilesIsRefusedWhileSendFilesIsOff(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.filesOff = true })
	a.session("aaaaaa", "")
	if who := view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})); !strings.Contains(who, "AGENTS SEND NO FILES") {
		t.Errorf("who doesn't say this device sends no files:\n%s", who)
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
// Its presence says so, so the sender is told when it sends, and who and lookup show it before.
func TestReceiveFilesOffTakesTheTextAlone(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.filesOff = true })
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see that bob's device takes no files", func() bool {
		return strings.Contains(view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})), "AGENTS TAKE NO FILES")
	})
	name := a.named(bob)
	if look := view.Lookup(a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: name})); !strings.Contains(look, "takes no files") {
		t.Errorf("lookup doesn't say bob takes no files:\n%s", look)
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
	a := newDeviceWith(t, remote, func(d *device) { d.files = `{"pageSendFiles":true,"pageReceiveFiles":true}` })
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
	if code, body := pa.sendFiles(bobName, "from me", web.Upload{Name: "notes.txt", Data: []byte("mine\n")}); code != 200 {
		t.Errorf("the person's send with files, their Send files on: %d %s", code, body)
	}

	// From bob to a's agent and to a's person: the agent gets the text alone, the person the file.
	r = b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: bus.Addr(a.self(), "aaaaaa"), Subject: "for the agent",
		Files: []string{writeFile(t, "shot.png", []byte("a picture"))}})
	if r.Error != "" || !strings.Contains(r.Note, "Receive files is off for the agents") {
		t.Errorf("to a's agent: %+v, want a note that it takes no files", r)
	}
	if m := inboxMessage(t, a, "aaaaaa"); len(m.Files) != 0 {
		t.Errorf("a's agent got files %+v; want the text alone", m.Files)
	}
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
	if code, body := pa.call("PUT", "/api/settings", pa.token, `{"wake":{"when":"within"},"pageSendFiles":false,"pageReceiveFiles":false}`); code != 200 {
		t.Fatalf("a save naming no switch: %d %s", code, body)
	}
	if _, body := pa.call("GET", "/api/settings", pa.token, ""); !strings.Contains(body, `"pageSendFiles":true`) || !strings.Contains(body, `"pageReceiveFiles":true`) {
		t.Errorf("a save naming no switch changed them: %s", body)
	}
	if code, body := pa.call("PUT", "/api/settings", pa.token, `{"wake":{"when":"within"},"pageReceiveFiles":false,"filesSet":["pageReceiveFiles"]}`); code != 200 {
		t.Fatalf("a save naming one switch: %d %s", code, body)
	}
	if _, body := pa.call("GET", "/api/settings", pa.token, ""); !strings.Contains(body, `"pageSendFiles":true`) || !strings.Contains(body, `"pageReceiveFiles":false`) {
		t.Errorf("a save naming pageReceiveFiles: %s", body)
	}
}

// A person who turned Receive files off is shown so to a sender, and told of when sent to.
func TestAPersonWhoTakesNoFilesIsShownSo(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.files = `{"sendFiles":true,"receiveFiles":true}` })
	b := newDevice(t, remote)
	b.session("bbbbbb", "")
	pa := a.page()
	pa.choose("ada-lovelace")
	eventually(t, "b to see ada take no files", func() bool {
		return strings.Contains(view.Who(b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"})), "ada-lovelace") &&
			strings.Contains(view.Who(b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"})), ", takes no files")
	})
	if look := view.Lookup(b.do(proto.Request{Op: proto.OpLookup, Session: "bbbbbb", Name: "ada-lovelace"})); !strings.Contains(look, "takes no files") {
		t.Errorf("lookup of ada:\n%s", look)
	}
	r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: "ada-lovelace", Subject: "the chart",
		Files: []string{writeFile(t, "chart.png", []byte("a chart"))}})
	if r.Error != "" || !strings.Contains(r.Note, "ada-lovelace takes no files: they turned Receive files off") {
		t.Errorf("to ada: %+v, want a note that she takes no files", r)
	}
}
