package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/proto"
)

// send's to names one recipient, or several separated by commas, or a JSON array a model wrote
// into the string (#520).
func TestRecipients(t *testing.T) {
	for _, c := range []struct {
		to   string
		want []string
	}{
		{"quiet-otter", []string{"quiet-otter"}},
		{" quiet-otter , brisk-finch,", []string{"quiet-otter", "brisk-finch"}},
		{`["quiet-otter", "dev/aaaaaa"]`, []string{"quiet-otter", "dev/aaaaaa"}},
		{"quiet-otter,quiet-otter, quiet-otter", []string{"quiet-otter"}},
		{"[not json", []string{"[not json"}},
		{" , ", nil},
		{"", nil},
	} {
		if got := recipients(c.to); !slices.Equal(got, c.want) {
			t.Errorf("recipients(%q) = %q, want %q", c.to, got, c.want)
		}
	}
}

// listDaemon answers sends as a daemon would for two sessions: quiet-otter at dev/aaaaaa and
// brisk-finch at dev/bbbbbb. Anything else is a name nobody has.
func listDaemon() *fakeDaemon {
	at := map[string]string{"quiet-otter": "dev/aaaaaa", "brisk-finch": "dev/bbbbbb", "dev/aaaaaa": "dev/aaaaaa", "dev/bbbbbb": "dev/bbbbbb"}
	return &fakeDaemon{sendReply: func(req proto.Request) proto.Response {
		addr, ok := at[req.To]
		if !ok {
			return proto.Response{Error: "no session is called " + req.To + "; see who"}
		}
		name := ""
		if addr != req.To {
			name = req.To
		}
		return proto.Response{OK: true, Messages: []proto.MessageView{{ID: "id-" + strings.TrimPrefix(addr, "dev/"), To: addr, ToName: name, Subject: req.Subject}}}
	}}
}

func (f *fakeDaemon) sends() []proto.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []proto.Request
	for _, r := range f.requests {
		if r.Op == proto.OpSend {
			out = append(out, r)
		}
	}
	return out
}

func callSend(t *testing.T, to string) (*fakeDaemon, *mcp.CallToolResult) {
	t.Helper()
	return callSendTo(t, listDaemon(), to)
}

func callSendTo(t *testing.T, f *fakeDaemon, to string) (*fakeDaemon, *mcp.CallToolResult) {
	t.Helper()
	return callSendWith(t, f, to, "the API changed")
}

func callSendWith(t *testing.T, f *fakeDaemon, to, subject string) (*fakeDaemon, *mcp.CallToolResult) {
	t.Helper()
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, time.Hour)
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "send",
		Arguments: map[string]any{"to": to, "subject": subject, "body": "details"}})
	if err != nil {
		t.Fatal(err)
	}
	return f, res
}

// A send to a list is a send to each: one ordinary send per recipient, with the same subject and
// body. One that fails, a group, and a session named twice, once by name and once by address, don't
// stop the rest, and the result says what became of each, in the order named.
func TestSendToAListSendsToEachOnce(t *testing.T) {
	f, res := callSend(t, "dev/aaaaaa, quiet-otter, group:ui, calm-heron, brisk-finch, *")
	if res.IsError {
		t.Fatalf("a send that reached two recipients is an error: %+v", res)
	}
	var got []string
	for _, r := range f.sends() {
		if r.Subject != "the API changed" || r.Body != "details" {
			t.Errorf("a send carried %q / %q", r.Subject, r.Body)
		}
		got = append(got, r.To)
	}
	// Names first, so the address of a session already sent to by name is known.
	if want := []string{"quiet-otter", "calm-heron", "brisk-finch"}; !slices.Equal(got, want) {
		t.Errorf("the daemon was asked to send to %q, want %q", got, want)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	want := "Sent to 2 of 5, each as a message of its own:\n" +
		"- dev/aaaaaa is quiet-otter, so it got the message once.\n" +
		"- Sent id-aaaaaa to quiet-otter (dev/aaaaaa).\n" +
		"- Not sent to group:ui: a list is for sessions and people, each of whom gets a message of their own; send to group:ui on its own\n" +
		"- Not sent to calm-heron: no session is called calm-heron; see who\n" +
		"- Sent id-bbbbbb to brisk-finch (dev/bbbbbb).\n" +
		"- Not sent to *: a list is for sessions and people, each of whom gets a message of their own; send to * on its own\n"
	if text != want {
		t.Errorf("result:\n%s\nwant:\n%s", text, want)
	}
}

// A list none of whose recipients got the message is an error, so the sender can't take it for sent.
func TestSendToAListNobodyGotIsAnError(t *testing.T) {
	_, res := callSend(t, "calm-heron, group:ui")
	if !res.IsError {
		t.Errorf("a list nobody got isn't an error: %+v", res.Content[0].(*mcp.TextContent).Text)
	}
}

// A device's group is a group in a list too, though its tag can pass the length a group's has
// (Sol's review of #628): the daemon would send it to the whole device.
func TestALongDevicesGroupIsRefusedInAList(t *testing.T) {
	f, res := callSend(t, "group:device-abcdefghijklmnopqrstuvwx-1234, quiet-otter")
	for _, s := range f.sends() {
		if strings.HasPrefix(s.To, "group:") {
			t.Errorf("the device's group was sent to: %+v", f.sends())
		}
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "Not sent to group:device-abcdefghijklmnopqrstuvwx-1234: a list is for sessions") {
		t.Errorf("result = %q", text)
	}
}

// One recipient, even with a comma after it, is the send it always was.
func TestSendToOneIsUnchanged(t *testing.T) {
	f, res := callSend(t, "quiet-otter,")
	if got := f.sends(); len(got) != 1 || got[0].To != "quiet-otter" {
		t.Fatalf("sends = %+v", got)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; text != "Sent id-aaaaaa to quiet-otter (dev/aaaaaa).\n" {
		t.Errorf("result = %q", text)
	}
}

// Past the limit, nothing is sent.
func TestSendToTooManyIsRefused(t *testing.T) {
	var many []string
	for i := range maxRecipients + 1 {
		many = append(many, fmt.Sprintf("dev/s%d", i))
	}
	f, res := callSend(t, strings.Join(many, ","))
	if !res.IsError || len(f.sends()) != 0 {
		t.Errorf("a send to %d: error %v, %d sends", len(many), res.IsError, len(f.sends()))
	}
}

// A refusal that doesn't name the recipient is about the message or the sender, and would be the
// same for every recipient, so the rest aren't tried. One that names it is that recipient's alone,
// and the daemon's "not sent: " isn't said twice.
func TestSendToAListStopsOnARefusalOfTheMessage(t *testing.T) {
	f := &fakeDaemon{sendReply: func(req proto.Request) proto.Response {
		if req.To == "quiet-otter" {
			return proto.Response{Error: "not sent: quiet-otter is offline and takes no messages."}
		}
		return proto.Response{Error: "not sent: the body holds what looks like a key"}
	}}
	f, res := callSendTo(t, f, "quiet-otter, brisk-finch, calm-heron, dev/aaaaaa")
	if got := f.sends(); len(got) != 2 {
		t.Errorf("the daemon was asked %d times, want 2: once for the offline one, once for the key", len(got))
	}
	text := res.Content[0].(*mcp.TextContent).Text
	want := "Sent to 0 of 4, each as a message of its own:\n" +
		"- Not sent to quiet-otter: quiet-otter is offline and takes no messages\n" +
		"- Not sent to brisk-finch: the body holds what looks like a key\n" +
		"- Not sent to calm-heron: not tried, as the send to brisk-finch failed for a reason that isn't about brisk-finch\n" +
		"- Not sent to dev/aaaaaa: not tried, as the send to brisk-finch failed for a reason that isn't about brisk-finch\n"
	if text != want || !res.IsError {
		t.Errorf("result (error %v):\n%s\nwant:\n%s", res.IsError, text, want)
	}
}

// A refusal that is about one recipient counts against it alone, and the rest of the list is sent;
// one about the message or the sender stops it (#541). The daemon says which (Refused); a daemon
// from before that is judged by its words, whose refusals of a name that isn't one, of a mute and of
// an offline session don't always quote the recipient as it was named.
func TestSendToAListGoesOnPastARefusalOfOneRecipient(t *testing.T) {
	const (
		oldShape   = "a message needs a recipient (a session's name, device/session, a device, a group such as group:ui, or * for everyone; see who) and a subject"
		oldMuted   = "your message to dev/cccccc was not delivered. dev/cccccc has muted you. Nothing you send can reach them until they unmute you."
		oldOffline = "not sent: dev/cccccc is offline and takes no messages: its T3 thread is settled, or it is a delegate another thread started, which is reached through that thread."
		oldSettled = "not sent: dev/cccccc is a T3 thread that is settled, so it is offline and takes no messages. It is back once someone opens the thread again."
		oldChild   = "not sent: dev/cccccc is a delegate that another thread started, and a delegate is reached through the thread that started it. Message calm-heron instead."
		delegate   = "you are a delegate that another thread started, and only that thread is on Murmur: tell it what you would send or what you are doing, and it decides what goes out. It is quiet-otter."
		oldDevice  = "not sent: olddev-0001 runs a version of Murmur from before the bus was encrypted, so it couldn't read this. It can once it runs Murmur 0.3 or later."
		oldUnseen  = "not sent: ghostdev isn't a device this one has seen, so there is no key to encrypt to"
	)
	for _, c := range []struct {
		name, first, subject string
		refusal              proto.Response
		goesOn               bool
	}{
		// The issue's case: a name with capitals, from the daemon of this version and an older one.
		{"a name that isn't one", "Quiet-Otter", "the API changed",
			proto.Response{Error: `not sent: "Quiet-Otter" isn't a recipient.`, Refused: proto.RefusedRecipient}, true},
		{"a name that isn't one, from an older daemon", "Quiet-Otter", "the API changed", proto.Response{Error: oldShape}, true},
		{"a mute, at an address that moved", "dev/aaaaaa", "the API changed",
			proto.Response{Error: oldMuted, Refused: proto.RefusedRecipient}, true},
		{"a mute, from an older daemon", "dev/aaaaaa", "the API changed", proto.Response{Error: oldMuted}, true},
		{"offline, from an older daemon", "dev/aaaaaa", "the API changed", proto.Response{Error: oldOffline}, true},
		{"settled, from an older daemon", "dev/aaaaaa", "the API changed", proto.Response{Error: oldSettled}, true},
		{"a delegate, from an older daemon", "dev/aaaaaa", "the API changed", proto.Response{Error: oldChild}, true},
		// No key for the recipient's device: one from before encryption, or one never seen.
		{"a device with no key", "quiet-otter", "the API changed", proto.Response{Error: oldDevice, Refused: proto.RefusedRecipient}, true},
		{"a device from before encryption, from an older daemon", "quiet-otter", "the API changed", proto.Response{Error: oldDevice}, true},
		{"a device never seen, from an older daemon", "ghostdev/abcdef", "the API changed", proto.Response{Error: oldUnseen}, true},
		// The sender is a delegate, and the refusal names its parent, the first recipient: it is still
		// the sender's.
		{"a delegate sending", "quiet-otter", "the API changed", proto.Response{Error: delegate, Refused: proto.RefusedSend}, false},
		{"no subject, from an older daemon", "Quiet-Otter", "", proto.Response{Error: oldShape}, false},
		{"a key, from an older daemon", "quiet-otter", "the API changed", proto.Response{Error: "not sent: the body holds what looks like a key"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The second recipient is of the first's kind, so it is sent to after it: names go first.
			second := "brisk-finch"
			if strings.Contains(c.first, "/") {
				second = "dev/bbbbbb"
			}
			f := &fakeDaemon{sendReply: func(req proto.Request) proto.Response {
				if req.To == c.first {
					return c.refusal
				}
				return proto.Response{OK: true, Messages: []proto.MessageView{{ID: "id-bbbbbb", To: "dev/bbbbbb", Subject: req.Subject}}}
			}}
			f, res := callSendWith(t, f, c.first+", "+second, c.subject)
			var tried []string
			for _, r := range f.sends() {
				tried = append(tried, r.To)
			}
			text := res.Content[0].(*mcp.TextContent).Text
			if c.goesOn {
				if !slices.Equal(tried, []string{c.first, second}) || res.IsError || !strings.Contains(text, "Sent id-bbbbbb to dev/bbbbbb") {
					t.Errorf("sent to %q (error %v):\n%s", tried, res.IsError, text)
				}
				return
			}
			if !slices.Equal(tried, []string{c.first}) || !res.IsError || !strings.Contains(text, second+": not tried") {
				t.Errorf("sent to %q (error %v):\n%s", tried, res.IsError, text)
			}
		})
	}
}

// A call no daemon answered is about the sender, whatever its words: they can hold a short
// recipient, a device ID such as e, by chance, and the rest of the list would wait on the daemon
// again for each.
func TestACallNoDaemonAnsweredStopsAList(t *testing.T) {
	for _, why := range []string{
		"can't reach the Murmur daemon: dial tcp 127.0.0.1:52011: connect: connection refused",
		errPassive.Error(),
	} {
		if refusedRecipient(proto.Response{}, why, "e", "the API changed") {
			t.Errorf("%q is taken for a refusal of e alone", why)
		}
	}
}
