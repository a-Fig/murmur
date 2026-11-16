package daemon_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// Send files and Receive files are off until a person turns them on (#765, Tyler, 2026-11-16). With
// sending off, a send with files is refused before anything is read or pushed, saying the user can
// turn it on, and who says so first.
func TestASendWithFilesIsRefusedWhileSendFilesIsOff(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.filesOff = true })
	a.session("aaaaaa", "")
	if who := view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})); !strings.Contains(who, "SENDS NO FILES") {
		t.Errorf("who doesn't say this device sends no files:\n%s", who)
	}
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "a log",
		Files: []string{writeFile(t, "build.log", []byte("a line\n"))}})
	if !strings.Contains(r.Error, "not sent: sending files is off in this repo") || !strings.Contains(r.Error, "Send files") {
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
		return strings.Contains(view.Who(a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"})), "TAKES NO FILES")
	})
	name := a.named(bob)
	if look := view.Lookup(a.do(proto.Request{Op: proto.OpLookup, Session: "aaaaaa", Name: name})); !strings.Contains(look, "takes no files") {
		t.Errorf("lookup doesn't say bob takes no files:\n%s", look)
	}
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "the failing page",
		Files: []string{writeFile(t, "shot.png", []byte("not really a picture"))}})
	if r.Error != "" || !strings.Contains(r.Note, "takes no files: Receive files is off on its device") {
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
