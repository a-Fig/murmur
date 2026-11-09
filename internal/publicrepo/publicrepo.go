// Package publicrepo tells murmur.afig.dev the address of a public repo that uses Murmur. That
// address is the only thing Murmur sends us (#199, Tyler, 2026-11-08): it is already public, so
// there is nothing to opt out of, and a private repo sends nothing at all. We use it only to count
// how many repos and people use Murmur; the rest of the count is read from the public repo's own
// bus, which anyone can read.
//
// Each repo is reported once in its life, not once per clone: the clone that reports it pushes
// refs/murmur/reported, and every clone looks for that ref before doing anything. `mur init`,
// `mur join` and a starting daemon each try; until the repo is reported, a daemon looks again once
// a UTC day, so a repo that goes public is reported within a day.
package publicrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// What `mur join` tells the user's agent about this, and what `mur init` prints after it has
// reported a repo. join.md says the same, at more length.
const (
	Statement = "The only thing " + brand.Product + " ever sends us, at murmur.afig.dev, is this repo's address, and only if the repo is public: " +
		"once for the whole repo, so we can count how many repos and people use " + brand.Product + ". " +
		"A private repo sends nothing at all. Update checks go to GitHub, not to us."
	Told = "Told murmur.afig.dev this repo's address, since the repo is public. That address is the only thing " +
		brand.Product + " ever sends us, once per repo, so we can count how many repos and people use it."
)

// Options are a report's inputs; the zero values of the optional ones are the real ones.
type Options struct {
	Version string // mur's own; a development build ("-dev"), or none, never reports
	GitDir  string // the clone's git common dir: its remote and its state dir

	URL    string           // where to report; brand.ReportURL
	API    string           // GitHub's repos endpoint, ending in a slash
	Client *http.Client     // 10 s timeout
	Now    func() time.Time // time.Now
	Every  time.Duration    // how often a daemon looks whether a look is due; 1 h
	Log    *log.Logger      // where a failed report is noted; nowhere
}

func (o *Options) defaults() {
	if o.URL == "" {
		o.URL = brand.ReportURL
	}
	if o.API == "" {
		o.API = "https://api.github.com/repos/"
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every == 0 {
		o.Every = time.Hour
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
}

// Result is what a look found.
type Result int

const (
	Waiting   Result = iota // nothing to do until tomorrow: today's look found the repo private or not on GitHub
	Reported                // this look told murmur.afig.dev
	Known                   // the repo was reported before, by this clone or another
	Private                 // GitHub doesn't show the repo to someone signed out
	NotGitHub               // the remote isn't a github.com repo
	Dev                     // a development build, which never reports
)

// Done says whether nothing will ever be left to do for this clone.
func (r Result) Done() bool { return r == Reported || r == Known || r == Dev }

// Run looks now, then every Every until the repo is reported or ctx ends. A daemon runs it for as
// long as it runs; a look that fails is tried again at the next one.
func Run(ctx context.Context, o Options) {
	o.defaults()
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		res, err := Once(ctx, o)
		if err != nil && ctx.Err() == nil {
			o.Log.Printf("reporting a public repo: %v", err)
		}
		if res.Done() {
			return
		}
		t.Reset(o.Every)
	}
}

// Once reports the repo if it is public and not reported yet, and says what it found. A look that
// found the repo private, or not on GitHub, isn't repeated the same UTC day.
func Once(ctx context.Context, o Options) (Result, error) {
	o.defaults()
	if o.Version == "" || strings.HasSuffix(o.Version, "-dev") {
		return Dev, nil
	}
	g := gitx.Git{Dir: o.GitDir}
	remote, err := bus.RemoteFor(ctx, g)
	if err != nil {
		return Waiting, err
	}
	repo := &bus.Repo{Git: g, Remote: remote, Name: brand.Name, Email: brand.Name + "@" + brand.Name + ".invalid"}
	st := readState(o.GitDir)
	day := o.Now().UTC().Format(time.DateOnly)
	addr := Address(repo.Config(ctx, "remote."+remote+".url"))
	// What this clone knows is kept by address, so a remote changed to another repo is looked at
	// afresh (Sol's review of #239).
	if addr != "" && st.Known == addr {
		return Known, nil
	}
	sent := addr != "" && st.Sent == addr // the site has it: only the ref is left
	if !sent && st.Looked == day {
		return Waiting, nil
	}
	if addr == "" {
		st.Looked = day
		return NotGitHub, writeState(o.GitDir, st)
	}
	sha, err := repo.RemoteRef(ctx, brand.ReportedRef)
	if err != nil {
		return Waiting, err
	}
	if sha != "" {
		st.Known = addr // so the next look needn't ask the remote
		return Known, writeState(o.GitDir, st)
	}
	if !sent {
		full, public, err := lookUp(ctx, o, strings.TrimPrefix(addr, "github.com/"))
		if err != nil {
			return Waiting, err
		}
		if !public {
			st.Looked = day
			return Private, writeState(o.GitDir, st)
		}
		if err := send(ctx, o, "github.com/"+full); err != nil {
			return Waiting, err // not stored, so sent again at the next look
		}
		// From here the site has it. Only the ref is left, retried at every look until it is on the
		// remote, without sending the address again.
		st.Sent = addr
		if err := writeState(o.GitDir, st); err != nil {
			return Waiting, err
		}
	}
	if err := mark(ctx, repo, o.Now().UTC()); err != nil {
		return Waiting, fmt.Errorf("recording the report on the bus: %w", err)
	}
	st.Known = addr
	writeState(o.GitDir, st) // if this fails, the next look finds the ref on the remote
	return Reported, nil
}

// mark pushes refs/murmur/reported, created only if it is absent. Another clone's ref already
// there is as good as this one's.
func mark(ctx context.Context, repo *bus.Repo, at time.Time) error {
	msg, err := bus.Encode(record{V: 1, At: at}, "reported to "+strings.TrimPrefix(brand.ReportURL, "https://"))
	if err != nil {
		return err
	}
	c, err := repo.NewCommit(ctx, "", msg)
	if err != nil {
		return err
	}
	if err := repo.Push(ctx, c, brand.ReportedRef, bus.Lease, ""); err != nil && !errors.Is(err, bus.ErrRejected) {
		return err
	}
	return nil
}

// record is refs/murmur/reported's commit: when the repo was reported, and nothing else.
type record struct {
	V  int       `json:"v"`
	At time.Time `json:"at"`
}

// lookUp asks GitHub about owner/name without credentials, as anyone signed out would: a private
// repo is answered 404, so nothing private is learned and no token is used. It returns GitHub's
// spelling of the name, which follows a rename.
func lookUp(ctx context.Context, o Options, ownerName string) (full string, public bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.API+ownerName, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", brand.Command) // no version: the address is all that is sent
	res, err := o.Client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("GitHub answered %s", res.Status)
	}
	var r struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r); err != nil {
		return "", false, err
	}
	if r.Private || Address("github.com/"+r.FullName) == "" {
		return "", false, nil
	}
	return r.FullName, true, nil
}

// Request is the whole of what Murmur sends us; site/src/usage.js reads it.
type Request struct {
	Repo string `json:"repo"` // github.com/<owner>/<name>
}

func send(ctx context.Context, o Options, addr string) error {
	body, _ := json.Marshal(Request{Repo: addr})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", brand.Command) // no version: the address is all that is sent
	res, err := o.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", o.URL, res.Status)
	}
	return nil
}

// state is what the last look left in the clone's state dir.
type state struct {
	Looked string `json:"looked"` // the UTC day, YYYY-MM-DD, a look last found the repo private or not on GitHub
	Sent   string `json:"sent"`   // the address the site stored; for it, only refs/murmur/reported is left to push
	Known  string `json:"known"`  // the address whose refs/murmur/reported this clone has seen or pushed
}

func statePath(gitDir string) string { return filepath.Join(gitDir, brand.StateDir, "publicrepo.json") }

func readState(gitDir string) state {
	var st state
	if b, err := os.ReadFile(statePath(gitDir)); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func writeState(gitDir string, st state) error {
	b, _ := json.Marshal(st)
	path := statePath(gitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Address is a remote's github.com/<owner>/<name>, lowercased, however the remote was written
// (https or ssh, with or without .git or a user name), or "" if it isn't a repo on github.com.
func Address(url string) string {
	parts := strings.Split(RepoKey(url), "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return ""
	}
	switch parts[0] {
	case "github.com", "www.github.com", "ssh.github.com":
		return "github.com/" + parts[1] + "/" + parts[2]
	}
	return ""
}

// RepoKey is a remote's address reduced to host and path, lowercased, so every way of writing one
// repo's address gives the same key: https://user@GitHub.com/a-Fig/murmur.git and
// git@github.com:a-Fig/murmur both give github.com/a-fig/murmur.
func RepoKey(url string) string {
	s := strings.TrimSpace(url)
	scheme := false
	if i := strings.Index(s, "://"); i >= 0 {
		s, scheme = s[i+3:], true
	}
	// Credentials or an ssh user end at the last @ before the path.
	slash := strings.IndexByte(s, '/')
	if at := strings.LastIndexByte(s, '@'); at >= 0 && (slash < 0 || at < slash) {
		s = s[at+1:]
	}
	if !scheme {
		// scp-like host:path, but not a Windows drive (C:\...)
		if i := strings.IndexByte(s, ':'); i > 1 && (slash < 0 || i < strings.IndexByte(s, '/')) {
			s = s[:i] + "/" + strings.TrimPrefix(s[i+1:], "/")
		}
	} else if host, rest, ok := strings.Cut(s, "/"); ok {
		if h, port, ok := strings.Cut(host, ":"); ok {
			if _, err := strconv.Atoi(port); err == nil {
				host = h
			}
		}
		s = host + "/" + rest
	}
	s = strings.TrimRight(strings.ReplaceAll(s, "\\", "/"), "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.ToLower(s)
}
