package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/proto"
)

// fakeClone makes a git dir that has joined, with daemon.json saying its page is at web ("" for
// none), and puts it on the record.
func fakeClone(t *testing.T, record, name, web string) string {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), name, ".git")
	state := filepath.Join(gitDir, "murmur")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "joined.json"), []byte(`{}`), 0o600)
	if web != "" {
		b, _ := json.Marshal(proto.Info{Addr: "127.0.0.1:1", Token: "t", Web: web})
		os.WriteFile(filepath.Join(state, "daemon.json"), b, 0o600)
	}
	if err := clones.Add(record, clones.Clone{GitDir: gitDir, Name: name}); err != nil {
		t.Fatal(err)
	}
	return gitDir
}

// The app opens on the clone it last showed when started outside a repo, else the first joined
// clone on the record, and says what to do when there is none.
func TestAppClone(t *testing.T) {
	record := t.TempDir()
	t.Setenv(clones.DirEnv, record)
	cfg := t.TempDir()
	outside := t.TempDir()
	ctx := context.Background()

	if _, err := appClone(ctx, outside, cfg); err == nil {
		t.Fatal("no clone on the record, and no error")
	}
	a := fakeClone(t, record, "alpha", "")
	b := fakeClone(t, record, "beta", "")
	if got, err := appClone(ctx, outside, cfg); err != nil || got != a {
		t.Fatalf("with nothing remembered: %q, %v; want alpha's, the first", got, err)
	}
	(&follower{cfg: cfg, gitDir: b}).remember()
	if got, _ := appClone(ctx, outside, cfg); got != b {
		t.Fatalf("got %q, want beta's, the one last shown", got)
	}
	os.RemoveAll(filepath.Dir(b))
	if got, _ := appClone(ctx, outside, cfg); got != a {
		t.Fatalf("got %q, want alpha's once beta is gone", got)
	}
}

// Started in a clone that joined, the app opens on it, as `mur ui` does, whatever it showed last.
func TestAppCloneHere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	record := t.TempDir()
	t.Setenv(clones.DirEnv, record)
	cfg := t.TempDir()
	other := fakeClone(t, record, "other", "")
	(&follower{cfg: cfg, gitDir: other}).remember()

	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	gitDir, _ := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	os.MkdirAll(filepath.Join(gitDir, "murmur"), 0o700)
	os.WriteFile(filepath.Join(gitDir, "murmur", "joined.json"), []byte(`{}`), 0o600)
	got, err := appClone(context.Background(), dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := filepath.EvalSymlinks(got); g != gitDir {
		t.Fatalf("got %q, want this repo's %q", got, gitDir)
	}
}

// follow keeps the window where the page is, moves to the clone the page's repo menu went to, and
// sends the window to the page again when its daemon comes back on another port.
func TestFollow(t *testing.T) {
	record := t.TempDir()
	t.Setenv(clones.DirEnv, record)
	cfg := t.TempDir()
	a := fakeClone(t, record, "alpha", "127.0.0.1:1001")
	b := fakeClone(t, record, "beta", "127.0.0.1:2002")
	pages := map[string]string{a: "http://127.0.0.1:1001/#token=a", b: "http://127.0.0.1:2002/#token=b"}
	var asked []string
	f := &follower{cfg: cfg, gitDir: a, page: func(_ context.Context, gitDir string) (string, error) {
		asked = append(asked, filepath.Base(filepath.Dir(gitDir)))
		return pages[gitDir], nil
	}}
	ctx := context.Background()

	if got := f.follow(ctx, "127.0.0.1:1001"); got != "" {
		t.Fatalf("the page is where its daemon is, and follow sent it to %q", got)
	}
	if got := f.follow(ctx, "127.0.0.1:2002"); got != "" || f.gitDir != b || lastClone(cfg) != b {
		t.Fatalf("after the repo menu: %q, following %q, remembered %q; want beta's, without a move", got, f.gitDir, lastClone(cfg))
	}
	// Beta's daemon went, and came back on a new port.
	os.Remove(filepath.Join(b, "murmur", "daemon.json"))
	pages[b] = "http://127.0.0.1:3003/#token=b2"
	if got := f.follow(ctx, "127.0.0.1:2002"); got != pages[b] || f.gitDir != b {
		t.Fatalf("after beta's daemon came back: %q, following %q; want beta's new page", got, f.gitDir)
	}
	if want := []string{"alpha", "beta", "beta"}; len(asked) != len(want) || asked[2] != "beta" || asked[0] != "alpha" {
		t.Fatalf("asked for pages of %v, want %v", asked, want)
	}
	// A clone that was left is followed no further, and its daemon isn't started.
	os.Remove(filepath.Join(b, "murmur", "joined.json"))
	if got := f.follow(ctx, "127.0.0.1:3003"); got != "" || len(asked) != 3 {
		t.Fatalf("after leaving: %q, asked %v", got, asked)
	}
}

// What `mur app` says when the window's process ends as it starts is the last line it logged.
func TestLastLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	os.WriteFile(p, []byte("an older run\nmur: old\n"), 0o600)
	from := int64(len("an older run\nmur: old\n"))
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("2026/11/15 10:00:00 starting\nmur: webview2: runtime not found\n")
	f.Close()
	if got := lastLine(p, from); got != "webview2: runtime not found" {
		t.Fatalf("got %q", got)
	}
	if got := lastLine(p, from+1000); got != "see "+p {
		t.Fatalf("with nothing new: %q", got)
	}
}
