package clones

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Picker is the record as the page's repo picker sees it, from the daemon of one clone (Self).
type Picker struct {
	Dir  string // the record's directory
	Self string // this daemon's git dir
	// Joined says whether the user is still in Murmur in a clone (join.Joined).
	Joined func(gitDir string) bool
}

var _ web.Repos = Picker{}

// runningWait is how long a clone's daemon has to accept a connection to count as running.
const runningWait = 500 * time.Millisecond

// slugWait is how long git has to say the remote of a clone recorded without a slug.
const slugWait = 2 * time.Second

// List is every clone on the record. Its daemon put this one there as it started.
func (p Picker) List(ctx context.Context) ([]web.Repo, error) {
	list, err := List(p.Dir, p.Joined)
	if err != nil {
		return nil, err
	}
	self := ID(p.Self)
	// A clone in the temporary folder is a test's or a demo's: anything that runs a daemon there
	// records its clone, and a picker full of fixtures buries the real repos (#1278). A page that is
	// itself one of them still lists them, so tests and demos see their siblings.
	if !inTemp(p.Self) {
		list = slices.DeleteFunc(list, func(c Clone) bool { return c.ID() != self && inTemp(c.GitDir) })
	}
	out := make([]web.Repo, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i] = web.Repo{ID: c.ID(), Name: c.Label(), Slug: c.Slug, Path: shortPath(c.Top()), Current: c.ID() == self}
		if out[i].Current {
			out[i].Running = true
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out[i].Slug == "" { // recorded before slugs were
				sctx, cancel := context.WithTimeout(ctx, slugWait)
				out[i].Slug = SlugIn(sctx, c.GitDir)
				cancel()
				// Recorded, so the next listing has it without a git: the page lists the repos on
				// every load, and the clone's own daemon records it only when it next starts
				// (#1164). A remote git can't say, or one with no name in it, is asked again.
				// Not for a clone that left while git was asked: leaving takes it off the record,
				// and this would put it back. Asked as late as can be, which narrows that and
				// doesn't end it; a record put back is one List passes over, as its clone has left.
				if out[i].Slug != "" && (p.Joined == nil || p.Joined(c.GitDir)) {
					c.Slug = out[i].Slug
					Add(p.Dir, c) // a record that can't be written is asked for again next time
				}
			}
			rctx, cancel := context.WithTimeout(ctx, runningWait)
			defer cancel()
			out[i].Running = client.Running(rctx, c.GitDir)
		}()
	}
	wg.Wait()
	return out, nil
}

// tempRoots are the system's temporary folder as written and with its links followed (macOS's is
// under /private/var as well as /var). A variable, for the tests, whose own folders are all in it.
var tempRoots = func() []string {
	t := filepath.Clean(os.TempDir())
	roots := []string{t}
	if r, err := filepath.EvalSymlinks(t); err == nil && r != t {
		roots = append(roots, r)
	}
	return roots
}

// inTemp says path is inside the temporary folder. Windows ignores case in paths.
func inTemp(path string) bool {
	path = filepath.Clean(path)
	for _, root := range tempRoots() {
		if filepath.Dir(root) == root {
			continue // a whole volume (TMPDIR=/) would hold every clone
		}
		rel, err := filepath.Rel(root, path)
		if runtime.GOOS == "windows" {
			rel, err = filepath.Rel(strings.ToLower(root), strings.ToLower(path))
		}
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// shortPath is path with the home directory as ~, which is how two clones of one repo are told
// apart on the page.
func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return "~" + string(filepath.Separator) + rel
	}
	return path
}

// Open starts the clone's daemon if it isn't running, as `mur ui` does, and asks it for its page's
// address with a one-time key. Only a clone on the record, still there and still joined, is opened:
// the page names it by ID, never by path.
func (p Picker) Open(ctx context.Context, id string) (string, error) {
	list, err := List(p.Dir, p.Joined)
	if err != nil {
		return "", err
	}
	for _, c := range list {
		if c.ID() != id {
			continue
		}
		// A daemon restarting onto an update refuses to give keys, and the next one gives them; one
		// that stops just then fails the call. Either way the next daemon is asked, until ctx ends.
		for {
			cl, err := client.Connect(ctx, c.GitDir, true)
			if err != nil {
				return "", err
			}
			u, err := cl.PageKeyURL(ctx)
			if errors.Is(err, client.ErrNoPage) {
				return "", errors.New(c.Label() + "'s daemon isn't serving its page; its daemon.log says why")
			}
			if err == nil {
				return u, nil
			}
			var ne net.Error
			if err.Error() != proto.ErrRestarting && !errors.As(err, &ne) && !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, syscall.ECONNRESET) {
				return "", err // a refusal, which asking again won't change
			}
			select {
			case <-ctx.Done():
				return "", err
			case <-time.After(time.Second):
			}
		}
	}
	return "", web.ErrNoRepo
}
