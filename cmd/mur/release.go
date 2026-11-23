package main

import (
	"context"
	"errors"
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
}

func newReleaseWatch(running, gitDir string) *releaseWatch {
	return &releaseWatch{running: running, gitDir: gitDir, interval: time.Minute}
}

// report is the updater's Report.
func (w *releaseWatch) report(s update.Status) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last, w.heard = s, true
}

// page is the daemon's Version. It reads a file at most once a minute, and never the network.
func (w *releaseWatch) page() web.Version {
	v := web.Version{Running: w.running, Install: installLine("")}
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
		// A prerelease (on the prerelease channel) is pinned: the installers' default is older.
		v.Latest, v.Install = latest, installLine(latest)
	}
	// Until the updater's first check, which waits for the daemon's first sync, updates count as
	// on: they are unless turned off, and the first check says so within seconds.
	v.Auto = !w.heard || (!w.last.Off && !w.last.Failed)
	v.Off = w.heard && w.last.Off
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
	releases        *releaseWatch
	// stopped says this daemon's updater has returned (a binary it can't replace, a build from
	// source), so nothing it is asked for happens until the daemon restarts.
	stopped atomic.Bool
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
	case u.stopped.Load():
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
