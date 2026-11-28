package update

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Another clone's updater, or an installer, puts a binary in place: every other daemon on the
// machine restarts onto it within the watch, not at its next hourly check, with updates off too,
// and says so, as after an install of its own (#1177). It waits for the binary to say its version:
// one that doesn't run yet, as while an installer writes it, is no daemon to restart onto.
func TestAnotherBinaryInPlaceRestartsTheDaemon(t *testing.T) {
	for _, env := range []string{"", "1"} {
		t.Run("MURMUR_NO_UPDATE="+env, func(t *testing.T) {
			path := exeName(t.TempDir())
			if err := os.WriteFile(path, []byte("running"), 0o755); err != nil {
				t.Fatal(err)
			}
			checked := make(chan struct{}, 8)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checked <- struct{}{}
				w.WriteHeader(http.StatusServiceUnavailable) // no release found: the check does nothing
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var mu sync.Mutex
			var reports []Status
			restarted := make(chan struct{})
			var logs bytes.Buffer
			done := make(chan struct{})
			go func() {
				defer close(done)
				Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(),
					Every: time.Hour, Watch: 20 * time.Millisecond, Log: log.New(&logs, "", 0),
					Getenv: func(k string) string {
						if k == noUpdate {
							return env
						}
						return ""
					},
					Report: func(s Status) { mu.Lock(); reports = append(reports, s); mu.Unlock() }},
					func() { close(restarted) })
			}()
			if env == "" {
				select {
				case <-checked:
				case <-ctx.Done():
					t.Fatal("no first check")
				}
			}
			time.Sleep(100 * time.Millisecond) // watches that find the same binary restart nothing
			select {
			case <-restarted:
				t.Fatal("restarted with the same binary in place")
			default:
			}
			for _, err := range []error{os.WriteFile(path+".new", []byte("another clone's install, half written"), 0o755), os.Rename(path+".new", path)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(200 * time.Millisecond) // watches that find a binary that doesn't run restart nothing
			select {
			case <-restarted:
				t.Fatal("restarted onto a binary that doesn't say its version")
			default:
			}
			t.Setenv(standInEnv, "1.2.4")
			standIn(t, path)
			select {
			case <-restarted:
			case <-ctx.Done():
				t.Fatal("no restart onto the binary in place")
			}
			<-done
			mu.Lock()
			defer mu.Unlock()
			if last := reports[len(reports)-1]; !last.Installed || last.Latest != "1.2.4" {
				t.Errorf("last report %+v, want Installed 1.2.4", last)
			}
			if !bytes.Contains(logs.Bytes(), []byte("another binary is in place (1.2.4)")) {
				t.Errorf("logged %q", logs.String())
			}
			if n := len(checked); n > 0 {
				t.Errorf("%d more checks of GitHub after the first, want none", n)
			}
		})
	}
}

// A binary moved aside and not yet replaced, as an installer of the old kind leaves it for a moment,
// is no replacement.
func TestAMissingBinaryIsNoReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	running, err := Identity(path)
	if err != nil {
		t.Fatal(err)
	}
	if replaced(path, running) {
		t.Fatal("the same file counts as replaced")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if replaced(path, running) {
		t.Fatal("a missing binary counts as replaced")
	}
}

// The version is the one `mur version` prints on its first line; a binary that prints something
// else, or doesn't run, says none.
func TestBinaryVersion(t *testing.T) {
	for out, want := range map[string]string{
		"mur 0.5.6\n": "0.5.6",
		"mur 0.6.0-rc.1\r\nMurmur 0.6.0 is out.\n": "0.6.0-rc.1",
		"mur v0.5.6\n":   "0.5.6",
		"mur nonsense\n": "",
		"t4 0.5.6\n":     "",
		"":               "",
	} {
		if got := printedVersion(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
	if got := binaryVersion(filepath.Join(t.TempDir(), "none")); got != "" {
		t.Errorf("a missing file says %q", got)
	}
}
