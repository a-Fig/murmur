// Package daemon is the per-clone process that owns the bus for its device: the only process
// that polls the remote and the only one that writes this device's refs. Sessions on the device
// reach it through package client.
package daemon

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
)

// Options configure a daemon. Zero values take the defaults in docs/VISION.md.
type Options struct {
	GitDir string        // the clone's git common dir, absolute
	Poll   time.Duration // how often to check the remote; 3 s
	// QuietPoll is how often to check it while no session is live and no page is open: 30 s. A
	// daemon that is only waiting to wake a T3 thread (see Idle) needn't look as often as one with
	// an agent working; it is never faster than Poll.
	QuietPoll  time.Duration
	Heartbeat  time.Duration // how often to re-push presence when nothing changed; 2 min
	StaleAfter time.Duration // presence unchanged this long means the device is gone; 6 min
	// PollCap is the slowest the poll gets while a session is live and nothing is happening: 15 s.
	// FastFor is how long after a conversation moves (this device sends a message, or delivers or
	// receives one for a session here, or a session starts) the poll stays at Poll: 3 min (#93).
	// Neither is ever below Poll.
	PollCap, FastFor time.Duration
	// Idle is how long a daemon with no sessions, no open page and no T3 thread to wake (see
	// holdsRouteLocked) goes on before it exits: 10 min. Negative: never.
	Idle time.Duration
	// SessionGrace is how long a session whose connection dropped stays registered, for the same
	// session ID to take it over: 60 s. Its MCP server restarts while the conversation carries on.
	SessionGrace time.Duration
	// StoppedInbox is how long a stopped session's inbox is kept for it to come back to: 24 h.
	StoppedInbox time.Duration
	// NameSource is where the randomness for session names comes from: crypto/rand's reader.
	// Tests give two devices the same one, to make them pick the same name.
	NameSource io.Reader
	// NameRetry is how long the first retry after a failed name assignment waits: 15 s. It
	// doubles with each failure in a row.
	NameRetry time.Duration
	// NamePool is how many names a daemon keeps registered ahead for sessions it has yet to start,
	// so that a new session has its name at once, without the remote: 10, enough for a burst of new
	// sessions (T3 reopening several threads at once) while the remote is slow or down. Whenever it
	// holds fewer, the daemon sets more aside. Negative: none, and every session waits for its name
	// to land in the registry.
	NamePool int
	// PushRetry is how long a delivery of a message as a turn that never got started (no `t3`
	// to run, no socket to connect to) waits before its one retry: 2 s. Any other failure may have
	// delivered the message, so it is not repeated, and a second failure of this kind too leaves
	// the message to the hook's notice.
	PushRetry time.Duration
	// PageAddr is where the page for humans is served: 127.0.0.1:0, a random port on loopback.
	// A host other than 127.0.0.1 is refused: off loopback anyone on the network could reach the
	// page, and on another loopback address the page's Host check would refuse every request.
	PageAddr string
	// PageOpen is how recently a page someone can see must have called its API to count as open,
	// which keeps the daemon running like a session does: 2 min. A hidden tab's calls don't count.
	PageOpen time.Duration
	// PageMessages is how many messages the page can look back on: 500.
	PageMessages int
	// SnapshotTimeout bounds the git a snapshot of a tool call's worktree runs (#21): 4 s. Tests on a
	// loaded machine raise it.
	SnapshotTimeout time.Duration
	// TitleEvery is how often the titles of the sessions' conversations are read from their
	// clients' state, and the least time between two reads of one: 5 s (#66).
	TitleEvery time.Duration
	// Consent says whether the user has joined Murmur in this clone (nil: always). A daemon exists
	// only for a user who did: it won't start without it, refuses requests once it is gone, and
	// stops within ConsentPoll of it going, so a leave that races a daemon still starting ends that
	// daemon too.
	Consent     func() bool
	ConsentPoll time.Duration // how often to look; 1 s
	Log         *log.Logger
	// CheckIn, if set, runs in the background for as long as the daemon serves: the daily
	// check-in that counts active users (internal/checkin). Tests leave it unset.
	CheckIn func(context.Context)
}

func (o *Options) defaults() {
	if o.Poll == 0 {
		o.Poll = 3 * time.Second
	}
	if o.QuietPoll == 0 {
		o.QuietPoll = 30 * time.Second
	}
	if o.PollCap == 0 {
		o.PollCap = 15 * time.Second
	}
	if o.FastFor == 0 {
		o.FastFor = 3 * time.Minute
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = 2 * time.Minute
	}
	if o.StaleAfter == 0 {
		o.StaleAfter = 6 * time.Minute
	}
	if o.Idle == 0 {
		o.Idle = 10 * time.Minute
	}
	if o.SessionGrace == 0 {
		o.SessionGrace = time.Minute
	}
	if o.StoppedInbox == 0 {
		o.StoppedInbox = 24 * time.Hour
	}
	if o.NameSource == nil {
		o.NameSource = rand.Reader
	}
	if o.NameRetry == 0 {
		o.NameRetry = 15 * time.Second
	}
	if o.NamePool == 0 {
		o.NamePool = 10
	}
	if o.PushRetry == 0 {
		o.PushRetry = 2 * time.Second
	}
	if o.PageAddr == "" {
		o.PageAddr = "127.0.0.1:0"
	}
	if o.PageOpen == 0 {
		o.PageOpen = 2 * time.Minute
	}
	if o.PageMessages == 0 {
		o.PageMessages = 500
	}
	if o.TitleEvery == 0 {
		o.TitleEvery = 5 * time.Second
	}
	if o.ConsentPoll == 0 {
		o.ConsentPoll = time.Second
	}
	if o.Log == nil {
		o.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
}

// ErrRunning means another daemon already holds this clone's lock.
var ErrRunning = errors.New("a daemon is already running for this clone")

// ErrNoConsent means the user hasn't joined Murmur in this clone, or has left since.
var ErrNoConsent = errors.New("this clone hasn't joined " + brand.Product)

// push publishes to the bus, if the user still has this clone joined. The checks at start and on
// each request leave a window for a leave to land in, and the loops that publish on their own (the
// device's presence, the names registry) would put the device on the remote in it: the watcher
// stops the daemon only a second later, and a daemon that was still starting has no daemon.json
// for Stop to find. goodbye is not a publication in that sense and doesn't come through here: it
// takes the device off the remote.
//
// A device pushes one ref at a time. Its presence, its log and the names registry used to be pushed
// each from its own goroutine, and a session starting or stopping set off two or three pushes in
// the same second; GitHub then refused one of them now and then with "[remote rejected] (failed)",
// which is not a lease refusal and cost a retry (see "Git is the transport" in docs/VISION.md).
func (d *Daemon) push(ctx context.Context, sha, ref string, mode bus.PushMode, expect string) error {
	if !d.takePush(ctx) {
		return ctx.Err()
	}
	defer d.donePush()
	// After the wait, which can be a push or two long: a leave may have landed meanwhile.
	if !d.consented() {
		return ErrNoConsent
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	return d.repo.Push(ctx, sha, ref, mode, expect)
}

// pushTimeout bounds one push, so a push that hangs doesn't hold up the device's others for good.
const pushTimeout = time.Minute

// takePush waits for this device's turn to push, and reports whether it got it before ctx ended.
// donePush gives the turn back.
func (d *Daemon) takePush(ctx context.Context) bool {
	select {
	case d.pushOne <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (d *Daemon) donePush() { <-d.pushOne }

// consented reports whether the user's consent to run in this clone stands.
func (d *Daemon) consented() bool { return d.opt.Consent == nil || d.opt.Consent() }

// maxInbox caps each session's unread messages; the oldest go first.
const maxInbox = 200

// Daemon is one running daemon. Lock order: syncMu, then advMu, then mu; or writeMu, then advMu,
// then mu.
type Daemon struct {
	opt    Options
	repo   *bus.Repo
	dir    string
	device string
	key    ed25519.PrivateKey // seals what this device writes to the bus
	boxKey *ecdh.PrivateKey   // opens what is encrypted for this device
	// configSHA is the config ref as last read (see readConfig). Caller holds mu.
	configSHA string
	host      string
	owner     string
	token     string
	started   time.Time
	log       *log.Logger

	mu       sync.Mutex
	sessions map[string]*session
	peers    map[string]*peer
	// unsealed is the devices whose last record read carried no seal, from a daemon that predates
	// seals; forged is when a record claiming a device last failed its seal (see sealOKLocked).
	unsealed map[string]bool
	forged   map[string]time.Time
	// presAt is the time, by its writer's clock, of each device's last sealed presence taken; a
	// sealed presence no newer is a replay (see updatePeer). Kept past a device's goodbye.
	presAt     map[string]time.Time
	known      map[string]string // local copy of each bus ref: ref → SHA last fetched
	st         state
	recent     []proto.EventView
	lastActive time.Time
	mode       syncMode
	pending    []bus.Event     // what arrived while down, for sessions that reconnect
	caughtUp   map[string]bool // sessions already given pending
	// backlog lists the mesh refs the first listing showed that haven't been read yet: they are
	// read under the mode the daemon started in, and the mode turns normal once all have been.
	// nil until that listing.
	backlog    map[string]bool
	backlogAt  time.Time            // when the backlog was taken
	unfetched  map[string]fetchFail // refs whose fetch failed, and when
	junkTried  map[string]time.Time // blocking refs clearOwnJunk last tried to delete, and when
	lastPollOK time.Time            // when the last successful sync listed the remote (monotonic)
	// convAt is when a conversation last moved, for how fast to poll (see pollEvery); zero if none
	// has since the daemon started.
	convAt time.Time
	// namesChecked says the registry has been looked at since this daemon started, even if its
	// tip is the one state.json recorded.
	namesChecked bool
	// lastPage is when a page someone can see last called its API, zero if none has. msgs is the
	// page's messages, oldest first, msgSeq the number of the newest and msgIDs the IDs in msgs.
	lastPage time.Time
	msgs     []pageMsg
	msgSeq   uint64
	msgIDs   map[string]bool
	// brackets holds the tool calls in flight whose start was snapshotted, by Claude session and
	// tool-use ID (see toolStart). snapLogged is when a failed snapshot was last logged.
	brackets   map[string]*bracket
	snapLogged time.Time
	// ending counts the calls whose end is being judged in the background (see toolEnd).
	ending int
	// unsnapped holds a channel for each ended call whose worktree hasn't been snapshotted again
	// yet, closed when it has; the next call's start waits for them (see toolStart).
	unsnapped []chan struct{}
	// pushing is the sessions a delivery goroutine is working for, and pushWarned those whose last
	// delivery failed or that have no way to be delivered to, so that is logged once, not for every
	// message (see deliver.go).
	pushing    map[string]bool
	pushWarned map[string]bool

	polls atomic.Int64 // the remote listings tried, so a test can see the rate

	syncMu  sync.Mutex // serializes syncs, so an older listing is never applied after a newer one
	advMu   sync.Mutex // serializes processing of fetched refs
	writeMu sync.Mutex // serializes appends to this device's log
	// cutMu keeps a write in step with the switch to encryption (#156): an event or presence is
	// encoded and pushed under its read lock, and the switch takes the write lock, so nothing
	// encoded in the clear is pushed once the switch is made. Taken before mu, never under advMu
	// by a writer.
	cutMu  sync.RWMutex
	ownTip string // the remote's tip of this device's log, as last known; under writeMu

	// setMu guards settings and settings.json. It is taken alone, never with another lock held.
	setMu    sync.Mutex
	settings pageSettings

	kick     chan struct{}
	pollWake chan struct{} // wakes pollLoop when a session or a page comes, so it looks at once
	pushKick chan struct{} // wakes pushLoop, which delivers messages into sessions as turns
	pushSem  chan struct{} // bounds the deliveries running at once
	nameKick chan struct{} // wakes nameLoop, the only user of nameSrc
	nameSrc  io.Reader
	pushOne  chan struct{} // held while this device pushes to the remote (see push)
	quit     chan struct{}
	quitOnce sync.Once

	// bg tracks connection handlers and background writes, so Run returns only after they
	// have; bgCtx is cancelled when the daemon starts shutting down.
	bg    sync.WaitGroup
	bgCtx context.Context
}

type session struct {
	id, agent, cwd, branch, doing, claudeSession string
	started                                      time.Time
	reconnect                                    bool // registered before, with a daemon that went away
	// worktree is the name of the folder the session's worktree is in, found when it registered.
	worktree string
	// since is, for a session back within StoppedInbox, when it stopped: what was sent after that
	// is its, however late this daemon reads it. Zero otherwise.
	since time.Time
	// dropped is when the session's connection dropped, zero while it is connected. Until the
	// grace runs out the session stays registered, and a hello with its ID takes it over; that
	// closes takeover, which the connection waiting out the grace is watching.
	dropped  time.Time
	takeover chan struct{}
	// key is what the session's name follows (see nameKey); name is "" until it lands.
	key, name string
	// clientPID is the MCP server's parent process, 0 if its hello gave none. A hook run by the
	// same process speaks for this session (see hookSessionsLocked).
	clientPID int
	// lastActive is when the session last made a request, or a hook of its did: its last tool call.
	// It is kept in memory and goes out with the next presence write, never as a write of its own.
	// Zero until its first.
	lastActive time.Time
	// route is how a message gets into the session as a turn (see deliver.go). It is local, like
	// claudeSession: the token in it opens the session's messaging socket, so it is never logged
	// or put on the page or the bus.
	route proto.Route
	// transcript is where Claude Code keeps the session's conversation, as its hook reported it;
	// it holds the title. title is the conversation's title as last read from its client (see
	// titles.go), "" if it has none. It is shown where doing, the status the agent set, is empty.
	transcript, title string
	// titleWarned says reading the title has been logged as failing, so that is logged once.
	titleWarned bool
}

// shownDoing is the status line other sessions see for s: what the agent set, else its
// conversation's title. Caller holds mu.
func (s *session) shownDoing() string {
	if s.doing != "" {
		return s.doing
	}
	return s.title
}

// syncMode says what a sync does with events this daemon hasn't processed.
type syncMode int

const (
	// modeSkip records every ref's tip without delivering anything: the first sync of a new
	// clone, or after a long gap, when no session is still waiting for what it missed.
	modeSkip syncMode = iota
	// modeCatchUp delivers what arrived while the daemon was down, but only to sessions that
	// were running before it went down and reconnected: the first sync after a restart.
	modeCatchUp
	modeNormal
)

// catchUpLimit is the longest outage after which a restarted daemon still delivers what it
// missed; past it, the sessions that were waiting have moved on. catchUpWindow is how long after
// starting it holds what it missed for sessions still reconnecting.
const (
	catchUpLimit  = 10 * time.Minute
	catchUpWindow = 30 * time.Second
)

// peer is another device as its presence ref shows it.
type peer struct {
	pres bus.Presence
	sha  string // the presence last read, or found unreadable
	// listed is the presence SHA last seen in a listing, read or not. lastChange is when this
	// daemon saw it change, on this machine's clock, so that staleness never compares two
	// machines' clocks.
	listed     string
	lastChange time.Time
}

// fetchFail is a ref whose fetch failed: at which listed SHA, and when. It is fetched again on
// its own a minute after at; a zero at means at the next poll.
type fetchFail struct {
	sha string
	at  time.Time
}

type inMsg struct {
	Event   bus.Event `json:"event"`
	Arrived time.Time `json:"arrived"`
	Noticed bool      `json:"noticed"`
	// Pushing says the message is being delivered into its session as a turn, so the hook's notice
	// and inbox leave it alone: it either arrives as the turn, and is gone from the inbox, or the
	// delivery fails and it is theirs again. PushFailed says that delivery failed. Neither is kept
	// over a restart.
	Pushing    bool `json:"-"`
	PushFailed bool `json:"-"`
	// Attempted says the message was handed to T3 or the socket, and it may be in the session's
	// conversation, though the daemon doesn't know (see deliver.go). It is saved before the handoff
	// and taken off only when the attempt provably went nowhere, so a restart in the middle of a
	// delivery, or after one that failed, finds it, and the message is left to the hook's notice
	// instead of being delivered a second time.
	Attempted bool `json:"attempted,omitempty"`
}

// state is what survives a restart, in state.json.
type state struct {
	// Initialized is set once the first syncs have read the backlog, recording every log's tip
	// as processed without delivering it: history from before this device joined isn't replayed.
	Initialized bool              `json:"initialized"`
	Processed   map[string]string `json:"processed"` // mesh ref → last SHA delivered
	// Junked holds the logs that were junk when this device first looked, with nothing read from
	// them yet, and when that look was: what was sent before it is history, not to be delivered.
	Junked map[string]time.Time `json:"junked,omitempty"`
	Inbox  map[string][]*inMsg  `json:"inbox"` // session → unread messages
	// Stopped holds the sessions that stopped, and when. Their inboxes are kept for StoppedInbox,
	// in case the session comes back; they are the only inboxes kept for sessions not running.
	Stopped map[string]time.Time `json:"stopped,omitempty"`
	// LastSync tells the next daemon how long this one was down.
	LastSync time.Time `json:"lastSync"`

	// Used is every session name this daemon has ever seen, in the registry's history or in
	// another device's presence, with the session's address where known. It only grows.
	Used map[string]string `json:"usedNames,omitempty"`
	// NameKeys is the name each of this device's session keys was given, for good.
	NameKeys map[string]string `json:"nameKeys,omitempty"`
	// NameHolders is the session on this device that took each name last, and SessionNames the
	// name each session took last: running or stopped, and never taken back. The registry records
	// the session a name was first given to, which can have a different ID from the one that
	// holds the name now; these two are how a message for the first is found its holder
	// (see forwardLocked).
	NameHolders  map[string]string `json:"nameHolders,omitempty"`
	SessionNames map[string]string `json:"sessionNames,omitempty"`
	// NamesTip is the registry's tip as last examined, readable or not: the lease for the next
	// push. NamesGood is the last tip that was a commit and was read in full. NamesRepair says the
	// registry lost names, and the next commit must be a new root holding every one.
	NamesTip    string `json:"namesTip,omitempty"`
	NamesGood   string `json:"namesGood,omitempty"`
	NamesRepair bool   `json:"namesRepair,omitempty"`
	// NamePool is the names the registry has set aside for this device that no session has taken
	// yet, oldest first. NameBinds is each name a session took from the pool whose assignment to it
	// isn't in the registry yet, with the session's ID. Reserved is each name the registry has set
	// aside for a device, this one or another, that no session is known to have, with the device.
	NamePool  []string          `json:"namePool,omitempty"`
	NameBinds map[string]string `json:"nameBinds,omitempty"`
	Reserved  map[string]string `json:"reservedNames,omitempty"`

	// Commits is the commits credited to each session on this device (see credit), oldest first, by
	// the session's ID. They are kept as long as the session's inbox is, and never leave the device.
	Commits map[string][]commitRec `json:"commits,omitempty"`

	// T3Routes is the T3 thread each session on this device is, with T3's command line, for the
	// sessions that are in T3: a thread outlives its MCP server, which T3 stops when the thread has
	// been idle for 30 minutes, and a message is still delivered to the thread, which wakes it.
	// Local only, like NameKeys. It is kept as long as the session's inbox is.
	T3Routes map[string]t3Route `json:"t3Routes,omitempty"`

	// Keys is each device's public key as first seen sealing a record, this device's own included:
	// from then on only records sealed with that key are taken as the device's (see sealOKLocked).
	Keys map[string]string `json:"keys,omitempty"`
	// BoxKeys is each device's box key, from its last sealed presence, that records are encrypted
	// to it with (#156). Encrypted says this repo's bus is encrypted, which a daemon learns from
	// the config ref and never unlearns: whoever can push could delete that ref.
	BoxKeys   map[string]string `json:"boxKeys,omitempty"`
	Encrypted bool              `json:"encrypted,omitempty"`
}

// t3Route is where a message for a session in T3 goes: T3's command line, its data directory if
// the session had one set, and the thread.
type t3Route struct {
	CLI    string `json:"cli"`
	Home   string `json:"home,omitempty"`
	Thread string `json:"thread"`
}

// Run runs a daemon until ctx is done, it is told to stop, or it has been idle too long.
func Run(ctx context.Context, opt Options) error {
	opt.defaults()
	dir := filepath.Join(opt.GitDir, brand.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := proc.Lock(filepath.Join(dir, "daemon.lock"))
	if errors.Is(err, proc.ErrLocked) {
		return ErrRunning
	}
	if err != nil {
		return err
	}
	defer lock.Close()

	d, err := open(ctx, opt, dir)
	if err != nil {
		return err
	}
	if !d.consented() {
		return ErrNoConsent
	}
	// Sync once before accepting sessions, so whatever this sync finds was sent before any of
	// them registered: it is the backlog, and nothing a new session should get.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	if err := d.pollOnce(sctx); err != nil {
		d.log.Printf("first sync failed; retrying in the background: %v", err)
	}
	scancel()
	// The sync can take seconds, long enough for a leave to have come and gone without finding a
	// daemon.json to stop.
	if !d.consented() {
		return ErrNoConsent
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	// A page that can't be served costs the page, not the daemon: sessions don't need it.
	pageLn, err := d.listenPage()
	if err != nil {
		d.log.Printf("the page for humans isn't served: %v", err)
	}
	if pageLn != nil {
		defer pageLn.Close()
	}
	infoPath := filepath.Join(dir, "daemon.json")
	info := proto.Info{Addr: ln.Addr().String(), Token: d.token, PID: os.Getpid(), Device: d.device}
	if pageLn != nil {
		info.Web = pageLn.Addr().String()
	}
	if err := writeJSON(infoPath, info); err != nil {
		return err
	}
	defer os.Remove(infoPath)
	d.mu.Lock()
	d.lastActive = time.Now() // the first sync may have taken a while; the idle time starts here
	d.mu.Unlock()
	d.log.Printf("device %s, remote %q, listening on %s", d.device, d.repo.Remote, info.Addr)
	if info.Web != "" {
		d.log.Printf("page for humans on %s", info.Web)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.bgCtx = ctx
	go d.serve(ctx, ln)
	if pageLn != nil {
		d.servePage(ctx, pageLn)
	}
	// Tracked, as the syncs it runs start background work of their own (clearOwnJunk).
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.pollLoop(ctx)
	}()
	// The presence loop is drained like other background work: a push still in flight when
	// the goodbye deletes presence could otherwise land after it and bring the device back.
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.presenceLoop(ctx)
	}()
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.nameLoop(ctx)
	}()
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.pushLoop(ctx)
	}()
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.titleLoop(ctx)
	}()
	if opt.Idle > 0 {
		go d.idleLoop(ctx)
	}
	if opt.Consent != nil {
		go d.consentLoop(ctx)
	}
	if opt.CheckIn != nil {
		go opt.CheckIn(ctx)
	}
	select {
	case <-ctx.Done():
	case <-d.quit:
	}
	cancel()
	ln.Close() // no new connections, so nothing joins bg while it drains
	drained := make(chan struct{})
	go func() { d.bg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(proto.DrainTimeout):
		d.log.Printf("background work still running at exit")
	}
	d.goodbye()
	return nil
}

func open(ctx context.Context, opt Options, dir string) (*Daemon, error) {
	now := time.Now()
	d := &Daemon{
		opt: opt, dir: dir, log: opt.Log, token: bus.RandHex(32), started: now, lastActive: now,
		sessions: map[string]*session{}, peers: map[string]*peer{}, known: map[string]string{},
		unsealed: map[string]bool{}, forged: map[string]time.Time{}, presAt: map[string]time.Time{},
		caughtUp: map[string]bool{}, unfetched: map[string]fetchFail{}, junkTried: map[string]time.Time{},
		kick: make(chan struct{}, 1), pollWake: make(chan struct{}, 1), nameKick: make(chan struct{}, 1), nameSrc: opt.NameSource,
		pushKick: make(chan struct{}, 1), pushSem: make(chan struct{}, maxPushes), pushOne: make(chan struct{}, 1),
		pushing: map[string]bool{}, pushWarned: map[string]bool{},
		quit: make(chan struct{}), msgIDs: map[string]bool{}, brackets: map[string]*bracket{},
	}
	device, key, err := loadIdentity(dir, opt.Log)
	if err != nil {
		return nil, err
	}
	d.device, d.key = device, key
	if d.boxKey, err = loadBoxKey(dir); err != nil {
		return nil, err
	}
	d.host, _ = os.Hostname()
	d.repo = &bus.Repo{Git: gitx.Git{Dir: opt.GitDir}, Name: device, Email: device + "@" + brand.Name + ".invalid", Key: key}
	if d.repo.Remote, err = bus.RemoteFor(ctx, d.repo.Git); err != nil {
		return nil, err
	}
	d.owner = d.repo.Config(ctx, "user.name")
	if err := d.loadState(); err != nil {
		return nil, err
	}
	if d.st.Keys[d.device] != bus.PublicKey(d.key) {
		// Saved at once: a key lost before this is saved would leave the device under its old ID
		// with a new key, which its peers refuse (see loadIdentity).
		d.st.Keys[d.device] = bus.PublicKey(d.key)
		d.saveLocked()
	}
	d.st.BoxKeys[d.device] = bus.BoxPublic(d.boxKey)
	d.loadSettings()
	wall := now.Round(0)
	if !d.st.Initialized || wall.Sub(d.st.LastSync) > catchUpLimit {
		d.mode = modeSkip
	} else {
		d.mode = modeCatchUp
	}
	clearStaleLocks(opt.GitDir, d.log)
	if err := d.restore(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// restore rebuilds what the daemon knows from the local copies of bus refs, so it can answer
// before its first sync completes, or while the network is down.
func (d *Daemon) restore(ctx context.Context) error {
	local, err := d.repo.LocalRefs(ctx)
	if err != nil {
		return err
	}
	d.advMu.Lock()
	defer d.advMu.Unlock()
	for ref, sha := range local {
		d.known[ref] = sha
	}
	d.ownTip = local[d.meshRef()]
	if d.ownTip != "" {
		// Junk pushed over this device's log: build on the last commit read from it instead. On a
		// failure to tell, the same: a tip that is behind gets its push refused, and the next
		// try builds on the remote's.
		if ok, _ := d.repo.IsCommit(ctx, d.ownTip); !ok {
			d.ownTip = d.st.Processed[d.meshRef()]
			if ok, _ := d.repo.IsCommit(ctx, d.ownTip); d.ownTip != "" && !ok {
				d.ownTip = ""
			}
		}
	}
	for ref, sha := range local {
		if strings.HasPrefix(ref, brand.PresencePrefix) {
			d.updatePeer(ctx, ref, sha)
		}
	}
	return nil
}

func (d *Daemon) meshRef() string     { return brand.MeshPrefix + d.device }
func (d *Daemon) presenceRef() string { return brand.PresencePrefix + d.device }

// stop asks Run to return.
func (d *Daemon) stop() { d.quitOnce.Do(func() { close(d.quit) }) }

// goodbye deletes this device's presence, so other devices drop it at their next sync instead of
// finding it stale six minutes later.
func (d *Daemon) goodbye() {
	ctx, cancel := context.WithTimeout(context.Background(), proto.GoodbyeTimeout)
	defer cancel()
	// In turn with the device's other pushes, as push does, but not through it: the goodbye takes
	// the device off the remote, which a leave wants too. A push still holding the turn, from work
	// that didn't drain in time, gets half the goodbye's time to finish; then it goes anyway.
	wait, stopWait := context.WithTimeout(ctx, proto.GoodbyeTimeout/2)
	if d.takePush(wait) {
		defer d.donePush()
	}
	stopWait()
	if err := d.repo.Push(ctx, "", d.presenceRef(), bus.FastForward, ""); err != nil {
		d.log.Printf("removing presence: %v", err)
	}
	d.log.Printf("stopped")
}

// clearStaleLocks removes .lock files under the bus refs. A git killed mid-write leaves one,
// and every later fetch of that ref fails until it is gone. The daemon holds the clone's lock
// and is the only writer of these refs, so one older than a few seconds is stale.
func clearStaleLocks(gitDir string, l *log.Logger) (removed bool) {
	root := filepath.Join(gitDir, filepath.FromSlash(brand.RefRoot))
	filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".lock") {
			return nil
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 30*time.Second {
			if os.Remove(path) == nil {
				l.Printf("removed stale lock %s", path)
				removed = true
			}
		}
		return nil
	})
	return removed
}

// consentLoop stops the daemon once the user's consent is gone.
func (d *Daemon) consentLoop(ctx context.Context) {
	t := time.NewTicker(d.opt.ConsentPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !d.consented() {
			d.log.Printf("this clone is no longer joined; exiting")
			d.stop()
			return
		}
	}
}

func (d *Daemon) idleLoop(ctx context.Context) {
	t := time.NewTicker(min(30*time.Second, d.opt.Idle/4+time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		wake := d.wakeSnapshot() // before mu: setMu is taken alone
		d.mu.Lock()
		// An open page keeps the daemon up as a session does: the idle time counts from the last
		// session going, or from the page no longer counting as open.
		quiet := d.lastActive
		if gone := d.lastPage.Add(d.opt.PageOpen); gone.After(quiet) {
			quiet = gone
		}
		idle := len(d.sessions) == 0 && !d.holdsRouteLocked(wake) && time.Since(quiet) > d.opt.Idle
		d.mu.Unlock()
		if idle {
			d.log.Printf("no sessions, no page open and no T3 thread to wake for %s; exiting", d.opt.Idle)
			d.stop()
			return
		}
	}
}

// holdsRouteLocked says the daemon has a T3 thread it could still wake with a message: the session
// of a thread stopped within StoppedInbox, whose route is kept (T3Routes) so that a message for it
// still reaches the thread though its MCP server is gone. T3 stops an idle thread's MCP server
// after about 30 minutes, and the thread is the one most worth waking (#87). The daemon stays up
// for it, as no other daemon reads the bus for this clone, and it ends when the route's inbox is
// no longer kept. A thread the page's wake setting says not to wake has nothing to stay up for.
// Caller holds mu.
func (d *Daemon) holdsRouteLocked(wake pageSettings) bool {
	for id := range d.st.T3Routes {
		if d.sessions[id] == nil && d.receivesLocked(id) && wake.wakes(d.sessionNameLocked(id)) {
			return true
		}
	}
	return false
}

// loadDevice reads this clone's device ID, creating it on first run: the hostname plus four
// random hex digits, so two clones on one machine are two devices.
func loadDevice(dir string) (string, error) {
	path := filepath.Join(dir, "device")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); bus.ValidDevice(id) {
			return id, nil
		}
	}
	host, _ := os.Hostname()
	id := slug(host) + "-" + bus.RandHex(4)
	return id, os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// slug makes a hostname safe as a ref component and short enough to read.
func slug(host string) string {
	host, _, _ = strings.Cut(strings.ToLower(host), ".")
	var b strings.Builder
	for _, r := range host {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 24 {
		s = strings.Trim(s[:24], "-")
	}
	if s == "" {
		s = "device"
	}
	return s
}

func validName(s string) bool { return bus.ValidName(s) }

func (d *Daemon) statePath() string { return filepath.Join(d.dir, "state.json") }

func (d *Daemon) loadState() error {
	d.st = state{Processed: map[string]string{}, Inbox: map[string][]*inMsg{}, Junked: map[string]time.Time{},
		Stopped: map[string]time.Time{}, Used: map[string]string{}, NameKeys: map[string]string{},
		NameHolders: map[string]string{}, SessionNames: map[string]string{}, Commits: map[string][]commitRec{},
		T3Routes: map[string]t3Route{}, NameBinds: map[string]string{}, Reserved: map[string]string{},
		Keys: map[string]string{}, BoxKeys: map[string]string{}}
	b, err := os.ReadFile(d.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &d.st); err != nil {
		d.log.Printf("state.json unreadable, starting fresh: %v", err)
		d.st = state{}
	}
	if d.st.Processed == nil {
		d.st.Processed = map[string]string{}
	}
	if d.st.Junked == nil {
		d.st.Junked = map[string]time.Time{}
	}
	if d.st.Inbox == nil {
		d.st.Inbox = map[string][]*inMsg{}
	}
	if d.st.Stopped == nil {
		d.st.Stopped = map[string]time.Time{}
	}
	if d.st.Used == nil {
		d.st.Used = map[string]string{}
	}
	if d.st.NameKeys == nil {
		d.st.NameKeys = map[string]string{}
	}
	if d.st.NameHolders == nil {
		d.st.NameHolders = map[string]string{}
	}
	if d.st.SessionNames == nil {
		d.st.SessionNames = map[string]string{}
	}
	if d.st.Commits == nil {
		d.st.Commits = map[string][]commitRec{}
	}
	if d.st.T3Routes == nil {
		d.st.T3Routes = map[string]t3Route{}
	}
	if d.st.NameBinds == nil {
		d.st.NameBinds = map[string]string{}
	}
	if d.st.Reserved == nil {
		d.st.Reserved = map[string]string{}
	}
	if d.st.Keys == nil {
		d.st.Keys = map[string]string{}
	}
	if d.st.BoxKeys == nil {
		d.st.BoxKeys = map[string]string{}
	}
	// V1's daemons also put notes about claims in inboxes, as from Murmur itself, which no address
	// on the bus can be. This one writes none, and would show one as a teammate's message.
	for sess, box := range d.st.Inbox {
		if box = slices.DeleteFunc(box, func(m *inMsg) bool { return m.Event.From == brand.Name }); len(box) == 0 {
			delete(d.st.Inbox, sess)
		} else {
			d.st.Inbox[sess] = box
		}
	}
	return nil
}

// saveLocked writes state.json. Caller holds mu.
func (d *Daemon) saveLocked() {
	if err := writeJSON(d.statePath(), d.st); err != nil {
		d.log.Printf("saving state: %v", err)
	}
}

// writeJSON writes a file atomically, so a crash never leaves half of one.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// noteLocked adds a line to the recent-events log that `log` shows. Caller holds mu.
func (d *Daemon) noteLocked(at time.Time, line string) {
	d.recent = append(d.recent, proto.EventView{At: at, Line: line})
	if len(d.recent) > 500 {
		d.recent = d.recent[len(d.recent)-500:]
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
