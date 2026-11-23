package main

import (
	"context"
	"path/filepath"
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

// The page tells updates turned off from a failed check, and shows a release going back to
// stable installed even though it is older than the one running (#1045).
func TestReleaseWatchOffAndGoingBack(t *testing.T) {
	w := newReleaseWatch("0.3.1-nightly.20261121", t.TempDir())
	w.report(update.Status{Off: true})
	if v := w.page(); !v.Off || v.Auto {
		t.Errorf("off: %+v", v)
	}
	w.report(update.Status{Latest: "0.3.2", Failed: true})
	if v := w.page(); v.Off || v.Auto {
		t.Errorf("failed: %+v", v)
	}
	w.report(update.Status{Latest: "0.3.0", Installed: true})
	if v := w.page(); v.Latest != "0.3.0" || v.Off {
		t.Errorf("gone back to stable: %+v", v)
	}
}

// Going back to stable isn't offered, and is refused, while an update waits for its restart or
// the updater has stopped; a build that can't go back at all says why, not "restart first".
func TestGoingBackWaits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("MURMUR_NO_UPDATE", "")
	_, clone := cloneOf(t, t.TempDir())
	gitDir := filepath.Join(clone, ".git")
	ctx := context.Background()
	const nightly = "0.3.1-nightly.20261121"
	w := newReleaseWatch(nightly, gitDir)
	u := &updateSetting{running: nightly, gitDir: gitDir, check: make(chan struct{}, 1), releases: w}
	if s, _ := u.UpdateSetting(ctx); !s.CanGoBack {
		t.Fatal("not offered from a nightly")
	}
	w.report(update.Status{Latest: "0.3.1-nightly.20261122", Installed: true})
	if s, _ := u.UpdateSetting(ctx); s.CanGoBack {
		t.Error("offered with an update waiting for its restart")
	}
	if _, err := u.BackToStable(ctx); err == nil || !strings.Contains(err.Error(), "restart onto it") {
		t.Errorf("with an update waiting: %v", err)
	}
	w.report(update.Status{})
	u.stopped.Store(true)
	if _, err := u.BackToStable(ctx); err == nil || !strings.Contains(err.Error(), "updater has stopped") {
		t.Errorf("with the updater stopped: %v", err)
	}
	dev := &updateSetting{running: "0.1.0-dev", gitDir: gitDir, check: make(chan struct{}, 1), releases: newReleaseWatch("0.1.0-dev", gitDir)}
	dev.stopped.Store(true)
	if _, err := dev.BackToStable(ctx); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("a build from source: %v", err)
	}
	u.stopped.Store(false)
	if s, err := u.BackToStable(ctx); err != nil || s.CanGoBack || s.Hold == "" {
		t.Fatalf("going back: %+v, %v", s, err)
	}
	select {
	case <-u.check:
	default:
		t.Error("going back asked for no check")
	}
}
