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
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/title"
	"github.com/a-Fig/murmur/internal/web"
)

// Options configure a daemon. Zero values take the defaults in docs/VISION.md.
type Options struct {
	GitDir string        // the clone's git common dir, absolute
	Poll   time.Duration // how often to check the remote; 3 s
	// QuietPoll is how often to check it while no session is live and no page is open: 30 s. A
	// daemon that is only waiting to wake a T3 thread (see Idle) needn't look as often as one with
	// an agent working; it is never faster than Poll.
	QuietPoll  time.Duration
	Heartbeat  time.Duration // how often to re-push presence when nothing changed, give or take a fifth; 2 min
	StaleAfter time.Duration // presence unchanged this long means the device is gone; 6 min
	// FetchLimit is how long one fetch may take within a sync: 15 s. A ref that runs past it on its
	// own is set aside; SlowFetchLimit is how long its fetch may then take, outside the syncs: 10 min.
	FetchLimit, SlowFetchLimit time.Duration
	// ReadFirstLimit is how long a sync may take over the names registry and every presence, which
	// it reads before the logs: 15 s, and never more than half of what is left of the sync.
	// A registry read that stalls (the check of one missing from the listing asks the remote again)
	// then can't use up the time the logs it fetched need.
	ReadFirstLimit time.Duration
	// FirstSyncLimit is how long the daemon's first sync may take before the background loops start
	// and a later sync tries again: 10 s. The daemon answers before it, but a registration and the
	// requests that want what it reads (who, send, lookup) wait for it, as long as this at most (#372).
	FirstSyncLimit time.Duration
	// PollCap is the slowest the poll gets while a session is live and nothing is happening: 15 s.
	// FastFor is how long after a conversation moves (this device sends a message, or queues one
	// for a session here or hands one over to it, or a session starts) the poll stays at Poll: 3 min (#93).
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
	// MuteGrace is how long after a session mutes another it still gets that session's direct
	// messages (see mutedHereLocked): 30 s. Negative: none.
	MuteGrace time.Duration
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
	// PushRetry is how long a handoff of a message as a turn that never got started (no `t3`
	// to run, no socket to connect to) waits before its one retry: 2 s. Any other failure may have
	// handed the message over, so it is not repeated, and a second failure of this kind too leaves
	// the message to the hook's notice.
	PushRetry time.Duration
	// ContinueHold is how long a Stop's continuation of a turn counts as at work outside T3 before
	// another hook of the session shows the turn went on: 2 min (see continueTurnLocked).
	ContinueHold time.Duration
	// PageAddr is where the page for humans is served: 127.0.0.1:0, a random port on loopback.
	// A host other than 127.0.0.1 is refused: off loopback anyone on the network could reach the
	// page, and on another loopback address the page's Host check would refuse every request.
	PageAddr string
	// PageListener is the page's listening socket, inherited from a daemon that restarted onto an
	// update (#255). Run takes it, and closes it if it doesn't serve the page on it.
	PageListener *os.File
	// PageOpen is how recently a page someone can see must have called its API to count as open,
	// which keeps the daemon running like a session does: 2 min. A hidden tab's calls don't count.
	PageOpen time.Duration
	// PageMessages is how many messages the page can look back on: 500. A daemon that starts reads
	// that many back from the logs it has (history.go).
	PageMessages int
	// SnapshotTimeout bounds the git a snapshot of a tool call's worktree runs (#21): 4 s. Tests on a
	// loaded machine raise it.
	SnapshotTimeout time.Duration
	// TitleEvery is how often the titles of the sessions' conversations are read from their
	// clients' state, and the least time between two reads of one: 5 s (#66).
	TitleEvery time.Duration
	// ThreadState reads a T3 thread's state from T3's database, for whether it is settled and how
	// long it has been idle (see threads.go): t3db.StateOf. Tests put a stand-in here. It is read
	// every TitleEvery.
	ThreadState func(ctx context.Context, cli, home, thread string) (t3db.State, error)
	// CodexLineage reads who started a Codex thread from Codex's state (title.CodexLineage), for
	// the sessions another session started (codexkids.go). Tests put a stand-in here.
	CodexLineage func(ctx context.Context, dir, thread string) (title.Lineage, error)
	// Consent says whether the user has joined Murmur in this clone (nil: always). A daemon exists
	// only for a user who did: it won't start without it, refuses requests once it is gone, and
	// stops within ConsentPoll of it going, so a leave that races a daemon still starting ends that
	// daemon too.
	Consent     func() bool
	ConsentPoll time.Duration // how often to look; 1 s
	Log         *log.Logger
	// Report, if set, runs in the background for as long as the daemon serves: it tells
	// murmur.afig.dev the repo's address if the repo is public and nobody has yet
	// (internal/publicrepo). Tests leave it unset.
	Report func(context.Context)
	// Update installs releases, then requests a safe restart.
	Update func(context.Context, func())
	// Version, if set, is this binary's version and what is known of a newer release, for the
	// page (#240). The daemon adds whether an installed update is waiting. The page calls for
	// it every few seconds, so it must not reach the network.
	Version func() web.Version
	// Clones, if set, is the user's record of this machine's joined clones (internal/clones): the
	// daemon adds its clone there as it starts, so a clone that joined before the record existed
	// is on it too. Tests leave it unset.
	Clones string
	// Repos, if set, lets the page list this machine's clones and switch to another's page (#348).
	Repos web.Repos
}

const defaultPageAddr = "127.0.0.1:0"

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
	if o.FetchLimit == 0 {
		o.FetchLimit = 15 * time.Second
	}
	if o.SlowFetchLimit == 0 {
		o.SlowFetchLimit = 10 * time.Minute
	}
	if o.ReadFirstLimit == 0 {
		o.ReadFirstLimit = 15 * time.Second
	}
	if o.FirstSyncLimit == 0 {
		o.FirstSyncLimit = 10 * time.Second
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
	if o.MuteGrace == 0 {
		o.MuteGrace = 30 * time.Second
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
	if o.ContinueHold == 0 {
		o.ContinueHold = 2 * time.Minute
	}
	if o.PageAddr == "" {
		o.PageAddr = defaultPageAddr
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
	if o.ThreadState == nil {
		o.ThreadState = t3db.StateOf
	}
	if o.CodexLineage == nil {
		o.CodexLineage = title.CodexLineage
	}
	if o.ConsentPoll == 0 {
		o.ConsentPoll = time.Second
	}
	if o.Log == nil {
		o.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
}

// ErrUpdated means a verified binary is installed and Run released the clone lock; the caller can restart it.
var ErrUpdated = errors.New("daemon binary updated")

// Updated is the ErrUpdated Run returns when it served the page: Page holds the page's listening
// socket open, for the new daemon to inherit, and the caller closes it once that has started. The
// port stays bound from one daemon to the next, so nothing else can take it in between and be sent
// what an open tab sends there (#255).
type Updated struct{ Page *os.File }

func (*Updated) Error() string { return ErrUpdated.Error() }
func (*Updated) Unwrap() error { return ErrUpdated }

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
// Pushes from other devices still meet this device's on GitHub; bus.Push sends a push refused that
// way again, within this device's turn.
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
	err := d.repo.Push(ctx, sha, ref, mode, expect)
	d.health.pushed(ctx, ref, err)
	return err
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
	opt     Options
	repo    *bus.Repo
	dir     string
	device  string
	signKey ed25519.PrivateKey // seals this device's presence (keys.go)
	boxKey  *ecdh.PrivateKey   // opens what is encrypted for this device
	host    string
	owner   string
	token   string // the sessions' secret, in daemon.json
	pageTok string // the page's secret (#42): the address carries it, so it opens the page, not the sessions' port
	// repoName is the repository's name from its remote's address, which the page's repo picker
	// shows.
	repoName string
	// page is the page's server, nil until it is made and when the page isn't served. OpPageKey
	// asks it for a key.
	page    atomic.Pointer[web.Server]
	started time.Time
	log     *log.Logger

	// maxRecord and maxSnapshot are the largest commits read from a device's log or presence, and
	// from the names registry (bus.MaxRecord, bus.MaxSnapshot; tests lower them).
	maxRecord, maxSnapshot int64

	// savesHeld puts off saveLocked while a page of a log is applied, under mu throughout, so the
	// page saves state.json once and not once for each tags event in it (see applyPageLocked).
	savesHeld bool

	mu         sync.Mutex
	sessions   map[string]*session
	peers      map[string]*peer
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
	backlog   map[string]bool
	backlogAt time.Time            // when the backlog was taken
	unfetched map[string]fetchFail // refs whose fetch failed, and when
	// slowRef is the ref being fetched outside the syncs, "" if none, and slowCancel stops that fetch.
	slowRef    string
	slowCancel context.CancelFunc
	junkTried  map[string]time.Time // blocking refs clearOwnJunk last tried to delete, and when
	lastPollOK time.Time            // when the last successful sync listed the remote (monotonic)
	// convAt is when a conversation last moved, for how fast to poll (see pollEvery); zero if none
	// has since the daemon started.
	convAt time.Time
	// namesChecked says the registry has been looked at since this daemon started, even if its
	// tip is the one state.json recorded.
	namesChecked bool
	// presenceCut is how many sessions the last presence left out to keep to bus.MaxSessions (see
	// capSessions), so that is logged when it changes, not on every heartbeat.
	presenceCut int
	// presenceFirst, when not nil, is closed once a presence listing the sessions started since it
	// was made has been pushed or has failed. A session's start on the log and its name's
	// assignment wait for it, so the push that shows the session goes first (see awaitPresence).
	presenceFirst chan struct{}
	// presenceSending is presenceFirst once a presence has taken it, until that presence has been
	// pushed or has failed.
	presenceSending chan struct{}
	// personClashes is the people's names a second device's presence has said since this daemon
	// started, logged once each (see notePersonLocked).
	personClashes map[string]bool
	// lastPage is when a page someone can see last called its API, zero if none has. msgs is the
	// page's messages, oldest first, msgSeq the number of the newest and msgIDs the IDs in msgs.
	lastPage time.Time
	msgs     []pageMsg
	msgSeq   uint64
	msgIDs   map[string]bool
	// ownMsgs is how many of msgs are to or from this device's person, which PageMessages doesn't
	// count (see keepMessageLocked).
	ownMsgs int
	// historyKick wakes historyLoop, which reads the page's earlier messages back from the logs
	// (history.go). historyWant is closed, by historyAsked, when a page or a lookup first asks for
	// them, and historyRead once they have been read for the first time, done or not; both are nil
	// for a daemon Run didn't start. historyOwed is the logs whose backlog was given up on before
	// it was fetched, which are read back once it is; under mu.
	historyKick  chan struct{}
	historyWant  chan struct{}
	historyRead  chan struct{}
	historyAsked sync.Once
	historyOwed  map[string]bool
	// historyLate says a call waited historyWait for the first read and it hadn't come, so no
	// other call waits for it (awaitHistory).
	historyLate atomic.Bool
	// acts is what sessions did other than send messages, for the page (activity.go), oldest
	// first; actSeq the number of the newest, actIDs the IDs in acts, and actSeen what each
	// session's presence last said of it, by address.
	acts    []pageAct
	actSeq  uint64
	actIDs  map[string]bool
	actSeen map[string]sessSeen
	// brackets holds the tool calls in flight whose start was snapshotted, by Claude session and
	// tool-use ID (see toolStart). snapLogged is when a failed snapshot was last logged.
	brackets   map[string]*bracket
	snapLogged time.Time
	// ending counts the calls whose end is being judged in the background (see toolEnd).
	ending int
	// panicLogged is when a panic in judging a call was last logged. survivePanics keeps such a
	// panic from crashing a test binary, for the test that causes one on purpose (see toolEnd).
	panicLogged   time.Time
	survivePanics bool
	// unsnapped holds a channel for each ended call whose worktree hasn't been snapshotted again
	// yet, closed when it has; the next call's start waits for them (see toolStart).
	unsnapped []chan struct{}
	// pushing is the sessions a handoff goroutine is working for, and pushWarned those whose last
	// handoff failed or that have no way to be handed a message, so that is logged once, not for every
	// message (see deliver.go).
	updating bool // under mu: no new handoffs after the safe restart point
	// updateReady says an update is installed and restartWhenIdle is waiting to restart onto it;
	// restartAsked, that the page asked for the restart, so an open page no longer holds it back.
	updateReady, restartAsked bool
	pushing                   map[string]bool
	pushWarned                map[string]bool
	// introduced is, for each conversation, the senders it has had a message from as a turn, so
	// later turns from them can say less (#216). It is keyed by the session's key, not its ID: a
	// Claude Code session that /clears keeps its ID but starts a conversation that has seen nothing,
	// and one that /resumes goes back to a conversation that has. It is kept in memory only: a daemon
	// that restarts introduces each sender once more, which errs the safe way.
	introduced map[string]map[string]bool
	// pushedAt is when a message last went into each session as a turn: the session is at work from
	// then, as far as the wake setting goes, before anything else says so (see idleSinceLocked).
	pushedAt map[string]time.Time
	// threads is what T3's database last said about each T3 thread a session here is in, and
	// threadWarned the threads whose state couldn't be read, logged once (see threads.go).
	// threadRead is when the read that gave each thread's state began: a read that began after
	// the hook last saw a session at work, and finds no run, ends its turn (see hookBusyLocked).
	threads      map[threadKey]t3db.State
	threadRead   map[threadKey]time.Time
	threadWarned map[threadKey]bool
	// handedTo is, for each T3 thread, the messages lately handed to it as a turn, oldest first, so
	// another session's copy of one is not handed to it again (see noteHandedLocked).
	handedTo map[threadKey][]handed
	// The sessions another session started, where T3 doesn't say so (codexkids.go): kids the
	// sessions here that are one, kidNative the children a hook named, by their client's ID,
	// with their parent's, codexLin who started each Codex thread read so far, and codexWarned
	// the threads whose read failed, logged once. All are made where they are first written.
	kids        map[string]kid
	kidNative   map[string]string
	codexLin    map[codexThread]title.Lineage
	codexWarned map[codexThread]bool

	// refused is when a presence claiming each device was last refused (see checkPresenceLocked), to
	// log it once a minute.
	refused    map[string]time.Time
	refusedSHA map[string]string // the presence last refused for each device, not read again; under mu

	// The team key (team.go). teamKeys is the team private keys this device holds, by TeamID;
	// teamChanged is closed and replaced when an epoch is read. Under mu. teamMu serializes the team
	// ref's writes; teamKick wakes teamLoop.
	teamKeys    bus.Keyring
	teamChanged chan struct{}
	teamMu      sync.Mutex
	teamKick    chan struct{}
	waitWarned  map[string]bool // logs already said to be waiting for the team key; under mu
	// syncSeq counts the syncs that got a listing, and teamReadSeq is the last of them that read the
	// team ref up to what it listed. unknownAt is, for each log stopped at a record encrypted for a
	// team key the team ref doesn't show, that record and the sync it was first met in
	// (openEventLocked). Under mu.
	syncSeq     int64
	teamReadSeq int64
	unknownAt   map[string]unknownKey

	polls atomic.Int64 // the remote listings tried, so a test can see the rate

	health health // what the page's health snapshot counts (health.go)

	syncMu  sync.Mutex // serializes syncs, so an older listing is never applied after a newer one
	advMu   sync.Mutex // serializes processing of fetched refs
	writeMu sync.Mutex // serializes appends to this device's log
	ownTip  string     // the remote's tip of this device's log, as last known; under writeMu

	// setMu guards settings and settings.json. It is taken alone, never with another lock held.
	setMu    sync.Mutex
	settings pageSettings

	kick     chan struct{}
	pollWake chan struct{} // wakes pollLoop when a session or a page comes, so it looks at once
	pushKick chan struct{} // wakes pushLoop, which hands messages over to sessions as turns
	pushSem  chan struct{} // bounds the handoffs running at once
	nameKick chan struct{} // wakes nameLoop, the only user of nameSrc
	nameSrc  io.Reader
	pushOne  chan struct{} // held while this device pushes to the remote (see push)
	quit     chan struct{}
	quitOnce sync.Once

	// bg tracks connection handlers and background writes, so Run returns only after they
	// have; bgCtx is cancelled when the daemon starts shutting down.
	bg    sync.WaitGroup
	bgCtx context.Context

	// firstSync is closed once Run's first sync is over, done or not (see awaitFirstSync). Nil for
	// a daemon Run didn't start, which has no first sync to wait for.
	firstSync chan struct{}
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
	// hooked says Murmur's hook has run for the session: it can be told of a message after any
	// tool call, so a message isn't handed to T3 while it is at work (#202). busyAt is when the
	// hook last saw it at work, a prompt or a tool call, zero once a turn ended (Stop) or before
	// the hook first ran; a subagent's tool call doesn't set it, as a background subagent works on
	// after its parent's turn ended. turnStart is when the hook first saw the current turn. stopSeen says the hook has reported the end of a turn: a session whose
	// clone was joined before #202 has no Stop hook until join runs again, and outside T3 nothing
	// else would ever end its turn. stopContinues says its last Stop could keep the turn going
	// with messages (proto.Request.Continues), which is a way in, inside T3 or out. toldUnread is
	// the unread count the hook last showed it. continuedAt is when a Stop last kept its turn going,
	// zero once another hook of the session showed the turn went on (see continueTurnLocked), and
	// continuations how many times in a row it did, which Claude Code caps (stopBlockCap).
	hooked, stopSeen, stopContinues bool
	busyAt, turnStart, continuedAt  time.Time
	toldUnread, continuations       int
	// hookSeen says a hook of the session has reported, a subagent's included; toolCalled is when
	// the session first called one of Murmur's tools (see hookOffLocked, #225). hookNative is the
	// conversation's own ID as a hook of an agent other than Claude Code reported it, for a session
	// whose MCP server hasn't learned it (see agentHookSessionsLocked).
	hookSeen   bool
	toolCalled time.Time
	hookNative string
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
	// modeSkip records every ref's tip without queueing anything: the first sync of a new
	// clone, or after a long gap, when no session is still waiting for what it missed.
	modeSkip syncMode = iota
	// modeCatchUp queues what arrived while the daemon was down, but only for sessions that
	// were running before it went down and reconnected: the first sync after a restart.
	modeCatchUp
	modeNormal
)

// catchUpLimit is the longest outage after which a restarted daemon still queues what it
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
	// sealed says the presence read was sealed: only such a one earns a grant of the team keys
	// (grantsDueLocked). locked says it was encrypted for a team key this device doesn't hold yet, to
	// read again once it does.
	sealed bool
	locked bool
}

// fetchFail is a ref whose fetch failed: at which listed SHA, and when. It is fetched again on
// its own a minute after at; a zero at means at the next poll. A slow one ran past FetchLimit, and is
// fetched outside the syncs instead, not before at, which tries have pushed back. Once that fetch has
// landed, the next sync reads what it brought.
type fetchFail struct {
	sha    string
	at     time.Time
	slow   bool
	tries  int
	landed bool
}

// fetchRetry is how long after its failure a ref that failed to fetch is tried again on its own.
const fetchRetry = time.Minute

// staleTempAge is how long a temporary download has gone unwritten before it is taken for one a
// stopped git left behind (see clearStaleTemp).
const staleTempAge = 10 * time.Minute

type inMsg struct {
	Event   bus.Event `json:"event"`
	Arrived time.Time `json:"arrived"`
	Noticed bool      `json:"noticed"`
	// NoticedAt is when the hook's notice told the session of the message. The turn that gives it
	// whole later says so, so the agent doesn't take it for a second message (#355). Zero for one
	// noticed by a daemon from before this.
	NoticedAt time.Time `json:"noticedAt,omitzero"`
	// Pushing says the message is being handed over to its session as a turn, so the hook's notice
	// and inbox leave it alone: it either arrives as the turn, and is gone from the inbox, or the
	// handoff fails and it is theirs again. PushFailed says that handoff failed. Neither is kept
	// over a restart.
	Pushing    bool `json:"-"`
	PushFailed bool `json:"-"`
	// Attempted says the message was handed to T3 or the socket, and it may be in the session's
	// conversation, though the daemon doesn't know (see deliver.go). It is saved before the handoff
	// and taken off only when the attempt provably went nowhere, so a restart in the middle of a
	// handoff, or after one that failed, finds it, and the message is left to the hook's notice
	// instead of being handed over a second time.
	Attempted bool `json:"attempted,omitempty"`
	// Held says the hook told the session of the message while it was at work, where it would
	// otherwise have gone in as a turn (#202). It goes in as one when the turn ends, if it is still
	// unread, however long the turn took: the agent knows it is there, and it mustn't wait for good.
	Held bool `json:"held,omitempty"`
	// Released is when do-not-disturb came off the session, for a message queued through it:
	// how old it is, for whether it may still go in as a turn, counts from then (see pushable).
	Released time.Time `json:"released,omitzero"`
}

// state is what survives a restart, in state.json.
type state struct {
	// Initialized is set once the first syncs have read the backlog, recording every log's tip
	// as processed without queueing it: history from before this device joined isn't replayed.
	Initialized bool              `json:"initialized"`
	Processed   map[string]string `json:"processed"` // mesh ref → last SHA processed
	// Junked holds the logs that were junk when this device first looked, with nothing read from
	// them yet, and when that look was: what was sent before it is history, not to be queued.
	Junked map[string]time.Time `json:"junked,omitempty"`
	// TagsFrom holds the logs whose backlog was given up on before it could be fetched, each with
	// the last SHA read from it, or "" for none. Processed has moved past their messages, which are
	// history by then, but tags don't expire, so the tags events from here on are still to be read.
	TagsFrom map[string]string   `json:"tagsFrom,omitempty"`
	Inbox    map[string][]*inMsg `json:"inbox"` // session → unread messages
	// Stopped holds the sessions that stopped, and when. Their inboxes are kept for StoppedInbox,
	// in case the session comes back; they are the only inboxes kept for sessions not running.
	Stopped map[string]time.Time `json:"stopped,omitempty"`
	// LastSync tells the next daemon how long this one was down.
	LastSync time.Time `json:"lastSync"`

	// Person is the name this device's human chose on the page, "" until they do, and PersonSince
	// is when (#242). People is the address of every person's name this daemon has seen in a presence
	// or the registry: the first address seen with it, unless the registry gives the name to another
	// (see notePersonLocked). A message's name never goes in it. It only grows, so a person's earlier
	// name still reaches them.
	Person string `json:"person,omitempty"`
	// Nick is the nickname this device's user gave it (#190), "" if none.
	Nick        string            `json:"nick,omitempty"`
	PersonSince time.Time         `json:"personSince,omitzero"`
	People      map[string]string `json:"people,omitempty"`
	// PersonClaims is every person's name in the names registry, to the address that claimed it
	// first (#268). It only grows, as Used does, and a name in it is that person's: a name chosen
	// elsewhere at the same time is refused, and one held from before the registry had people is
	// asked to change.
	PersonClaims map[string]string `json:"personClaims,omitempty"`
	// PersonLogged is the last message in the person's log when this was saved. Anything after it
	// in the log was written by a daemon that stopped before it could save, so their inbox doesn't
	// have it yet.
	PersonLogged string `json:"personLogged,omitempty"`

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
	// NamesPeople says the registry was read from its start by a daemon that keeps people's claims
	// (#268). One from before read their claims and passed over them, so reading on from its
	// NamesGood would leave them unread for good: without this, the next read starts from the
	// beginning. NamesGood stays as it was, as it also shows whether the registry was rewritten.
	NamesPeople bool `json:"namesPeople,omitempty"`
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
	// been idle for 30 minutes, and a message is still handed to the thread, which wakes it.
	// Local only, like NameKeys. It is kept as long as the session's inbox is.
	T3Routes map[string]t3Route `json:"t3Routes,omitempty"`

	// Tags is what is kept about the tags of this device's sessions, by the session's name ("id:"
	// and its ID while the name is pending): the tags each set for itself and a human's marks
	// (#198). Derived tags aren't kept.
	Tags map[string]*tags.State `json:"tags,omitempty"`
	// Mutes is, for each of this device's sessions, by its name as Tags is, the sessions it has
	// muted (#27): their names, and when.
	Mutes map[string]map[string]time.Time `json:"mutes,omitempty"`

	// Pins is each device's keys as this device first pinned them (keys.go), this device's own
	// included, by device ID. A pin is never undone.
	Pins map[string]*keyPin `json:"pins,omitempty"`
	// PresenceAt is the time this device's last presence was signed with, which the next one's must
	// pass (see nextPresenceAtLocked).
	PresenceAt time.Time `json:"presenceAt,omitzero"`
	// The team ref as read (team.go): its tip, its epochs in order, and the team keys each device
	// has been given, by device ID. GrantMark is, for each device a grant was read for, the time its
	// newest presence was signed with when this daemon read the grant (grantsDueLocked).
	TeamTip     string               `json:"teamTip,omitempty"`
	Epochs      []teamEpoch          `json:"epochs,omitempty"`
	TeamGranted map[string][]string  `json:"teamGranted,omitempty"`
	GrantMark   map[string]time.Time `json:"grantMark,omitempty"`
	// Explained is, for each of this device's sessions, by its name as Tags is, the people whose
	// first message to it said in full that they see only what the session sends (#345); their
	// later messages end with the line an agent's do (see explained.go).
	// So are the groups whose messages have told it, in full, that it is in them (#184).
	Explained map[string][]string `json:"explained,omitempty"`
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
	inherited := opt.PageListener
	opt.PageListener = nil
	defer func() {
		if inherited != nil {
			inherited.Close()
		}
	}()
	dir := filepath.Join(opt.GitDir, brand.StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// On every start, before daemon.json is written: a directory an older version made is
	// tightened too. A clone whose state another user made can't be, and runs as before.
	if err := proc.Protect(dir); err != nil {
		opt.Log.Printf("keeping other users out of %s: %v", dir, err)
	}
	lock, err := proc.Lock(filepath.Join(dir, "daemon.lock"))
	if errors.Is(err, proc.ErrLocked) {
		return ErrRunning
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	// A daemon.json here now is a dead daemon's, as a live one would hold the lock. It goes at once,
	// not when this one's is written: until then a client would ping its address, find the lock
	// held, and take whatever answers there for this daemon.
	infoPath := filepath.Join(dir, "daemon.json")
	os.Remove(infoPath)

	d, err := open(ctx, opt, dir)
	if err != nil {
		return err
	}
	if !d.consented() {
		return ErrNoConsent
	}
	clearStaleTemp(d.opt.GitDir, d.log) // a daemon stopped mid-fetch leaves no one else to do it
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	// A daemon that restarted onto an update handed this one the page's socket, and left the page's
	// address and token beside it. The handoff is taken whether or not it is used: one left lying
	// about would be no good to a later daemon, which has no socket to go with it.
	h, handed := takePageHandoff(dir)
	// A page that can't be served costs the page, not the daemon: sessions don't need it.
	pageLn, keep, err := d.listenPage(inherited, h, handed)
	inherited = nil
	if err != nil {
		d.log.Printf("the page for humans isn't served: %v", err)
	}
	if pageLn != nil {
		defer pageLn.Close()
	}
	defer func() {
		if keep != nil {
			keep.Close()
		}
	}()
	info := proto.Info{Addr: ln.Addr().String(), Token: d.token, PID: os.Getpid(), Device: d.device}
	if pageLn != nil {
		info.Web, info.PageToken = pageLn.Addr().String(), d.pageTok
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.bgCtx = ctx
	d.firstSync = make(chan struct{})
	// Before the page is served: its calls read these (awaitHistory).
	d.historyRead, d.historyWant = make(chan struct{}), make(chan struct{})
	if pageLn != nil {
		d.servePage(ctx, pageLn) // first, so a page key asked for at once finds the page
	}
	go d.serve(ctx, ln)
	// The daemon answers, and daemon.json says where, before its first sync rather than after it
	// (#372). That sync takes seconds, and on a slow link or a loaded machine it ran past the 15 s a
	// client that starts a daemon waits for it to answer: `mur ui` then said the daemon didn't come
	// up when it had. What wants the sync waits for it instead (see awaitFirstSync).
	if err := writeJSON(infoPath, info); err != nil {
		return err
	}
	defer os.Remove(infoPath)
	d.log.Printf("device %s, remote %q, listening on %s", d.device, d.repo.Remote, info.Addr)
	if info.Web != "" {
		d.log.Printf("page for humans on %s", info.Web)
	}
	if opt.Consent != nil {
		go d.consentLoop(ctx)
	}
	// The page's earlier messages are read back from the logs already here (history.go), apart
	// from the first sync: a page opened on a daemon that has just started, or has no network, has
	// them, and no session waits for them. The read begins when a page or a lookup first asks.
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		d.historyLoop(ctx)
	}()
	d.kickHistory()

	if d.opt.Clones != "" {
		if err := clones.Add(d.opt.Clones, clones.Clone{GitDir: d.opt.GitDir, Name: d.repoName}); err != nil {
			d.log.Printf("this clone isn't on the record of this machine's clones, so other clones' pages can't switch to it: %v", err)
		}
	}

	// Sync once before sessions register (they wait for it), so whatever this sync finds was sent
	// before any of them registered: it is the backlog, and nothing a new session should get. A stop
	// asked for meanwhile cuts it short.
	sctx, scancel := context.WithTimeout(ctx, d.opt.FirstSyncLimit)
	go func() {
		select {
		case <-d.quit:
		case <-sctx.Done():
		}
		scancel()
	}()
	if err := d.pollOnce(sctx); err != nil && !d.stopping() {
		d.log.Printf("first sync failed; retrying in the background: %v", err)
	}
	scancel()
	// The sync can take seconds, long enough for a leave to have come and gone. The consent loop
	// would stop the daemon soon, but nothing should start for a user who has left. Decided before
	// what waits for the sync goes on: since the daemon answers before it (#372), a client can have
	// found it and the user left meanwhile, and a request that waited for the sync must find the
	// daemon as this leaves it, stopping or not, not see it stop a moment later.
	noConsent := !d.stopping() && !d.consented()
	if noConsent {
		d.stop()
	}
	close(d.firstSync)
	d.mu.Lock()
	d.lastActive = time.Now() // the first sync may have taken a while; the idle time starts here
	d.mu.Unlock()

	if !d.stopping() {
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
			d.teamLoop(ctx)
		}()
		d.bg.Add(1)
		go func() {
			defer d.bg.Done()
			d.titleLoop(ctx)
		}()
		d.bg.Add(1)
		go func() {
			defer d.bg.Done()
			d.threadLoop(ctx)
		}()
		if opt.Idle > 0 {
			go d.idleLoop(ctx)
		}
		if opt.Report != nil {
			go opt.Report(ctx)
		}
		if opt.Update != nil {
			d.bg.Add(1)
			go func() { defer d.bg.Done(); opt.Update(ctx, func() { d.restartWhenIdle(ctx) }) }()
		}
	}
	select {
	case <-ctx.Done():
	case <-d.quit:
	}
	// daemon.json goes before the port does. A client trusts the address in it while the lock is
	// held, and the lock is held until the drain and the goodbye are done, which can take seconds:
	// with the port given up first, another user's process could take it and be trusted. Sessions
	// reconnect in that time, as their connections drop now, and find no daemon.
	os.Remove(infoPath)
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
	d.mu.Lock()
	updated := d.updating
	d.mu.Unlock()
	if updated {
		// The page's server has stopped, but keep still holds its socket, which the next daemon
		// inherits: a tab's requests wait in the socket's queue until that daemon answers them.
		if keep != nil {
			if err := writeJSON(filepath.Join(dir, pageHandoffFile), pageHandoff{Web: info.Web, Token: info.PageToken}); err != nil {
				d.log.Printf("the page won't carry over the restart: %v", err)
				return ErrUpdated
			}
			page := keep
			keep = nil
			return &Updated{Page: page}
		}
		return ErrUpdated
	}
	if noConsent {
		return ErrNoConsent
	}
	return nil
}

// pageHandoffFile is where a daemon restarting onto an update leaves the page's address and token
// for the daemon that replaces it, so a tab open on the page reconnects to the new one (#240). The
// new daemon takes them only with the page's socket, inherited (#255).
const pageHandoffFile = "page.json"

// pageHandoffAge is how old a handoff may be and still be taken up: a restart takes seconds.
const pageHandoffAge = 2 * time.Minute

type pageHandoff struct {
	Web   string `json:"web"`
	Token string `json:"token"`
}

// takePageHandoff reads and removes the handoff a restarting daemon left, if it is recent.
func takePageHandoff(dir string) (pageHandoff, bool) {
	path := filepath.Join(dir, pageHandoffFile)
	st, err := os.Stat(path)
	if err != nil {
		return pageHandoff{}, false
	}
	b, err := os.ReadFile(path)
	os.Remove(path)
	var h pageHandoff
	if err != nil || time.Since(st.ModTime()) > pageHandoffAge || json.Unmarshal(b, &h) != nil || h.Web == "" || h.Token == "" {
		return pageHandoff{}, false
	}
	return h, true
}

func open(ctx context.Context, opt Options, dir string) (*Daemon, error) {
	now := time.Now()
	d := &Daemon{
		opt: opt, dir: dir, maxRecord: bus.MaxRecord, maxSnapshot: bus.MaxSnapshot, log: opt.Log, token: bus.RandHex(32), pageTok: bus.RandHex(32), started: now, lastActive: now,
		sessions: map[string]*session{}, peers: map[string]*peer{}, known: map[string]string{},
		caughtUp: map[string]bool{}, unfetched: map[string]fetchFail{}, junkTried: map[string]time.Time{},
		kick: make(chan struct{}, 1), pollWake: make(chan struct{}, 1), nameKick: make(chan struct{}, 1), nameSrc: opt.NameSource,
		pushKick: make(chan struct{}, 1), pushSem: make(chan struct{}, maxPushes), pushOne: make(chan struct{}, 1),
		actIDs: map[string]bool{}, actSeen: map[string]sessSeen{}, historyKick: make(chan struct{}, 1),
		pushing: map[string]bool{}, pushWarned: map[string]bool{}, introduced: map[string]map[string]bool{}, pushedAt: map[string]time.Time{},
		threads: map[threadKey]t3db.State{}, threadRead: map[threadKey]time.Time{}, threadWarned: map[threadKey]bool{},
		handedTo: map[threadKey][]handed{}, quit: make(chan struct{}), msgIDs: map[string]bool{}, brackets: map[string]*bracket{},
		refused: map[string]time.Time{}, refusedSHA: map[string]string{},
		waitWarned: map[string]bool{}, unknownAt: map[string]unknownKey{}, teamChanged: make(chan struct{}), teamKick: make(chan struct{}, 1),
	}
	device, signKey, boxKey, err := loadIdentity(dir, opt.Log, sealedLocally(ctx, gitx.Git{Dir: opt.GitDir}))
	if err != nil {
		return nil, err
	}
	d.device, d.signKey, d.boxKey = device, signKey, boxKey
	if d.teamKeys, err = loadTeamKeys(dir); err != nil {
		return nil, err
	}
	d.host, _ = os.Hostname()
	d.repo = &bus.Repo{Git: gitx.Git{Dir: opt.GitDir}, Name: device, Email: device + "@" + brand.Name + ".invalid"}
	if d.repo.Remote, err = bus.RemoteFor(ctx, d.repo.Git); err != nil {
		return nil, err
	}
	d.owner = d.repo.Config(ctx, "user.name")
	d.repoName = clones.NameOf(d.repo.Config(ctx, "remote."+d.repo.Remote+".url"))
	if err := d.loadState(); err != nil {
		return nil, err
	}
	d.pinSelfLocked() // nothing else runs yet
	d.rereadTeamIfKeysLostLocked()
	d.loadSettings()
	d.loadPersonLog()
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

// stopping reports whether the daemon has been told to stop.
func (d *Daemon) stopping() bool {
	select {
	case <-d.quit:
		return true
	default:
		return false
	}
}

// awaitFirstSync waits until Run's first sync is over, done or not, for a request that wants what it
// reads: a registration, as what that sync reads is the backlog, which a session shouldn't be given
// (Run); who, send and lookup, which on a new clone would know no other device until then; and the
// page's send and name claim, which resolve names, push, and may sync on their own. It
// waits FirstSyncLimit at most, as the sync does, and less if ctx ends first. The daemon answers
// before that sync (#372), so a starter needn't wait for it.
func (d *Daemon) awaitFirstSync(ctx context.Context) {
	if d.firstSync == nil {
		return
	}
	select {
	case <-d.firstSync:
		return
	default:
	}
	t := time.NewTimer(d.opt.FirstSyncLimit)
	defer t.Stop()
	select {
	case <-d.firstSync:
	case <-ctx.Done():
	case <-t.C:
	}
}

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
// no longer kept. A thread the page's wake setting says not to wake now has nothing to stay up for:
// one set to never, one idle longer than its setting allows (it can only be busy again if someone
// types in it, which starts its MCP server, and with it a daemon), and one that is settled. One on
// do not disturb still counts: the tag may come off from another device's page, and only this
// daemon reads that and hands over what stayed queued.
// Caller holds mu.
func (d *Daemon) holdsRouteLocked(wake pageSettings) bool {
	for id := range d.st.T3Routes {
		if d.sessions[id] == nil && d.receivesLocked(id) && d.mayWakeLocked(id, wake) {
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
		TagsFrom: map[string]string{}, Stopped: map[string]time.Time{}, Used: map[string]string{}, NameKeys: map[string]string{},
		NameHolders: map[string]string{}, SessionNames: map[string]string{}, Commits: map[string][]commitRec{},
		T3Routes: map[string]t3Route{}, NameBinds: map[string]string{}, Reserved: map[string]string{},
		Tags: map[string]*tags.State{}, People: map[string]string{}, PersonClaims: map[string]string{},
		Pins: map[string]*keyPin{}, TeamGranted: map[string][]string{}, GrantMark: map[string]time.Time{}}
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
	if d.st.TagsFrom == nil {
		d.st.TagsFrom = map[string]string{}
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
	if d.st.Tags == nil {
		d.st.Tags = map[string]*tags.State{}
	}
	if d.st.Mutes == nil {
		d.st.Mutes = map[string]map[string]time.Time{}
	}
	if d.st.PersonClaims == nil {
		d.st.PersonClaims = map[string]string{}
	}
	if d.st.People == nil {
		d.st.People = map[string]string{}
	}
	if d.st.Pins == nil {
		d.st.Pins = map[string]*keyPin{}
	}
	if d.st.TeamGranted == nil {
		d.st.TeamGranted = map[string][]string{}
	}
	if d.st.GrantMark == nil {
		d.st.GrantMark = map[string]time.Time{}
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

// saveLocked writes state.json, unless a page of a log is being applied (see applyPageLocked), which
// saves once at its end. Caller holds mu.
func (d *Daemon) saveLocked() {
	if d.savesHeld {
		return
	}
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
	// Made new each time, never truncated: a file keeps the access list it was made with, and on
	// Windows a temporary file left by a crash from before .git\murmur was protected would carry
	// an open one onto daemon.json (see proc.Protect).
	os.Remove(tmp)
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

// restartWhenIdle waits without cancelling a handoff or stranding an open page. The
// same mutex keeps pushLoop from starting a handoff after the safe restart point.
func (d *Daemon) restartWhenIdle(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		d.mu.Lock()
		if ctx.Err() != nil {
			d.mu.Unlock()
			return
		}
		d.updateReady = true
		// A page key handed out for a tab on its way here (#348) dies with this daemon's page server:
		// the restart waits out its minute, so the tab isn't turned away.
		if len(d.pushing) == 0 && (d.restartAsked || !d.pageOpenLocked(time.Now())) && !d.pageKeysOut() {
			d.updating = true
			d.mu.Unlock()
			d.stop()
			return
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
