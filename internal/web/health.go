package web

import (
	"context"
	"net/http"
	"time"
)

// Health is how this device's daemon is doing with the remote and with its sessions: what a person
// would otherwise read out of daemon.log (#141, #87). It is this device's alone, read from the
// daemon's memory, and goes nowhere but the page. Counts are since the daemon started.
type Health struct {
	At         time.Time        `json:"at"` // when the snapshot was taken
	Sync       SyncHealth       `json:"sync"`
	Pushes     PushesHealth     `json:"pushes"`
	Fetches    FetchHealth      `json:"fetches"`
	Delivery   DeliveryHealth   `json:"delivery"`
	Daemon     DaemonHealth     `json:"daemon"`
	PublicRepo PublicRepoHealth `json:"publicRepo"`
}

// SyncHealth is the daemon's look at the remote: a listing of the bus's refs, then fetching and
// reading the ones that moved. While it fails, what the page and `who` show of other devices is
// as of LastOK.
type SyncHealth struct {
	LastOK time.Time `json:"lastOK,omitzero"` // the last sync that worked; zero if none has
	// LastOKAge is how many seconds ago that was, by this machine's monotonic clock; -1 if none has.
	LastOKAge int64 `json:"lastOKAgeSeconds"`
	// FailingSince is when the syncs failing now began to, zero while they work; Failures is how
	// many in a row have.
	FailingSince time.Time `json:"failingSince,omitzero"`
	Failures     int       `json:"consecutiveFailures"`
	Syncs        int64     `json:"syncs"`  // tried
	Failed       int64     `json:"failed"` // of those, how many failed
	// LastError is the last sync's failure, kept after the syncs work again, so an outage that has
	// passed can still be told.
	LastError *HealthError `json:"lastError,omitempty"`
}

// HealthError is a failure reduced to what is safe to show: a class, and one line with any
// credentials taken out. A line that still looks like it holds a secret is not shown.
type HealthError struct {
	Class   string    `json:"class"` // one of the Err* classes
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// HealthError.Class values.
const (
	ErrDNS         = "dns"          // the remote's host couldn't be looked up
	ErrAuth        = "auth"         // the remote wants credentials, or refused the ones given
	ErrRateLimited = "rate-limited" // the remote is limiting requests
	ErrRejected    = "rejected"     // the remote refused a push: a hook, a rule, or the ref had moved
	ErrTimeout     = "timeout"      // it took longer than allowed
	ErrOther       = "other"
)

// PushesHealth is this device's pushes, by the ref they went to.
type PushesHealth struct {
	Mesh     PushHealth `json:"mesh"`     // its log: messages and other events
	Presence PushHealth `json:"presence"` // its presence, on every change and heartbeat
	Names    PushHealth `json:"names"`    // the names registry, shared by every device
	Other    PushHealth `json:"other"`    // deletes of refs that block the bus's own
}

// PushHealth counts one kind of push. A push the remote refused because the ref had moved
// (LeaseRefused) is the bus working as meant, another device having written first, and is tried
// again; one it refused for a reason of its own (Refused, "[remote rejected]") or that decided
// nothing (Failed: the network, credentials, a timeout) is the trouble #141 found.
type PushHealth struct {
	Pushed       int64 `json:"pushed"`
	LeaseRefused int64 `json:"leaseRefused"`
	Refused      int64 `json:"refused"`
	Failed       int64 `json:"failed"`
	// LastError is the last push that was refused by the server or failed. A lease refusal doesn't
	// replace it.
	LastError *HealthError `json:"lastError,omitempty"`
}

// FetchHealth is the refs a sync couldn't bring, and the backlogs given up on.
type FetchHealth struct {
	Refs []RefHealth `json:"refs"`
	// BacklogGiveUps is how many devices' logs, unread since this daemon started, it gave up on
	// after 10 minutes, skipping their messages.
	BacklogGiveUps int64 `json:"backlogGiveUps"`
}

// RefHealth is one ref whose fetch failed, or that is too slow to fetch within a sync.
type RefHealth struct {
	Ref  string `json:"ref"`
	Kind string `json:"kind"` // mesh, presence, names
	// Slow says it ran past the time a fetch has within a sync, and is fetched apart from the
	// syncs; Tries is how many of those fetches have failed, and Landed that one has brought it.
	Slow   bool `json:"slow,omitempty"`
	Tries  int  `json:"tries,omitempty"`
	Landed bool `json:"landed,omitempty"`
	// Next is the earliest it is tried again; zero for the next sync.
	Next time.Time `json:"next,omitzero"`
}

// DeliveryHealth is how messages reached this device's sessions, by the way they went in.
type DeliveryHealth struct {
	T3     HandoffHealth `json:"t3"`     // handed to a T3 thread as a turn
	Socket HandoffHealth `json:"socket"` // handed through a session's messaging socket as a turn
	CLI    HandoffHealth `json:"cli"`    // handed through the client's own command line as a turn (Codex, OpenCode)
	Hook   HookHealth    `json:"hook"`
}

// HandoffHealth counts handoffs of a message as a turn. One that failed is left to the hook's
// notice, which the agent still gets.
type HandoffHealth struct {
	Tried  int64 `json:"tried"`
	Handed int64 `json:"handed"`
	Failed int64 `json:"failed"`
}

// HookHealth counts what Murmur's hook gave sessions: messages whole, and notices of messages. Whole
// counts per session: a message for everyone given whole to two of a hook's sessions counts twice.
// Notices counts per reply: the hook shows a message's notice once across the inboxes it speaks
// for, so that message's notice counts once.
type HookHealth struct {
	Whole   int64 `json:"whole"`
	Notices int64 `json:"notices"`
}

// DaemonHealth is the daemon itself.
type DaemonHealth struct {
	Started  time.Time `json:"started"`
	Version  string    `json:"version,omitempty"`
	Protocol int       `json:"protocol"`
	// Poll is how often it looks at the remote now: PollQuiet, PollFast or PollSlow; PollEvery is the
	// wait in seconds, which is longer while syncs fail.
	Poll      string  `json:"poll,omitempty"`
	PollEvery float64 `json:"pollEverySeconds,omitempty"`
	// UpdateChecked is when a daemon of this clone last learned from GitHub which release is the
	// latest; zero if none has. A check that failed, or found no release, doesn't move it.
	UpdateChecked time.Time `json:"updateChecked,omitzero"`
}

// DaemonHealth.Poll values.
const (
	PollQuiet = "quiet" // no session live and no page open
	PollFast  = "fast"  // a conversation moved lately
	PollSlow  = "slow"  // sessions are open and nothing is said, so it looks less often
)

// PublicRepoHealth is the one thing Murmur sends us, and whether it went: a public repo's address,
// once for the repo (internal/publicrepo).
type PublicRepoHealth struct {
	State string `json:"state"` // one of the Repo* states
	// Repo is the address that is sent, or would be: github.com/<owner>/<name>. "" off GitHub.
	Repo string `json:"repo,omitempty"`
	// Looked is the UTC day a look last found the repo private or not on GitHub.
	Looked string `json:"looked,omitempty"`
}

// PublicRepoHealth.State values.
const (
	RepoReported  = "reported"   // sent, by this clone or another
	RepoWaiting   = "waiting"    // not sent yet: no look has found it public, or the last one failed
	RepoPrivate   = "private"    // GitHub doesn't show it to someone signed out, so nothing is sent
	RepoNotGitHub = "not-github" // the remote isn't on github.com, so nothing is sent
	RepoDev       = "dev"        // a build from source, which never sends
)

func (s *Server) getHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	h, err := s.src.Health(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.Fetches.Refs == nil {
		h.Fetches.Refs = []RefHealth{}
	}
	writeJSON(w, h)
}
