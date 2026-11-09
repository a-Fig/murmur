package main

import (
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/join"
)

func TestUpdateRestartForwardsDaemonFlags(t *testing.T) {
	_, clone := cloneOf(t, t.TempDir())
	ctx := context.Background()
	p, err := join.Locate(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	// The daemon can be started outside the clone. A relative log path must
	// continue to name the same file when its replacement starts in the clone.
	// It starts in a directory of its own: on Windows CI the package is on D: and the temporary
	// directory on C:, and no relative path leads from one drive to the other.
	t.Chdir(t.TempDir())
	if err := os.Mkdir("logs", 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join("logs", "daemon custom.log")
	absoluteLog, err := filepath.Abs(logPath)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	logWriter := log.Writer()
	defer func() {
		if os.Stderr != stderr {
			os.Stderr.Close()
		}
		os.Stdout, os.Stderr = stdout, stderr
		log.SetOutput(logWriter)
		debug.SetCrashOutput(nil, debug.CrashOptions{})
	}()
	var gotFlags []string
	started := false
	err = runDaemonWith(ctx, []string{"-C", clone, "--poll", "17s", "--idle=-1s", "--log", logPath},
		func(_ context.Context, o daemon.Options) error {
			if o.Poll != 17*time.Second || o.Idle != -time.Second {
				t.Fatalf("options: poll=%s idle=%s", o.Poll, o.Idle)
			}
			return daemon.ErrUpdated
		}, func(exe, gitDir string, page *os.File, flags ...string) error {
			started = true
			if page != nil {
				t.Errorf("a page socket %s was handed over that Run didn't return", page.Name())
			}
			if exe == "" || !samePath(t, gitDir, filepath.Join(clone, ".git")) {
				t.Fatalf("replacement target: %q %q", exe, gitDir)
			}
			gotFlags = flags
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--C=" + clone, "--idle=-1s", "--log=" + absoluteLog, "--poll=17s"}
	if !started || !reflect.DeepEqual(gotFlags, want) {
		t.Fatalf("started=%v flags=%q, want %q", started, gotFlags, want)
	}
}

// samePath says a and b name the same directory once symlinks are resolved: git reports a macOS
// temp directory as /private/var/..., which t.TempDir names /var/....
func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	return ra == rb
}

// The page's socket Run returns on an update goes to the new daemon (#255).
func TestUpdateRestartHandsOverThePage(t *testing.T) {
	_, clone := cloneOf(t, t.TempDir())
	ctx := context.Background()
	p, err := join.Locate(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ln.(*net.TCPListener).File()
	ln.Close()
	if err != nil {
		t.Fatal(err)
	}
	var handed *os.File
	err = runDaemonWith(ctx, []string{"-C", clone},
		func(context.Context, daemon.Options) error { return &daemon.Updated{Page: f} },
		func(exe, gitDir string, page *os.File, flags ...string) error {
			handed = page
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if handed != f {
		t.Fatalf("the new daemon was handed %v, want the page's socket", handed)
	}
	// Once the new daemon is started, this process lets its own hold go.
	if other, err := net.Listen("tcp", ln.Addr().String()); err != nil {
		t.Fatalf("the restarting process still holds the page's port: %v", err)
	} else {
		other.Close()
	}
}
