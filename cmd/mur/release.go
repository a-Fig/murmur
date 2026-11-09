package main

import (
	"strings"
	"sync"
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if now := time.Now(); now.Sub(w.readAt) >= w.interval {
		w.recorded, w.readAt = update.Newer(w.gitDir, w.running), now
	}
	latest := w.last.Latest
	// Once a release is installed, that is the one a restart starts, whatever came out since.
	if !w.last.Installed && w.recorded != "" && (latest == "" || update.After(w.recorded, latest)) {
		latest = w.recorded
	}
	if latest != "" && update.After(latest, w.running) {
		// A prerelease (on the prerelease channel) is pinned: the installers' default is older.
		v.Latest, v.Install = latest, installLine(latest)
	}
	// Until the updater's first check, which waits for the daemon's first sync, updates count as
	// on: they are unless turned off, and the first check says so within seconds.
	v.Auto = !w.heard || (!w.last.Off && !w.last.Failed)
	return v
}
