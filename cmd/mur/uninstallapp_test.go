package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/desktop"
	"github.com/a-Fig/murmur/internal/join"
)

// configIn points the user's config directory at dir, for os.UserConfigDir on each system.
func configIn(t *testing.T, dir string) string {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("APPDATA", dir)
	case "darwin":
		t.Setenv("HOME", dir)
		dir = filepath.Join(dir, "Library", "Application Support")
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	if got, err := os.UserConfigDir(); err != nil || got != dir {
		t.Fatalf("config directory %q, %v; want %q", got, err, dir)
	}
	return dir
}

// Uninstall takes the app's folder out of Murmur's, which could then never go (#746), unless its
// window is open: that one is named for the user to close.
func TestUninstallApp(t *testing.T) {
	app := filepath.Join(configIn(t, t.TempDir()), brand.Name, "app")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"app.log", "last.json"} {
		os.WriteFile(filepath.Join(app, f), []byte("x"), 0o600)
	}
	in, err := desktop.Claim(filepath.Join(app, "window.json"), "")
	if err != nil || in == nil {
		t.Fatalf("claim: %v", err)
	}
	var r join.Report
	left := uninstallApp(&r)
	if len(left) != 1 || !strings.Contains(left[0], "close the window") || len(r.Problems) != 0 {
		t.Errorf("with the window open: left %q, problems %q", left, r.Problems)
	}
	if _, err := os.Stat(filepath.Join(app, "app.log")); err != nil {
		t.Errorf("the open window's files went: %v", err)
	}
	in.Close()
	r = join.Report{}
	if left := uninstallApp(&r); len(left) != 0 || len(r.Problems) != 0 {
		t.Errorf("with the window closed: left %q, problems %q", left, r.Problems)
	}
	if _, err := os.Lstat(app); !os.IsNotExist(err) {
		t.Errorf("the app's folder is still there: %v", err)
	}
}
