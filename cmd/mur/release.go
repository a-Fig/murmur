package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a-Fig/murmur/internal/update"
	"github.com/a-Fig/murmur/internal/web"
)

// releaseWatch is what the daemon knows of newer releases, for the page (#240): the updater's
// last check, else the latest release a daemon in this clone last recorded (update.Newer).
type releaseWatch struct {
	running, gitDir string

	mu       sync.Mutex
	last     update.Status
	heard    bool      // the updater has reported a check
	recorded string    // update.Newer, read at most once a minute
	readAt   time.Time // when recorded was read
	interval time.Duration
	// installing says a person asked for an install of installFor (Update now) that the updater
	// hasn't answered; installErr is why the last one asked for failed.
	installing bool
	installFor string
	installErr string
	// envOff says MURMUR_NO_UPDATE is set for this daemon, which Update now doesn't override.
	envOff bool
	// stopped says this daemon's updater has returned (a binary it can't replace, a build from
	// source), so nothing it is asked for happens until the daemon restarts. It is set under mu.
	stopped atomic.Bool
}

func newReleaseWatch(running, gitDir string) *releaseWatch {
	return &releaseWatch{running: running, gitDir: gitDir, interval: time.Minute, envOff: update.EnvOff(os.Getenv)}
}

// report is the updater's Report.
func (w *releaseWatch) report(s update.Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last, w.heard = s, true
	if s.Asked {
		w.installing, w.installErr = false, s.Error
		// The check recorded the release it found, which may not be the one asked for: read it
		// at once rather than show the old one for up to a minute.
		w.readAt = time.Time{}
	}
	if s.Installed {
		w.installing, w.installErr = false, ""
	}
}

// ask notes a person's request to install version, unless one is on its way already: it says
// whether this is a new one, for the updater to be told.
func (w *releaseWatch) ask(version string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped.Load() {
		return false, errUpdaterStopped
	}
	if w.installing {
		return false, nil
	}
	w.installing, w.installFor, w.installErr = true, version, ""
	return true, nil
}

var errUpdaterStopped = errors.New("this daemon's updater has stopped; restart the daemon first")

// stop is the updater returning: nothing asked of it happens until the daemon restarts, so an
// install asked for and not answered ends with that.
func (w *releaseWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped.Store(true)
	if w.installing {
		w.installing, w.installErr = false, errUpdaterStopped.Error()
	}
}

// page is the daemon's Version. At most once a minute it reads a file and, when the release there
// is a prerelease, asks git config for one key (update.Newer). It never asks the network.
func (w *releaseWatch) page() web.Version {
	v := web.Version{Running: w.running}
	if strings.HasSuffix(w.running, "-dev") {
		return v // a build from source is updated from source
	}
	// Newer runs git, so not under the lock, which the updater's reports wait on.
	w.mu.Lock()
	due := time.Since(w.readAt) >= w.interval
	if due {
		w.readAt = time.Now()
	}
	w.mu.Unlock()
	if due {
		recorded := update.Newer(w.gitDir, w.running)
		w.mu.Lock()
		w.recorded = recorded
		w.mu.Unlock()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	latest := w.last.Latest
	// Once a release is installed, that is the one a restart starts, whatever came out since.
	if !w.last.Installed && w.recorded != "" && (latest == "" || update.After(w.recorded, latest)) {
		latest = w.recorded
	}
	// An installed release is shown even when it is older: going back to stable from a
	// prerelease (#1045) installs one, and a restart starts it.
	if latest != "" && (w.last.Installed || update.After(latest, w.running)) {
		v.Latest = latest
	}
	// Until the updater's first check, which waits for the daemon's first sync, updates count as
	// on: they are unless turned off, and the first check says so within seconds.
	v.Auto = !w.heard || (!w.last.Off && !w.last.Failed)
	v.Off = w.heard && w.last.Off
	v.Installing = w.installing
	if w.installFor == v.Latest {
		v.InstallError = w.installErr // an error about another release would mislead
	}
	// Updates won't install Latest (off here, or the last attempt failed), so a person can.
	v.CanInstall = v.Latest != "" && !w.last.Installed && !v.Auto && !w.installing && !w.envOff && !w.stopped.Load()
	return v
}

// installed says a release is installed and waits for the restart onto it.
func (w *releaseWatch) installed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last.Installed
}

// updateSetting is the page's Updates (#1045): the machine's setting in the global git config. A
// change asks the updater for a check soon, so a choice of On, or going back to stable, takes
// effect in seconds rather than at the hourly check, and the page's banner follows a choice of Off.
type updateSetting struct {
	running, gitDir string
	check           chan struct{}
	install         chan string // a person's Update now, for the updater's Options.Install
	releases        *releaseWatch
}

func (u *updateSetting) nudge() {
	select {
	case u.check <- struct{}{}:
	default: // one is already asked for
	}
}

// waiting says why going back to stable can't happen now: an update is installed and waits for
// the restart, which would start it whatever the hold says, or the updater has stopped.
func (u *updateSetting) waiting() error {
	switch {
	case u.releases.installed():
		return errors.New("an update is installed; restart onto it first")
	case u.releases.stopped.Load():
		return errors.New("this daemon's updater has stopped; restart the daemon first")
	}
	return nil
}

func (u *updateSetting) web(s update.Setting) web.UpdateSetting {
	return web.UpdateSetting{Choice: s.Choice, Effective: s.Effective, Why: s.Why, Hold: s.Hold, CanGoBack: s.CanGoBack && u.waiting() == nil}
}

func (u *updateSetting) UpdateSetting(ctx context.Context) (web.UpdateSetting, error) {
	return u.web(update.Read(ctx, os.Getenv, u.gitDir, u.running)), nil
}

func (u *updateSetting) SetUpdates(ctx context.Context, choice string) (web.UpdateSetting, error) {
	s, err := update.Set(ctx, os.Getenv, u.gitDir, u.running, choice)
	if err == nil {
		u.nudge()
	}
	return u.web(s), err
}

func (u *updateSetting) BackToStable(ctx context.Context) (web.UpdateSetting, error) {
	// Back says why it can't (a stable release, a build from source, updates off); waiting says why
	// it can't now.
	if s := update.Read(ctx, os.Getenv, u.gitDir, u.running); s.CanGoBack {
		if err := u.waiting(); err != nil {
			return u.web(s), err
		}
	}
	s, err := update.Back(ctx, os.Getenv, u.gitDir, u.running)
	if err == nil {
		u.nudge()
	}
	return u.web(s), err
}

// InstallNow is the page's Update now (#1045): the updater installs version, the release the page
// showed, once, even with updates off here, and the page follows it in Version. It is checked here
// against what this daemon last knew; the updater installs nothing if GitHub now has another.
func (u *updateSetting) InstallNow(ctx context.Context, version string) error {
	v := u.releases.page()
	switch {
	case v.Installing:
		return nil // on its way already
	case u.releases.installed():
		return errors.New("an update is installed; restart onto it")
	case u.releases.stopped.Load():
		return errUpdaterStopped
	case u.releases.envOff:
		return errors.New("MURMUR_NO_UPDATE is set for this daemon")
	case v.Latest == "":
		return errors.New("no newer release is known here")
	case v.Latest != version:
		return fmt.Errorf("%s is the newer release now, not %s", v.Latest, version)
	case !v.CanInstall:
		return errors.New("updates are on here, so it installs on its own")
	}
	if fresh, err := u.releases.ask(version); !fresh {
		return err
	}
	select {
	case u.install <- version:
	default: // one is already waiting for the updater
	}
	return nil
}
