// Command devserver serves the web page with made-up devices, sessions and messages, for working
// on the page without a daemon or a team: go run ./internal/web/devserver
//
// The fixtures are a hackathon team's (#560). They change while it runs: a message arrives every 10 to 40 seconds and sessions go on
// being active, so the page's polling has something to show. Most messages answer the one before,
// so a conversation goes back and forth as a real one does.
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	repos := flag.String("repos", "", "comma-separated repo names: each is served on a port of its own, and the page's picker switches between them (#348); the last starts stopped")
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
		src.account = web.Account{Name: "maya", Addr: src.self + "/human", Taken: "maya-mbp-91c0"}
	}
	go src.run()
	page := web.NewWithGitHub(src, token, fixturePRs, fixtureGitHub(time.Now()))
	if *repos != "" {
		serveRepos(strings.Split(*repos, ","), ln, src, page)
	}
	fmt.Printf("http://%s/#token=%s\n", ln.Addr(), token)
	srv := &http.Server{Handler: page, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// devRepo is one made-up clone, with a page of its own on a port of its own, as each clone's daemon
// serves its own.
type devRepo struct {
	name, path string
	addr       string
	page       *web.Server
	mu         sync.Mutex
	running    bool
}

// devRepos is the picker's view from one of them.
type devRepos struct {
	all  []*devRepo
	self int
}

func (r devRepos) List(ctx context.Context) ([]web.Repo, error) {
	var out []web.Repo
	for i, d := range r.all {
		d.mu.Lock()
		out = append(out, web.Repo{ID: strconv.Itoa(i), Name: d.name, Path: d.path, Running: d.running, Current: i == r.self})
		d.mu.Unlock()
	}
	return out, nil
}

// Open takes a moment, and longer for a repo whose daemon has to start.
func (r devRepos) Open(ctx context.Context, id string) (string, error) {
	i, err := strconv.Atoi(id)
	if err != nil || i < 0 || i >= len(r.all) {
		return "", web.ErrNoRepo
	}
	d := r.all[i]
	d.mu.Lock()
	wait := 300 * time.Millisecond
	if !d.running {
		wait = 1500 * time.Millisecond
	}
	d.mu.Unlock()
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()
	return "http://" + d.addr + "/#key=" + d.page.PageKey(), nil
}

// serveRepos serves a page for each name after the first, which is the page on ln, and gives them
// all the picker. Each repo after the first has fewer devices and messages, so the switch shows.
func serveRepos(names []string, ln net.Listener, first *fixtures, firstPage *web.Server) {
	all := make([]*devRepo, len(names))
	for i, name := range names {
		name = strings.TrimSpace(name)
		d := &devRepo{name: name, path: "~/code/" + name, running: i == 0 || i < len(names)-1}
		src := first
		if i == 0 {
			d.addr, d.page = ln.Addr().String(), firstPage
		} else {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				log.Fatal(err)
			}
			src = newFixtures(time.Now())
			src.version = first.version
			src.devices = src.devices[:1+i%2]
			src.messages = src.messages[len(src.messages)/(i+1):]
			go src.run()
			d.addr, d.page = l.Addr().String(), web.NewWithGitHub(src, randomHex(16), fixturePRs, fixtureGitHub(time.Now()))
			srv := &http.Server{Handler: d.page, ReadHeaderTimeout: 10 * time.Second}
			go func() { log.Fatal(srv.Serve(l)) }()
		}
		src.mu.Lock()
		src.repo = name
		src.mu.Unlock()
		all[i] = d
	}
	for i, d := range all {
		d.page.WithRepos(devRepos{all: all, self: i})
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// fixturePRs answers as gh would for the fixture branches, a little slowly.
func fixturePRs(ctx context.Context) (map[string]web.PR, error) {
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]web.PR{
		"auth-flow":      {Number: 12, URL: "https://github.com/example/huddle/pull/12"},
		"landing-page":   {Number: 14, Draft: true, URL: "https://github.com/example/huddle/pull/14"},
		"api-rate-limit": {Number: 17, URL: "https://github.com/example/huddle/pull/17"},
		"demo-polish":    {Number: 19, Draft: true, URL: "https://github.com/example/huddle/pull/19"},
	}, nil
}

type fixtures struct {
	mu       sync.Mutex
	self     string
	devices  []web.Device
	messages []web.Message
	activity []web.Activity
	mutes    []web.Mute
	settings web.Settings
	version  web.Version
	downTill time.Time // a restart in progress: State fails until then
	account  web.Account
	repo     string // the repo's name, for the picker
	// receipts is how far each of the person's messages got (#26). sam-thinkpad stands for a device
	// on an older Murmur, which sends none.
	receipts map[string][]web.Receipt
	// picks is the colour each device's person picked (#573), by device; the rest are chosen.
	picks map[string]bus.ColorPick
	// files is the files each message carries (#570), by the message's ID.
	files map[string][]web.Upload
}

// attach gives the message last added these files. Caller holds mu, or is still building f.
func (f *fixtures) attach(files ...web.Upload) {
	m := &f.messages[len(f.messages)-1]
	if f.files == nil {
		f.files = map[string][]web.Upload{}
	}
	f.files[m.ID] = files
	for i, name := range fileNames(files) {
		m.Files = append(m.Files, web.File{Name: name, Size: int64(len(files[i].Data))})
	}
}

// noFile is why a file can't be had, in words to show (web.ErrNoFile).
type noFile string

func (e noFile) Error() string        { return string(e) }
func (e noFile) Is(target error) bool { return target == web.ErrNoFile }

// fileNames is the names files are sent under, as the daemon makes them: safe, and distinct.
func fileNames(files []web.Upload) []string {
	named := make([]bus.File, len(files))
	for i, u := range files {
		named[i].Name = u.Name
	}
	names := make([]string, len(files))
	for i, f := range bus.CleanFiles(named) {
		names[i] = f.Name
	}
	return names
}

// File is a file of a fixture message, after the moment a fetch takes. One called gone can't be had.
func (f *fixtures) File(ctx context.Context, id string, index int) (string, []byte, error) {
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	files := f.files[id]
	if index >= len(files) {
		return "", nil, web.ErrNoFile
	}
	if name := fileNames(files)[index]; !strings.HasPrefix(name, "gone") {
		return name, files[index].Data, nil
	}
	return "", nil, noFile(files[index].Name + " isn't here: it couldn't be fetched, or was sent more than a week ago")
}

// screenshot draws a picture that stands for a screenshot of a sign-in page: w by h, dark, with a
// card and a button, so the page has an image to show that looks like what agents send.
func screenshot(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := func(r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
	}
	fill(img.Bounds(), color.RGBA{0x12, 0x14, 0x1a, 0xff})
	fill(image.Rect(0, 0, w, h/12), color.RGBA{0x1c, 0x20, 0x29, 0xff})
	card := image.Rect(w/3, h/4, 2*w/3, 3*h/4)
	fill(card, color.RGBA{0x23, 0x28, 0x33, 0xff})
	for i := range 2 {
		y := card.Min.Y + (i+2)*card.Dy()/6
		fill(image.Rect(card.Min.X+w/30, y, card.Max.X-w/30, y+h/22), color.RGBA{0x15, 0x18, 0x1f, 0xff})
	}
	fill(image.Rect(card.Min.X+w/30, card.Max.Y-h/8, card.Max.X-w/30, card.Max.Y-h/16), color.RGBA{0x5b, 0x8d, 0xef, 0xff})
	fill(image.Rect(card.Min.X+w/30, card.Min.Y+h/20, card.Min.X+w/6, card.Min.Y+h/20+h/40), color.RGBA{0xd7, 0xdc, 0xe5, 0xff})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func newFixtures(now time.Time) *fixtures {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	f := &fixtures{
		self:    "tyler-mbp-7a20",
		account: web.Account{Suggested: "tyler"},
		settings: web.Settings{
			Wake:           web.Wake{When: web.WakeWithin},
			SessionWake:    map[string]web.Wake{"brave-heron": {When: web.WakeNever}},
			Busy:           web.BusySubject,
			SessionBusy:    map[string]string{"quiet-otter": web.BusyWhole},
			DefaultMinutes: 5,
		},
	}
	f.activity = fixtureActivity(now)
	// Maya and Jordan picked colours, so Settings shows two as taken.
	f.picks = map[string]bus.ColorPick{
		"maya-mbp-91c0":   {Device: "maya-mbp-91c0", Color: 9, At: ago(26 * time.Hour)},
		"jordan-xps-2b90": {Device: "jordan-xps-2b90", Color: 2, At: ago(3 * time.Hour)},
	}
	// A hackathon team on its second afternoon, racing to a 5 pm demo of Huddle, an app that writes
	// meeting notes: four people, five machines, agents from three clients.
	f.devices = []web.Device{
		{
			ID: "tyler-mbp-7a20", Host: "tyler-mbp", Owner: "Tyler", Self: true, Reachable: true,
			LastSeen: ago(5 * time.Second),
			Sessions: []web.Session{
				{Name: "quiet-otter", Agent: "claude-code", Branch: "demo-polish", Worktree: "demo-polish",
					Doing: "Adding empty states and a loading skeleton to the dashboard", Started: ago(52 * time.Minute), LastActive: ago(20 * time.Second),
					Tags: []tags.Tag{{Name: "group:demo"}, {Name: "group:frontend"}, {Name: "model:opus-5.5", Src: tags.SrcDerived}, {Name: "ui"}}},
				{Name: "brave-heron", Agent: "codex", Branch: "api-rate-limit", Worktree: "api-rate-limit",
					Doing: "Limiting /api/summarize to 10 requests a minute per user", Started: ago(2*time.Hour + 4*time.Minute), LastActive: ago(3 * time.Minute),
					Tags: []tags.Tag{{Name: "group:demo"}, {Name: "model:gpt-5.1-codex"}, {Name: "backend", Src: tags.SrcHuman}}},
				{Name: "calm-finch", Agent: "claude-code", Branch: "main", Worktree: "huddle",
					Started: ago(5 * time.Hour), LastActive: ago(26 * time.Minute)},
				// Left open since last night: the page folds it away as quiet.
				{Name: "gentle-gecko", Agent: "claude-code", Branch: "websocket-spike", Worktree: "websocket-spike",
					Doing: "Trying live transcripts over a WebSocket", Started: ago(20 * time.Hour), LastActive: ago(15 * time.Hour)},
			},
		},
		{
			ID: "maya-mbp-91c0", Host: "maya-mbp", Owner: "Maya Chen", Nick: "maya", Reachable: true, LastSeen: ago(40 * time.Second),
			Sessions: []web.Session{
				{Name: "tidy-badger", Agent: "claude-code", Branch: "auth-flow",
					Doing: "Wiring GitHub sign-in through NextAuth", Started: ago(3 * time.Hour), LastActive: ago(50 * time.Second),
					Tags: []tags.Tag{{Name: "group:frontend"}, {Name: "model:sonnet-5.5"}, {Name: "reviewing"}}},
				{Name: "crisp-lark", Agent: "t3", Branch: "landing-page",
					Doing: "Writing the landing page's hero and its three feature cards", Started: ago(95 * time.Minute), LastActive: ago(2 * time.Minute),
					Tags: []tags.Tag{{Name: "group:frontend"}}},
				{Name: "solar-sparrow", Agent: "codex", Branch: "auth-flow",
					Doing:   "Writing tests for the session middleware, including what happens when a GitHub token expires in the middle of the demo",
					Started: ago(70 * time.Minute), LastActive: ago(1 * time.Minute)},
			},
		},
		{
			ID: "jordan-xps-2b90", Host: "jordan-xps", Owner: "Jordan Reyes", Nick: "jordan", Reachable: true, LastSeen: ago(25 * time.Second),
			Sessions: []web.Session{
				{Name: "rapid-ibis", Agent: "claude-code", Branch: "api-rate-limit",
					Doing: "Load-testing /api/summarize at 50 requests a second", Started: ago(40 * time.Minute), LastActive: ago(15 * time.Second),
					Tags: []tags.Tag{{Name: "group:demo"}, {Name: "backend"}}},
				{Name: "mellow-yak", Agent: "claude-code", Branch: "db-migrations",
					Doing: "Adding the transcripts table and its indexes", Started: ago(4 * time.Hour), LastActive: ago(12 * time.Minute),
					Tags: []tags.Tag{{Name: "backend"}}},
				{Name: "keen-pike", Agent: "t3", Branch: "deploy",
					Doing: "Moving staging's secrets into Fly", Started: ago(2 * time.Hour), LastActive: ago(4 * time.Minute),
					Tags: []tags.Tag{{Name: "group:demo"}}},
			},
		},
		{
			ID: "sam-thinkpad-a81e", Host: "sam-thinkpad", Owner: "Sam Okafor", Reachable: true, LastSeen: ago(70 * time.Second),
			Sessions: []web.Session{
				{Name: "bold-crane", Agent: "codex", Branch: "landing-page",
					Doing: "Fixing the mobile nav on the landing page", Started: ago(45 * time.Minute), LastActive: ago(30 * time.Second),
					Tags: []tags.Tag{{Name: "group:frontend"}}},
				{Name: "keen-bison", Agent: "claude-code", Branch: "seed-data",
					Doing: "Seeding three teams and forty meetings for the demo", Started: ago(90 * time.Minute), LastActive: ago(6 * time.Minute),
					Tags: []tags.Tag{{Name: "group:demo"}}},
			},
		},
		{
			// Sam's desktop at home, running the summary eval. It has gone to sleep.
			ID: "sam-desktop-07be", Host: "sam-desktop", Owner: "Sam Okafor", Reachable: false, LastSeen: ago(47 * time.Minute),
			Sessions: []web.Session{
				{Name: "calm-moose", Agent: "codex", Branch: "summary-eval",
					Doing: "Scoring summaries of 200 transcripts against hand-written ones", Started: ago(6 * time.Hour), LastActive: ago(48 * time.Minute)},
				{Name: "mellow-lark", Agent: "claude-code", Started: ago(7 * time.Hour), LastActive: ago(55 * time.Minute)},
			},
		},
	}
	// Two other people have accounts, so the page's list of people has someone in it.
	f.devices[1].Person = &web.Person{Name: "maya", Addr: "maya-mbp-91c0/human", LastActive: ago(40 * time.Second),
		Tags: tags.Person()}
	f.devices[2].Person = &web.Person{Name: "jordan", Addr: "jordan-xps-2b90/human", LastActive: ago(20 * time.Second),
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
	// A person's message to a session, and a session's to a person.
	fromPerson := func(d time.Duration, from, to, subject, body string) {
		f.messages = append(f.messages, web.Message{ID: randomHex(6), From: from, FromAddr: personAddr(f.devices, from), FromPerson: true,
			To: to, ToKind: web.ToSession, Subject: subject, Body: body, At: ago(d)})
	}
	toPerson := func(d time.Duration, from, to, subject, body string) {
		f.messages = append(f.messages, web.Message{ID: randomHex(6), From: from, To: to, ToAddr: personAddr(f.devices, to), ToPerson: true,
			ToKind: web.ToSession, Subject: subject, Body: body, At: ago(d)})
	}
	// Messages with files (#570): a screenshot and a log between two agents, and one to the person
	// whose second file can't be had.
	msg(200*time.Minute, "tidy-badger", "quiet-otter", web.ToSession, "The sign-in page after the redirect",
		"The callback lands here with the form empty. The log has the request that fails.")
	f.attach(web.Upload{Name: "sign-in.png", Data: screenshot(960, 600)},
		web.Upload{Name: "auth.log", Data: []byte(strings.Repeat("GET /auth/callback?code=... 302\nPOST /auth/session 401 token expired\n", 40))})
	toPerson(190*time.Minute, "quiet-otter", "tyler", "Two views of the fix",
		"Before and after. The trace from the first run is gone by now.")
	f.attach(web.Upload{Name: "before.png", Data: screenshot(640, 400)}, web.Upload{Name: "after.png", Data: screenshot(640, 480)},
		web.Upload{Name: "gone-trace.zip", Data: make([]byte, 3<<20)})
	msg(3*time.Hour, "tidy-badger", "*", web.ToAll, "Taking auth-flow",
		"I'm on GitHub sign-in. Please stay out of app/auth and middleware.ts until I push; tell me if you need something from them.")
	msg(170*time.Minute, "brave-heron", "tidy-badger", web.ToSession, "The rate limit sits behind your middleware",
		"api-rate-limit wraps /api/* in middleware.ts too. I'll run after yours, so the session is there when I key the limit on the user.")
	msg(165*time.Minute, "tidy-badger", "brave-heron", web.ToSession, "Re: The rate limit sits behind your middleware",
		"Works for me. The user is on req.auth once mine has run.")
	msg(2*time.Hour, "keen-pike", "group:demo", web.ToGroup, "Staging is up",
		"huddle-staging.fly.dev deploys from main on every merge now. Secrets live in Fly; don't put them back in .env.")
	msg(80*time.Minute, "rapid-ibis", "group:demo", web.ToGroup, "Load test numbers",
		"/api/summarize holds 50 req/s for two minutes. p95 is 4.1 s, nearly all of it the model call. Key the limit on the user, not the IP: the demo room shares one IP.")
	msg(62*time.Minute, "mellow-yak", "jordan-xps-2b90", web.ToDevice, "Staging needs a database reset",
		"The transcripts table changed shape. Run `pnpm db:reset` on staging before the next deploy, or the seed script fails.")
	msg(50*time.Minute, "quiet-otter", "crisp-lark", web.ToSession, "Hero copy and the demo script",
		"The demo opens on the landing page. Can the hero say \"Notes that write themselves\", so it matches the first line of the script?")
	msg(47*time.Minute, "crisp-lark", "quiet-otter", web.ToSession, "Re: Hero copy and the demo script",
		"Done, it's on landing-page. I shortened the subhead to fit one line at 1280 px.")
	msg(30*time.Minute, "crisp-lark", "maya-mbp-91c0", web.ToDevice, "Landing page copy is in", "")
	msg(22*time.Minute, "tidy-badger", "quiet-otter", web.ToSession, "Review of demo-polish", long)
	msg(18*time.Minute, "brave-heron", "*", web.ToAll, "Merged main, rebase",
		"api-rate-limit is in main. middleware.ts changed shape, so rebase before you push or you'll fight the conflict.")
	msg(12*time.Minute, "quiet-otter", "tidy-badger", web.ToSession, "Re: Review of demo-polish",
		"All three fixed and pushed. Can you look again before 4? The demo starts on that dashboard.")
	msg(9*time.Minute, "bold-crane", "group:frontend", web.ToGroup, "Mobile nav is fixed",
		"The landing page's menu no longer covers the sign-in button below 400 px.")
	fromPerson(6*time.Minute, "jordan", "rapid-ibis", "Is the limit on in staging?", "")
	toPerson(5*time.Minute, "rapid-ibis", "jordan", "Re: Is the limit on in staging?",
		"Yes, since the 3:12 deploy. A second browser on your account gets a 429 after ten summaries a minute.")
	msg(4*time.Minute, "keen-bison", "group:demo", web.ToGroup, "Seed data is on staging",
		"Three teams, forty meetings, transcripts included. Sign in as demo@huddle.dev to see the full dashboard.")
	fromPerson(3*time.Minute, "maya", "crisp-lark", "Can you send me a screenshot of the landing page on a phone?", "")
	toPerson(2*time.Minute, "crisp-lark", "maya", "Re: Can you send me a screenshot of the landing page on a phone?",
		"It's at the bottom of #14: 390 px wide, with bold-crane's nav fix.")
	msg(90*time.Second, "solar-sparrow", "tidy-badger", web.ToSession, "Middleware tests pass",
		"Twelve tests, including a GitHub token that expires mid-session: it signs the user out instead of throwing.")
	f.mutes = []web.Mute{
		{By: "quiet-otter", Muted: "keen-pike", At: ago(40 * time.Minute), CanUnmute: true},
		{By: "solar-sparrow", Muted: "rapid-ibis", At: ago(12 * time.Minute)},
	}
	return f
}

// personAddr is the address of the person called name.
func personAddr(devices []web.Device, name string) string {
	for _, d := range devices {
		if d.Person != nil && d.Person.Name == name {
			return d.Person.Addr
		}
	}
	return ""
}

const long = `Looked at demo-polish. It's close; three things before the demo:

1. The empty state says "No meetings yet" while the first fetch is still running. Show the skeleton until the request settles, or a slow staging response reads as an empty account.
2. The skeleton rows are 48 px and the real rows 56 px, so the list jumps when it loads. Match them.
3. The Summarize button stays enabled during a request. Two clicks make two calls, and with brave-heron's limit the second can come back as a 429 on stage.

Smaller, if there's time:

    - The meeting card's date is in UTC. The demo is in Pacific.
    - "Re-generate" should be "Regenerate".

Everything else reads well. Ping me when it's pushed and I'll look again.`

func (f *fixtures) run() {
	subjects := []string{
		"Rebased on main", "Is anyone in app/api/summarize?", "Staging deploy is green",
		"Can I take the empty states?", "Pushed the seed data", "Done with the rate limit",
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if time.Now().Before(f.downTill) {
		return web.State{}, errors.New("restarting")
	}
	st := web.State{Repo: f.repo, Device: f.self, Mutes: append([]web.Mute(nil), f.mutes...), Version: f.version, Account: f.account}
	if len(f.receipts) > 0 {
		st.Receipts = map[string][]web.Receipt{}
		for id, rs := range f.receipts {
			st.Receipts[id] = append([]web.Receipt(nil), rs...)
		}
	}
	colors := f.colorsLocked()
	st.Mode = f.settings.Mode
	st.SendFiles, st.ReceiveFiles = f.settings.SendFiles, f.settings.ReceiveFiles
	st.AgentsSendOff, st.AgentsReceiveOff = f.settings.AgentsSendOff, f.settings.AgentsReceiveOff
	for _, d := range f.devices {
		d.Sessions = append([]web.Session(nil), d.Sessions...)
		if d.Self {
			d.DND = st.Mode == web.ModeDND
			if st.Mode == web.ModeInvisible {
				d.Sessions = nil // as the daemon does: this device's agents are off Murmur (#681)
			}
		}
		d.Color, d.Picked = colors[d.ID].Color, colors[d.ID].Picked
		st.Devices = append(st.Devices, d)
	}
	return st, nil
}

// colorsLocked settles every device's colour as the daemon does. Caller holds mu.
func (f *fixtures) colorsLocked() map[string]bus.DeviceColor {
	var picks []bus.ColorPick
	for _, d := range f.devices {
		picks = append(picks, cmp.Or(f.picks[d.ID], bus.ColorPick{Device: d.ID}))
	}
	return bus.DeviceColors(picks)
}

// SetColor picks this device's colour as the daemon does, refusing one another device has.
func (f *fixtures) SetColor(ctx context.Context, color int) (web.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if color < 0 || color > bus.Colors {
		return web.Account{}, fmt.Errorf("there is no colour %d", color)
	}
	colors := f.colorsLocked()
	for _, d := range f.devices {
		if c := colors[d.ID]; !d.Self && c.Picked && c.Color == color-1 {
			return web.Account{}, fmt.Errorf("%s has that colour", cmp.Or(d.Nick, d.Owner, d.ID))
		}
	}
	f.account.Color, f.account.ColorLost = color, ""
	f.picks[f.self] = bus.ColorPick{Device: f.self, Color: color, At: time.Now()}
	return f.account, nil
}

// Messages uses the count of messages seen as its cursor.
func (f *fixtures) Messages(ctx context.Context, cursor string) ([]web.Message, string, error) {
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

// SetNick takes a nickname the way the daemon does, refusing another device's ID or person.
func (f *fixtures) SetNick(ctx context.Context, nick string) (web.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if nick != "" && !bus.ValidNick(nick) {
		return web.Account{}, fmt.Errorf("%q can't be a nickname. Use 2 to %d lowercase letters and digits, with single dashes between words, and not a session's name.", nick, names.MaxPerson)
	}
	if nick == "maya" || nick == "jordan" {
		return web.Account{}, fmt.Errorf("%s is the name of the person on %s.", nick, strings.TrimSuffix(personAddr(f.devices, nick), "/human"))
	}
	f.account.Nick = nick
	for i := range f.devices {
		if f.devices[i].Self {
			f.devices[i].Nick = cmp.Or(nick, f.account.Name)
		}
	}
	return f.account, nil
}

// SetAccount takes a name the way the daemon does, refusing the other person's.
func (f *fixtures) SetAccount(ctx context.Context, name string) (web.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !bus.ValidPerson(name) {
		return web.Account{}, fmt.Errorf("%q can't be a name. Use 2 to %d lowercase letters and digits, with single dashes between words.", name, names.MaxPerson)
	}
	if name == "maya" || name == "jordan" {
		return web.Account{}, fmt.Errorf("%s is taken, by the person on %s.", name, strings.TrimSuffix(personAddr(f.devices, name), "/human"))
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
func (f *fixtures) Send(ctx context.Context, to, text string, files []web.Upload) (web.Sent, error) {
	f.mu.Lock()
	takenBy, invisible := f.account.Taken, f.settings.Mode == web.ModeInvisible
	f.mu.Unlock()
	if invisible {
		return web.Sent{}, errors.New("this device is invisible: go online in Settings to send")
	}
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
	if strings.TrimSpace(text) == "" && len(files) > 0 {
		text = strings.Join(fileNames(files), ", ") // as the daemon does
	}
	subject, body := web.SplitText(text)
	m := web.Message{ID: randomHex(6), From: f.account.Name, FromAddr: f.account.Addr, FromPerson: true,
		Subject: subject, Body: body, At: time.Now()}
	switch to {
	case "*":
		m.To, m.ToAddr, m.ToKind = "*", "*", web.ToAll
	default:
		if tags.IsGroup(to) {
			m.To, m.ToAddr, m.ToKind = to, to, web.ToGroup
			break
		}
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
	if len(files) > 0 {
		f.attach(files...)
		m = f.messages[len(f.messages)-1]
	}
	f.receive(m)
	sent := web.Sent{ID: m.ID}
	if m.ToKind == web.ToSession && !m.ToPerson {
		go f.answer(m)
	}
	return sent, nil
}

// receive gives m a receipt per recipient (#26), and moves each along as a recipient's device would
// mark it: queued, then delivered or handed over, then for some, read. A recipient on sam-thinkpad
// sends none. Caller holds mu.
func (f *fixtures) receive(m web.Message) {
	var rs []web.Receipt
	for _, d := range f.devices {
		if d.Person != nil && d.Person.Addr == m.ToAddr {
			rs = append(rs, web.Receipt{Addr: d.Person.Addr, Name: d.Person.Name, Person: true})
		}
		for _, s := range d.Sessions {
			if s.Addr == m.ToAddr || m.ToKind == web.ToAll || m.ToKind == web.ToDevice && d.ID == m.To ||
				m.ToKind == web.ToGroup && slices.ContainsFunc(s.Tags, func(t tags.Tag) bool { return t.Name == m.To }) {
				r := web.Receipt{Addr: s.Addr, Name: s.Name}
				if d.ID == "sam-thinkpad-a81e" {
					r.State = web.ReceiptNone
				}
				rs = append(rs, r)
			}
		}
	}
	if len(rs) == 0 {
		return
	}
	if f.receipts == nil {
		f.receipts = map[string][]web.Receipt{}
	}
	f.receipts[m.ID] = rs
	steps := []string{bus.MarkQueued, bus.MarkDelivered, bus.MarkRead}
	for i := range rs {
		if rs[i].State == web.ReceiptNone {
			continue
		}
		last := 1 + mrand.IntN(len(steps)) // some stay queued or delivered
		if len(rs) == 1 {
			last = len(steps)
		}
		go func(i int) {
			for _, state := range steps[:last] {
				time.Sleep(time.Duration(1500+mrand.IntN(2500)) * time.Millisecond)
				f.mu.Lock()
				f.receipts[m.ID][i].State, f.receipts[m.ID][i].At = state, time.Now()
				f.mu.Unlock()
			}
		}(i)
	}
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

// SetTags applies a human's change as the session's device would. The fixtures keep only the
// session's current tags, so its state is rebuilt from them, each by where it came from: a derived
// tag counted as the session's own would show as set by the session after any change (#224).
func (f *fixtures) SetTags(ctx context.Context, addr string, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.devices {
		for j := range f.devices[i].Sessions {
			s := &f.devices[i].Sessions[j]
			if s.Addr != addr && s.Name != addr {
				continue
			}
			var self, derived []string
			human := map[string]tags.Mark{}
			for _, t := range s.Tags {
				switch t.Src {
				case tags.SrcHuman:
					human[t.Name] = tags.Mark{On: true}
				case tags.SrcDerived:
					derived = append(derived, t.Name)
				default:
					self = append(self, t.Name)
				}
			}
			st := tags.State{Self: self, Human: human}
			if err := st.SetHuman(tags.Base(self, derived), add, remove, "Tyler", time.Now()); err != nil {
				return err
			}
			s.Tags = tags.Effective(st.Self, derived, st.Human)
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
