package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/desktop"
)

// mur stop says when the app will start the daemon again (#639): its window starts its clone's
// daemon within desktop.FollowEvery, so a stop on purpose came back with no word why. Only for the
// clone the window shows, and only while the window is open.
func TestAppShowsTheStoppedClone(t *testing.T) {
	app := filepath.Join(configIn(t, t.TempDir()), brand.Name, "app")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	shown, other := t.TempDir(), t.TempDir()
	if appShows(shown) {
		t.Error("no app at all, and the clone is shown")
	}
	b, _ := json.Marshal(lastFile{GitDir: shown})
	if err := os.WriteFile(filepath.Join(app, "last.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if appShows(shown) {
		t.Error("no window open, and the clone it last showed is shown")
	}
	in, err := desktop.Claim(filepath.Join(app, "window.json"), "")
	if err != nil || in == nil {
		t.Fatalf("claim: %v", err)
	}
	defer in.Close()
	if !appShows(shown) {
		t.Error("the window is open on the clone, and it isn't shown")
	}
	if !appShows(shown + string(filepath.Separator) + ".") {
		t.Error("the clone written another way isn't the same one")
	}
	if appShows(other) {
		t.Error("another clone is shown")
	}
}

// The real mur stop, on a clone whose daemon runs, says the open app will start it again, and says
// nothing of the kind once the window has closed.
func TestStopSaysTheAppStartsTheDaemonAgain(t *testing.T) {
	b := newBox(t)
	b.mustRun(b.clone, "init")
	b.mustRun(b.clone, "join")
	cfg := filepath.Join(b.home, ".config") // os.UserConfigDir in the box's environment
	switch runtime.GOOS {
	case "windows":
		cfg = filepath.Join(b.home, "AppData", "Roaming")
	case "darwin":
		cfg = filepath.Join(b.home, "Library", "Application Support")
	}
	app := filepath.Join(cfg, brand.Name, "app")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	last, _ := json.Marshal(lastFile{GitDir: filepath.Join(b.clone, ".git")})
	if err := os.WriteFile(filepath.Join(app, "last.json"), last, 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := desktop.Claim(filepath.Join(app, "window.json"), "") // this process stands in for the window
	if err != nil || in == nil {
		t.Fatalf("claim: %v", err)
	}
	out := b.mustRun(b.clone, "stop")
	if !strings.Contains(out, "Stopped the daemon.") || !strings.Contains(out, "app has this repo open, so it starts the daemon again within 10 seconds") {
		t.Errorf("mur stop with the app open on the clone said:\n%s", out)
	}
	in.Close()
	b.mustRun(b.clone, "ui", "--no-open") // starts the daemon again
	if out := b.mustRun(b.clone, "stop"); !strings.Contains(out, "Stopped the daemon.") || strings.Contains(out, "app") {
		t.Errorf("mur stop with the window closed said:\n%s", out)
	}
}
