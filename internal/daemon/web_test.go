package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of the page for humans (#22): the daemon serves it, and answers its API from what it
// already holds. The tests reach it as a browser does, over HTTP with the token from the address
// that `mur ui` prints.

// page is one device's page, reached the way a browser reaches it.
type page struct {
	t     *testing.T
	base  string // http://127.0.0.1:port
	token string
}

func (d *device) page() *page {
	d.t.Helper()
	raw, err := d.c.PageURL()
	if err != nil {
		d.t.Fatalf("the page's address: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		d.t.Fatalf("the page's address %q: %v", raw, err)
	}
	token, ok := strings.CutPrefix(u.Fragment, "token=")
	if !ok || token == "" || u.Host == "" || u.Path != "/" {
		d.t.Fatalf("the page's address %q should be http://host:port/#token=...", raw)
	}
	return &page{t: d.t, base: u.Scheme + "://" + u.Host, token: token}
}

// call sends a request, with token as the bearer token unless it is "".
func (p *page) call(method, path, token, body string) (int, string) {
	p.t.Helper()
	req, err := http.NewRequest(method, p.base+path, strings.NewReader(body))
	if err != nil {
		p.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// status sends a GET and says how it was answered. Unlike call, it reports a daemon that isn't
// there as an error, not a failure of the test.
func (p *page) status(path, token string) (int, error) { return p.statusOf(path, token, false) }

// hiddenStatus is status for a page in a tab nobody can see, which says so with each call.
func (p *page) hiddenStatus(path, token string) (int, error) { return p.statusOf(path, token, true) }

func (p *page) statusOf(path, token string, hidden bool) (int, error) {
	req, err := http.NewRequest("GET", p.base+path, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if hidden {
		req.Header.Set("X-Page-Hidden", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// get fetches a path of the API with the right token and decodes it.
func (p *page) get(path string, out any) string {
	p.t.Helper()
	code, body := p.call("GET", path, p.token, "")
	if code != http.StatusOK {
		p.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		p.t.Fatalf("GET %s: %v\n%s", path, err, body)
	}
	return body
}

func (p *page) state() web.State {
	p.t.Helper()
	var st web.State
	p.get("/api/state", &st)
	return st
}

// messages reads the messages after cursor.
func (p *page) messages(cursor string) (msgs []web.Message, next string) {
	p.t.Helper()
	var r struct {
		Messages []web.Message `json:"messages"`
		Cursor   string        `json:"cursor"`
	}
	p.get("/api/messages?cursor="+url.QueryEscape(cursor), &r)
	return r.Messages, r.Cursor
}

func (p *page) settings() web.Settings {
	p.t.Helper()
	var set web.Settings
	p.get("/api/settings", &set)
	return set
}

// findSession finds a session by name on the page, and the device it is on.
func findSession(st web.State, name string) (web.Device, web.Session, bool) {
	for _, dv := range st.Devices {
		for _, s := range dv.Sessions {
			if s.Name == name {
				return dv, s, true
			}
		}
	}
	return web.Device{}, web.Session{}, false
}

func subjects(msgs []web.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// worktreeSession registers a session working in cwd.
func (d *device) worktreeSession(id, agent, cwd string) string {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: agent, Cwd: cwd})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self
}

// The page shows this device and the others it knows, each with its sessions: a session here has
// its worktree's folder, never a path, and one on another device has none.
func TestPageStateShowsBothDevices(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)

	// A worktree of a's clone, in a folder whose parent stands for the user's name, and a session
	// working in a directory inside it.
	tree := filepath.Join(t.TempDir(), "private-user-name", "feature-tree")
	git(t, filepath.Dir(a.gitDir), "worktree", "add", "-q", "-b", "feature-x", tree)
	cwd := filepath.Join(tree, "pkg", "deep")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	alice := a.worktreeSession("aaaaaa", "codex", cwd)
	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "wiring the page"})
	bob, _ := b.session("bbbbbb", "")

	aliceName, bobName := a.named(alice), a.named(bob)
	bobSeenByB := b.named(bob)
	if bobSeenByB != bobName {
		t.Fatalf("bob is %q on b and %q on a", bobSeenByB, bobName)
	}
	var st web.State
	var body string
	pg := a.page()
	eventually(t, "both sessions on a's page", func() bool {
		body = pg.get("/api/state", &st)
		_, _, ok := findSession(st, bobName)
		return ok
	})

	if st.Device != a.self() {
		t.Fatalf("state is of device %q, want %q", st.Device, a.self())
	}
	dv, s, ok := findSession(st, aliceName)
	if !ok {
		t.Fatalf("no session %q on the page: %s", aliceName, body)
	}
	if !dv.Self || dv.ID != a.self() || !dv.Reachable {
		t.Errorf("this device: %+v", dv)
	}
	if s.Agent != "codex" || s.Branch != "feature-x" || s.Doing != "wiring the page" || s.Worktree != "feature-tree" ||
		s.Addr != alice {
		t.Errorf("session here: %+v", s)
	}
	if s.Started.IsZero() || time.Since(s.Started) > time.Minute {
		t.Errorf("started %v", s.Started)
	}
	// Last active is when the session registered or last asked the daemon for anything (#21): the
	// status it set a moment ago.
	if s.LastActive.IsZero() || time.Since(s.LastActive) > time.Minute || s.LastActive.Before(s.Started) || len(s.Committed) != 0 {
		t.Errorf("last active %v, committed %v; it registered and set its status a moment ago", s.LastActive, s.Committed)
	}

	dv, s, ok = findSession(st, bobName)
	if !ok {
		t.Fatalf("no session %q on the page: %s", bobName, body)
	}
	if dv.Self || dv.ID != b.self() || !dv.Reachable {
		t.Errorf("the other device: %+v", dv)
	}
	if s.Agent != "test" || s.Worktree != "" || s.Addr != bob {
		t.Errorf("session there: %+v", s)
	}
	// Another device's session has the last-active time its presence carries, on this machine's clock.
	if s.LastActive.IsZero() || time.Since(s.LastActive) > time.Minute {
		t.Errorf("last active of the session there: %v", s.LastActive)
	}
	if dv.LastSeen.IsZero() || time.Since(dv.LastSeen) > time.Minute {
		t.Errorf("the other device was last seen %v", dv.LastSeen)
	}
	if len(st.Mutes) != 0 {
		t.Errorf("mutes %+v; those are #27", st.Mutes)
	}
	// What the page holds of a worktree is its folder: the path above it never leaves.
	if !strings.Contains(body, `"feature-tree"`) || strings.Contains(body, "private-user-name") {
		t.Errorf("the state shows more of the path than the folder: %s", body)
	}

	// b's page shows a's session without a worktree: that stays on the machine.
	var bst web.State
	bpg := b.page()
	eventually(t, "a's session on b's page", func() bool {
		bpg.get("/api/state", &bst)
		_, _, ok := findSession(bst, aliceName)
		return ok
	})
	if dv, s, _ := findSession(bst, aliceName); dv.Self || s.Worktree != "" || s.Branch != "feature-x" {
		t.Errorf("a's session on b's page: %+v on %+v", s, dv)
	}
}

// A device whose presence stops changing is shown as unreachable, as `who` shows it stale, and
// its last-seen time stops with it.
func TestPageStateMarksStaleDevice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	bobName := a.named(bob)
	pg := a.page()
	var st web.State
	eventually(t, "b on a's page, reachable", func() bool {
		st = pg.state()
		dv, _, ok := findSession(st, bobName)
		return ok && dv.Reachable
	})

	b.offline(remote, true) // b can't push heartbeats, and its presence goes stale on a
	eventually(t, "b unreachable on a's page", func() bool {
		st = pg.state()
		dv, _, ok := findSession(st, bobName)
		return ok && !dv.Reachable
	})
	dv, _, _ := findSession(st, bobName)
	if time.Since(dv.LastSeen) < a.stale-time.Second {
		t.Errorf("unreachable but last seen %v ago", time.Since(dv.LastSeen))
	}
}

// The API gives nothing without the page's token, and nothing to a request that names another
// host. The daemon's own token, which sessions use, is not the page's (#42): the address reaches
// the browser's history and extensions, and the daemon's token lets its holder send as this device.
func TestPageNeedsTheToken(t *testing.T) {
	t.Parallel()
	d := newDevice(t, newRemote(t))
	pg := d.page()
	info := d.infoFile(t)
	if info.PageToken == "" || info.PageToken != pg.token {
		t.Fatalf("the address carries %q, daemon.json says the page's token is %q", pg.token, info.PageToken)
	}
	if info.PageToken == info.Token {
		t.Fatal("the page and the sessions share a token")
	}
	if code, body := pg.call("GET", "/api/state", info.Token, ""); code != http.StatusUnauthorized {
		t.Errorf("the sessions' token opens the page's API: %d %s", code, body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/state", ""},
		{"GET", "/api/messages", ""},
		{"GET", "/api/settings", ""},
		{"PUT", "/api/settings", `{"wake":{"when":"never"}}`},
		{"POST", "/api/unmute", `{"by":"a-b","muted":"c-d"}`},
	} {
		for _, token := range []string{"", "not-the-token", pg.token + "x"} {
			if code, body := pg.call(c.method, c.path, token, c.body); code != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: %d %s", c.method, c.path, token, code, body)
			}
		}
	}
	if code, _ := pg.call("GET", "/api/state", pg.token, ""); code != http.StatusOK {
		t.Errorf("with the token: %d", code)
	}
	// The page itself, which holds no secrets, loads without it.
	if code, body := pg.call("GET", "/", "", ""); code != http.StatusOK || !strings.Contains(body, "<html") {
		t.Errorf("the page: %d %.100s", code, body)
	}
	// A rebinding page reaches 127.0.0.1 under its own name.
	req, _ := http.NewRequest("GET", pg.base+"/api/state", nil)
	req.Host = "attacker.example"
	req.Header.Set("Authorization", "Bearer "+pg.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("a request for another host was answered")
	}
}

// infoFile reads daemon.json, where sessions and `mur ui` find the daemon.
func (d *device) infoFile(t *testing.T) proto.Info {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.gitDir, brand.StateDir, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info proto.Info
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// The page serves loopback only, on a port of its own.
func TestPageIsOnLoopbackOnItsOwnPort(t *testing.T) {
	t.Parallel()
	d := newDevice(t, newRemote(t))
	info := d.infoFile(t)
	host, port, err := net.SplitHostPort(info.Web)
	if err != nil {
		t.Fatalf("web %q: %v", info.Web, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("the page is on %s", host)
	}
	if port == "0" || port == "8080" || info.Web == info.Addr {
		t.Errorf("the page is on %s, the sessions on %s", info.Web, info.Addr)
	}
}

// A page that can't be served costs the page and nothing else: the daemon runs, sessions work,
// and the log says why.
func TestPageNotServedDaemonRuns(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	var out syncBuf
	d := newDeviceWith(t, newRemote(t), func(d *device) { d.pageAddr, d.logTo = taken.Addr().String(), &out })

	if _, err := d.c.PageURL(); err != client.ErrNoPage {
		t.Errorf("page address: %v", err)
	}
	if web := d.infoFile(t).Web; web != "" {
		t.Errorf("daemon.json offers the page at %q", web)
	}
	addr, _ := d.session("aaaaaa", "")
	eventually(t, "the session to register", func() bool { return d.seesSession(addr) })
	if log := out.String(); !strings.Contains(log, "page for humans isn't served") {
		t.Errorf("the log doesn't say why: %q", log)
	}

	// An address that isn't on loopback is refused too: the page is for this machine. So is a
	// loopback address other than 127.0.0.1, where the page's Host check would refuse every request
	// made to the address daemon.json gave out.
	for _, addr := range []string{"0.0.0.0:0", "[::1]:0", "127.0.0.2:0"} {
		var out2 syncBuf
		e := newDeviceWith(t, newRemote(t), func(d *device) { d.pageAddr, d.logTo = addr, &out2 })
		if u, err := e.c.PageURL(); err != client.ErrNoPage {
			t.Errorf("page on %s: %q, %v", addr, u, err)
		}
		if log := out2.String(); !strings.Contains(log, "127.0.0.1") {
			t.Errorf("the log doesn't say why the page isn't on %s: %q", addr, log)
		}
		e.stop()
	}
}

// syncBuf is a log the daemon writes to and the test reads.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Messages between devices are on every device's page, oldest first, with the senders' and
// recipients' names, including a message between two other devices.
func TestPageMessages(t *testing.T) {
	t.Parallel()
	began := time.Now()
	remote := newRemote(t)
	a, b, c := newDevice(t, remote), newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := c.session("cccccc", "")
	devs := []*device{a, b, c}
	names := map[string]string{}
	for _, addr := range []string{alice, bob, carol} {
		for _, d := range devs {
			name := d.named(addr)
			if names[addr] != "" && names[addr] != name {
				t.Fatalf("%s is %q and %q", addr, names[addr], name)
			}
			names[addr] = name
		}
	}
	aName, bName, cName := names[alice], names[bob], names[carol]
	pages := []*page{a.page(), b.page(), c.page()}
	for _, pg := range pages {
		if msgs, cursor := pg.messages(""); len(msgs) != 0 || cursor == "" {
			t.Fatalf("a page with nothing sent: %+v, cursor %q", msgs, cursor)
		}
	}

	send := func(d *device, from, to, subject, body string) {
		t.Helper()
		if r := d.do(proto.Request{Op: proto.OpSend, Session: from, To: to, Subject: subject, Body: body}); r.Error != "" {
			t.Fatalf("send %q: %s", subject, r.Error)
		}
	}
	// One at a time, each on every page before the next is sent: the order across devices is the
	// order each daemon read them in, which only a wait makes the order they were sent in.
	// Each also carries the addresses the bus did, which stay put while a name can land later.
	sent := []struct {
		from       *device
		sess       string
		to         string
		subject    string
		body       string
		wantFrom   string
		wantTo     string
		wantToKind string
		fromAddr   string
		toAddr     string
	}{
		{a, "aaaaaa", bob, "first", "the first body", aName, bName, web.ToSession, alice, bob},
		{b, "bbbbbb", aName, "second", "", bName, aName, web.ToSession, bob, alice}, // addressed by name
		{a, "aaaaaa", "*", "third", "", aName, "*", web.ToAll, alice, "*"},
		{b, "bbbbbb", a.self(), "fourth", "to a device", bName, a.self(), web.ToDevice, bob, a.self()},
		{c, "cccccc", bName, "fifth", "", cName, bName, web.ToSession, carol, bob},
	}
	for i, m := range sent {
		send(m.from, m.sess, m.to, m.subject, m.body)
		for n, pg := range pages {
			eventually(t, "message "+m.subject+" on page "+string(rune('a'+n)), func() bool {
				msgs, _ := pg.messages("")
				return len(msgs) == i+1
			})
		}
	}
	for n, pg := range pages {
		msgs, _ := pg.messages("")
		if len(msgs) != len(sent) {
			t.Fatalf("page %c: %+v", 'a'+n, msgs)
		}
		for i, m := range sent {
			got := msgs[i]
			if got.Subject != m.subject || got.Body != m.body || got.From != m.wantFrom || got.To != m.wantTo ||
				got.ToKind != m.wantToKind || got.ID == "" || got.At.Before(began.Add(-time.Second)) || got.At.After(time.Now()) ||
				got.FromAddr != m.fromAddr || got.ToAddr != m.toAddr {
				t.Errorf("page %c, message %d: %+v, want %+v", 'a'+n, i+1, got, m)
			}
		}
	}

	// A cursor returns what came after it, and the one it returned stays valid.
	pg := pages[2]
	all, cursor := pg.messages("")
	if again, next := pg.messages(cursor); len(again) != 0 || next != cursor {
		t.Errorf("after the last: %+v, cursor %q then %q", again, cursor, next)
	}
	send(a, "aaaaaa", cName, "sixth", "")
	var fresh []web.Message
	eventually(t, "the sixth message on c's page", func() bool {
		fresh, _ = pg.messages(cursor)
		return len(fresh) > 0
	})
	if got := subjects(fresh); !reflect.DeepEqual(got, []string{"sixth"}) {
		t.Errorf("after the cursor: %q", got)
	}
	if fresh[0].ID == all[len(all)-1].ID {
		t.Errorf("the cursor repeated a message")
	}
}

// The cursor keeps its meaning when the oldest messages have been dropped: it names the last
// message seen, not a place in the list.
func TestPageMessageCursorAcrossTheRingWrapping(t *testing.T) {
	t.Parallel()
	d := newDeviceWith(t, newRemote(t), func(d *device) { d.pageMsgs = 3 })
	pg := d.page()
	send := func(subjects ...string) {
		t.Helper()
		for _, s := range subjects {
			if r := d.do(proto.Request{Op: proto.OpSend, To: "*", Subject: s}); r.Error != "" {
				t.Fatalf("send %q: %s", s, r.Error)
			}
		}
	}
	read := func(cursor string, want ...string) string {
		t.Helper()
		msgs, next := pg.messages(cursor)
		if got := subjects(msgs); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
			t.Fatalf("after %q: %q, want %q", cursor, got, want)
		}
		return next
	}

	send("m1", "m2")
	c2 := read("", "m1", "m2")
	send("m3", "m4", "m5") // the ring holds three: m1 and m2 are dropped
	c5 := read(c2, "m3", "m4", "m5")
	send("m6", "m7")
	read(c5, "m6", "m7")
	read("", "m5", "m6", "m7")
	// A cursor from before the ring's start gets what is left; none of it is lost twice.
	c7 := read(c2, "m5", "m6", "m7")
	read(c7)
	// A cursor that is not one of the daemon's is an error; one from another run, which a page
	// can hold across a restart, has seen nothing.
	if code, _ := pg.call("GET", "/api/messages?cursor=bogus", pg.token, ""); code == http.StatusOK {
		t.Errorf("a bogus cursor was accepted")
	}
	read("1abc-2", "m5", "m6", "m7")
}

// Settings start at the defaults, survive a restart, and are refused when the page's own check
// refuses them.
func TestPageSettingsPersist(t *testing.T) {
	t.Parallel()
	d := newDevice(t, newRemote(t))
	pg := d.page()
	def := web.Settings{Wake: web.Wake{When: web.WakeWithin}, Busy: web.BusySubject, DefaultMinutes: 3}
	if got := pg.settings(); !reflect.DeepEqual(got, def) {
		t.Fatalf("defaults: %+v, want %+v", got, def)
	}

	want := web.Settings{
		Wake:           web.Wake{When: web.WakeWithin, Minutes: 12},
		SessionWake:    map[string]web.Wake{"quiet-otter": {When: web.WakeNever}, "brave-heron": {When: web.WakeAlways}},
		Busy:           web.BusyWhole,
		SessionBusy:    map[string]string{"quiet-otter": web.BusySubject},
		DefaultMinutes: 3,
	}
	body, _ := json.Marshal(want)
	if code, out := pg.call("PUT", "/api/settings", pg.token, string(body)); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, out)
	} else {
		var saved web.Settings
		if err := json.Unmarshal([]byte(out), &saved); err != nil || !reflect.DeepEqual(saved, want) {
			t.Fatalf("PUT answered %s (%v), want %+v", out, err, want)
		}
	}
	if got := pg.settings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after PUT: %+v", got)
	}

	// Bad settings are refused, and the ones in force stay.
	for _, bad := range []string{
		`{"wake":{"when":"sometimes"}}`,
		`{"wake":{"when":"always","minutes":5}}`,
		`{"wake":{"when":"within","minutes":1441}}`,
		`{"wake":{"when":"within","minutes":-1}}`,
		`{"wake":{"when":""}}`,
		`{"wake":{"when":"never"},"sessionWake":{"quiet-otter":{"when":"later"}}}`,
		`{"wake":{"when":"never"},"sessionWake":{"":{"when":"never"}}}`,
		`{"wake":{"when":"never"},"extra":1}`,
		`{"wake":{"when":"never"},"busy":"body"}`,
		`{"wake":{"when":"never"},"sessionBusy":{"quiet-otter":""}}`,
		`{"wake":{"when":"never"},"sessionBusy":{"quiet-otter":"all"}}`,
		`{"wake":{"when":"never"},"sessionBusy":{"":"whole"}}`,
		`not json`,
	} {
		if code, out := pg.call("PUT", "/api/settings", pg.token, bad); code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d %s", bad, code, out)
		}
	}
	if got := pg.settings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after refused settings: %+v", got)
	}

	d.restart()
	pg = d.page()
	if got := pg.settings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a restart: %+v, want %+v", got, want)
	}
	dir := filepath.Join(d.gitDir, brand.StateDir)
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Errorf("settings.json: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "settings.json.*")); len(left) != 0 {
		t.Errorf("a temporary file is left: %v", left)
	}

	// A file the page wouldn't have saved is not read: the defaults stand, and the page can save
	// over it.
	d.stop()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"wake":{"when":"bogus"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.start()
	pg = d.page()
	if got := pg.settings(); !reflect.DeepEqual(got, def) {
		t.Fatalf("with a bad file: %+v, want the defaults", got)
	}
	if code, out := pg.call("PUT", "/api/settings", pg.token, `{"wake":{"when":"never"}}`); code != http.StatusOK {
		t.Fatalf("PUT over a bad file: %d %s", code, out)
	}
	d.restart()
	if got := d.page().settings(); got.Wake.When != web.WakeNever || got.Busy != web.BusySubject {
		t.Fatalf("after saving over a bad file: %+v", got)
	}

	// A file from before the busy setting (#204) keeps its wake setting, and the busy one is the
	// default.
	d.stop()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"wake":{"when":"always"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.start()
	old := web.Settings{Wake: web.Wake{When: web.WakeAlways}, Busy: web.BusySubject, DefaultMinutes: 3}
	if got := d.page().settings(); !reflect.DeepEqual(got, old) {
		t.Fatalf("with a file from before the busy setting: %+v, want %+v", got, old)
	}
}

// Unmuting a mute that isn't there is refused, and says so (#27). mute_test.go has the rest.
func TestPageUnmuteOfNoMuteIsRefused(t *testing.T) {
	t.Parallel()
	d := newDevice(t, newRemote(t))
	pg := d.page()
	code, body := pg.call("POST", "/api/unmute", pg.token, `{"by":"quiet-otter","muted":"brave-heron"}`)
	if code == http.StatusOK || code == http.StatusNoContent || !strings.Contains(body, "hasn't muted brave-heron") {
		t.Errorf("unmute: %d %s", code, body)
	}
}

// exited says whether d's daemon has returned.
func (d *device) exited() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

// A page that keeps calling the API keeps a daemon with no sessions from exiting, as a session
// would; one that has stopped lets it go, and one whose calls don't carry the token never held it.
// Nor does a page in a tab nobody can see: it goes on polling for as long as the browser runs, and
// a tab forgotten in the background would keep the daemon, and its polling of GitHub, up for good.
func TestOpenPageKeepsDaemonUp(t *testing.T) {
	// Not parallel: its calls must come more often than the page counts as open, and the other
	// daemons must exit inside its window; other tests' load would stretch both.
	const idle, open = 3 * time.Second, 2 * time.Second
	remote := newRemote(t)
	timed := func(d *device) { d.idle, d.pageOpen = idle, open }
	// The watched one last: each daemon's idle time runs from its start, and starting one takes a
	// while on a loaded machine.
	stranger := newDeviceWith(t, remote, timed)
	forgotten := newDeviceWith(t, remote, timed)
	sg, fg := stranger.page(), forgotten.page()
	watched := newDeviceWith(t, remote, timed)
	pg := watched.page()

	// Calls for longer than the idle time. Without a page open, the daemon would be gone by now.
	var last time.Time
	for end := time.Now().Add(3 * idle); time.Now().Before(end); time.Sleep(250 * time.Millisecond) {
		last = time.Now()
		if code, err := pg.status("/api/state", pg.token); err != nil || code != http.StatusOK {
			t.Fatalf("the page's call: %d, %v; the daemon should stay up while the page is open", code, err)
		}
		if code, err := sg.status("/api/state", "not-the-token"); err == nil && code != http.StatusUnauthorized {
			t.Fatalf("a call without the token: %d", code)
		}
		if code, err := fg.hiddenStatus("/api/state", fg.token); err == nil && code != http.StatusOK {
			t.Fatalf("a hidden page's call: %d", code)
		}
		if watched.exited() {
			t.Fatalf("the daemon exited %s after the page's last call, with the page open", time.Since(last))
		}
	}
	if !stranger.exited() {
		t.Errorf("calls without the token kept a daemon up")
	}
	if !forgotten.exited() {
		t.Errorf("a hidden page's calls kept a daemon up")
	}

	// The calls stop. The page counts as open for a while more, and the idle time runs from then.
	eventually(t, "the daemon to exit once the calls stopped", watched.exited)
	if took := time.Since(last); took < open+idle {
		t.Errorf("the daemon exited %s after the page's last call; the page was open for %s and the idle time is %s", took, open, idle)
	}
}

// Sessions keep the daemon up as before, with or without the page.
func TestSessionKeepsDaemonUpWithPage(t *testing.T) {
	t.Parallel()
	d := newDeviceWith(t, newRemote(t), func(d *device) { d.idle, d.pageOpen = time.Second, 500*time.Millisecond })
	d.session("aaaaaa", "")
	holds(t, "the daemon running with a session", 4*time.Second, func() bool { return !d.exited() })
}

// A daemon whose first sync failed reads the logs it then finds as history, skipping what was sent
// before it started, but queues what was sent since for the sessions registered meanwhile. The
// page shows those messages, and the history after them, read back (#138).
func TestPageMessagesAfterAFailedFirstSync(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	pushEvent(t, remote, "refs/murmur/mesh/old-0001", bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: "old-0001/x", At: time.Now().Add(-10 * time.Minute).UTC(), To: "*", Subject: "backlog"})
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	// Pushes go out, so presence is seen, but listing and fetching fail.
	git(t, dir, "config", "remote.origin.pushurl", remote)
	git(t, dir, "config", "remote.origin.url", filepath.Join(filepath.Dir(remote), "nowhere.git"))
	b := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	b.start()
	bob, _ := b.session("bbbbbb", "")
	// a sees b's device, not bob: b, never having read the team ref, encrypts its presence for
	// itself alone (docs/VISION.md, "The bus is encrypted").
	eventually(t, "a sees b", func() bool { return a.seesDevices(2) })

	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "broadcast"})
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "direct"})
	git(t, dir, "config", "remote.origin.url", remote)

	n := 0
	eventually(t, "bob gets both", func() bool {
		n += len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages)
		return n >= 2
	})
	pg := b.page()
	var got []string
	eventually(t, "the backlog on b's page, read back", func() bool {
		msgs, _ := pg.messages("")
		got = subjects(msgs)
		return len(got) >= 3
	})
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"backlog", "broadcast", "direct"}) {
		t.Errorf("b's page shows %q, want the broadcast, the direct message and the backlog", got)
	}
	// The backlog is on the page only: bob was given the two sent since he started, and no more.
	if more := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages; n != 2 || len(more) != 0 {
		t.Errorf("bob was given %d messages and then %+v, want the two sent since he started", n, more)
	}
}

// The page's token lets a program use the page's API, which writes this device's settings and
// session tags, so it stays private: not in the log, not readable by other users in daemon.json,
// and not sent on as a Referer by anything the page links to.
func TestPageTokenStaysPrivate(t *testing.T) {
	t.Parallel()
	var out syncBuf
	d := newDeviceWith(t, newRemote(t), func(d *device) { d.logTo = &out })
	pg := d.page()
	pg.state()
	if code, _ := pg.call("GET", "/api/state", "wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("a wrong token: %d", code)
	}
	if strings.Contains(out.String(), pg.token) {
		t.Errorf("the log holds the token:\n%s", out.String())
	}
	if runtime.GOOS != "windows" { // where files are guarded by ACLs, not mode bits
		fi, err := os.Stat(filepath.Join(d.gitDir, brand.StateDir, "daemon.json"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("daemon.json is %v, readable by others", fi.Mode().Perm())
		}
	}
	req, _ := http.NewRequest("GET", pg.base+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(string(b), `name="referrer" content="no-referrer"`) {
		t.Errorf("the page doesn't forbid Referers: %q", resp.Header.Get("Referrer-Policy"))
	}
}
