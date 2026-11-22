// Package clones is this user's record of the clones on this machine that joined Murmur, for the
// page's repo picker (#348). Each clone's daemon serves its own page, on its own port with its own
// token, and none knows of the others, so they are written down: `mur join` adds a clone, `mur
// leave` takes it off, and a daemon adds its own clone each time it starts, which takes in clones
// that joined before the record existed. The page lists them, and moves its tab to another's page
// (see the web package's Repos).
//
// The record is a directory with one small file per clone, so that daemons and joins writing at
// once never need a lock: each writes or removes only its own clone's file, by rename. A clone
// deleted from disk, or left by hand, isn't listed.
package clones

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/gitx"
)

// Clone is one clone in the record.
type Clone struct {
	// GitDir is the clone's git common dir, which its worktrees share, as its daemon has it.
	GitDir string `json:"gitDir"`
	// Name is the repository's name from its remote's address ("murmur" for
	// github.com/a-Fig/murmur), or "" if it wasn't known when the clone was recorded.
	Name string `json:"name,omitempty"`
}

// ID names the clone in the record and to the page: a hash of its git dir, so the page never
// hands a path back to be acted on.
func (c Clone) ID() string { return ID(c.GitDir) }

// Top is the clone's top directory, its main working tree.
func (c Clone) Top() string {
	if filepath.Base(c.GitDir) == ".git" {
		return filepath.Dir(c.GitDir)
	}
	return c.GitDir
}

// Label is what the page calls the clone: its repository's name, else its folder's.
func (c Clone) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return filepath.Base(c.Top())
}

// ID is the record's name for the clone with this git dir. Windows paths differ in case alone
// for the same folder (c: and C:), so there the case is ignored.
func ID(gitDir string) string {
	p := filepath.Clean(gitDir)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:8])
}

// DirEnv, if set, is the record's directory in place of the user's config directory. Tests set it.
const DirEnv = "MURMUR_CLONES_DIR"

// DefaultDir is the record's directory: clones in the user's config directory, beside the
// sessions' records (internal/here), unless DirEnv names another.
func DefaultDir() (string, error) {
	if dir := os.Getenv(DirEnv); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, brand.Name, "clones"), nil
}

func file(dir, gitDir string) string { return filepath.Join(dir, ID(gitDir)+".json") }

// Add records the clone, or updates its record. A name of "" keeps the name already recorded:
// `mur join` has none to give, and the clone's daemon gives it on starting.
func Add(dir string, c Clone) error {
	c.GitDir = filepath.Clean(c.GitDir)
	if c.Name == "" {
		var old Clone
		if b, err := read(file(dir, c.GitDir), false); err == nil && json.Unmarshal(b, &old) == nil {
			if old.GitDir == c.GitDir {
				c.Name = old.Name
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".clone-*") // mode 0600; a leading dot keeps List from reading it half-written
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = again(true, func() error { return os.Rename(f.Name(), file(dir, c.GitDir)) })
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// again runs f until it succeeds or has failed 20 times, 10 ms apart. On Windows a file another
// process is reading can't be replaced, and one being replaced can't be read, each for a moment, and
// a daemon writes its clone's file as it starts while another clone's page may be listing them
// (#479). internal/here waits the same way for its own renames. Windows can also say a file is
// missing while another is renamed onto it (Go's cmd/go/internal/robustio), so f finding no file is
// tried again only where the file is known to be there (there): a file just written or just listed.
// Elsewhere no file is an answer (no name recorded yet, a clone already off the record), and isn't
// waited for.
func again(there bool, f func() error) error {
	for try := 1; ; try++ {
		err := f()
		if err == nil || !there && errors.Is(err, os.ErrNotExist) || try == 20 {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// read is os.ReadFile, tried again for a moment (again).
func read(path string, there bool) ([]byte, error) {
	var b []byte
	err := again(there, func() (err error) {
		b, err = os.ReadFile(path)
		return err
	})
	return b, err
}

// Remove takes the clone off the record. A clone that isn't on it is no error.
func Remove(dir, gitDir string) error {
	err := again(false, func() error { return os.Remove(file(dir, gitDir)) })
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List is the clones on the record that are still there and still joined, by label, then path.
// joined says whether the user is still in Murmur in the clone with that git dir. It only reads: a
// clone whose folder is gone, or that has left, is passed over, not taken off. Between seeing it gone
// and removing its file, the folder could come back and `mur join` write the file again, and the
// removal would lose that join (Sol's review, 2026-11-09). `mur leave` takes a clone off; a deleted
// clone's file, a few dozen bytes, stays. A file that can't be read is passed over too, once a read
// has been tried for a moment (see again), even if it seems not to be there.
func List(dir string, joined func(gitDir string) bool) ([]Clone, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Clone
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		b, err := read(filepath.Join(dir, name), true) // just listed (see again)
		if err != nil {
			continue
		}
		var c Clone
		if json.Unmarshal(b, &c) != nil || c.GitDir == "" || !filepath.IsAbs(c.GitDir) || name != c.ID()+".json" {
			continue // not a record this package wrote
		}
		if fi, err := os.Stat(c.GitDir); err != nil || !fi.IsDir() {
			continue // deleted, or on a drive not mounted just now
		}
		if !joined(c.GitDir) {
			continue
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Clone) int {
		if c := strings.Compare(strings.ToLower(a.Label()), strings.ToLower(b.Label())); c != 0 {
			return c
		}
		return strings.Compare(a.GitDir, b.GitDir)
	})
	return out, nil
}

// NameIn is the name of the repository whose clone has this git dir, from the address of the remote
// its bus is on (murmur.remote, else origin); "" if git can't say.
func NameIn(ctx context.Context, gitDir string) string {
	g := gitx.Git{Dir: gitDir}
	remote, _ := g.Run(ctx, "", "config", "--get", brand.ConfigRemote)
	if remote = strings.TrimSpace(remote); remote == "" {
		remote = "origin"
	}
	u, err := g.Run(ctx, "", "config", "--get", "remote."+remote+".url")
	if err != nil {
		return ""
	}
	return NameOf(u)
}

// NameOf is a repository's name from its remote's address: the last part of its path, without
// .git. https://github.com/a-Fig/murmur.git, git@github.com:a-Fig/murmur and /srv/murmur.git all
// give "murmur". An address it can't read gives "".
func NameOf(remoteURL string) string {
	s := strings.TrimSpace(remoteURL)
	if s == "" {
		return ""
	}
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		s = u.Path
	} else if i := strings.Index(s, ":"); i > 1 && !strings.ContainsAny(s[:i], `/\`) {
		s = s[i+1:] // scp-like: host:path
	}
	s = strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/")
	s = strings.TrimSuffix(path.Base(s), ".git")
	if s == "." || s == "/" {
		return ""
	}
	return s
}
