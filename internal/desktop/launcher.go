package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// The launcher (#616) starts the app as any other app starts: a Start menu shortcut on Windows,
// Murmur.app in ~/Applications on macOS, a .desktop file on Linux. `mur app` puts it in place each
// time it runs, so it follows the installed mur if that moves, and `mur uninstall` takes it out.
// A launcher points at the installed mur rather than at a copy, so mur's self-update, which
// replaces the binary where it is, updates the app too. docs/design/desktop.md has the reasons.

// launcherVersion goes up when what a launcher holds changes, so every existing one is made again.
const launcherVersion = 1

// stamp records the launcher last put in place, beside the app's other files, so `mur app` does
// nothing when it is already right: reading a shortcut back takes COM on Windows.
type stamp struct {
	Version int    `json:"version"`
	Exe     string `json:"exe"`
	Where   string `json:"where"`
}

const stampFile = "launcher.json"

// InstallLauncher puts the launcher for exe, the installed mur, in place, or moves it onto exe.
// dir is the app's directory in the user's config directory, where an icon the launcher needs is
// kept. It says where the launcher is, and whether it made or changed it now. It leaves alone a
// launcher of the same name that isn't Murmur's, one the person took out (until mur moves), and
// everything when exe is a build in a temporary directory, as `go run` makes (Opus review).
func InstallLauncher(exe, dir string) (where string, changed bool, err error) {
	if !Supported || devBuild(exe) {
		return "", false, nil
	}
	want := stamp{Version: launcherVersion, Exe: exe, Where: launcherPath()}
	if want.Where == "" {
		return "", false, nil
	}
	had := readStamp(dir)
	switch {
	case had == want && exists(want.Where):
		return want.Where, false, nil
	case had == want:
		return "", false, nil // taken out by the person since it was put there
	case exists(want.Where) && had.Where != want.Where && !ownsLauncher(want.Where):
		return "", false, fmt.Errorf("%s is there already, and isn't %s's", want.Where, brand.Product)
	}
	if err := installLauncher(exe, dir, want.Where); err != nil {
		return "", false, err
	}
	b, _ := json.Marshal(want)
	os.WriteFile(filepath.Join(dir, stampFile), b, 0o600)
	return want.Where, true, nil
}

// RemoveLauncher takes the launcher out, if it is Murmur's, and says where it was, or "" if
// there was none.
func RemoveLauncher(dir string) (string, error) {
	had := readStamp(dir)
	where := launcherPath()
	os.Remove(filepath.Join(dir, stampFile))
	if where == "" || !exists(where) || had.Where != where && !ownsLauncher(where) {
		return "", nil
	}
	removeLauncherFiles(dir)
	return where, os.RemoveAll(where)
}

func readStamp(dir string) stamp {
	var s stamp
	if b, err := os.ReadFile(filepath.Join(dir, stampFile)); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

// devBuild says exe is a build in a temporary directory, which a launcher mustn't point at: it
// goes when its directory is cleaned up. A variable, so a test can build a launcher for one.
var devBuild = func(exe string) bool {
	tmp := filepath.Clean(os.TempDir()) + string(filepath.Separator)
	return strings.HasPrefix(strings.ToLower(filepath.Clean(exe)), strings.ToLower(tmp)) || strings.Contains(exe, "go-build")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
