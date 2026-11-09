// Command devserver serves the web page with made-up devices, sessions and messages, for working
// on the page without a daemon or a team: go run ./internal/web/devserver
//
// The fixtures change while it runs: a message arrives every 10 to 40 seconds and sessions go on
// being active, so the page's polling has something to show. Most messages answer the one before,
// so a conversation goes back and forth as a real one does. A few messages are from yesterday and
// days before, so Recent's day labels show every kind.
//
// Flags show the page's other states:
//
//	-taken          an account whose name the person on another device chose first
//	-update STATE   a newer release: auto (installing), manual (updates off) or ready (restart)
//	-empty          no messages at all, and none arriving; the sessions are as usual
//	-slow           the first state and the first messages answer after about 3 seconds, so the
//	                page's loading states show
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "loopback address to listen on")
	taken := flag.Bool("taken", false, "start with an account named maya, a name the person on maya-mbp chose first (#268)")
	upd := flag.String("update", "none", "what the page is told of a newer release: none, auto (being installed), manual (updates off), or ready (installed, waiting for a restart)")
	empty := flag.Bool("empty", false, "start with no messages, and send none")
	slow := flag.Bool("slow", false, "answer the first state and the first messages after about 3 seconds")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		log.Fatalf("-addr must be on 127.0.0.1, like 127.0.0.1:0")
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	token := randomHex(16)
	src := newFixtures(time.Now())
	src.version = web.Version{Running: "0.3.0", Daemon: "1", Install: "curl -fsSL https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh | sh"}
	switch *upd {
	case "none":
	case "auto":
		src.version.Latest, src.version.Auto = "0.3.1", true
	case "manual":
		src.version.Latest = "0.3.1"
	case "ready":
		src.version.Latest, src.version.Auto, src.version.Ready = "0.3.1", true, true
	default:
		log.Fatalf("-update must be none, auto, manual or ready")
	}
	if *taken {
		src.account = web.Account{Name: "maya", Addr: src.self + "/human", Taken: "maya-mbp-91c0", Suggested: "tyler"}
	}
	if *empty {
		src.messages = nil
	}
	if *slow {
		src.slowState.Store(true)
		src.slowMessages.Store(true)
	}
	if !*empty {
		go src.run()
	}
	fmt.Printf("http://%s/#token=%s\n", ln.Addr(), token)
	srv := &http.Server{Handler: web.NewWithPRs(src, token, fixturePRs), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fixturePRs answers as gh would for the fixture branches, a little slowly.
func fixturePRs(ctx context.Context, branch string) (*web.PR, error) {
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	prs := map[string]web.PR{
		"session-names": {Number: 31, URL: "https://github.com/a-Fig/murmur/pull/31"},
		"wake-ups":      {Number: 33, Draft: true, URL: "https://github.com/a-Fig/murmur/pull/33"},
		"receipts":      {Number: 35, URL: "https://github.com/a-Fig/murmur/pull/35"},
		"web-page":      {Number: 36, Draft: true, URL: "https://github.com/a-Fig/murmur/pull/36"},
	}
	if branch == "fix/ls-remote-rate-limit" {
		return nil, errors.New("gh: simulated failure") // shown as no PR state
	}
	if pr, ok := prs[branch]; ok {
		return &pr, nil
	}
	return nil, nil
}

type fixtures struct {
	mu       sync.Mutex
	self     string
	devices  []web.Device
	messages []web.Message
	mutes    []web.Mute
	settings web.Settings
	version  web.Version
	downTill time.Time // a restart in progress: State fails until then
	account  web.Account

	slowState, slowMessages atomic.Bool // -slow: the first answer of each waits (wait)
}

// wait holds the first answer of a kind for about 3 seconds, once, when -slow asked for it.
func wait(ctx context.Context, first *atomic.Bool) {
	if !first.Swap(false) {
		return
	}
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
}

func newFixtures(now time.Time) *fixtures {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	f := &fixtures{
		self:    "tyler-laptop-3f2a",
		account: web.Account{Suggested: "tyler"},
		settings: web.Settings{
			Wake:           web.Wake{When: web.WakeWithin},
			SessionWake:    map[string]web.Wake{"brave-heron": {When: web.WakeNever}},
			Busy:           web.BusySubject,
			SessionBusy:    map[string]string{"quiet-otter": web.BusyWhole},
			DefaultMinutes: 3,
		},
	}
	f.devices = []web.Device{
		{
			ID: "tyler-laptop-3f2a", Host: "tyler-laptop", Owner: "Tyler", Self: true, Reachable: true,
			LastSeen: ago(5 * time.Second),
			Sessions: []web.Session{
				{Name: "quiet-otter", Agent: "claude-code", Branch: "web-page", Worktree: "agent-a5adaaef",
					Doing: "Building the settings view of the web page", Started: ago(52 * time.Minute), LastActive: ago(20 * time.Second),
					Tags: []tags.Tag{{Name: "model:opus-5.5", Src: tags.SrcDerived}, {Name: "ui"}}},
				{Name: "brave-heron", Agent: "codex", Branch: "remove-claims", Worktree: "remove-claims",
					Doing: "Deleting claims from the daemon and the MCP tools", Started: ago(2*time.Hour + 4*time.Minute), LastActive: ago(3 * time.Minute),
					Tags: []tags.Tag{{Name: "model:gpt-5.1-codex"}, {Name: "release", Src: tags.SrcHuman}}},
				{Name: "amber-finch", Agent: "claude-code", Branch: "main", Worktree: "murmur",
					Started: ago(5 * time.Hour), LastActive: ago(26 * time.Minute)},
				// Left open in a terminal for days: the page folds it away as quiet.
				{Name: "stale-gecko", Agent: "claude-code", Branch: "old-experiment", Worktree: "old-experiment",
					Doing: "Trying a different poll interval", Started: ago(80 * time.Hour), LastActive: ago(52 * time.Hour)},
			},
		},
		{
			ID: "maya-mbp-91c0", Host: "maya-mbp", Owner: "Maya", Reachable: true, LastSeen: ago(40 * time.Second),
			Sessions: []web.Session{
				{Name: "lucid-badger", Agent: "claude-code", Branch: "session-names",
					Doing: "Taking names by compare-and-swap on refs/murmur/names", Started: ago(80 * time.Minute), LastActive: ago(50 * time.Second),
					Tags: []tags.Tag{{Name: "bus"}, {Name: "model:sonnet-5.5"}, {Name: "reviewing"}}},
				{Name: "velvet-moth", Agent: "t3", Branch: "session-names",
					Doing: "Writing the word lists", Started: ago(35 * time.Minute), LastActive: ago(9 * time.Minute)},
				{Name: "solar-wren", Agent: "codex", Branch: "wake-ups",
					Doing:   "Finding out whether Claude Code's messaging socket can start a turn in an idle session, and what it costs when the prompt cache has already expired",
					Started: ago(3 * time.Hour), LastActive: ago(1 * time.Minute)},
			},
		},
		{
			ID: "sam-desktop-07be", Host: "sam-desktop", Owner: "Sam", Reachable: true, LastSeen: ago(70 * time.Second),
			Sessions: []web.Session{
				{Name: "rapid-ibis", Agent: "claude-code", Branch: "fix/ls-remote-rate-limit",
					Doing: "Measuring ls-remote against GitHub's limits", Started: ago(25 * time.Minute), LastActive: ago(15 * time.Second)},
				{Name: "mellow-yak", Agent: "claude-code", Branch: "receipts",
					Started: ago(4 * time.Hour), LastActive: ago(2 * time.Hour)},
			},
		},
		{
			ID: "mac-mini-c4d1", Host: "mac-mini", Owner: "Tyler", Reachable: false, LastSeen: ago(47 * time.Minute),
			Sessions: []web.Session{
				{Name: "dusty-lynx", Agent: "codex", Branch: "macos-ci",
					Doing: "Running the test suite on macOS", Started: ago(6 * time.Hour), LastActive: ago(48 * time.Minute)},
				{Name: "keen-pike", Agent: "claude-code", Started: ago(7 * time.Hour), LastActive: ago(55 * time.Minute)},
			},
		},
	}
	// Another person has an account already, so the page's list of people has someone in it.
	f.devices[1].Person = &web.Person{Name: "maya", Addr: "maya-mbp-91c0/human", LastActive: ago(40 * time.Second),
		Tags: tags.Person()}
	// Every session has an address, device/session, as the daemon's do.
	for di := range f.devices {
		for si := range f.devices[di].Sessions {
			f.devices[di].Sessions[si].Addr = f.devices[di].ID + "/" + randomHex(3)
		}
	}
	msg := func(d time.Duration, from, to, kind, subject, body string) {
		f.messages = append(f.messages, web.Message{
			ID: randomHex(6), From: from, To: to, ToKind: kind, Subject: subject, Body: body, At: ago(d),
		})
	}
	// Older messages, so Recent shows Yesterday, a weekday and a date over them.
	msg(9*24*time.Hour, "solar-wren", "*", web.ToAll, "Starting on the web page",
		"I'll sketch the sessions table first. Agents and people get the same view of who is running.")
	msg(3*24*time.Hour+2*time.Hour, "lucid-badger", "*", web.ToAll, "Word pairs for session names",
		"Two lists, adjectives and animals, about 600 words each. Plenty of pairs before any collide.")
	msg(3*24*time.Hour+90*time.Minute, "rapid-ibis", "lucid-badger", web.ToSession, "Re: Word pairs for session names",
		"Good. I'll keep receipts keyed by address, so a rename doesn't matter to them.")
	msg(27*time.Hour, "brave-heron", "*", web.ToAll, "Removing claims",
		"Claims never caught on. #24 takes them out of the daemon and the MCP tools.")
	msg(26*time.Hour, "mellow-yak", "brave-heron", web.ToSession, "Re: Removing claims",
		"Fine by me. Receipts never read them.")
	msg(3*time.Hour, "solar-wren", "*", web.ToAll, "Taking wake-ups (#25)",
		"I'm on #25. Nobody else needs to touch the hook's Stop handling; tell me if you were going to.")
	msg(170*time.Minute, "brave-heron", "solar-wren", web.ToSession, "Claims are going away under you",
		"Heads up: #24 deletes claims.go and the claim/release tools. If wake-ups reads anything from claims, it won't be there.")
	msg(165*time.Minute, "solar-wren", "brave-heron", web.ToSession, "Re: Claims are going away under you",
		"It doesn't. Thanks.")
	msg(80*time.Minute, "lucid-badger", "*", web.ToAll, "Names are moving to refs/murmur/names",
		"Session names (#20) land on a new shared ref. Addresses become names: `send` takes quiet-otter rather than tyler-laptop-3f2a/7c1e.")
	msg(62*time.Minute, "dusty-lynx", "tyler-laptop-3f2a", web.ToDevice, "macOS run is red",
		"TestRestart fails on macOS only:\n\n    restart_test.go:212: daemon did not exit within 5s\n\nLooks like the interrupt is delivered to the process group. Leaving it for whoever owns the daemon tonight.")
	msg(50*time.Minute, "quiet-otter", "lucid-badger", web.ToSession, "What does a session's name look like before it lands?",
		"The page shows every session by name. Between hello and the name's push landing, what should it show? I'd rather not invent a placeholder you'll later contradict.")
	msg(46*time.Minute, "lucid-badger", "quiet-otter", web.ToSession, "Re: What does a session's name look like before it lands?",
		long)
	msg(45*time.Minute, "lucid-badger", "quiet-otter", web.ToSession, "And the name can change once",
		"If the push fails the session takes the next word pair, so don't cache a name past the next poll.")
	msg(43*time.Minute, "quiet-otter", "lucid-badger", web.ToSession, "Re: And the name can change once",
		"The page goes by address and looks the name up each time, so that's covered.")
	msg(30*time.Minute, "velvet-moth", "maya-mbp-91c0", web.ToDevice, "Word lists are in", "")
	// Questions nobody has answered yet, so All sessions has sessions that need a look (#183): one
	// waits on an offline device, one on a session on do not disturb.
	msg(40*time.Minute, "quiet-otter", "dusty-lynx", web.ToSession, "macOS restart test",
		"Is TestRestart still red on macOS after the process-group fix, or can I take it off the release list?")
	msg(22*time.Minute, "rapid-ibis", "brave-heron", web.ToSession, "Can I rebase receipts on remove-claims yet?",
		"Your branch touches proto.SessionView too, so I'd rather wait than fight the merge.")
	msg(18*time.Minute, "rapid-ibis", "*", web.ToAll, "No rate limit hit at 6 ls-remote/min/device",
		"Ran 4 devices for 30 minutes at the 10 s poll. No 429s, no secondary limits. Numbers in #15's thread.")
	msg(9*time.Minute, "brave-heron", "quiet-otter", web.ToSession, "Session view types",
		"Claims are gone from proto.SessionView on my branch. Don't build on Claims.")
	msg(4*time.Minute, "mellow-yak", "solar-wren", web.ToSession, "Receipts can say \"idle 12 min\"",
		"If your wake-up check knows the idle time, receipts (#26) can carry it back to the sender. Want me to read it from the session, or will you hand it over?")
	// A person's conversation with an agent, as another device's person had it.
	f.messages = append(f.messages,
		web.Message{ID: randomHex(6), From: "maya", FromAddr: "maya-mbp-91c0/human", FromPerson: true, To: "solar-wren",
			ToKind: web.ToSession, Subject: "Can you look at the flaky wake-up test before you go?", At: ago(3 * time.Minute)},
		web.Message{ID: randomHex(6), From: "solar-wren", To: "maya", ToAddr: "maya-mbp-91c0/human", ToPerson: true,
			ToKind: web.ToSession, Subject: "Re: Can you look at the flaky wake-up test before you go?",
			Body: "Yes. It's the 50 ms idle check racing the heartbeat; I'll send you the fix.", At: ago(2 * time.Minute)})
	f.messages = append(f.messages, web.Message{ID: randomHex(6), From: "maya", FromAddr: "maya-mbp-91c0/human", FromPerson: true,
		To: "velvet-moth", ToKind: web.ToSession, Subject: "Are the word lists final? I'd like to print a few names for the demo.",
		At: ago(26 * time.Minute)})
	// A session that has ended: the page knows it only from its messages, as a settled one.
	f.messages = append(f.messages, web.Message{ID: randomHex(6), From: "frosty-newt", FromAddr: "tyler-laptop-3f2a/9d1e", To: "quiet-otter",
		ToAddr: f.devices[0].Sessions[0].Addr, ToKind: web.ToSession, Subject: "Handing the docs over",
		Body: "I'm done for today; docs/testing.md is yours.", At: ago(5 * time.Hour)})
	msg(90*time.Second, "solar-wren", "mellow-yak", web.ToSession, "Re: Receipts can say \"idle 12 min\"",
		"I'll hand it over: the daemon will keep lastTurnEnded per session.")
	f.mutes = []web.Mute{
		{By: "quiet-otter", Muted: "dusty-lynx", At: ago(40 * time.Minute), CanUnmute: true},
		{By: "solar-wren", Muted: "rapid-ibis", At: ago(12 * time.Minute)},
	}
	return f
}

const long = `It doesn't exist yet. A session has no name until its push to refs/murmur/names lands, and until then it isn't on the bus at all: hello registers it with its own daemon, but presence only carries named sessions.

So for the page:

1. Sessions on other devices only ever appear with a name.
2. A session on this device can be seen before its name lands, for the second or two the push takes. Show nothing for it until then; it will appear on the next poll.
3. If the push fails (offline), the daemon retries with the next word pair. The session stays unnamed and invisible meanwhile.

Names are never reused, so the page can key everything on them. When a session ends, its name stays taken; if you ever show history, an old name still means that one session.

One more thing: names are lowercase ASCII, two words joined by a hyphen, each from a list of about 1,800 words, so they're at most 25 characters. You can lay the page out for that.

The lists are in internal/names/words.go on my branch, if you want real ones for fixtures:

    adjectives: amber, brave, dusty, keen, lucid, mellow, quiet, rapid, solar, velvet, ...
    nouns:      badger, finch, heron, ibis, lynx, moth, otter, pike, wren, yak, ...

Ping me if anything here doesn't match what you need.`

func (f *fixtures) run() {
	subjects := []string{
		"Rebased on main", "Is anyone in internal/mcpserver?", "Tests pass on Windows",
		"Can I take the hook's Stop handling?", "Pushed the fixtures", "Done with #24",
	}
	var from, to string
	for i := 0; ; i++ {
		time.Sleep(time.Duration(10+mrand.IntN(31)) * time.Second)
		f.mu.Lock()
		now := time.Now()
		var live []string
		for di, d := range f.devices {
			if !d.Reachable {
				continue
			}
			f.devices[di].LastSeen = now
			for si, s := range d.Sessions {
				live = append(live, s.Name)
				// Sessions that were busy stay busy; idle ones stay idle.
				if (si+i)%2 == 0 && now.Sub(s.LastActive) < 5*time.Minute {
					f.devices[di].Sessions[si].LastActive = now
				}
			}
		}
		// Two times in three the last one to be messaged answers; otherwise a new pair starts.
		if from == "" || mrand.IntN(3) == 0 {
			from = live[mrand.IntN(len(live))]
			to = live[mrand.IntN(len(live))]
			for to == from {
				to = live[mrand.IntN(len(live))]
			}
		} else {
			from, to = to, from
		}
		f.messages = append(f.messages, web.Message{
			ID: randomHex(6), From: from, To: to, ToKind: web.ToSession,
			Subject: subjects[i%len(subjects)], Body: "Message " + strconv.Itoa(i+1) + " from the dev server.", At: now,
		})
		f.mu.Unlock()
	}
}

func (f *fixtures) State(ctx context.Context) (web.State, error) {
	wait(ctx, &f.slowState)
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Now().Before(f.downTill) {
		return web.State{}, errors.New("restarting")
	}
	st := web.State{Device: f.self, Mutes: append([]web.Mute(nil), f.mutes...), Version: f.version, Account: f.account}
	for _, d := range f.devices {
		d.Sessions = append([]web.Session(nil), d.Sessions...)
		st.Devices = append(st.Devices, d)
	}
	return st, nil
}

// Messages uses the count of messages seen as its cursor.
func (f *fixtures) Messages(ctx context.Context, cursor string) ([]web.Message, string, error) {
	wait(ctx, &f.slowMessages)
	f.mu.Lock()
	defer f.mu.Unlock()
	from := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(f.messages) {
			return nil, "", errors.New("bad cursor")
		}
		from = n
	}
	return append([]web.Message(nil), f.messages[from:]...), strconv.Itoa(len(f.messages)), nil
}

// SetAccount takes a name the way the daemon does, refusing the other person's.
func (f *fixtures) SetAccount(ctx context.Context, name string) (web.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !bus.ValidPerson(name) {
		return web.Account{}, fmt.Errorf("%q can't be a name. Use 2 to %d lowercase letters and digits, with single dashes between words.", name, names.MaxPerson)
	}
	if name == "maya" {
		return web.Account{}, errors.New("maya is taken, by the person on maya-mbp-91c0.")
	}
	f.account.Name, f.account.Addr, f.account.Suggested, f.account.Taken = name, f.self+"/human", "", ""
	for i := range f.devices {
		if f.devices[i].Self {
			f.devices[i].Person = &web.Person{Name: name, Addr: f.account.Addr, LastActive: time.Now(), Tags: tags.Person()}
		}
	}
	return f.account, nil
}

// Send takes as long as a push does, refuses what scrub would, and has the session answer a few
// seconds later, as an agent does, going active first so the page can show it working.
func (f *fixtures) Send(ctx context.Context, to, text string) (web.Sent, error) {
	f.mu.Lock()
	takenBy := f.account.Taken
	f.mu.Unlock()
	if takenBy != "" {
		return web.Sent{}, fmt.Errorf("the person on %s has your name; choose another before you send", takenBy)
	}
	select {
	case <-time.After(700 * time.Millisecond):
	case <-ctx.Done():
		return web.Sent{}, ctx.Err()
	}
	if strings.Contains(text, "AKIA") {
		return web.Sent{}, errors.New("not sent: it looks like it holds a secret, and the bus keeps everything forever")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	subject, body := web.SplitText(text)
	m := web.Message{ID: randomHex(6), From: f.account.Name, FromAddr: f.account.Addr, FromPerson: true,
		Subject: subject, Body: body, At: time.Now()}
	switch to {
	case "*":
		m.To, m.ToAddr, m.ToKind = "*", "*", web.ToAll
	default:
		m.To, m.ToAddr, m.ToKind = to, to, web.ToSession
		isDevice := false
		for _, d := range f.devices {
			if d.ID == to {
				isDevice = true
			}
			if d.Person != nil && d.Person.Name == to {
				m.ToAddr, m.ToPerson = d.Person.Addr, true
			}
			for _, s := range d.Sessions {
				if s.Name == to || s.Addr == to {
					m.To, m.ToAddr = s.Name, s.Addr
				}
			}
		}
		if isDevice {
			m.ToKind = web.ToDevice
		}
	}
	f.messages = append(f.messages, m)
	sent := web.Sent{ID: m.ID}
	if m.ToKind == web.ToSession && !m.ToPerson {
		go f.answer(m)
	}
	return sent, nil
}

// answer is the session m went to, starting a turn and then answering with `send`.
func (f *fixtures) answer(m web.Message) {
	time.Sleep(1500 * time.Millisecond)
	f.touch(m.To)
	time.Sleep(time.Duration(3+mrand.IntN(4)) * time.Second)
	f.touch(m.To)
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := web.Message{ID: randomHex(6), From: m.To, FromAddr: m.ToAddr, To: f.account.Name, ToAddr: f.account.Addr,
		ToKind: web.ToSession, ToPerson: true, Subject: "Re: " + m.Subject,
		Body: "On it. I'll send you the result when it's done.", At: time.Now()}
	f.messages = append(f.messages, reply)
	f.account.Unread = append(f.account.Unread, reply.ID)
}

func (f *fixtures) touch(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.devices {
		for j := range f.devices[i].Sessions {
			if f.devices[i].Sessions[j].Name == name {
				f.devices[i].Sessions[j].LastActive = time.Now()
			}
		}
	}
}

func (f *fixtures) MarkRead(ctx context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.account.Unread = slices.DeleteFunc(f.account.Unread, func(id string) bool { return slices.Contains(ids, id) })
	return nil
}

func (f *fixtures) Settings(ctx context.Context) (web.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copySettings(f.settings), nil
}

func (f *fixtures) SetSettings(ctx context.Context, s web.Settings) (web.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	local := map[string]bool{}
	for _, d := range f.devices {
		if d.Self {
			for _, ss := range d.Sessions {
				local[ss.Name] = true
			}
		}
	}
	for name := range s.SessionWake {
		if !local[name] {
			return web.Settings{}, fmt.Errorf("%s is not a session on this device", name)
		}
	}
	for name := range s.SessionBusy {
		if !local[name] {
			return web.Settings{}, fmt.Errorf("%s is not a session on this device", name)
		}
	}
	s.DefaultMinutes = f.settings.DefaultMinutes
	f.settings = copySettings(s)
	return copySettings(f.settings), nil
}

func copySettings(s web.Settings) web.Settings {
	out := s
	out.SessionWake = map[string]web.Wake{}
	for k, v := range s.SessionWake {
		out.SessionWake[k] = v
	}
	out.SessionBusy = map[string]string{}
	for k, v := range s.SessionBusy {
		out.SessionBusy[k] = v
	}
	return out
}

// Restart acts out a daemon restarting onto the installed update: the page loses the server for
// a few seconds, then finds the new version.
func (f *fixtures) Restart(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.version.Ready {
		return web.ErrNoUpdate
	}
	f.version = web.Version{Running: f.version.Latest, Install: f.version.Install, Daemon: "2"}
	f.downTill = time.Now().Add(3 * time.Second)
	return nil
}

// SetTags applies a human's change as the session's device would, with no derived tags to
// account for.
func (f *fixtures) SetTags(ctx context.Context, addr string, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.devices {
		for j := range f.devices[i].Sessions {
			s := &f.devices[i].Sessions[j]
			if s.Addr != addr && s.Name != addr {
				continue
			}
			var self []string
			human := map[string]tags.Mark{}
			for _, t := range s.Tags {
				if t.Src == tags.SrcHuman {
					human[t.Name] = tags.Mark{On: true}
				} else {
					self = append(self, t.Name)
				}
			}
			st := tags.State{Self: self, Human: human}
			if err := st.SetHuman(self, add, remove, "Tyler", time.Now()); err != nil {
				return err
			}
			s.Tags = tags.Effective(st.Self, nil, st.Human)
			return nil
		}
	}
	return errors.New("no such session")
}

func (f *fixtures) Unmute(ctx context.Context, by, muted string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.mutes {
		if m.By == by && m.Muted == muted {
			if !m.CanUnmute {
				return errors.New("that mute was made on another device")
			}
			f.mutes = append(f.mutes[:i], f.mutes[i+1:]...)
			return nil
		}
	}
	return errors.New("no such mute")
}
