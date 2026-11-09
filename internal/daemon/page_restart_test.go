package daemon_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of the page across a daemon's restart onto an update (#255). The tab open on the page keeps
// polling with its token while one daemon gives way to the next. If the page's port were free in
// between, another user's process could take it and be sent that token, so the port is handed from
// one daemon to the next and never left free, and the token goes only with it.

// pageRun is one run of a daemon, with its page, outside the device harness: these tests need Run's
// error, which the harness treats as a failure.
type pageRun struct {
	t      *testing.T
	gitDir string
	cancel context.CancelFunc
	done   chan error
}

// runPage starts a daemon for the clone at gitDir. inherited, if not nil, is the page's socket from
// the daemon before. Closing update tells the daemon an update is installed.
func runPage(t *testing.T, gitDir string, inherited *os.File, update <-chan struct{}) *pageRun {
	t.Helper()
	os.Remove(filepath.Join(gitDir, brand.StateDir, "daemon.json")) // the run before may have left it
	ctx, cancel := context.WithCancel(context.Background())
	r := &pageRun{t: t, gitDir: gitDir, cancel: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- daemon.Run(ctx, daemon.Options{GitDir: gitDir, Poll: 100 * time.Millisecond,
			Heartbeat: 500 * time.Millisecond, StaleAfter: 3 * time.Second, Idle: -1, NamePool: -1,
			PageListener: inherited, Log: testLog(),
			Update: func(ctx context.Context, restart func()) {
				select {
				case <-update:
					restart()
				case <-ctx.Done():
				}
			}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
		}
	})
	return r
}

// info waits for the daemon's daemon.json, with its page.
func (r *pageRun) info() proto.Info {
	r.t.Helper()
	var info proto.Info
	eventually(r.t, "daemon.json with the page", func() bool {
		b, err := os.ReadFile(filepath.Join(r.gitDir, brand.StateDir, "daemon.json"))
		return err == nil && json.Unmarshal(b, &info) == nil && info.Web != "" && info.PageToken != ""
	})
	return info
}

// wait is the error Run returned.
func (r *pageRun) wait() error {
	r.t.Helper()
	select {
	case err := <-r.done:
		r.done <- err // for the cleanup
		return err
	case <-time.After(60 * time.Second):
		r.t.Fatal("the daemon didn't stop")
		return nil
	}
}

// getState sends GET /api/state to the page at addr, as a tab does, and returns the status and,
// if it was answered, the state.
func getState(addr, token string) (int, web.State, error) {
	req, err := http.NewRequest("GET", "http://"+addr+"/api/state", nil)
	if err != nil {
		return 0, web.State{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return 0, web.State{}, err
	}
	defer resp.Body.Close()
	var st web.State
	if resp.StatusCode == http.StatusOK {
		err = json.NewDecoder(resp.Body).Decode(&st)
	}
	return resp.StatusCode, st, err
}

// postRestart asks the page at addr to restart its daemon onto the installed update, and returns
// the status: 409 until the daemon has the update ready.
func postRestart(addr, token string) int {
	req, err := http.NewRequest("POST", "http://"+addr+"/api/restart", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The restart #255 is about: the port is bound from the first daemon's last answer to the next
// daemon's first, so nothing else can take it; a tab's request sent in between waits and is answered
// by the new daemon, with the same token; and the tab sees a new daemon, which reloads it (#245).
func TestPagePortIsNeverFreeAcrossAnUpdate(t *testing.T) {
	gitDir := newClone(t, newRemote(t))
	update := make(chan struct{})
	first := runPage(t, gitDir, nil, update)
	info := first.info()
	code, before, err := getState(info.Web, info.PageToken)
	if err != nil || code != http.StatusOK {
		t.Fatalf("the page before the restart: %d %v", code, err)
	}

	// The open page holds the update back until it asks for the restart, as its button does (#240).
	close(update)
	eventually(t, "the page's restart", func() bool { return postRestart(info.Web, info.PageToken) == http.StatusNoContent })
	err = first.wait()
	var up *daemon.Updated
	if !errors.As(err, &up) || up.Page == nil {
		t.Fatalf("Run returned %v, want an Updated holding the page's socket", err)
	}
	if !errors.Is(err, daemon.ErrUpdated) {
		t.Fatalf("Run's %v isn't ErrUpdated", err)
	}
	// The daemon has gone, and its page's port is still bound: another process can't take it.
	if ln, err := net.Listen("tcp", info.Web); err == nil {
		ln.Close()
		t.Fatalf("%s was free after the daemon stopped for the update", info.Web)
	}

	// The tab polls in the gap. The connection is made, and the request sent, before the next
	// daemon starts: both wait in the socket's queue.
	conn, err := net.DialTimeout("tcp", info.Web, 10*time.Second)
	if err != nil {
		t.Fatalf("connecting in the gap: %v", err)
	}
	defer conn.Close()
	req, err := http.NewRequest("GET", "http://"+info.Web+"/api/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+info.PageToken)
	if err := req.Write(conn); err != nil {
		t.Fatalf("sending in the gap: %v", err)
	}

	next := runPage(t, gitDir, up.Page, nil)
	got := next.info()
	if got.Web != info.Web || got.PageToken != info.PageToken {
		t.Fatalf("the next daemon's page is %s with token %q, want %s with the same token", got.Web, got.PageToken, info.Web)
	}
	conn.SetDeadline(time.Now().Add(60 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("the request sent in the gap: %v", err)
	}
	defer resp.Body.Close()
	var after web.State
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&after) != nil {
		t.Fatalf("the request sent in the gap: %s", resp.Status)
	}
	if after.Version.Daemon == "" || after.Version.Daemon == before.Version.Daemon {
		t.Fatalf("the tab can't tell the new daemon (%q) from the old (%q), so it won't reload", after.Version.Daemon, before.Version.Daemon)
	}
	if _, err := os.Stat(filepath.Join(gitDir, brand.StateDir, "page.json")); !os.IsNotExist(err) {
		t.Fatalf("the handoff was left behind: %v", err)
	}
}

// Without the socket, the handoff is no good: the port may have been free, and whatever took it
// was sent the token. The next daemon serves the page with a new token, and refuses the old one.
func TestPageWithoutItsSocketGetsANewToken(t *testing.T) {
	gitDir := newClone(t, newRemote(t))
	update := make(chan struct{})
	first := runPage(t, gitDir, nil, update)
	info := first.info()
	close(update)
	var up *daemon.Updated
	if err := first.wait(); !errors.As(err, &up) {
		t.Fatalf("Run returned %v, want an Updated", err)
	}
	// The socket goes with nobody (a start through WMI can't hand it over), and the port is free.
	up.Page.Close()
	// The attack in #255: another process takes the port in the gap, is sent the tab's request with
	// its token, and lets the port go again before the next daemon starts.
	taken, err := net.Listen("tcp", info.Web)
	if err != nil {
		t.Fatalf("taking the freed port: %v", err)
	}
	taken.Close()

	next := runPage(t, gitDir, nil, nil)
	got := next.info()
	if got.PageToken == info.PageToken {
		t.Fatal("the page kept its token without its socket")
	}
	if code, _, err := getState(got.Web, info.PageToken); err != nil || code != http.StatusUnauthorized {
		t.Fatalf("the old token at the new page: %d %v, want 401", code, err)
	}
	if code, _, err := getState(got.Web, got.PageToken); err != nil || code != http.StatusOK {
		t.Fatalf("the new token: %d %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, brand.StateDir, "page.json")); !os.IsNotExist(err) {
		t.Fatalf("the unused handoff was left for a later daemon: %v", err)
	}
}

// A daemon that stops for any other reason lets the port go and leaves no handoff.
func TestPagePortFreedOnAPlainStop(t *testing.T) {
	gitDir := newClone(t, newRemote(t))
	r := runPage(t, gitDir, nil, nil)
	info := r.info()
	r.cancel()
	if err := r.wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ln, err := net.Listen("tcp", info.Web)
	if err != nil {
		t.Fatalf("%s still bound after the daemon stopped: %v", info.Web, err)
	}
	ln.Close()
	if _, err := os.Stat(filepath.Join(gitDir, brand.StateDir, "page.json")); !os.IsNotExist(err) {
		t.Fatalf("a plain stop left a handoff: %v", err)
	}
}

// newClone clones remote and returns its git dir, joined (the daemon runs only where the user did).
func newClone(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	return filepath.Join(dir, ".git")
}
