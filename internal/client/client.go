// Package client finds a clone's daemon, starting it if asked, and talks to it.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
)

// ErrNoDaemon means no daemon is running for the clone and the caller didn't ask for one.
var ErrNoDaemon = errors.New("no daemon is running for this clone")

// FindGitDir returns the git common dir of the repository containing dir: shared by all of a
// clone's worktrees, so they share one daemon.
func FindGitDir(ctx context.Context, dir string) (string, error) {
	out, err := gitx.RunIn(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository (%s)", dir)
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// StateDir is the daemon's directory for a git common dir.
func StateDir(gitDir string) string { return filepath.Join(gitDir, brand.StateDir) }

// Client talks to one clone's daemon. It is safe for concurrent use.
type Client struct {
	GitDir string
	Spawn  bool // start a daemon when none answers
	// MaySpawn, if set, is asked each time a daemon would be started, and says no to stop it. A
	// client that outlives the user's consent (the MCP server) needs it: it learns the consent is
	// gone when the daemon it talked to goes away, which is the moment it would start another.
	MaySpawn func() bool

	// launch starts a daemon; start, unless a test says otherwise.
	launch func() error

	mu   sync.Mutex
	info proto.Info
	// older says the daemon that last answered a ping speaks an older protocol than this binary.
	older bool
}

func (c *Client) current() proto.Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// ErrNoPage means the daemon is running but isn't serving the page for humans.
var ErrNoPage = errors.New("the daemon isn't serving the page")

// PageURL is the address that opens the page in a browser. The page's token goes in the
// fragment, which a browser never sends to a server, and the page keeps it for its own requests.
// It is not the daemon's token: the address reaches the browser's history and extensions, and
// the daemon's lets whoever holds it send as this device (#42). A daemon from before the page had
// a token of its own takes the daemon's.
func (c *Client) PageURL() (string, error) {
	info := c.current()
	if info.Web == "" {
		return "", ErrNoPage
	}
	token := info.PageToken
	if token == "" {
		token = info.Token
	}
	return "http://" + info.Web + "/#token=" + token, nil
}

// Connect finds the daemon for gitDir, starting one if spawn is set and none answers.
func Connect(ctx context.Context, gitDir string, spawn bool) (*Client, error) {
	c := &Client{GitDir: gitDir, Spawn: spawn}
	return c, c.connect(ctx)
}

// connect finds the clone's daemon. One that speaks an older protocol than this binary (left running
// by the mur it replaced) is stopped and replaced if the client may start daemons; a client that
// may not, such as `mur stop`, which has to be able to stop it, takes it as it is.
func (c *Client) connect(ctx context.Context) error {
	if c.load() && c.ping(ctx) == nil {
		if !c.Outdated() || !c.mayStart() {
			return nil
		}
		if err := c.replace(ctx); err != nil {
			return err
		}
	} else if !c.mayStart() {
		return ErrNoDaemon
	}
	launch := c.launch
	if launch == nil {
		launch = c.start
	}
	if err := launch(); err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if c.load() && c.ping(ctx) == nil && !c.Outdated() {
			return nil
		}
	}
	return fmt.Errorf("the daemon didn't come up; see %s", filepath.Join(StateDir(c.GitDir), "daemon.log"))
}

func (c *Client) mayStart() bool {
	return c.Spawn && (c.MaySpawn == nil || c.MaySpawn())
}

// Outdated reports whether the daemon that last answered speaks an older protocol than this binary:
// one that Connect found and, without permission to start daemons, left running.
func (c *Client) Outdated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.older
}

// replaceWait is how long replace waits for a daemon it asked to stop to let go of the clone: as long
// as a shutdown may take (draining its work, then saying goodbye, with the lock held throughout), and
// a little more for the request to arrive and the process to go. A caller that gives up sooner ends
// the wait sooner. A variable, for the test.
var replaceWait = proto.ShutdownMax + 5*time.Second

// replace stops the daemon at the address c has, which is outdated, and waits until it has let go of
// the clone, so that the daemon started next finds the lock free. Another client may be doing the
// same: if the daemon.json that appears isn't the old one's, the work is done.
func (c *Client) replace(ctx context.Context) error {
	old := c.current()
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	c.roundTrip(sctx, proto.Request{Op: proto.OpStop}) // by the old address and token, never a newer daemon's
	cancel()
	lock := filepath.Join(StateDir(c.GitDir), "daemon.lock")
	for end := time.Now().Add(replaceWait); ; {
		if c.load() && c.current().Token != old.Token {
			return nil
		}
		l, err := proc.Lock(lock)
		if err == nil {
			l.Close()
			return nil
		}
		if !errors.Is(err, proc.ErrLocked) {
			return fmt.Errorf("replacing the daemon of an older version: %w", err)
		}
		if time.Now().After(end) {
			return fmt.Errorf("the daemon of an older version (pid %d) didn't exit when asked to", old.PID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *Client) load() bool {
	b, err := os.ReadFile(filepath.Join(StateDir(c.GitDir), "daemon.json"))
	if err != nil {
		return false
	}
	var info proto.Info
	if json.Unmarshal(b, &info) != nil || info.Addr == "" {
		return false
	}
	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return true
}

func (c *Client) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, _, err := c.roundTrip(ctx, proto.Request{Op: proto.OpPing})
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	c.mu.Lock()
	c.older = resp.Protocol < proto.Protocol
	c.mu.Unlock()
	return nil
}

// start launches a daemon detached from this process. If two clients start one at once, the
// second daemon finds the lock taken and exits.
func (c *Client) start() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// A running MCP server can outlive an update. Start the installed path rather
	// than the renamed Windows backup (or Linux's deleted executable name).
	exe = installedExecutable(exe)
	return StartDaemon(exe, c.GitDir, nil)
}

// StartDaemon starts the installed executable after a self-update has released the clone lock.
// Forwarded flags use --name=value, as recorded from the daemon's parsed FlagSet. page, if not nil,
// is the page's listening socket, which the new daemon inherits so that the port is never free
// between the two (#255).
func StartDaemon(exe, gitDir string, page *os.File, flags ...string) error {
	dir := StateDir(gitDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(dir, "daemon.log")
	// WMI cannot inherit logf and adds its path as --log. Open the forwarded
	// log destination so that fallback preserves a custom --log too.
	for _, f := range flags {
		if path, ok := strings.CutPrefix(f, "--log="); ok && path != "" {
			logPath = path
		}
	}
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 4<<20 {
		os.Rename(logPath, logPath+".old")
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	// Run in the clone's top directory, where git resolves a relative remote URL from. The
	// daemon still never uses it as a working tree: every command it runs gets --git-dir.
	cwd := gitDir
	if filepath.Base(cwd) == ".git" {
		cwd = filepath.Dir(cwd)
	}
	args := append([]string{"daemon"}, flags...)
	// The launcher's cwd may differ from the original daemon's. The resolved
	// clone directory wins over a forwarded relative --git-dir.
	args = append(args, "--git-dir", gitDir)
	var hand *proc.Handover
	if page != nil {
		hand = &proc.Handover{File: page, Env: brand.PageListenerEnv}
	}
	p, err := proc.StartDetached(exe, args, cwd, logf, hand)
	if err != nil {
		return err
	}
	// A nil process means the daemon was started and had already exited; connect polls for it.
	if p == nil {
		return nil
	}
	return p.Release()
}

// Stop asks the clone's daemon to exit and waits for it to go. It reports whether there was one.
func Stop(ctx context.Context, gitDir string) (bool, error) {
	c, err := Connect(ctx, gitDir, false)
	if errors.Is(err, ErrNoDaemon) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resp, err := c.Do(ctx, proto.Request{Op: proto.OpStop})
	if err == nil && resp.Error != "" {
		err = errors.New(resp.Error)
	}
	if err != nil {
		return true, err
	}
	// The daemon removes daemon.json as it goes, and holds the clone's files until then.
	info := filepath.Join(StateDir(gitDir), "daemon.json")
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && ctx.Err() == nil; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(info); err != nil {
			break
		}
	}
	return true, nil
}

// ErrStillRunning is what WaitGone returns when a daemon still holds the clone's lock at the deadline.
var ErrStillRunning = errors.New("the daemon is still running")

// WaitGone waits, up to d, until no daemon holds the clone's lock, and reports whether one did when
// it began. A daemon still starting has no daemon.json yet, so Stop doesn't see it; it does hold the
// lock. If one still does at the deadline the error is ErrStillRunning, so a caller can't take a
// slow daemon for a gone one.
func WaitGone(ctx context.Context, gitDir string, d time.Duration) (held bool, err error) {
	for end := time.Now().Add(d); ; time.Sleep(50 * time.Millisecond) {
		l, lerr := proc.Lock(filepath.Join(StateDir(gitDir), "daemon.lock"))
		if lerr == nil {
			l.Close()
			return held, nil
		}
		if errors.Is(lerr, fs.ErrNotExist) {
			return held, nil // no state directory: no daemon has ever run here
		}
		if !errors.Is(lerr, proc.ErrLocked) {
			return held, lerr
		}
		held = true
		if ctx.Err() != nil {
			return held, ctx.Err()
		}
		if time.Now().After(end) {
			return held, ErrStillRunning
		}
	}
}

// Do sends one request. If the daemon couldn't be reached, it reconnects (starting a new daemon
// when Spawn is set) and tries once more. Once a request has been sent it is never resent: the
// daemon may have acted on it, and a second send would happen twice.
func (c *Client) Do(ctx context.Context, req proto.Request) (proto.Response, error) {
	resp, sent, err := c.roundTrip(ctx, req)
	if err == nil || sent || ctx.Err() != nil {
		return resp, err
	}
	if cerr := c.connect(ctx); cerr != nil {
		return proto.Response{}, fmt.Errorf("%v (reconnecting: %v)", err, cerr)
	}
	resp, _, err = c.roundTrip(ctx, req)
	return resp, err
}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var dialer net.Dialer
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return dialer.DialContext(dctx, "tcp", addr)
}

// roundTrip sends one request and reads the response. sent reports whether the request was
// written, after which the daemon may have acted on it.
func (c *Client) roundTrip(ctx context.Context, req proto.Request) (resp proto.Response, sent bool, err error) {
	info := c.current()
	if info.Addr == "" {
		return proto.Response{}, false, ErrNoDaemon
	}
	conn, err := dial(ctx, info.Addr)
	if err != nil {
		return proto.Response{}, false, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	// Cancelling ctx ends a write or read already under way: a deadline in the past makes both
	// return at once. The MCP server cancels a call when its session moves to another repo or
	// leaves, and the call must not reach the old repo's daemon, or wait on it, after that.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if ctx.Err() != nil {
		return proto.Response{}, false, ctx.Err()
	}
	req.Token = info.Token
	if err := writeLine(conn, req); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return proto.Response{}, true, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return proto.Response{}, true, err
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return proto.Response{}, true, err
	}
	return resp, true, nil
}

// Hello registers a session and returns the connection that keeps it registered. The session
// lasts until the connection closes.
func (c *Client) Hello(ctx context.Context, h proto.Hello) (net.Conn, proto.Response, error) {
	// Every registration pings first, which is also where a daemon of an older version is found.
	if err := c.connect(ctx); err != nil {
		return nil, proto.Response{}, err
	}
	conn, err := dial(ctx, c.current().Addr)
	if err != nil {
		if cerr := c.connect(ctx); cerr != nil {
			return nil, proto.Response{}, cerr
		}
		if conn, err = dial(ctx, c.current().Addr); err != nil {
			return nil, proto.Response{}, err
		}
	}
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := writeLine(conn, proto.Request{Token: c.current().Token, Op: proto.OpHello, Hello: &h}); err != nil {
		conn.Close()
		return nil, proto.Response{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, proto.Response{}, err
	}
	conn.SetDeadline(time.Time{})
	var resp proto.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		conn.Close()
		return nil, proto.Response{}, err
	}
	if !resp.OK {
		conn.Close()
		return nil, resp, errors.New(resp.Error)
	}
	return sessionConn{conn}, resp, nil
}

// sessionConn is a session's connection, whose Close shuts the socket down before closing it. A
// close alone ends only this process's descriptor: a child forked at that moment, not yet started
// as its program, holds a copy, and the daemon sees the session end only when that copy goes. In
// the daemon's tests one such child never got that far (#217), and the session never ended.
type sessionConn struct{ net.Conn }

func (c sessionConn) Close() error {
	if tc, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		tc.CloseWrite()
	}
	return c.Conn.Close()
}

func writeLine(conn net.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(b, '\n'))
	return err
}

func installedExecutable(exe string) string {
	exe = strings.TrimSuffix(exe, " (deleted)")
	base := filepath.Base(exe)
	if i := strings.LastIndex(base, ".old-.mur-update-"); i >= 0 {
		exe = filepath.Join(filepath.Dir(exe), base[:i])
	}
	return exe
}
