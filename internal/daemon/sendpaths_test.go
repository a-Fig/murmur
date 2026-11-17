package daemon_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// The send path limit's harder cases (#773): a folder swapped for a link between two looks, a main
// worktree whose git directory is elsewhere, a worktree inside the main one, and on Windows a
// junction and a temp folder under its short name.

const refusedSend = "isn't in this repo's clone or the temp folder"

func sendOne(d *device, sess, path string) string {
	return d.do(proto.Request{Op: proto.OpSend, Session: sess, To: "*", Subject: "a file", Files: []string{path}}).Error
}

func putFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("contents of "+filepath.Base(path)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A folder in the main worktree swapped for a link to the git directory after it was looked at,
// and before it is opened, leads nowhere: the folder opened isn't the one looked at. Not parallel:
// the hook is the package's.
func TestSendRefusesAFolderSwappedForALinkToTheGitDirectory(t *testing.T) {
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.tempDir = t.TempDir() })
	a.session("aaaaaa", "")
	clone := filepath.Dir(a.gitDir)
	// sub/config, a file of the same name as the git directory's, which the swap would read.
	path := putFile(t, filepath.Join(clone, "sub", "config"))
	if err := sendOne(a, "aaaaaa", path); err != "" {
		t.Fatalf("before the swap: %s", err)
	}
	// A link a user can make: a junction on Windows, which needs no privilege; a symbolic link
	// elsewhere.
	link := func(at string) error {
		if runtime.GOOS == "windows" {
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", at, a.gitDir).CombinedOutput(); err != nil {
				return fmt.Errorf("%v: %s", err, out)
			}
			return nil
		}
		return os.Symlink(".git", at)
	}
	if err := link(filepath.Join(clone, "probe")); err != nil {
		t.Skipf("can't make a link here: %v", err)
	}
	os.Remove(filepath.Join(clone, "probe"))
	swapped := false
	t.Cleanup(daemon.SetSendRace(func(part string) {
		if part != "sub" || swapped {
			return
		}
		swapped = true
		if err := os.Rename(filepath.Join(clone, "sub"), filepath.Join(clone, "sub-was")); err != nil {
			t.Error(err)
		}
		if err := link(filepath.Join(clone, "sub")); err != nil {
			t.Error(err)
		}
	}))
	// Refused, whether the opening follows the link and finds another folder than the one looked at
	// or won't follow it at all.
	if err := sendOne(a, "aaaaaa", path); err == "" {
		t.Error("with sub swapped for a link to .git, the file was sent")
	} else {
		t.Logf("refused: %s", err)
	}
	if !swapped {
		t.Error("the swap never ran")
	}
}

// A main worktree made with --separate-git-dir is found from a session's folder, followed through
// its links; the git directory, though it is no worktree's .git folder, is still refused. A
// worktree inside the main one is a worktree too.
func TestSendReadsAMainWorktreeWithItsGitDirectoryElsewhere(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	base := t.TempDir()
	main, sep, temp := filepath.Join(base, "main"), filepath.Join(base, "sep.git"), filepath.Join(base, "temp")
	if err := os.MkdirAll(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, base, "clone", "-q", "--no-local", "--separate-git-dir", sep, remote, main)
	a := newDeviceWith(t, remote, func(d *device) { d.gitDir, d.tempDir = sep, temp })
	file := putFile(t, filepath.Join(main, "docs", "notes.txt"))
	a.session("bbbbbb", "") // in no folder
	if err := sendOne(a, "bbbbbb", file); !strings.Contains(err, refusedSend) {
		t.Errorf("with no session in the main worktree, which is then found nowhere: %q, want it refused", err)
	}
	cwd := filepath.Join(main, "docs")
	if link := filepath.Join(base, "into-main"); os.Symlink(main, link) == nil {
		cwd = filepath.Join(link, "docs") // the session's folder, by a link
	}
	conn, _, err := a.c.Hello(context.Background(), proto.Hello{ID: "aaaaaa", Agent: "test", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := sendOne(a, "aaaaaa", file); err != "" {
		t.Errorf("a file in a main worktree whose git directory is elsewhere: %s", err)
	}
	if err := sendOne(a, "aaaaaa", filepath.Join(sep, "config")); !strings.Contains(err, refusedSend) {
		t.Errorf("the separate git directory's config: %q, want it refused", err)
	}
	nested := filepath.Join(main, "nested")
	git(t, main, "worktree", "add", "-q", "--detach", nested)
	if err := sendOne(a, "aaaaaa", putFile(t, filepath.Join(nested, "diff.txt"))); err != "" {
		t.Errorf("a file in a worktree inside the main one: %s", err)
	}
}

// A junction, which any Windows user can make, is followed as a link is: into the clone it is
// sent from, and into the git directory or elsewhere it is refused.
func TestSendFollowsAJunctionAsALink(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are Windows'")
	}
	t.Parallel()
	remote := newRemote(t)
	base := t.TempDir()
	temp, outside := filepath.Join(base, "temp"), filepath.Join(base, "outside")
	a := newDeviceWith(t, remote, func(d *device) { d.tempDir = temp })
	a.session("aaaaaa", "")
	clone := filepath.Dir(a.gitDir)
	putFile(t, filepath.Join(clone, "src", "main.go"))
	secret := putFile(t, filepath.Join(outside, "id_ed25519"))
	junction := func(link, target string) string {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J %s %s: %v\n%s", link, target, err, out)
		}
		return link
	}
	if err := os.MkdirAll(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	src := junction(filepath.Join(temp, "src"), filepath.Join(clone, "src"))
	if err := sendOne(a, "aaaaaa", filepath.Join(src, "main.go")); err != "" {
		t.Errorf("by a junction into the clone: %s", err)
	}
	for what, path := range map[string]string{
		"by a junction to the git directory":  filepath.Join(junction(filepath.Join(temp, "g"), a.gitDir), "config"),
		"by a junction to a folder elsewhere": filepath.Join(junction(filepath.Join(temp, "out"), outside), filepath.Base(secret)),
	} {
		if err := sendOne(a, "aaaaaa", path); !strings.Contains(err, refusedSend) {
			t.Errorf("%s: %q, want it refused", what, err)
		}
	}
}

// A temp folder named by its short Windows name (RUNNER~1, as GitHub's runners have it) takes a file
// by its long name, and the other way round.
func TestSendTakesTempByItsShortName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("short names are Windows'")
	}
	t.Parallel()
	long := filepath.Join(t.TempDir(), "a temp folder with a long name")
	shot := putFile(t, filepath.Join(long, "shot.png"))
	short := shortName(long)
	if short == "" || strings.EqualFold(short, long) {
		t.Skipf("no short name here: %q", short)
	}
	remote := newRemote(t)
	for what, c := range map[string]struct{ temp, file string }{
		"temp by its short name, the file by its long": {short, shot},
		"temp by its long name, the file by its short": {long, filepath.Join(short, "shot.png")},
	} {
		a := newDeviceWith(t, remote, func(d *device) { d.tempDir = c.temp })
		a.session("aaaaaa", "")
		if err := sendOne(a, "aaaaaa", c.file); err != "" {
			t.Errorf("%s: %s", what, err)
		}
	}
}
