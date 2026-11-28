package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
)

// One check per machine (#1177). Every daemon on a machine checks at its start and hourly, and
// each used to ask GitHub itself. Now whichever asks first records GitHub's answer for its channel
// (the release, with its assets, or why the ask failed) in the machine's shared directory, under a
// lock, and every other daemon takes that answer while it is younger than its hour. A check a
// person asked for (a changed setting, Update now) asks GitHub all the same, and its answer is
// recorded for the rest. A daemon that sees the record change checks soon after (Run), taking the
// answer and reading the setting again, so a change on one clone's page reaches every clone's.
//
// A daemon that finds the lock held waits for it, up to sharedWait: daemons that start together
// check together, and one that went to GitHub on its own instead made as many asks as there were
// clones. A failed ask is taken for sharedFailLife only, so one blip doesn't stop every clone's
// updates for the hour. A recorded release says which URLs the binary and SHA256SUMS come from,
// and whoever can write the record could point them elsewhere: one whose download URLs aren't
// the release's own on GitHub is no answer (recordedAssetsOK).

const (
	sharedFile = "releases.json"
	sharedLock = "releases.lock"
	// sharedKeep is how long an answer stays in the file once nothing has asked for it.
	sharedKeep = 24 * time.Hour
	// sharedFailLife is how long a failed ask is taken by other daemons.
	sharedFailLife = 5 * time.Minute
)

// sharedWait is how long a check waits for another daemon's ask of GitHub to end. Past it, it
// takes what is recorded, if anything, and otherwise asks GitHub itself.
var sharedWait = 30 * time.Second

// answers is the shared file: GitHub's last answer for each channel of each releases URL.
type answers struct {
	// Seq counts the writes, so a daemon can tell another's write from its own (Options.seen),
	// which a file's time can't on a file system that keeps it to the second or two.
	Seq     int64             `json:"seq"`
	Answers map[string]answer `json:"answers"`
}

type answer struct {
	At      time.Time `json:"at"`
	Release *release  `json:"release,omitempty"` // nil: no release on the channel
	Error   string    `json:"error,omitempty"`   // the ask failed
}

// pick is fetch, through the machine's shared answers when it has a directory for them (Shared).
func pick(ctx context.Context, o Options, prerelease bool) (*release, error) {
	if o.Shared == "" {
		return fetchNow(ctx, o, prerelease)
	}
	if err := os.MkdirAll(o.Shared, 0o700); err != nil {
		return fetchNow(ctx, o, prerelease)
	}
	key := o.URL + " stable"
	if prerelease {
		key = o.URL + " prerelease"
	}
	lock, err := waitLock(ctx, filepath.Join(o.Shared, sharedLock), sharedWait)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Another daemon's ask is taking long, or the lock can't be had: what is recorded, if it
		// will do, else GitHub, unrecorded.
		if rel, ok, err := recorded(o, readAnswers(o.Shared), key); ok {
			return rel, err
		}
		return fetchNow(ctx, o, prerelease)
	}
	defer lock.Close()
	all := readAnswers(o.Shared)
	if rel, ok, err := recorded(o, all, key); ok {
		return rel, err
	}
	rel, err := fetchNow(ctx, o, prerelease)
	if ctx.Err() != nil {
		return rel, err // a check cut short says nothing of GitHub
	}
	if o.answered != nil {
		*o.answered = time.Now()
	}
	a := answer{At: time.Now().UTC(), Release: rel}
	if err != nil {
		a.Error = err.Error()
	}
	now := time.Now()
	for k, old := range all.Answers {
		if now.Sub(old.At) > sharedKeep {
			delete(all.Answers, k)
		}
	}
	all.Answers[key] = a
	before := all.Seq
	all.Seq++
	if writeAnswers(o.Shared, all) && o.seen != nil && *o.seen == before {
		*o.seen = all.Seq // nobody else's write went unseen
	}
	return rel, err
}

// recorded is the answer the record holds under key, if another daemon may take it: not one a
// person asked GitHub for (fresh), not past the daemon's hour, a failure not past sharedFailLife,
// and a release only with its download URLs on GitHub's own.
func recorded(o Options, all answers, key string) (rel *release, ok bool, err error) {
	a, ok := all.Answers[key]
	if !ok || o.fresh || a.At.After(time.Now()) {
		return nil, false, nil
	}
	age := time.Since(a.At)
	if age >= o.Every || a.Error != "" && age >= sharedFailLife {
		return nil, false, nil
	}
	if a.Release != nil && !recordedAssetsOK(o.URL, a.Release) {
		return nil, false, nil
	}
	if o.answered != nil {
		*o.answered = a.At
	}
	if a.Error != "" {
		return nil, true, errors.New(a.Error)
	}
	if a.Release != nil {
		a.Release.checked = a.At
	}
	return a.Release, true, nil
}

// recordedTag is the only shape of tag a recorded release may have: a release version, with no
// build metadata. version() drops what follows a "+", so a tag such as
// v99.0.0+/../../../../../evil/repo/releases/download/v1 passed it, and its assets' URLs, built on
// it, matched exactly; GitHub resolves the dot segments and redirects to the other repo's asset
// (dandy-mulberry's re-check of #1315).
var recordedTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// recordedAssetsOK says every asset of a release from the record downloads from where GitHub
// serves that release's assets: https://github.com/a-Fig/murmur/releases/download/<tag>/<name>.
// A releases API a test names instead serves them from its own host.
func recordedAssetsOK(api string, rel *release) bool {
	if !recordedTag.MatchString(rel.Tag) {
		return false
	}
	scheme, host, dir := "https", "github.com", "/a-Fig/murmur/releases/download/"+rel.Tag+"/"
	if api != githubReleases {
		u, err := url.Parse(api)
		if err != nil || u.Host == "" {
			return false
		}
		scheme, host, dir = u.Scheme, u.Host, ""
	}
	for _, a := range rel.Assets {
		if a.Name == "" || a.Name == "." || a.Name == ".." || !recordedName.MatchString(a.Name) {
			return false
		}
		u, err := url.Parse(a.URL)
		if err != nil || u.Scheme != scheme || u.Host != host || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			u.ForceQuery || u.RawPath != "" || path.Clean(u.Path) != u.Path {
			return false
		}
		if dir != "" && u.Path != dir+a.Name {
			return false
		}
	}
	return true
}

// recordedName is the shape of a recorded asset's name: Murmur's are mur-<os>-<arch>[.exe] and
// SHA256SUMS, and nothing in one is escaped in its URL.
var recordedName = regexp.MustCompile(`^[0-9A-Za-z._-]+$`)

// waitLock takes the lock at path, waiting up to wait while another process holds it.
func waitLock(ctx context.Context, path string, wait time.Duration) (*proc.Held, error) {
	deadline := time.Now().Add(wait)
	for {
		lock, err := proc.Lock(path)
		if !errors.Is(err, proc.ErrLocked) || !time.Now().Before(deadline) {
			return lock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// fetchNow is fetch, the release marked as GitHub's answer as of now.
func fetchNow(ctx context.Context, o Options, prerelease bool) (*release, error) {
	rel, err := fetch(ctx, o, prerelease)
	if rel != nil {
		rel.checked = time.Now()
	}
	return rel, err
}

func readAnswers(dir string) answers {
	var all answers
	if b, err := os.ReadFile(filepath.Join(dir, sharedFile)); err == nil {
		json.Unmarshal(b, &all) // a file in a shape this doesn't know is no answer
	}
	if all.Answers == nil {
		all.Answers = map[string]answer{}
	}
	return all
}

// writeAnswers replaces the file in one step, so a daemon reading it never sees half of it, and
// says whether it did.
func writeAnswers(dir string, all answers) bool {
	b, err := json.Marshal(all)
	if err != nil {
		return false
	}
	file := filepath.Join(dir, sharedFile)
	tmp := file + ".tmp"
	// Every daemon on the machine reads the file every few seconds, and Windows refuses a rename
	// over a file that is open (renameRetried).
	return os.WriteFile(tmp, b, 0o600) == nil && renameRetried(tmp, file) == nil
}

// sharedSeq is the count of writes of the shared answers, zero if never or with no Shared.
func sharedSeq(dir string) int64 {
	if dir == "" {
		return 0
	}
	return readAnswers(dir).Seq
}
