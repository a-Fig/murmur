package main

import (
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/update"
)

// What the page is told of a newer release follows the updater's last check (#240).
func TestReleaseWatch(t *testing.T) {
	w := newReleaseWatch("0.3.0", t.TempDir())
	if v := w.page(); v.Running != "0.3.0" || v.Latest != "" || !v.Auto || v.Install == "" {
		t.Errorf("before any check: %+v", v)
	}
	for _, c := range []struct {
		s      update.Status
		latest string
		auto   bool
	}{
		{update.Status{Latest: "0.3.0"}, "", true},
		{update.Status{Latest: "0.3.1"}, "0.3.1", true},
		{update.Status{Latest: "0.3.1", Failed: true}, "0.3.1", false},
		{update.Status{Off: true}, "", false},
		{update.Status{Latest: "0.2.9"}, "", true},
	} {
		w.report(c.s)
		v := w.page()
		if v.Latest != c.latest || v.Auto != c.auto {
			t.Errorf("after %+v: latest %q auto %v, want %q %v", c.s, v.Latest, v.Auto, c.latest, c.auto)
		}
	}

	// A release out after one is installed doesn't change which one the restart starts.
	w.recorded, w.readAt = "0.3.2", time.Now()
	w.report(update.Status{Latest: "0.3.1", Installed: true})
	if v := w.page(); v.Latest != "0.3.1" {
		t.Errorf("installed 0.3.1, page told of %s", v.Latest)
	}

	// A prerelease, seen on the prerelease channel, is pinned in the install line; a stable one isn't.
	pre := newReleaseWatch("0.3.0", t.TempDir())
	pre.report(update.Status{Latest: "0.3.1-nightly.20261109"})
	if v := pre.page(); v.Latest != "0.3.1-nightly.20261109" || !strings.Contains(v.Install, "MURMUR_VERSION") || !strings.Contains(v.Install, "v0.3.1-nightly.20261109") {
		t.Errorf("prerelease: %+v", v)
	}
	pre.report(update.Status{Latest: "0.3.1"})
	if v := pre.page(); strings.Contains(v.Install, "MURMUR_VERSION") {
		t.Errorf("stable release pinned: %+v", v)
	}

	// A build from source says nothing of releases.
	dev := newReleaseWatch("0.1.0-dev", t.TempDir())
	dev.report(update.Status{Latest: "0.3.1"})
	if v := dev.page(); v.Latest != "" {
		t.Errorf("dev build told of %s", v.Latest)
	}
}
