package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of a person's account (#242): a human on the page has a name, sends to agents, and gets what
// agents send back there.

// choose gives the page's person a name, and fails the test if that is refused.
func (p *page) choose(name string) {
	p.t.Helper()
	code, body := p.call("PUT", "/api/account", p.token, `{"name":"`+name+`"}`)
	if code != http.StatusOK {
		p.t.Fatalf("choosing the name %s: %d %s", name, code, body)
	}
}

// sendText sends as the page's person, and returns the message's ID.
func (p *page) sendText(to, text string) string {
	p.t.Helper()
	b, _ := json.Marshal(map[string]string{"to": to, "text": text})
	code, body := p.call("POST", "/api/send", p.token, string(b))
	if code != http.StatusOK {
		p.t.Fatalf("sending to %s: %d %s", to, code, body)
	}
	var sent web.Sent
	if err := json.Unmarshal([]byte(body), &sent); err != nil || sent.ID == "" {
		p.t.Fatalf("what a send returned: %q: %v", body, err)
	}
	return sent.ID
}

func (p *page) messageWith(subject string) (web.Message, bool) {
	msgs, _ := p.messages("")
	for _, m := range msgs {
		if m.Subject == subject {
			return m, true
		}
	}
	return web.Message{}, false
}

// whoEntry is who is at addr, as device d's who lists them.
func (d *device) whoEntry(addr string) (proto.SessionView, bool) {
	for _, dv := range d.do(proto.Request{Op: proto.OpWho}).Devices {
		for _, s := range dv.Sessions {
			if s.Addr == addr {
				return s, true
			}
		}
	}
	return proto.SessionView{}, false
}

func TestAPersonIsAParticipantOnceTheyHaveAName(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	pa, pb := a.page(), b.page()
	addr := a.self() + "/" + bus.PersonSession

	if got := pa.state().Account; got.Name != "" || got.Addr != "" {
		t.Errorf("with no name chosen, the account is %+v", got)
	}
	if _, listed := a.whoEntry(addr); listed {
		t.Error("who lists a person who has chosen no name")
	}
	if code, _ := pa.call("POST", "/api/send", pa.token, `{"to":"*","text":"hi"}`); code != http.StatusBadRequest {
		t.Errorf("sending with no name: %d, want a refusal", code)
	}

	pa.choose("ada-lovelace")
	if got := pa.state().Account; got.Name != "ada-lovelace" || got.Addr != addr {
		t.Errorf("the account: %+v, want the name and %s", got, addr)
	}
	if e, ok := a.whoEntry(addr); !ok || e.Name != "ada-lovelace" || e.Agent != bus.AgentPerson {
		t.Errorf("who on their own device: %+v, %v", e, ok)
	}
	eventually(t, "the other device to list them", func() bool {
		e, ok := b.whoEntry(addr)
		return ok && e.Name == "ada-lovelace" && e.Agent == bus.AgentPerson
	})
	eventually(t, "the other device's page to show them", func() bool {
		for _, d := range pb.state().Devices {
			if d.Person != nil && d.Person.Name == "ada-lovelace" && d.Person.Addr == addr {
				return true
			}
		}
		return false
	})
	for _, d := range pb.state().Devices {
		for _, s := range d.Sessions {
			if s.Name == "ada-lovelace" {
				t.Errorf("a person is listed among the sessions: %+v", s)
			}
		}
	}

	// A name that won't do says why, and leaves things as they were.
	for name, why := range map[string]string{
		"ada-lovelace": "taken",           // the other device's person has it
		"Bad Name":     "can't be a name", // not in the alphabet
		"quiet-otter":  "can't be a name", // reads as a session's
		"everyone":     "can't be a name", // reads as a role
		a.self():       "a device's ID",   // a message to it would go to the device
		"x":            "can't be a name", // too short
		brand.Name:     "can't be a name", // reads as the tool's own words
	} {
		code, body := pb.call("PUT", "/api/account", pb.token, `{"name":"`+name+`"}`)
		if code != http.StatusBadRequest || !strings.Contains(body, why) {
			t.Errorf("the name %q: %d %s, want a refusal saying %q", name, code, body, why)
		}
	}
	if got := pb.state().Account.Name; got != "" {
		t.Errorf("a refused name was kept: %q", got)
	}

	// Lookup knows them as it knows a session.
	r := b.do(proto.Request{Op: proto.OpLookup, Name: "ada-lovelace"})
	if r.Error != "" || r.Lookup == nil || r.Lookup.Addr != addr || r.Lookup.Agent != bus.AgentPerson {
		t.Errorf("lookup from the other device: %+v, %q", r.Lookup, r.Error)
	}
	r = a.do(proto.Request{Op: proto.OpLookup, Name: "ada-lovelace"})
	if r.Error != "" || r.Lookup == nil || !r.Lookup.Running || r.Lookup.Name != "ada-lovelace" {
		t.Errorf("lookup on their own device: %+v, %q", r.Lookup, r.Error)
	}
}

// No agent's session can take a person's address.
func TestASessionCantTakeAPersonsAddress(t *testing.T) {
	a := newDevice(t, newRemote(t))
	conn, resp, err := a.c.Hello(context.Background(), proto.Hello{ID: bus.PersonSession, Agent: "test"})
	if conn != nil {
		conn.Close()
	}
	if err == nil && resp.Error == "" {
		t.Errorf("a hello as %q was taken: %+v", bus.PersonSession, resp)
	}
}

// The whole of it, nobody typing but the person: their message goes into an agent's T3 thread as a
// turn saying how to answer, and the agent's `send` to their name reaches their page as unread.
func TestAPersonsMessageBecomesATurnAndTheAnswerReachesThePage(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool {
		_, ok := b.whoEntry(a.self() + "/" + bus.PersonSession)
		return ok && a.seesSession(bob)
	})

	long := strings.Repeat("Can you look at the failing test in restart_test.go? ", 4)
	sent := pa.sendText(bobName, long+"\nIt only fails on macOS.")
	eventually(t, "the handoff", func() bool { return len(calls()) > 0 })
	turn := calls()[0].Stdin
	for _, want := range []string{
		"Message from ada [human] (on " + brand.Product + "'s page",
		"It only fails on macOS.",
		"ada sees only what you `send`, not this conversation. Answer with " + brand.Product + "'s `send` to ada.",
	} {
		if !strings.Contains(turn, want) {
			t.Errorf("the turn lacks %q:\n%s", want, turn)
		}
	}
	if strings.Contains(turn, "if a reply is needed") {
		t.Errorf("the turn leaves the answer optional:\n%s", turn)
	}

	// What they sent is on their page already, marked as theirs, and isn't unread.
	msgs, _ := pa.messages("")
	i := slices.IndexFunc(msgs, func(m web.Message) bool { return m.ID == sent })
	if i < 0 {
		t.Fatalf("the sent message isn't among the page's: %v", subjects(msgs))
	}
	if m := msgs[i]; !m.FromPerson || m.ToPerson || m.From != "ada" || m.To != bobName || !strings.HasSuffix(m.Subject, "…") || !strings.Contains(m.Body, "macOS") {
		t.Errorf("the sent message: %+v", m)
	}
	if got := pa.state().Account.Unread; len(got) != 0 {
		t.Errorf("their own message is unread: %v", got)
	}

	// The agent answers by name.
	r := b.send("bbbbbb", "ada", "Looking at it", "It's the interrupt.")
	if r.Note != "" {
		t.Errorf("the answer's note: %q", r.Note)
	}
	eventually(t, "the answer to reach the page", func() bool {
		_, ok := pa.messageWith("Looking at it")
		return ok
	})
	m, _ := pa.messageWith("Looking at it")
	if m.FromPerson || !m.ToPerson || m.To != "ada" || m.From != bobName || m.ToKind != web.ToSession || m.Body != "It's the interrupt." {
		t.Errorf("the answer on the page: %+v", m)
	}
	if got := pa.state().Account.Unread; !slices.Equal(got, []string{m.ID}) {
		t.Errorf("unread: %v, want the answer, %s", got, m.ID)
	}
	if got := a.do(proto.Request{Op: proto.OpInbox}).Messages; slices.ContainsFunc(got, func(v proto.MessageView) bool { return v.Subject == "Looking at it" }) {
		t.Errorf("the CLI's inbox got what went to the person: %+v", got)
	}

	// Agents' broadcasts show on the page, and aren't the person's unread.
	b.send("bbbbbb", "*", "To everyone", "")
	eventually(t, "the broadcast to reach the page", func() bool {
		_, ok := pa.messageWith("To everyone")
		return ok
	})
	if got := pa.state().Account.Unread; len(got) != 1 {
		t.Errorf("a broadcast counts as unread: %v", got)
	}

	// Opening the conversation reads it.
	b2, _ := json.Marshal(map[string][]string{"ids": {m.ID}})
	if code, body := pa.call("POST", "/api/read", pa.token, string(b2)); code != http.StatusNoContent {
		t.Fatalf("marking read: %d %s", code, body)
	}
	if got := pa.state().Account.Unread; len(got) != 0 {
		t.Errorf("unread after reading: %v", got)
	}
}

// A person's direct message goes in past how long a thread has been idle, ahead of the agent's that
// is left waiting. A broadcast from the page doesn't, or it would wake every thread in the repo.
// Do not disturb and Never are a human's choices too, and hold.
func TestAPersonWakesALongIdleThreadWhereAnAgentWouldnt(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-bob": {LastRun: time.Now().Add(-time.Hour)}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })

	a.send("aaaaaa", bob, "from an agent", "")
	eventually(t, "the agent's message to reach b", func() bool { return logHas(b, "from an agent") })
	pa.sendText("*", "to everyone")
	eventually(t, "the broadcast to reach b", func() bool { return logHas(b, "to everyone") })
	holds(t, "neither waking a thread idle for an hour", 800*time.Millisecond, func() bool { return len(calls()) == 0 })

	pa.sendText(bobName, "a question for you")
	eventually(t, "the person's message to go in", func() bool { return len(calls()) >= 1 })
	if !strings.Contains(calls()[0].Stdin, "a question for you") {
		t.Errorf("what went in first: %s", calls()[0].Stdin)
	}
	// The thread is at work now, as far as the wake setting goes, so what was waiting follows.
	eventually(t, "what was waiting to follow", func() bool { return len(calls()) == 3 })

	pb := b.page()
	if code, body := pb.call("PUT", "/api/settings", pb.token, `{"wake":{"when":"never"}}`); code != http.StatusOK {
		t.Fatalf("setting never: %d %s", code, body)
	}
	n := len(calls())
	pa.sendText(bobName, "while set to never")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "while set to never") })
	holds(t, "never to hold against a person", 800*time.Millisecond, func() bool { return len(calls()) == n })
	if code, body := pb.call("PUT", "/api/settings", pb.token, `{"wake":{"when":"within"}}`); code != http.StatusOK {
		t.Fatalf("setting within: %d %s", code, body)
	}
}

// A person's name still reaches them after they change it, and their page calls them by the new
// one, on messages from before too.
func TestAPersonsEarlierNameStillReachesThem(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	b.session("bbbbbb", "")
	pa := a.page()
	pa.choose("ada")
	addr := a.self() + "/" + bus.PersonSession
	eventually(t, "b to know them", func() bool {
		e, ok := b.whoEntry(addr)
		return ok && e.Name == "ada"
	})
	pa.choose("countess")
	eventually(t, "b to know the new name", func() bool {
		e, ok := b.whoEntry(addr)
		return ok && e.Name == "countess"
	})
	b.send("bbbbbb", "ada", "by the old name", "")
	eventually(t, "the message to arrive", func() bool {
		m, ok := pa.messageWith("by the old name")
		return ok && m.To == "countess" && m.ToAddr == addr
	})
}

// What an agent answers while a person's daemon is down is theirs when it comes back, however long
// that takes: the daemon exits soon after their page closes, and an answer can take hours. What they
// had, read and unread, is still there.
func TestAPersonsMessagesOutlastTheDaemon(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	b.session("bbbbbb", "")
	pa := a.page()
	pa.choose("ada")
	eventually(t, "b to know them", func() bool {
		_, ok := b.whoEntry(a.self() + "/" + bus.PersonSession)
		return ok
	})
	sent := pa.sendText(b.self()+"/bbbbbb", "before the restart")
	b.send("bbbbbb", "ada", "first answer", "")
	eventually(t, "the first answer", func() bool {
		_, ok := pa.messageWith("first answer")
		return ok
	})

	a.stop()
	// By name, though their device has left presence: the daemon remembers who had it.
	eventually(t, "b to see the device gone or stale", func() bool {
		r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: "ada", Subject: "while away"})
		return r.Error == "" && strings.Contains(r.Note, "isn't on "+brand.Product)
	})
	aged(t, a, 24*time.Hour) // long past anything a daemon would catch up on for a session
	a.start()
	pa = a.page()

	if got := pa.state().Account.Name; got != "ada" {
		t.Errorf("the name after a restart: %q", got)
	}
	msgs, _ := pa.messages("")
	if !slices.ContainsFunc(msgs, func(m web.Message) bool { return m.ID == sent && m.FromPerson }) {
		t.Errorf("what they sent is gone: %v", subjects(msgs))
	}
	if !slices.Contains(subjects(msgs), "first answer") {
		t.Errorf("what they were sent is gone: %v", subjects(msgs))
	}
	eventually(t, "what came while away", func() bool {
		_, ok := pa.messageWith("while away")
		return ok
	})
	m, _ := pa.messageWith("while away")
	if got := pa.state().Account.Unread; !slices.Contains(got, m.ID) {
		t.Errorf("what came while away isn't unread: %v", got)
	}
	// Each message once, however many times the daemon read them.
	seen := map[string]int{}
	msgs, _ = pa.messages("")
	for _, m := range msgs {
		seen[m.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("message %s is on the page %d times", id, n)
		}
	}
}

// aged sets a stopped device's state as though it had last synced d ago.
func aged(t *testing.T, a *device, d time.Duration) {
	t.Helper()
	path := filepath.Join(a.gitDir, brand.StateDir, "state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	st["lastSync"] = time.Now().Add(-d).Format(time.RFC3339Nano)
	if b, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Agents talking among themselves don't push a person's conversations out of the page's memory, and
// the limit still holds for what isn't theirs.
func TestAgentsChatterDoesntPushOutAPersonsMessages(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.pageMsgs = 3 })
	b := newDevice(t, remote)
	b.session("bbbbbb", "")
	pa := a.page()
	pa.choose("ada")
	eventually(t, "b to know them", func() bool {
		_, ok := b.whoEntry(a.self() + "/" + bus.PersonSession)
		return ok
	})
	pa.sendText(b.self()+"/bbbbbb", "keep me")
	for i := range 6 {
		b.send("bbbbbb", "*", fmt.Sprintf("chatter %d", i), "")
	}
	eventually(t, "the chatter to arrive", func() bool {
		_, ok := pa.messageWith("chatter 5")
		return ok
	})
	msgs, _ := pa.messages("")
	got := subjects(msgs)
	if !slices.Contains(got, "keep me") {
		t.Errorf("the person's message was pushed out: %v", got)
	}
	if len(got) != 4 || slices.Contains(got, "chatter 2") {
		t.Errorf("the page holds %v, want the person's and the last 3", got)
	}
}

// A daemon that stops between logging a message and saving its state leaves the message known by ID
// but in nobody's inbox. It counts as unread after the restart, and what was already read stays read.
func TestAMessageLoggedBeforeACrashIsStillUnread(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	b.session("bbbbbb", "")
	pa := a.page()
	pa.choose("ada")
	eventually(t, "b to know them", func() bool {
		_, ok := b.whoEntry(a.self() + "/" + bus.PersonSession)
		return ok
	})
	b.send("bbbbbb", "ada", "already read", "")
	var read web.Message
	eventually(t, "the first message", func() bool {
		var ok bool
		read, ok = pa.messageWith("already read")
		return ok
	})
	ids, _ := json.Marshal(map[string][]string{"ids": {read.ID}})
	if code, body := pa.call("POST", "/api/read", pa.token, string(ids)); code != http.StatusNoContent {
		t.Fatalf("marking read: %d %s", code, body)
	}
	self := a.self()
	a.stop()

	lost := bus.Event{
		V: bus.Version, ID: "00000000000000aa", Type: bus.Message,
		From: self + "/bbbbbb", To: self + "/" + bus.PersonSession, Subject: "logged, then the crash",
	}
	line, err := json.Marshal(lost)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.gitDir, brand.StateDir, "person-messages.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()

	a.start()
	got := a.page().state().Account.Unread
	if !slices.Contains(got, lost.ID) {
		t.Errorf("the message from before the crash isn't unread: %v", got)
	}
	if slices.Contains(got, read.ID) {
		t.Errorf("a message already read is unread again: %v", got)
	}
}

// A log grown to more than twice what is kept is cut back when the daemon starts, and the newest
// messages are the ones left. On Windows this only works once the reader has closed the file.
func TestALongPersonLogIsCutBack(t *testing.T) {
	a := newDevice(t, newRemote(t))
	pa := a.page()
	pa.choose("ada")
	self := a.self()
	a.stop()

	path := filepath.Join(a.gitDir, brand.StateDir, "person-messages.jsonl")
	var b strings.Builder
	enc := json.NewEncoder(&b)
	const kept = 2000 // maxPersonLog
	total := 2*kept + 1
	for i := range total {
		if err := enc.Encode(bus.Event{
			V: bus.Version, ID: fmt.Sprintf("%016x", i+1), Type: bus.Message,
			From: self + "/bbbbbb", To: self + "/" + bus.PersonSession,
			Subject: fmt.Sprintf("message %d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	a.start()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got), "\n"); n != kept {
		t.Errorf("the log has %d lines after a restart, want %d", n, kept)
	}
	if !strings.Contains(string(got), fmt.Sprintf(`"message %d"`, total-1)) {
		t.Error("the newest message isn't among those kept")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file is still there: %v", err)
	}
}

// Two people who choose one name at the same moment can't both have it: the names registry takes one
// claim, and the other is told the name is taken (#268).
func TestTwoPeopleCantTakeOneName(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	pa, pb := a.page(), b.page()
	type result struct {
		code int
		body string
	}
	results := make(chan result, 2)
	for _, p := range []*page{pa, pb} {
		go func() {
			code, body := p.call("PUT", "/api/account", p.token, `{"name":"ada"}`)
			results <- result{code, body}
		}()
	}
	won, lost := 0, 0
	for range 2 {
		r := <-results
		switch {
		case r.code == http.StatusOK:
			won++
		case r.code == http.StatusBadRequest && strings.Contains(r.body, "taken"):
			lost++
		default:
			t.Errorf("choosing ada: %d %s", r.code, r.body)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("%d took the name and %d were refused, want 1 and 1", won, lost)
	}
	winner, loser := a, b
	if pb.state().Account.Name == "ada" {
		winner, loser = b, a
	}
	if got := loser.page().state().Account.Name; got != "" {
		t.Errorf("the one refused has a name: %q", got)
	}
	// Both devices send to ada's one owner.
	want := winner.self() + "/" + bus.PersonSession
	for _, d := range []*device{a, b} {
		eventually(t, "ada to resolve to the winner", func() bool {
			r := d.do(proto.Request{Op: proto.OpLookup, Name: "ada"})
			return r.Lookup != nil && r.Lookup.Addr == want
		})
	}
}

// Two accounts from before people's names were claimed can share one. The first daemon to claim it
// keeps it; the other's person is asked for another name, can't send until they choose one, and
// keeps what was sent to them (#268).
func TestANameHeldFromBeforeIsClaimedOnce(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	b.session("bbbbbb", "")
	selfA := a.self()
	a.stop()
	b.stop()
	// As a daemon from #249 left them: a name, and no claim.
	for _, d := range []*device{a, b} {
		setState(t, d, "person", "ada")
		setState(t, d, "personSince", time.Now())
		setState(t, d, "personClaims", map[string]string{})
	}
	a.start()
	eventually(t, "a to claim ada", func() bool {
		r := a.do(proto.Request{Op: proto.OpLookup, Name: "ada"})
		return r.Lookup != nil && r.Lookup.Addr == selfA+"/"+bus.PersonSession && a.page().state().Account.Taken == ""
	})
	b.start()
	b.session("bbbbbb", "")
	pb := b.page()
	eventually(t, "b to learn the name is a's", func() bool { return pb.state().Account.Taken == selfA })
	if code, body := pb.call("POST", "/api/send", pb.token, `{"to":"bbbbbb","text":"hi"}`); code != http.StatusBadRequest || !strings.Contains(body, "choose another") {
		t.Errorf("sending with a name someone else has: %d %s", code, body)
	}
	r := b.do(proto.Request{Op: proto.OpLookup, Name: "ada"})
	if r.Lookup == nil || r.Lookup.Addr != selfA+"/"+bus.PersonSession {
		t.Errorf("ada on b's own device: %+v", r.Lookup)
	}
	pb.choose("ada-b")
	if got := pb.state().Account; got.Name != "ada-b" || got.Taken != "" {
		t.Errorf("after choosing again: %+v", got)
	}
	if code, body := pb.call("POST", "/api/send", pb.token, `{"to":"bbbbbb","text":"hi"}`); code != http.StatusOK {
		t.Errorf("sending once renamed: %d %s", code, body)
	}
}

// A daemon from before #268 read the registry past people's claims without keeping them. Upgraded, it
// reads the registry again from the start, and a name claimed back then is taken (#268).
func TestAnUpgradedDaemonReadsClaimsItPassedOver(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.page().choose("ada")
	eventually(t, "b to read a's claim", func() bool {
		r := b.do(proto.Request{Op: proto.OpLookup, Name: "ada"})
		return r.Lookup != nil && r.Lookup.Addr == a.self()+"/"+bus.PersonSession
	})
	a.stop() // its presence goes, so only the registry says the name is a's
	b.stop()
	// As an older daemon would have left it: the registry read, its people's claims not kept.
	setState(t, b, "personClaims", map[string]string{})
	setState(t, b, "people", map[string]string{})
	setState(t, b, "namesPeople", false)
	b.start()
	pb := b.page()
	eventually(t, "b to take ada for a's again", func() bool {
		code, body := pb.call("PUT", "/api/account", pb.token, `{"name":"ada"}`)
		if code == http.StatusOK {
			t.Fatalf("b took ada, which a claimed first")
		}
		return strings.Contains(body, "taken")
	})
}

// A person's account is tagged human wherever it is listed, on their device and on another, and no
// session can take the tag or have it read off the bus (#267).
func TestAPersonIsTaggedHuman(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	b.session("bbbbbb", "")
	a.page().choose("ada")
	addr := a.self() + "/" + bus.PersonSession
	human := func(ts []tags.Tag) bool { return tags.Has(ts, tags.Human) }

	if e, ok := a.whoEntry(addr); !ok || !human(e.Tags) {
		t.Errorf("who on their own device: %+v, %v", e, ok)
	}
	eventually(t, "the other device to list them as human", func() bool {
		e, ok := b.whoEntry(addr)
		return ok && human(e.Tags)
	})
	for _, d := range []*device{a, b} {
		r := d.do(proto.Request{Op: proto.OpLookup, Name: "ada"})
		if r.Lookup == nil || !human(r.Lookup.Tags) {
			t.Errorf("lookup: %+v, %q", r.Lookup, r.Error)
		}
		var person *web.Person
		for _, dv := range d.page().state().Devices {
			if dv.Person != nil {
				person = dv.Person
			}
		}
		if person == nil || !human(person.Tags) {
			t.Errorf("the page: %+v", person)
		}
	}

	// who human lists the people; who -human the agents.
	for _, d := range []*device{a, b} {
		for _, dv := range d.do(proto.Request{Op: proto.OpWho, Tags: []string{"human"}}).Devices {
			for _, s := range dv.Sessions {
				if s.Addr != addr {
					t.Errorf("who human lists %s", s.Addr)
				}
			}
		}
		for _, dv := range d.do(proto.Request{Op: proto.OpWho, Tags: []string{"-human"}}).Devices {
			for _, s := range dv.Sessions {
				if s.Addr == addr {
					t.Error("who -human lists the person")
				}
			}
		}
	}

	// A session can't claim it, nor can a human on the page give it or take it.
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"human"}}); r.Error == "" {
		t.Errorf("a session tagged itself human: %+v", r.Tags)
	}
	pa := a.page()
	for _, body := range []string{
		`{"addr":"` + a.self() + `/aaaaaa","add":["human"]}`,
		`{"addr":"` + addr + `","remove":["human"]}`,
	} {
		if code, _ := pa.call("POST", "/api/tags", pa.token, body); code == http.StatusOK || code == http.StatusNoContent {
			t.Errorf("the page changed the human tag: %s", body)
		}
	}
	if e, _ := a.whoEntry(a.self() + "/aaaaaa"); human(e.Tags) {
		t.Errorf("a session has the human tag: %+v", e.Tags)
	}
	if e, _ := a.whoEntry(addr); !human(e.Tags) {
		t.Errorf("the person lost the human tag: %+v", e.Tags)
	}

	// Their daemon gone, they are known by name alone, and still tagged.
	a.stop()
	eventually(t, "b to forget a's presence", func() bool {
		r := b.do(proto.Request{Op: proto.OpLookup, Name: "ada"})
		return r.Lookup != nil && !r.Lookup.Running
	})
	if r := b.do(proto.Request{Op: proto.OpLookup, Name: "ada"}); r.Lookup == nil || !human(r.Lookup.Tags) {
		t.Errorf("lookup of a person whose daemon has gone: %+v, %q", r.Lookup, r.Error)
	}
}
