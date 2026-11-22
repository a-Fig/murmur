package daemon_test

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/title"
)

// Every refusal of a send says whether it is about the recipient alone or about the message or the
// sender (#541), which is how a send to a list knows whether to go on to the rest. A refusal for
// the recipient also quotes it as it was given, which is what an MCP server from before the field
// goes by.
func TestASendRefusalSaysWhatItIsAbout(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	d := newCodexDevice(t, remote, &fakeCodex{lin: map[string]title.Lineage{}})
	alice, _ := d.agentSession("aaaaaa", "opencode", 700, proto.Route{Native: "ses-alice"})
	bob, _ := d.agentSession("bbbbbb", "opencode", 700, proto.Route{Native: "ses-bob"})
	kid, _ := d.agentSession("cccccc", "opencode", 700, proto.Route{Native: "ses-kid"})
	aliceName, bobName, kidName := d.named(alice), d.named(bob), d.named(kid)
	if r := d.mute("bbbbbb", aliceName, false); r.Error != "" {
		t.Fatalf("bob mutes alice: %s", r.Error)
	}
	d.do(proto.Request{Op: proto.OpNotices, Native: "ses-kid", Child: true, ParentNative: "ses-alice"})
	eventually(t, "the child to leave who", func() bool { return !d.listsSession(kid) })
	nobody := "calm-heron"
	for _, n := range []string{"brisk-finch", "quiet-otter", "calm-heron"} {
		if n != aliceName && n != bobName && n != kidName && names.Valid(n) {
			nobody = n
		}
	}

	for _, c := range []struct {
		what, from, to, subject, body string
		about, says                   string
	}{
		{"a name with capitals", "aaaaaa", "Quiet-Otter", "hi", "", proto.RefusedRecipient, `"Quiet-Otter" isn't a recipient`},
		{"not a group", "aaaaaa", "group:UI!", "hi", "", proto.RefusedRecipient, `"group:UI!" isn't a group`},
		{"a name nobody has", "aaaaaa", nobody, "hi", "", proto.RefusedRecipient, nobody},
		{"a mute, by name", "aaaaaa", bobName, "hi", "", proto.RefusedRecipient, bobName + " has muted you"},
		{"a mute, by address", "aaaaaa", bob, "hi", "", proto.RefusedRecipient, bob + " has muted you"},
		{"a delegate", "bbbbbb", kidName, "hi", "", proto.RefusedRecipient, kidName + " is a delegate"},
		// No key to encrypt to, which names the device.
		{"a device never seen", "aaaaaa", "ghostdev/abcdef", "hi", "", proto.RefusedRecipient, "ghostdev isn't a device this one has seen"},
		{"no subject", "aaaaaa", "Quiet-Otter", "", "", proto.RefusedSend, "needs a recipient"},
		// A group that is one, with no subject, is refused for the subject, not as "isn't a group".
		{"no subject, to a group", "aaaaaa", "group:ui", " ", "", proto.RefusedSend, "and a subject"},
		{"a key", "aaaaaa", bobName, "hi", "AKIAIOSFODNN7EXAMPLE", proto.RefusedSend, "not sent"},
		{"a body too long", "aaaaaa", bobName, "hi", strings.Repeat("x", 70<<10), proto.RefusedSend, "the body is over"},
		{"not registered", "zzzzzz", bobName, "hi", "", proto.RefusedSend, "isn't registered"},
		// A delegate sending is told to go through its parent, by name, though that is who it sent to.
		{"a delegate sending", "cccccc", aliceName, "hi", "", proto.RefusedSend, "you are a delegate"},
	} {
		r := d.do(proto.Request{Op: proto.OpSend, Session: c.from, To: c.to, Subject: c.subject, Body: c.body})
		if r.OK || r.Refused != c.about || !strings.Contains(r.Error, c.says) {
			t.Errorf("%s: ok %v, refused %q (want %q), error %q (want it to say %q)", c.what, r.OK, r.Refused, c.about, r.Error, c.says)
		}
	}
	// The refusal of a recipient with capitals suggests it in lowercase only if that is someone.
	for _, c := range []struct{ to, hint string }{
		{strings.ToUpper(bobName[:1]) + bobName[1:], "did you mean " + bobName + "?"},
		{strings.ToUpper(bob), "did you mean " + bob + "?"},
		{strings.ToUpper(nobody[:1]) + nobody[1:], ""},
		{"DEV/ABC", ""},
	} {
		r := d.trySend("aaaaaa", c.to, "hi")
		if c.hint == "" && strings.Contains(r.Error, "did you mean") || !strings.HasSuffix(r.Error, c.hint) || r.Refused != proto.RefusedRecipient {
			t.Errorf("%s: refused %q, error %q (want it to end %q)", c.to, r.Refused, r.Error, c.hint)
		}
	}
	if r := d.trySend("bbbbbb", aliceName, "hi"); !r.OK || r.Refused != "" {
		t.Errorf("a send that went: ok %v, refused %q, error %q", r.OK, r.Refused, r.Error)
	}
}
