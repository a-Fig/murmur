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
	if v := w.page(); v.Running != "0.3.0" || v.Latest != "" || !v.Auto || v.CanInstall {
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
	u.releases.stopped.Store(true)
	if _, err := u.BackToStable(ctx); err == nil || !strings.Contains(err.Error(), "updater has stopped") {
		t.Errorf("with the updater stopped: %v", err)
	}
	dev := &updateSetting{running: "0.1.0-dev", gitDir: gitDir, check: make(chan struct{}, 1), releases: newReleaseWatch("0.1.0-dev", gitDir)}
	dev.releases.stopped.Store(true)
	if _, err := dev.BackToStable(ctx); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("a build from source: %v", err)
	}
	u.releases.stopped.Store(false)
	if s, err := u.BackToStable(ctx); err != nil || s.CanGoBack || s.Hold == "" {
		t.Fatalf("going back: %+v, %v", s, err)
	}
	select {
	case <-u.check:
	default:
		t.Error("going back asked for no check")
	}
}

// Update now (#1045) is offered when updates won't install a newer release on their own, installs
// once through the updater, and the page follows it until the updater answers.
func TestUpdateNow(t *testing.T) {
	ctx := context.Background()
	w := newReleaseWatch("0.3.0", t.TempDir())
	u := &updateSetting{running: "0.3.0", gitDir: w.gitDir, check: make(chan struct{}, 1), install: make(chan struct{}, 1), releases: w}
	asked := func() bool {
		select {
		case <-u.install:
			return true
		default:
			return false
		}
	}

	// Updates on and working: no button, and the endpoint refuses.
	w.report(update.Status{Latest: "0.3.1"})
	if v := w.page(); v.CanInstall {
		t.Errorf("offered with updates on: %+v", v)
	}
	if err := u.InstallNow(ctx, "0.3.1"); err == nil || asked() {
		t.Errorf("installed with updates on: %v", err)
	}

	// Off here, or the last attempt failed: offered.
	for _, s := range []update.Status{{Latest: "0.3.1", Off: true}, {Latest: "0.3.1", Failed: true, Error: "download: 503"}} {
		w.report(s)
		if v := w.page(); !v.CanInstall {
			t.Errorf("not offered after %+v: %+v", s, v)
		}
	}
	// Nothing newer: nothing to offer.
	w.report(update.Status{Latest: "0.3.0", Off: true})
	if v := w.page(); v.CanInstall {
		t.Errorf("offered with nothing newer: %+v", v)
	}

	w.report(update.Status{Latest: "0.3.1", Off: true})
	if err := u.InstallNow(ctx, "0.3.2"); err == nil || asked() {
		t.Errorf("installed a release the page didn't show: %v", err)
	}
	if err := u.InstallNow(ctx, "0.3.1"); err != nil || !asked() {
		t.Fatalf("Update now: %v", err)
	}
	if v := w.page(); !v.Installing || v.CanInstall {
		t.Errorf("while installing: %+v", v)
	}
	// A second click while it installs asks for nothing more.
	if err := u.InstallNow(ctx, "0.3.1"); err != nil || asked() {
		t.Errorf("second click: %v", err)
	}
	// An hourly check's answer isn't the one asked for.
	w.report(update.Status{Off: true})
	if v := w.page(); !v.Installing {
		t.Errorf("an hourly check ended the install: %+v", v)
	}
	// It failed: the page says why, and offers it again.
	w.report(update.Status{Latest: "0.3.1", Off: true, Failed: true, Asked: true, Error: "release checksum mismatch"})
	if v := w.page(); v.Installing || v.InstallError != "release checksum mismatch" || !v.CanInstall {
		t.Errorf("after a failed install: %+v", v)
	}
	// Again, and it installed: waiting for the restart, with nothing more to offer.
	if err := u.InstallNow(ctx, "0.3.1"); err != nil || !asked() {
		t.Fatalf("Update now again: %v", err)
	}
	if v := w.page(); v.InstallError != "" {
		t.Errorf("an old error shown while installing again: %+v", v)
	}
	w.report(update.Status{Latest: "0.3.1", Installed: true, Asked: true})
	if v := w.page(); v.Installing || v.CanInstall || v.Latest != "0.3.1" {
		t.Errorf("installed: %+v", v)
	}
	if err := u.InstallNow(ctx, "0.3.1"); err == nil || !strings.Contains(err.Error(), "restart onto it") {
		t.Errorf("with an update installed: %v", err)
	}

	// An updater that has stopped can't install, so it isn't offered.
	s := newReleaseWatch("0.3.0", t.TempDir())
	s.report(update.Status{Latest: "0.3.1", Off: true})
	s.stopped.Store(true)
	if v := s.page(); v.CanInstall {
		t.Errorf("offered with the updater stopped: %+v", v)
	}
}

// `mur version`'s install line pins a prerelease, since the installers' default is the latest
// stable release, which is older; a stable one isn't pinned.
func TestInstallLinePinsPrereleases(t *testing.T) {
	if l := installLine("0.3.1-nightly.20261109"); !strings.Contains(l, "MURMUR_VERSION") || !strings.Contains(l, "v0.3.1-nightly.20261109") {
		t.Errorf("prerelease: %s", l)
	}
	if l := installLine("0.3.1"); strings.Contains(l, "MURMUR_VERSION") {
		t.Errorf("stable release pinned: %s", l)
	}
}
