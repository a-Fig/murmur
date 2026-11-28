package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

const (
	sharedFile = "releases.json"
	sharedLock = "releases.lock"
	// sharedKeep is how long an answer stays in the file once nothing has asked for it.
	sharedKeep = 24 * time.Hour
)

// answers is the shared file: GitHub's last answer for each channel of each releases URL.
type answers struct {
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
	lock, err := proc.Lock(filepath.Join(o.Shared, sharedLock))
	if err != nil {
		return fetchNow(ctx, o, prerelease)
	}
	defer lock.Close()
	key := o.URL + " stable"
	if prerelease {
		key = o.URL + " prerelease"
	}
	all := readAnswers(o.Shared)
	if a, ok := all.Answers[key]; ok && !o.fresh && time.Since(a.At) < o.Every && !a.At.After(time.Now()) {
		if a.Error != "" {
			return nil, errors.New(a.Error)
		}
		if a.Release != nil {
			a.Release.checked = a.At
		}
		return a.Release, nil
	}
	rel, err := fetchNow(ctx, o, prerelease)
	if ctx.Err() != nil {
		return rel, err // a check cut short says nothing of GitHub
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
	writeAnswers(o.Shared, all)
	return rel, err
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

// writeAnswers replaces the file in one step, so a daemon reading it never sees half of it.
func writeAnswers(dir string, all answers) {
	b, err := json.Marshal(all)
	if err != nil {
		return
	}
	path := filepath.Join(dir, sharedFile)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}

// sharedChanged is when the shared answers were last written, zero if never or with no Shared.
func sharedChanged(dir string) time.Time {
	if dir == "" {
		return time.Time{}
	}
	fi, err := os.Stat(filepath.Join(dir, sharedFile))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
