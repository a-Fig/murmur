package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/desktop"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/logfile"
	"github.com/a-Fig/murmur/internal/web"
)

// runApp opens the page in a window of its own (#23; docs/design/desktop.md). It starts the window
// as a process of its own and returns, so the terminal it was typed in is free and can be closed;
// --wait keeps it in the foreground.
func runApp(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("app")
	wait := fs.Bool("wait", false, "stay until the window closes")
	devtools := fs.Bool("devtools", false, "let the window open the web view's developer tools")
	page := fs.String("page", "", "show this address and no clone's: the devserver's (internal/web/devserver), for work on the app")
	fromLauncher := fs.Bool("from-launcher", false, "started from the app's launcher, which needs no putting in place and has no terminal to tell")
	fs.Parse(args)
	if !desktop.Supported {
		return desktop.Run(ctx, desktop.Options{})
	}
	if *page != "" {
		return desktop.Run(ctx, desktop.Options{Page: *page, Open: browse, Debug: *devtools})
	}

	cfg, err := appDir()
	if err != nil {
		return err
	}
	// A window is open already: it comes forward, and no second one opens (#616). Macs start an
	// app's bundle once, but a `mur app` typed in a terminal is another process. One typed in a
	// joined repo, or given one with -C, moves the window to that repo, as it would open there.
	window := filepath.Join(cfg, "window.json")
	asked := ""
	if gitDir, err := client.FindGitDir(ctx, *dir); err == nil && join.Joined(gitDir) && !*fromLauncher {
		asked = gitDir
	}
	if (!*wait || *fromLauncher) && desktop.BringForward(window, asked) {
		if !*fromLauncher {
			fmt.Fprintln(out, "Brought the open window forward.")
		}
		return nil
	}
	// Started from Murmur.app, this process is the window, so the Dock's icon is the bundle's.
	if desktop.FromBundle() {
		*wait = true
	}
	gitDir, err := appClone(ctx, *dir, cfg)
	if err != nil {
		return err
	}
	if !*wait && !*fromLauncher {
		putLauncher(cfg, out) // once there is a repo for the app to show
	}
	if !*wait {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		logPath := filepath.Join(cfg, "app.log")
		if fi, err := os.Stat(logPath); err == nil && fi.Size() > logfile.Max {
			os.Rename(logPath, logPath+".old")
		}
		logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer logf.Close()
		start, _ := logf.Seek(0, io.SeekEnd)
		args := []string{"app", "--wait", "-C", top(gitDir)}
		if *devtools {
			args = append(args, "--devtools")
		}
		if *fromLauncher {
			args = append(args, "--from-launcher") // so the window isn't asked to show a repo
		}
		exited, err := desktop.Detach(exe, args, logf)
		if err != nil {
			return err
		}
		if *fromLauncher {
			return nil // nobody is at a terminal to be told it opened
		}
		// A window that can't be made (no web view, no screen) fails within a second or two, and
		// says why in the log: that is worth saying here, where someone is looking.
		select {
		case err := <-exited:
			if err == nil { // another window claimed first, and came forward
				fmt.Fprintln(out, "Brought the open window forward.")
				return nil
			}
			return fmt.Errorf("the window closed as it opened: %s", lastLine(logPath, start))
		case <-time.After(appStartWait):
		}
		fmt.Fprintln(out, "Opened.")
		return nil
	}

	// This process is the window: it says so before starting a daemon, which can take seconds,
	// so a second click meanwhile finds it rather than opening another (Opus review). Two clicks
	// at once both get this far; the second finds the first's claim here, and stops.
	inst, err := desktop.Claim(window, asked)
	if err != nil {
		log.Printf("a second mur app will open a second window: %v", err)
	} else if inst == nil {
		if !*fromLauncher {
			fmt.Fprintln(out, "Brought the open window forward.")
		}
		return nil
	}
	defer inst.Close()
	addr, err := pageOf(ctx, gitDir)
	if err != nil {
		return err
	}
	f := &follower{cfg: cfg, gitDir: gitDir, page: pageOf}
	f.remember()
	prefs, _ := web.ReadPreferences(client.StateDir(gitDir))
	return desktop.Run(ctx, desktop.Options{
		Page:       addr,
		LightFrame: !prefs.DarkFrame(),
		Follow:     f.follow,
		Open:       browse,
		Debug:      *devtools,
		Instance:   inst,
		Switch:     f.switchTo,
	})
}

// putLauncher puts the app's launcher in place for this mur (#616), or moves it here if mur has
// moved, and says so when it did. Failing to is no reason not to open the window.
func putLauncher(cfg string, out io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	where, changed, err := desktop.InstallLauncher(exe, cfg)
	switch {
	case err != nil:
		fmt.Fprintf(out, "Couldn't add the app's launcher: %v\n", err)
	case changed:
		fmt.Fprintf(out, "Added %s's launcher, to start it as any other app: %s\n", brand.Product, where)
	}
}

// appStartWait is how long `mur app` watches the window's process before saying it opened.
const appStartWait = 2 * time.Second

// lastLine is the last line written to the log at path after offset from, without mur's prefix.
func lastLine(path string, from int64) string {
	b, err := os.ReadFile(path)
	if err != nil || int64(len(b)) <= from {
		return "see " + path
	}
	lines := strings.Split(strings.TrimSpace(string(b[from:])), "\n")
	return strings.TrimPrefix(strings.TrimSpace(lines[len(lines)-1]), brand.Command+": ")
}

// appDir is the app's directory in the user's config directory, beside the clones' record: its
// log, and the clone it last showed.
func appDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cfg, brand.Name, "app")
	return dir, os.MkdirAll(dir, 0o700)
}

// uninstallApp takes out the desktop app for `mur uninstall`: the launcher `mur app` put in place
// (#616), then the app's folder, its log, the clone it last showed, its window's record and the
// launcher's, so that Murmur's folder in the config directory can go after it (#746). An open window
// keeps the folder, as it writes its log and record there, and is named for the user to close. So
// does a launcher that couldn't be taken out, whose record says it is still Murmur's.
func uninstallApp(r *join.Report) (left []string) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(cfg, brand.Name, "app")
	where, err := desktop.RemoveLauncher(dir)
	switch {
	case err != nil:
		r.Problems = append(r.Problems, "taking out the app's launcher ("+where+"): "+err.Error())
		return nil
	case where != "":
		r.Did = append(r.Did, "Took out the app's launcher ("+where+")")
	}
	if _, err := os.Lstat(dir); err != nil {
		return nil
	}
	if desktop.WindowOpen(filepath.Join(dir, "window.json")) {
		return []string{brand.Product + "'s app, whose window is open, and its files (" + dir + "): close the window, then run uninstall again."}
	}
	// A window just starting, between its start and its record, isn't found: on Windows its log,
	// which it writes to, then keeps the folder, and the report has the error; elsewhere the folder
	// goes and the window runs with no record. Rare enough to leave (Opus review).
	if err := os.RemoveAll(dir); err != nil {
		r.Problems = append(r.Problems, "removing the app's files ("+dir+"): "+err.Error())
		return nil
	}
	r.Did = append(r.Did, "Removed the desktop app's files ("+dir+")")
	return nil
}

// appClone is the clone the window opens on. A daemon serves one clone's page, so the app has to
// pick one, and the page's repo menu moves it to any other:
//
//  1. the clone it was started in, as `mur ui` does, if that clone joined;
//  2. else the clone it last showed (from a shortcut, the Dock, or a terminal in the home folder);
//  3. else a joined clone whose daemon is running, else any joined clone.
func appClone(ctx context.Context, dir, cfg string) (string, error) {
	if gitDir, err := client.FindGitDir(ctx, dir); err == nil && join.Joined(gitDir) {
		return gitDir, nil
	}
	record, err := clones.DefaultDir()
	if err != nil {
		return "", err
	}
	list, err := clones.List(record, join.Joined)
	if err != nil {
		return "", err
	}
	if last := lastClone(cfg); last != "" {
		for _, c := range list {
			if c.ID() == clones.ID(last) {
				return c.GitDir, nil
			}
		}
	}
	for _, c := range list {
		if client.Running(ctx, c.GitDir) {
			return c.GitDir, nil
		}
	}
	if len(list) > 0 {
		return list[0].GitDir, nil
	}
	return "", errors.New("no repo on this machine has joined " + brand.Product + ": ask an agent in one to join, or run `" + brand.Command + " join` there")
}

type lastFile struct {
	GitDir string `json:"gitDir"`
}

func lastClone(cfg string) string {
	b, err := os.ReadFile(filepath.Join(cfg, "last.json"))
	if err != nil {
		return ""
	}
	var l lastFile
	json.Unmarshal(b, &l)
	return l.GitDir
}

// follower tracks which clone's page the window shows: the page says where it is served from,
// and the page's repo menu can move it to another clone's.
type follower struct {
	cfg string
	// page starts the clone's daemon if it isn't running and returns its page's address.
	page func(ctx context.Context, gitDir string) (string, error)

	mu     sync.Mutex
	gitDir string
	failed string // the last failure logged, so a daemon that keeps failing fills no log
}

func (f *follower) remember() {
	f.mu.Lock()
	b, _ := json.Marshal(lastFile{GitDir: f.gitDir})
	f.mu.Unlock()
	p := filepath.Join(f.cfg, "last.json")
	if err := os.WriteFile(p+".tmp", b, 0o600); err == nil {
		os.Rename(p+".tmp", p)
	}
}

// follow is desktop.Options.Follow: given the host the page last reported, it returns the page
// to send the window to, or "". The clone is the one whose daemon is at that host; when none is,
// as after the daemon went, the clone stays the one it was, and its daemon is started again.
func (f *follower) follow(ctx context.Context, host string) string {
	f.mu.Lock()
	gitDir := f.gitDir
	f.mu.Unlock()
	if host != "" && client.PageHost(gitDir) != host {
		if found := cloneAt(host); found != "" && found != gitDir {
			f.mu.Lock()
			f.gitDir = found
			f.mu.Unlock()
			f.remember()
			gitDir = found
		}
	}
	if !join.Joined(gitDir) {
		return "" // left: the page says so, and nothing should start its daemon
	}
	page, err := f.page(ctx, gitDir)
	if err != nil {
		if msg := fmt.Sprintf("the page of %s: %v", gitDir, err); msg != f.failed {
			log.Print(msg)
			f.failed = msg
		}
		return ""
	}
	f.failed = ""
	if u, err := url.Parse(page); err != nil || u.Host == host {
		return ""
	}
	return page
}

// switchTo is desktop.Options.Switch: it moves the window to the clone at gitDir, which a later
// `mur app` asked for, and returns its page, or "" when the window shows that clone already.
func (f *follower) switchTo(ctx context.Context, gitDir string) string {
	f.mu.Lock()
	same := gitDir == f.gitDir
	f.gitDir = gitDir
	f.mu.Unlock()
	if same {
		return ""
	}
	f.remember()
	page, err := f.page(ctx, gitDir)
	if err != nil {
		log.Printf("the page of %s: %v", gitDir, err)
		return ""
	}
	return page
}

func pageOf(ctx context.Context, gitDir string) (string, error) {
	c, err := client.Connect(ctx, gitDir, true)
	if err != nil {
		return "", err
	}
	return c.PageURL()
}

// cloneAt is the joined clone whose daemon serves its page at host, or "".
func cloneAt(host string) string {
	record, err := clones.DefaultDir()
	if err != nil {
		return ""
	}
	list, _ := clones.List(record, join.Joined)
	for _, c := range list {
		if client.PageHost(c.GitDir) == host {
			return c.GitDir
		}
	}
	return ""
}

// top is a git common dir's working tree, where -C can point.
func top(gitDir string) string {
	return clones.Clone{GitDir: gitDir}.Top()
}
